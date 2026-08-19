# internal/server — the playground app (fork)

The standalone chat/image/speech app served on `-playground-port`, and the **server-owned turn
runner** behind it. The tools' own fetch paths are in [`tools.md`](tools.md); the browser half is
`ui-svelte/playground-chat.md` / `playground-tools.md`. Route list in [`routes.md`](routes.md).

## `playground.go` — the app and its storage

`Playground` struct + `SetPlayground`/`markPlayground`; plaintext per-user login (`pg_user` cookie,
`users.json`), server-backed chat history + prefs, and `/api/mode` so **one bundle serves dashboard
or playground per port**.

**Storage** is per-user `DataDir/users/<user>/{chats,imagechats,speechchats,prefs}.json`. Generated
media (inline `data:` base64) is split out on write into `media/<hash>.<ext>` (`extractMedia`, a
regex over raw bytes — structure-agnostic, byte-preserves numbers/timestamps, dedups by content
hash) and served via `GET /api/media/{file...}` (per-user, Range-capable). Boot-time `Migrate()`
folds the old flat inline-base64 layout into this.

Playground requests are exempt from `adminChain` (own port + login) — see
[`http-core.md`](http-core.md).

## `turns.go` — the turn runner

A turn is a **server goroutine** (`turnManager`, one `activeTurn` per user) streaming ONE completion
plus the whole tool loop, the reasoning-budget finalize and the qm-tools approval gate — straight
into `chats.json` (the single source of truth, merge-guarded via `guardedChatsPut`) and to any
attached SSE viewer. A closed or refreshed tab neither loses nor stops the answer.

Endpoints: `POST /api/chats/turn`, `GET .../stream` (SSE snapshot + tail), `/state`, `DELETE`
(stop), `POST .../approve`. The self-call loops back through the normal proxy with the configured
API key injected. See `turns_design.md` for the full design.

- **The client MUST call `DELETE` to stop.** Aborting the SSE fetch only detaches the viewer.
  `handleTurnStop` blocks until the runner is actually done, so an immediate re-send can't race into
  a 409.
- **Repeat tool calls are deduped per turn** (`doneCalls`, keyed name+args): the second identical
  call gets the first result back with a note, re-executes nothing and paints no second card — weak
  models re-issue the same channel listing round after round.
- Search-card `kind` distinguishes the three YouTube tools (`youtube` = transcript,
  `youtube-search`, `youtube-comments`), because one shared kind labelled a metadata listing as if
  the video had been watched.
- **`at.lens()` returns UTF-16 code-unit lengths, not bytes** (`utf16Len`). `turnSearch.At` /
  `ReasoningAt` are split points the UI applies with JS string indices, so a byte offset drifted
  right by one unit per emoji and dropped tool cards inside a word.
