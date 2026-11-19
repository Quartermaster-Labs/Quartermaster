# internal/server — the playground app (fork)

The standalone chat/image/speech app served on `-playground-port`, and the **server-owned turn
runner** behind it. The tools' own fetch paths are in [`tools.md`](tools.md); the browser half is
`ui-svelte/playground-chat.md` / `playground-tools.md`. Route list in [`routes.md`](routes.md).

## `playground.go` — the app and its storage

`Playground` struct + `SetPlayground`/`markPlayground`; plaintext per-user login (`pg_user` cookie,
`users.json`), server-backed chat history + prefs, and `/api/mode` so **one bundle serves dashboard
or playground per port**.

**Storage** is per-user `DataDir/users/<user>/{chats,imagechats,videochats,speechchats,musicchats,musicscores,prefs}.json`. Generated
media (inline `data:` base64) is split out on write into `media/<hash>.<ext>` (`extractMedia`, a
regex over raw bytes — structure-agnostic, byte-preserves numbers/timestamps, dedups by content
hash) and served via `GET /api/media/{file...}` (per-user, Range-capable). Boot-time `Migrate()`
folds the old flat inline-base64 layout into this.

`musicchats.json` never holds inline audio. A song is tens of MB and every history store PUTs its
whole list, so a thread of inline takes would hit `maxBlobBytes` after a handful. The Music tab
uploads each result first (`POST /api/media`, `handlePlaygroundMediaUpload`: audio or image
content types only, 256 MB cap, named by content hash, so a re-upload is a no-op) and the thread
carries only the returned `/api/media/...` ref. That opens a window where a file exists but no
JSON names it yet, so `gcMedia` skips anything younger than `mediaGCGrace` (10 min), and a
re-upload of an existing file touches its mtime to restart the clock.

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

- **Compaction rides the chat's own KV** (`POST /api/chats/compact`, `turnscompact.go`). The client
  posts the exact turn body it would send next (system prompt, history, tools) with the summary
  instruction appended as a final **user** message (Qwen templates reject a non-first system
  message); the server runs the same `inlineMedia` + `replayToolCalls` assembly under the same
  `X-Conversation-Id` (shared `selfCompletionRequest`), so the prefill is just the instruction.
  The old standalone summary request was a different conversation to the slot cache: it evicted
  the chat (multi-GB save on a hybrid) and the next turn restored a snapshot longer than the
  compacted body, which `staleRestore` then had to throw away. Tools stay in the body and
  `tool_choice` stays unset: llama.cpp drops the tool list for `"none"`, which rewrites the system
  block and voids the whole prefix. Synchronous, not persisted: the client still owns `summary` +
  `compactedCount`.
- **The client MUST call `DELETE` to stop.** Aborting the SSE fetch only detaches the viewer.
  `handleTurnStop` blocks until the runner is actually done, so an immediate re-send can't race into
  a 409.
- **Repeat tool calls are deduped per turn** (`doneCalls`, keyed name+args): the second identical
  call gets the first result back with a note, re-executes nothing and paints no second card — weak
  models re-issue the same channel listing round after round.
- **A turn that only repeats stops calling tools.** The repeat guard re-executes nothing, so no
  per-tool cap ever counts a repeat, and a model that ignores the note loops forever (2026-11-19:
  one `calculate` call about a hundred times, a round a second, until the model was unloaded).
  After `turnMaxDupRounds` (2) rounds in a row made only of repeats, or `turnMaxRounds` (64)
  rounds of any kind, the runner appends `toolLoopNudge` at the tail (KV prefix kept) and sends
  `tool_choice: "none"`. A model that calls a tool anyway ends the turn with `toolLoopMarker`.
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
- A **`prefill` delta** (`done`, `total`) is prompt-processing progress, shown as a percentage beside
  the label ("Processing prompt 46%", or after the digest label). Streamed rounds ask for
  `return_progress`; llama-server answers with one token-less data frame per batch carrying
  `prompt_progress {total, cache, processed}`, and `readSSE` reports `processed-cache` of
  `total-cache`. **That frame must not count as the first token**: it arrives as prefill STARTS, and
  treating it as data would clear the digest label before any of the prompt it names was read.
  Shown only from 2048 new tokens (`prefillShowTokens`), deduped to whole percent, cleared at the
  first token and at round end, and replayed in the snapshot like `busy`. Digest parts are
  non-streamed calls, so they show the part label only.

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