- A **`busy` delta** carries the live activity label (`busyLabel`: "Searching for …", "Reading
  example.com") the UI shimmers next to the source counter — set before each tool runs, cleared
  after, and **replayed in `subscribe()`'s snapshot** so a reattaching tab does not sit on a stale
  or missing status. Instant local tools (time, math, units) get `""`: a label that flickers for
  3 ms is noise.

### Reasoning-box titles

Generated **per box, as it closes** (`startTitler`/`titleJob`, `titlegen.go`):

- `reasoningTitle` for the field-based trace, queued when the answer's first content token arrives —
  the field can take no more text after that.
- `thinkTitles`, one per inline `<think>` span (capped at `titlegenMaxSpans`) — queued from
  `closeInline` for a server-spliced span, and from `queueTitles` for a span the MODEL wrote as
  literal tags in its content, which has no close event of its own: there the close IS a content
  token, so `endedThinkSpan` tail-checks each delta (never the whole content) and the scan runs once
  per closed box, not on a timer.

Fanned as a Replace-style `titles` delta, replayed in `subscribe()`'s snapshot, persisted on the
assistant message. **A closed box is final, so titling it does not wait on the turn** — a tool loop
runs for minutes, and end-of-turn-only titling left every finished box on the UI's local heuristic
for the whole run. The still-open box is never queued: a title of a half-written thought is wrong
by the time the thought finishes.

One serial worker per turn with a `titleQueueDepth` backlog and non-blocking sends, so titling can
never fan out CPU processes against the generating model or stall the stream. `titleReasoning` then
**stops the worker and fills the gaps** between `endInline()` and the final `flush()`. A cancelled
turn drains its queue and skips titling entirely.

That mop-up sits between the last token and the turn's `done` event, and the UI gates the whole
message footer (divider, word counts, regenerate/copy/speak) on `done` — so every second spent
there is a second the finished answer sits on screen looking half-rendered. Per-title timeouts
alone bounded it at `titlegenMaxSpans` x `titlegenTimeout`, and this is the machine's worst moment:
the titler spawns the GPU-linked llama binary (CPU-only, but it still enumerates devices) while the
router may be swapping a model in for someone else. So the pass gets **one** deadline for the whole
list, `titlegenMopupBudget` (2.5s); spans it doesn't reach keep the UI's local heuristic.

## `titlegen.go` — the title model

One-line titles for collapsed reasoning boxes ("Thought for 2s · Weighing the two quant options").
`POST /api/chats/title` also names a whole conversation, but **the playground no longer calls it**:
at 80M the model tail-copies a chat opener instead of naming its topic, so `chatCompact.ts`
`generateTitle()` went back to the chat model (warm by definition — the title is made right after
that model's first answer). The route stays for other clients and answers 200 with `""` when
unavailable.

**FLAN-T5-small** (80M, ~79 MiB Q8_0, `assets/titlegen-flan-t5-small-q8_0.gguf`, **`go:embed`ed**
so it exists in every install) run **exec-per-request on CPU** (`-ngl 0`, mutex-serialized, 4s
timeout — the mutex is taken *before* the clock starts, else a run queued behind another one is
killed on arrival and logged as a failure it never had) — no scheduler entry, no VRAM, no group eviction. Routing a title through the loaded chat
model would swap a model in, or contend for the slot, to produce six words of chrome.

- **It must run under `llama-completion`, never `llama-server`**: llama.cpp's server has no
  encoder-decoder path (never calls `llama_encode`), so a T5 gguf 400s or asserts there. Hence the
  CLI shell-out and the `siblingExe` lookup next to the ★default `llama`-kind registry entry (falls
  back to `llama-cli`).
- **Model resolution**: `QM_TITLEGEN_MODEL` env → else extract-once (write+rename, size-checked)
  into `<dir(-generate)>/titlegen/`, deliberately OUTSIDE the models root so discovery never
  publishes it as a servable model.
- **Reads stdout only** (`cmd.Output()`): the CLI's timestamped load/perf log goes to stderr, and
  merging the streams makes the first "output" line a log line.
- Best-effort by contract — no model, no CLI, a timeout or garbage all leave the UI on its local
  `thinkSummary()` heuristic.
- **Refusal filter** (`sanitizeTitle`): the model sometimes *answers* the trace instead of naming it
  ("I'm sorry", "As an AI language model", "There is no ..."), titling a quant-comparison box with an
  apology. Such titles are dropped to "" — unless the source text itself is about apologizing —
  so the box falls back to the local heuristic.
- **Why FLAN and not a title fine-tune:** an article→headline model (tried at both t5-small and
  t5-base) is extractive in practice — it emits the input's own opening clause, which reads fine on
  reasoning prose but turns a chat opener into a truncated copy of the request. Instruction tuning
  plus the few-shot `titlegenShots` prompt gives an abstractive topic phrase and covers both callers
  with one model.

## `turnstools.go` — server-side tool + reasoning helpers

Server-side ports of the playground's tool and reasoning helpers so the turn runner drives the
model→tool→model loop headlessly; behaviourally identical to the client originals (`wiki.ts`,
`webSearch.ts`, `reasoning.ts`). Holds `searchWiki` over the **embedded** wiki corpus
(`//go:embed wiki_articles.json`, copied from `ui-svelte/src/lib/` by the Makefile `ui` target —
one source, two consumers) and `formatSearchResults`.

`parseSearchCount` lets the model pick how many results it gets (`count`/`num_results`/`limit`,
clamped 1–10, default 5): a fixed 5 is one or two real candidates once duplicates and listicles are
dropped, too thin for a shortlist. The count is part of the result cache key, so a wider re-ask is
not served the narrow cached answer.

### Anti-fabrication guards

Models invent YouTube videos — plausible title, plausible 11-char id, no tool call. Two layers,
wired in `runLoop`:

1. **`pastedNewVideo`** — the last user message links a video id seen nowhere earlier → force
   `tool_choice:"required"`, **round 1 only**.
2. **`unverifiedYtIDs`** vs ids seen in the conversation or any tool result → `unverifiedVideoMarker`
   **appended**, since deltas are append-only and a streamed answer cannot be retracted.

Both triggers are structural on purpose. A forced call removes the model's option to answer *or* to
ask which video is meant, so it fires only where a lookup is provably the only correct move —
naming YouTube ("why does it compress so hard?", a follow-up on an already-read transcript) is a
topic, not a task. A third layer — a regex spotting "let me fetch those now." with no call, nudging
the model to follow through — was **deleted**: matching English prose false-fired on finished
answers and burned a round on the model disputing the nudge. The marker layer is a check on output,
not a guess at intent, and catches the only failure that actually misleads.

## `turnsreplay.go` — tool-call replay into history

The client sends prior turns as role + content only. The calls they made and the results they got
live in the UI's `searches` metadata and never reach the model, so its own history shows nothing but
prose and no evidence a tool was ever called or ever worked. **Models copy that**: in one thread the
model spent three turns saying "let me grab the remaining three now" with zero calls.

`replayToolCalls` rebuilds each such turn as assistant-with-`tool_calls` → the real stored results as
`tool` messages → the answer it wrote, giving back both the evidence and the example. It runs only
when tools are on (a `tool_calls` message with no tools is unanswerable).

- Truncation is **per-result and fixed** (`replayResultMax`), never a whole-history budget: a shared
  budget would re-trim older results as newer turns spent it, changing the prompt prefix and voiding
  the conversation's KV cache on every message.
- URL-taking tools replay with `Sources[0].URL`, not `Query` — `Query` holds the resolved *title*
  after a successful fetch.
- `quartermaster` searches are skipped: a config action is not reference data, and the kind doesn't
  say which of the two QM tools ran. `time`, `units` and `memory` are skipped too — see below.

## `turns_qm.go` — the "quartermaster MCP"

Dispatch for the `quartermaster_inspect` / `quartermaster_configure` chat tools (advertised in
`ui-svelte/src/lib/qmTools.ts`). Both call quartermaster's OWN loopback API (`pg.SelfBase`) with the
turn's injected key, reusing existing handler validation + regen/reload and the
501-without-`-generate` gate. Deliberately **no load/unload** — swapping would evict the model
answering the chat.

A `configure` builds a fully-resolved `configPlan` **before** the approval gate, streams the
before→after `qmDiffRow` card (`kind:"approval"`, re-sent on reconnect), and applies only on accept.
Unanswered calls drop after `approvalTimeout` (5 min) so a closed tab can't wedge the single turn
slot.

- Raw responses handed to the model are capped at `qmBodyLimit` (24 KB); a body this package
  **parses** (`qmGetInto`) reads to `qmJSONLimit` (4 MB) instead and reports hitting it as an error.
  Sharing the 24 KB cap cut `/api/catalog` mid-object on a real fleet, and the tool answered
  "Couldn't read the model list: unexpected end of JSON input".
- One `qmDo` behind `qmReq`/`qmGetRaw`/`qmGetInto`, so the signed per-user cookie is minted the same
  way on every path.
- `target='models'` reads **`/api/catalog`, not `/v1/models`**: the discovery route is filtered by
  the scope of whatever API key `pickSelfKey` picked, so with scoped keys configured the tool
  answered "what models do I have?" with just the key's slice.
- Synthetic variants (`base@ctx32768`, `base@<backend>`) are folded under their base and counted
  only there; the model-id target is where they get detail. `qmModelVariants` finds the siblings in
  the catalog and `qmCmdDiff` reports each one's launch-flag deviations from the base (changed
  values, added/dropped flags, swapped exe) instead of just naming ids. Capped at
  `qmMaxDiffedVariants` (8), since each diff costs a loopback `/config` call; inspecting a variant id
  runs the diff the other way, against its base.
- Two targets exist so a tuning can be checked before it is proposed: **`target='estimate:<model
  id>'`** runs the `/api/models/{id}/estimate` sizer — what-if knobs go in a single free-form
  `options` object, because one whitelist server-side beats six top-level schema properties sitting
  in every conversation's KV-stable prefix; omitted knobs are **re-derived, not inherited**, so a
  bare estimate is the auto plan and `actual:true` is the loaded one. And **`target='backends'`**
  reads `/api/backends` for the exe/managed build per class, the ★ class default, and any exe
  missing from disk — a whole class of spawn failure invisible from a model's config.

**`turns_qm_fields.go`** — the editable-field catalog those tools advertise, **derived from the DTOs
by reflection** (`qmSpecsOf` over `overrideDTO`/`variantDTO`) so the tool surface is exactly the
cogwheel's surface, with no hand-kept list to drift (an earlier hand-written one made a model write
`--chat-template-file` into `extraArgs`). Pointer fields render as `T|null` (= inherit/auto).

## Assistant memory

**`memories.go`** — per-user standing facts carried between conversations. Unlike chats and prefs
this is **NOT a client-owned blob**: both the browser (Settings → Memory) and the model write it, so
a whole-array PUT from a tab that loaded before a `memory_save` would silently revert it. The server
owns the list (`DataDir/users/<user>/memories.json`) and every mutation is a per-entry
`upsertMemory`/`deleteMemory` under `p.mu`.

- A non-empty `id` matching nothing is an **error, not a create** — a wrong id means the model is
  editing a memory it never read.
- Past `maxMemories` (200) a save errors telling the model to delete one first. Nothing is
  auto-evicted: dropping a fact the user asked to keep is worse than refusing. `maxMemoryLen` is 800
  runes.
- `POST` force-sets `Source:"user"` — only the tool path may claim `"assistant"`, which is what lets
  the UI show what the model decided to remember on its own.
- Errors are `memErr`, written for the MODEL to read (say what to do next) and reused as the 400
  body.

**`turns_memory.go`** — dispatch for the `memory_save` / `memory_delete` tools (advertised in
`ui-svelte/src/lib/memoryTools.ts`). **Write-only by design — there is no `memory_read`/
`memory_list` tool.** Recall is by injection: the client renders `memoryBlock` into the system
prompt every turn, so the facts are already in front of the model, while a read tool would sit in
every conversation's KV-stable prefix and only fire when the model thought to call it. A write does
change the system prompt, but the block is rendered append-only, so an ordinary save moves the KV
divergence point to the tail rather than to line one.

**Dedupe lives here, not in the prompt.** An idless `upsertMemory` first scans for a near-duplicate
(`memoryDuplicateOf`: normalized-equal, whole-string containment, or ≥0.8 word-set Jaccard). A
verbatim restatement writes **nothing at all**, not even `UpdatedAt`; a near-restatement folds in —
longer text wins, tags union, `CreatedAt` and `Source` survive. The outcome
(`memoryCreated`/`Updated`/`Merged`/`Duplicate`) comes back to `memorySave`, which words the result
accordingly: on a duplicate it explicitly tells the model NOT to announce a save that did not
happen. This is what lets the prompt drop the old "check your block first" rule.

No approval gate (unlike `quartermaster_configure`): writes are frequent and reversible, and
visibility comes from the chat card plus the Settings panel. The save result **states that the new
memory is not in the block it was given this turn**, or a model that re-reads its context concludes
the save failed and saves again.

`"memory"` does **not** replay into history (`turnsreplay.go`) — the recorded query is the fact, not
the call's arguments, and a replayed `memory_save` reads as a second save. `time` and `units` are
skipped for the mirror-image reason: they record a rendered label ("15.6 in → cm") rather than their
arguments, so replaying them would put a call in the transcript that could not have been made.