That mop-up sits between the last token and the turn's `done` event, so the UI is **not** told the
answer is finished by `done`. `run` fans an **`answer`** delta (with `genMs`) the moment the prose is
final, before the mop-up, and `subscribe()` replays it — everything the bubble holds back until the
reply stops moving (the footer, Sources, the rendered diagram/SVG cards, the ask wizard) keys off
that, while the composer stays locked on `done` because the server still owns one turn per user.
Without it every second in the mop-up was a second the finished answer sat on screen looking
half-rendered. Per-title timeouts
alone bounded it at `titlegenMaxSpans` x `titlegenTimeout`, and this is the machine's worst moment:
the titler spawns the GPU-linked llama binary (CPU-only, but it still enumerates devices) while the
router may be swapping a model in for someone else. So the pass gets **one** deadline for the whole
list, `titlegenMopupBudget` (2.5s); spans it doesn't reach keep the UI's local heuristic.

### Scheduler hold headers

`streamSSE` stamps two headers the router's scheduler reads (see
[`../router/CLAUDE.md`](../router/CLAUDE.md)):

- `X-QM-Hold-Ms` — `playgroundHoldMs` when the round offers tools (it may come back for another
  round, and between rounds the scheduler cannot tell a mid-loop pause from a finished
  conversation), `0` when it does not, since a tool-less turn cannot loop and should release the
  GPU immediately.
- `X-QM-Patience-Ms` — `playgroundPatienceMs` (60s), far below the 5-minute default: someone is
  watching this answer arrive, so it waits much less behind a background agent's hold than an
  unattended API client would.

### The "Waiting its turn" label

The router narrates a pre-stream wait in SSE comment frames, and `streamSSE` reads exactly one of
them: `: qm-status: waiting <pos>` / `: qm-status: loading` (see
[`../router/CLAUDE.md`](../router/CLAUDE.md)). `waiting` raises the ordinary busy label —
"Waiting its turn", plus `(#N)` when something else is queued ahead — so the user sees that nothing
is wrong and the answer is not lost, only behind another model. `loading` leaves whatever label is
up alone (the UI says "Loading model…" from its own residency signal, but only on a bubble with no
text yet — on a later round the digest label below is the one to keep). The first `data:` frame
clears the label unconditionally: a token ends every wait, and every prefill, by definition.

The router only narrates a wait it can see. A model that is already loaded passes it at once, and if
every llama-server slot is busy (`--parallel 1`: another user's turn, a digest, an API client) the
request then queues INSIDE llama-server, silently, for as long as the other request takes. A round
that asked for `return_progress` covers that: llama-server sends its first `prompt_progress` frame
the moment a slot takes the request, so `watchQueue` raises the same "Waiting its turn" when no byte
has arrived after `queueLabelDelay` (1.5 s) and `Server.slotsFull` says the model has more requests
in flight than `-np` slots. The first byte ends the watch. A command without `-np` is never "full"
(llama-server's default is not 1, and other backends batch).

### The digest label ("Reading through the page")

A tool's label comes down the moment the tool returns, but the model then has to **prefill** what it
returned before it can write a token — seconds of dead air for a long page, with the tool card
itself sitting inside a collapsed reasoning trail. So a round whose tool produced ≥
`digestMinChars` (1500) of text raises `digestLabel(name)` ("Reading through the page / the
transcript / the results") for the length of that prefill, cleared by the next round's first token.
Short results (a date, an FX rate) get none: a label that flashes on and off reads as a glitch.

## `turnsdigest.go`: a message too long for the window

Not to be confused with the digest LABEL above. A pasted wall of text or an attached book can be
longer than the model's context, and compaction cannot help: it folds OLD turns and always keeps
the newest message verbatim. So before round 1, `run` calls `digestOversize`, which rewrites only
the last user message, and only when the turn opted in (`turnStart.DigestAt`, the client's
`COMPACT_AT`; a rewrite turn sends none) and the prompt would pass `DigestAt` of the window.

- **The window is resolved server-side** (`Server.conversationCtx`, `apigroup.go`): the per-slot
  `-c` off the command the model is RUNNING under, else the configured one. `perSlotCtx` is shared
  with `/v1/models`, so the catalog and the digest cannot disagree. Never taken from the body.
- **Task first.** One cheap call sees the first and last ~2k tokens and answers
  `<task>...</task>` or `<task>NONE</task>`. Every part is then read FOR that request, because a
  blind summary keeps the gist and drops the one line the question was about. Each part's prompt
  also asks the model to copy out any request it meets, which catches one buried mid-text; those
  are used only when the probe found none.
- **Map, then reduce.** Parts (`splitChunks`: paragraph > line > sentence > space, UTF-8 safe)
  are ~45% of the window each and read independently. Only if the notes together overflow are
  neighbours merged, at most `digestMaxReduce` (3) times. Rolling refine ("summary so far + next
  part") was rejected: it re-summarizes part 1 once per part.
- **Note size comes from the room, not a constant** (`noteTarget`, `noteMaxTokens`). Each note is
  asked for two thirds of an even share of the free room, capped at a sixth of its part
  (`digestMinCompression`), floored at 256. `max_tokens` is the target plus half again, because
  models overrun a stated length and a note cut at `max_tokens` loses its END, which is the end of
  its part. A flat 3000-token cap did exactly that to a 4-part book on a 131k window: 12k of ~96k
  tokens of room used, every note cut, and the answer listed the chapters it never saw. The cost is
  decode time: four notes near 9.7k each is ~14 minutes of decode at ~45 t/s, against ~4.5 before.
- **The ending stays verbatim** (`tailCut`, up to 4k tokens): the likeliest place for the
  question, and paraphrasing the question is the worst loss there is.
- **Tokens are estimated from bytes**, starting at 3.5 bytes/token and recalibrated from the
  probe's `usage.prompt_tokens`. Inlined images count a flat 1024 tokens, never their base64
  bytes. A part that still overflows is halved and retried (`digestMaxSplits`).
- **Overflow fallback.** If the estimate said "fits" and llama-server refuses anyway
  (`exceed_context_size_error`, which comes before any prefill, so the miss is free), `run`
  digests with `force` and llama-server's exact `n_prompt_tokens`, then retries once. Only while
  the turn is `pristine()`: an overflow after a tool round is caused by the tool results, and
  rerunning would repeat the searches.
- **Calls** are non-streamed, thinking off, temperature 0.2, under
  `X-Conversation-Id: <chat>#digest`, so the parts share a KV prefix (one system prompt for all)
  without being mistaken for the chat. The cost is one slot-cache switch away from the chat and
  back. They send `X-QM-Hold-Ms`, which `selfCompletionRequest` omits for a tool-less body, so the
  model is not swapped out between part 3 and part 4.
- **Persisted** on the user message in chats.json as `digest` (the exact replacement text) and
  `digestInfo {parts, tokens, task}`. The client sends `digest` in place of the content from then
  on (`forModel`), byte for byte, so the next turn reuses the KV instead of reading the text
  again. Editing the message drops both fields.
- **Refusals carry numbers**: over `digestMaxParts` (64) parts, a context too small to hold a
  part, a history that already fills the window (compact first), or notes that still overflow
  after the merges.

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

**The rebuild is the fallback, not the first choice** (`turnsrecord.go`). What it produces is close
to, but not the same bytes as, what the turn loop forwarded when that turn ran: live puts the
round's prose ON the `tool_calls` message (the rebuild empties it and glues that prose onto the
front of the answer), sends the full result plus its cite reminder (the rebuild truncates at
`replayResultMax` and appends `replayNote`), and uses the model's own tool-call ids (the rebuild
invents `hist_i_j`). So a turn served with one set of bytes came back as another on the next
message — **the prompt prefix changing retroactively, mid-conversation**. On plain attention that
is a tail reprefill; on a recurrent/hybrid arch, which cannot rewind to an arbitrary earlier
position, it is a total one: measured on qwen3.8-27b, `cached_tokens: 0` and 96 s of prefill on a
56k prompt whose history was identical but for one tool block five messages back.

So each turn records the exact tail it forwarded — assistant-with-calls, tool results, any nudge —
and the next turn splices those bytes back in.

- **Keyed by the turn's own search metadata** (`replayRecordKey`: chat id + each search's
  kind/query/results), never by message index — compaction and edits shift indices, and a shifted
  index would splice one turn's results under another turn's answer. Both sides compute it from the
  same bytes, since the client hands the metadata straight back.
- **`spoken` / `trimSpoken`.** The client concatenates every round's content into ONE stored
  message, so replaying it whole would send the round prose twice — once inside the recorded
  `tool_calls` message, once at the front of the answer. The record carries what it already said and
  takes it back off. That is now only the fallback for a record with no `final`.
- **`reasoning_content` is part of the bytes** (`assistantRound`). Qwen templates render an
  assistant message's thinking as `<think>` + `reasoning_content`, so a message without the field
  goes up as an EMPTY think block while the KV holds the real one. Every tool-round message carries
  its round's reasoning, and the record keeps the answering round verbatim as `final`, which the
  replay sends instead of the client's stored answer (that one merges all rounds' thinking into one
  box plus inline `<think>` spans in `content`, so the final round's reasoning is not recoverable
  from it). Measured on qwen3.8-27b, one tool round: 606/606 tokens reused with the field, 546/606
  without, and the loss recurred every round and again at the next turn.
- **In memory, LRU-bounded** (`replayStoreMaxEntries` / `replayStoreMaxBytes`). Persisting it would
  put megabytes of tool output into chats.json for the client to sync on every read, to save one
  reprefill per conversation per restart. A miss — restart, eviction, an imported chat — falls back
  to the rebuild, which is what every turn did before.
- Recorded from a `defer`, so an errored or cancelled turn still replays the results it did get.
- A hit replays a turn's tools **whatever their kind**, including the `memory`/`time`/`units`/
  `quartermaster` calls the rebuild skips. That is not a regression of the rules below: those exist
  because a *reconstructed* call is a claim about something that may never have happened, while a
  recorded one is what upstream already has in its KV.

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

**A paraphrase is escalated, not merged.** The same fact in different words shares its content words
but not its shape, so it lands nowhere near 0.8 and two entries would otherwise coexist forever.
Loosening the merge threshold is the wrong fix — a merge is lossy. Instead `nearestMemory`
(content-word Jaccard >= `memoryNearJaccard` 0.3, at least `memoryNearMinShared` 2 shared words,
stop words including `user` stripped) finds the closest existing entry on a **create** and
`memorySave` names it in the tool result, with the two calls that would merge them. The model is
good at judging two texts put in front of it and bad at scanning its whole block unprompted, so the
check happens after the write, on one specific pair, only when there is something to decide.

No approval gate (unlike `quartermaster_configure`): writes are frequent and reversible, and
visibility comes from the live `Remembering that` / `Forgetting that` label (`busyLabel` +
`digestLabel` in `turns.go`, the only digest labelled on kind rather than on result size — a memory
write returns two lines instantly, and without it the write and the prefill after it read as an idle
chat), plus the chat card and the Settings panel. The save result **states that the new
memory is not in the block it was given this turn**, or a model that re-reads its context concludes
the save failed and saves again.

`"memory"` does **not** get REBUILT into history (`turnsreplay.go`) — the recorded query is the
fact, not the call's arguments, and a rebuilt `memory_save` reads as a second save. (A turn still in
the verbatim record replays its real call and its real result, which says the memory was stored.) `time` and `units` are
skipped for the mirror-image reason: they record a rendered label ("15.6 in → cm") rather than their
arguments, so replaying them would put a call in the transcript that could not have been made.
