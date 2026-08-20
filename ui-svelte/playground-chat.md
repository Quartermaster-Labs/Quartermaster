# ui-svelte — playground chat

`src/components/playground/ChatInterface.svelte` + `ChatMessage.svelte`. Tools, modes,
attachments and memory are in [`playground-tools.md`](playground-tools.md); image/speech
studios in [`playground-media.md`](playground-media.md).

## `ChatInterface.svelte`

Chat with **vision** (paperclip image attach → `ContentPart`/`getImageUrls`), **Rewrite mode**
(`sendRewrite` + side-by-side `RewriteDiff.svelte` via `lib/wordDiff.ts`; tools are off but
**reasoning is not** - deciding what to keep, cut and re-register is the hard part of a rewrite,
and a no-think transform came back visibly worse), **web search**
(`lib/webSearch.ts` → `/api/websearch`), a live KV context-usage bar, and **auto-compaction**
(`lib/chatCompact.ts`: `summarizeConversation`/`generateTitle`, `COMPACT_AT`/`KEEP_RECENT`).

Pure helpers live in `chatHelpers.ts` (`quotePrefix`, `TEMP_STEPS`/`TEMP_LABELS` +
`nearestTempIdx`, `currentDateLine`, `REWRITE_SYSTEM`, attach limits + `validateImageFile`/
`fileToDataUrl`).

### Generation is server-run

The client POSTs to `/api/chats/turn` and subscribes to `/api/chats/turn/stream` (SSE). The
server owns the turn — tool loop, reasoning budget, qm-tools approval — writes into `chats.json`,
and **survives a closed/refreshed tab**; the tab reattaches on reload. See
`internal/server/playground.md`.

While a tool runs the header shows a **live activity label** shimmering next to the source
counter — the server's `busy` delta (`busyLabel` state, `""` = idle) — because a bare counter
ticking from 2 to 7 with no other movement reads as a stall. The label is replayed in the stream
snapshot, so a reattached tab shows what the turn is doing *right now*, not the last thing it
finished.

### Reasoning effort

The composer's **Reasoning** control is a dropdown, not a checkbox, because effort is a chat-*template*
feature: a model's template validates the value against its own list and `raise_exception`s on anything
else. The options are therefore whatever the server advertised as `capabilities.reasoning_effort` on
`/v1/models`, never a ladder hardcoded here; a model that advertises none falls back to plain **None/On**.
`lib/effort.ts` owns the pure part — `effortOptions` (None first, ladder sorted cheapest-first),
`resolveEffort` (maps one persisted pick onto whatever THIS model accepts, defaulting to **medium** rather
than the template's own default, which is the top rung), and `requestEffort` (what reaches the wire).

One store (`reasoningEffortStore`) covers both worlds: `"none"` is thinking off, `"on"` is thinking with no
level, anything else is a level. It follows the user across models and is resolved per model, so switching
to a model with a different ladder never sends it a level it would 500 on.

The value rides to the server as `reasoningEffort` on `/api/chats/turn`, which forwards it as the standard
top-level `reasoning_effort` and lets the request filter translate it — the same path an external client
takes. Note that **Settings → Thinking Budget is disabled for a model with levels** (the server skips it):
the level already is the budget, and cutting thinking off between rounds would rewrite the template's system
block mid-conversation. Switching level mid-chat does the same, so it is a per-conversation choice, not a
per-message one.

### What gets saved (`stores/chatHistory.ts`)

The store holds every session the tab has open; **persistence is filtered, not the store**
(`isDisposable`, applied by `keepable()` inside `pushChats`/`saveChatsNow`). This kills the two
kinds of junk history: the blank session "New chat" just made, and a turn where the model never
answered and the bubble is nothing but `**Error:** ...`.

The predicate is deliberately written as *is this positively junk*, not *is this good enough to
keep* — a PUT is destructive (the server GCs the media of any session that vanishes from the
array), so an unrecognized shape falls through to KEEP. A session is disposable only when it has
no messages at all, or when **no** assistant turn in it produced anything (text, reasoning,
searches or a tool call). Consequences worth knowing:

- The error tail both the client and `turns.go` append is stripped before the check, so a reply
  that streamed prose and *then* died is kept — as is a chat whose earlier turns answered and
  only the last one failed, and an answer that merely quotes an `**Error:**` mid-prose.
- A chat carrying per-chat `instructions` is kept even with no messages: setting them is a
  deliberate act, not a stray "New chat" click.
- The live turn is exempt. `saveChatsNow(id)` takes the id of the turn about to start — at that
  moment the session is only a user message plus an empty assistant bubble, and the server needs
  it on disk to have somewhere to write; `pushChats` exempts `generatingChatId` for the same
  reason during streaming. As a second net, `guardedChatsPut` re-appends the generating session
  if a PUT ever omits it.
- The filter runs over the whole array, so junk saved by older builds is pruned on the next PUT.
  An errored chat therefore disappears on the next flush after the turn ends, but stays on screen
  until the tab is reloaded.

### Per-chat model memory

A session records the model it ran on (`ChatSession.model`, written by `rememberModel()` on an
explicit picker change and at every turn start); switching chats re-selects it, guarded by
`lastAppliedChat` so it fires only on an actual switch, never over the user's own pick.

Typing **warm-loads** the selected model after ~500 ms (`loadModel`, once per model, skipped when
ready/starting or streaming) so the first token doesn't wait on a cold swap. The dashboard's Chat
button (`?model=`) opens a **fresh** session pinned to that model (`startChat(model)`) rather than
repointing the open thread.

### Autoscroll

Follows the newest content off a **ResizeObserver on the message-list content wrapper**, not off
`messages` alone: collapsing a reasoning/tool box mid-stream resizes the list without touching the
array. The container carries `overflow-anchor: none` because Chrome's scroll anchoring fights a
pinned-to-bottom list and could strand the view above the still-growing reply until a reload.

`userScrolledUp` is re-derived from the live scroll position on every event — a "this scroll was
ours" flag leaked (assigning `scrollTop` to where it already is fires no event) and swallowed the
user's next real scroll.

## `ChatMessage.svelte` — reasoning headers + Sources

A collapsed thought box reads `Thought for 2s · <gist>` — or `Searched the web · <gist>` /
`Read 2 pages · <gist>` when the box ran tools.

**The verb half is deterministic, not generated.** `reasoning.ts` `activityLabel()` reads the
kinds of the tool cards nested in that box (`boxActivity`), so it is true by construction. Asking
the title model to phrase the activity instead was built and measured, and every verb-leading
prompt degraded an 80M model into tail-copying and self-repetition (recorded on `titlegenShots`
in `internal/server/titlegen.go`). Instant local tools (time/calc/units/currency) are excluded — a
verb for a unit conversion is noise beside a real gist — and a box with no tools keeps the duration.

**The gist half comes from two places.** While the turn streams it is `lib/reasoning.ts`
`thinkSummary()` — local heuristics (strip stacked openers "Okay, so, let me…", first sentence long
enough to mean something, word-boundary truncation), instant and free. The server then sends
`titles` deltas carrying `reasoningTitle` + `thinkTitles[]` from a vendored 79 MiB CPU title model
(FLAN-T5-small, `internal/server/titlegen.go`) and those overwrite the heuristic per box —
**one box at a time, as each box closes** (a closed box's text is final; only the open one is still
moving), with a gap-filling pass at end of turn, so a long tool-looping turn doesn't leave every
finished box on the heuristic until it is over.

A delta is Replace-style and carries the whole array, blanks included; `thinkTitles` is indexed by
span ordinal exactly like `thinkMs`, and coalesced think rounds keep the first round's title. Shown
only while collapsed. **Never the chat model**: that round trip would swap a model into VRAM (one
GPU, one pool) for a label.

**Conversation titles are the opposite call.** `chatCompact.ts` `generateTitle()` asks the chat
model directly. It routed through `POST /api/chats/title` first and the titles were bad — an 80M
model summarizing prose it was handed works, naming a topic from a chat opener does not, it copies
the opening clause. The VRAM argument doesn't apply either: the title is generated right after that
model streamed the first answer, so it is already resident.

**Sources** renders only when `!isStreaming` — mid-stream it pins under the growing answer and its
count churns with every tool round.

## `ChatMessage.svelte` — read-aloud

The speaker button under an assistant reply POSTs `/v1/audio/speech` (`lib/speechApi.ts`) with
`effectiveTtsModel` and the Speech tab's voice.

- **Chunked + pipelined** (`speakStreamed`): a speech request only returns once the *whole* clip is
  synthesised, so speaking a long reply in one POST means waiting out the entire answer before the
  first word is audible. `splitForSpeech` cuts the prose on sentence boundaries — a deliberately
  short first chunk (140 chars) so sound starts fast, then 190 and 240 — and one request stays in flight
  ahead of playback, so all but the first chunk are synthesised while earlier audio plays. Stopping
  aborts the pending request, which is why `SPEAK_MAX` can be generous (12k chars): an over-long
  reply is no longer one multi-minute request that has to finish before anything happens.
- **Replayed, not regenerated**: the chunks are cached per message under `model|voice|text`, so a
  second click on the speaker plays instantly and a voice change in Settings (or an edit to the
  message) invalidates the cache. Capped at 24 MB per message — past that, later chunks still play
  but aren't kept.
- **Generated** vs **not**: the speaker icon brightens to `text-txtmain` once every chunk of the
  current `model|voice|text` is cached, i.e. a click will replay without touching the model.
  `speakStreamed` reports the chunking through `onChunks` so the message can tell "all of it" from
  "some of it".
- **Volume + speed** (`chatTtsVolumeStore`, `chatTtsRateStore`, persisted prefs): revealed on hover
  over the speaker, because they are adjusted *while* listening and the pointer is already there.
  Both act on the `<audio>` element, never on synthesis — neither engine takes a rate or a gain, and
  re-synthesising to change one would throw away the replay cache. A run is many elements, so
  `SpeakOptions.settings` is a **getter** re-read per chunk, and a `$effect` on `audioEl` applies a
  mid-clip change to the element already playing. `preservesPitch` keeps 2x fast rather than
  chipmunked. They render **inline** in the footer row (there is slack between the buttons and the
  right-aligned word count) rather than in a popover; only the speed list is a dropdown, opening
  upward because the row is at the bottom of the bubble, and ordered fastest-first so it reads
  ascending on screen. The controls stay up while that list is open — by then the pointer is over
  the list, not the button.
- **Follow the reader** (`lib/speechHighlight.ts`): the chunk being spoken is tinted orange
  (`::highlight(tts-active)`, `--color-speak`) and everything already spoken dims to
  `--color-txtsecondary`. Painted with the **CSS Custom Highlight API** over `Range`s, so nothing in
  the DOM is wrapped — code-copy buttons, diagram canvases, citation chips and `use:` actions are
  untouched, and an unsupported browser simply shows no marker. Chunk → screen is a *fuzzy word
  alignment*, not a substring search: `speechText()` has already flattened the markdown the reader
  sees, so both sides are reduced to alphanumeric words and walked forward with a monotonic cursor
  (a repeated sentence must not send the marker backwards) that tolerates on-screen words the speech
  dropped. A chunk that won't align confidently yields `null` — nothing painted, cursor unmoved —
  because guessing highlights the wrong sentence. Reasoning boxes and `pre` blocks are excluded from
  the word stream (their text is on screen but never spoken); inline `code` is **not**, since its
  text survives into the speech. Ranges are resolved once per run, off `onChunks`, and the position
  advances on `onChunkPlaying`.
- **One request at a time**: `generateSpeech` funnels every caller (read-aloud chunks, the Speech
  tab, the Settings voice preview) through a single promise chain. tts-server generates on one
  worker anyway, and TTS.cpp's completed-task map is keyed by an unseeded `rand()` that is never
  erased on read — the more tasks in flight and in that map, the likelier a request is handed
  ANOTHER task's result (the old voice's audio, or a 500 `Model returned an empty response.` when
  the squatter is a voices task). One retry on that message covers the residual case; the real fix
  is in tts.cpp's `simple_server_task`.

- **Model**: the explicit `chatTtsModelStore` pick from the side-rail **Settings → General**, else
  the first installed TTS model (auto-picked so read-aloud works without a setup step). The button
  is not rendered at all when no TTS model is installed.
- **Voice**: `chatTtsVoiceStore` = the same `playground-speech-voice` pref key — one person, one
  voice. The list comes from `lib/voices.ts` (`cachedVoices`/`fetchVoices`/`voiceLabel`), shared
  with the Speech tab so one normalization serves both qwentts's `{voices:[{name,kind}]}` **and**
  TTS.cpp's `{<model id>: [names]}` map.

The **voice list** is cached in the server-backed prefs blob (`lib/voices.ts`), not localStorage.
That cache is load-bearing, not a nicety: `GET /v1/audio/voices` proxies to tts-server, so an
uncached read forces a model load, and without an entry `safeVoice()` sends `""` — the user's
chosen voice silently stops being used while its name stays on screen. Keeping it beside the voice
pref means both halves of "speak as af_bella" follow the person and survive a site-data clear.
`fetchVoices` never writes a failed fetch into it: storing `DEFAULT_VOICES` on a non-OK response
made `hasCachedVoices()` true with a list holding only the default, and the picker's clamp then
rewrote the saved voice to `""` — a refresh while the model was still loading lost the selection.

**The voice pref is one per user but voices are per engine**, so `generateSpeech` runs every
caller's voice through `safeVoice(model, voice)`: an unknown name is not a 400 on TTS.cpp — its
Kokoro runner `TTS_ABORT`s and takes the whole tts-server process down (502 + a dead backend). A
model with nothing cached sends `""` (both engines' own default speaker) rather than a name that
might not exist there.

Because that substitution is silent, `voiceSubstitution(model, voice)` renders a warning line under
the picker naming the voice that will actually speak — a substituted voice and a **mislabelled**
voice pack are indistinguishable otherwise (the name stays on screen while a different speaker
talks; on Kokoro `""` resolves to `af_heart`, i.e. a female voice for a male-named pick). Note
quartermaster never assigns gender or any label to a voice: names come verbatim from the engine, so
a genuinely wrong-sounding name with no warning line is a gguf/TTS.cpp issue, not ours.

### Fetching the voice list is an explicit opt-in

It renders from the localStorage cache and only fetches when that model is **already loaded** —
`GET /v1/audio/voices` proxies to tts-server, so an eager fetch would swap a model into VRAM just
because someone opened Settings. The ⟳ button is the opt-in. The list also refetches when the TTS
model **becomes ready**, not only when the selection changes — the panel is normally opened while
the model is idle (same fix the Speech tab carries).

Clamping the saved voice into the list is gated on `hasCachedVoices(model)`: `DEFAULT_VOICES`
(`[""]`) is a placeholder, and clamping against it rewrote the user's stored voice to `""` before
the real list ever arrived.

### The 🔊 preview button

Speaks one fixed short sentence through the selected model+voice (`generateSpeech`) — same load
cost, same explicit opt-in — and turns into ⏹ while playing; the blob URL is revoked on stop/end
and on unmount, since a paused `Audio` still holds it.

- It keys on `model|voice` (`ttsTestKey`): clicking with a **different** voice selected restarts the
  preview instead of reading as a stop, because `ended` does not reliably fire on tts-server's WAV
  and a stuck ⏹ made the whole feature look one-shot. An `onerror` handler clears it too, and both
  handlers no-op unless their own element is still the current one — a late event from the previous
  preview must not kill the new one.
- A click while the previous preview is still **generating** cancels it (AbortController) rather
  than being swallowed — a preview that has to load the model first runs for tens of seconds. Both
  cancellation shapes (`AbortError`, play() "interrupted by a call to pause") are suppressed instead
  of rendered as an error, which is what made a second click look like a failure. A `ttsTestSeq`
  counter drops the aborted attempt's own catch/finally so it can't clear `busy` or post an error
  for a preview nobody awaits.
- `VOICE_TEST_LINE` is deliberately plain ASCII (an em dash / curly quote is an unknown token to a
  phonemizer-less engine) and is printed under the picker + in the tooltip — otherwise the only way
  to learn what the button says is to press it.

Both pickers are native `<select>`s, not `ModelSelector`: the settings body is a scroll container,
and an absolutely-positioned menu is clipped by it (the options rendered further down the page
instead of over the row).

`speechText()` flattens markdown first, because a reply read verbatim says "star star note star
star", and drops code fences entirely; it caps at 4k chars on a paragraph boundary, since TTS cost
is linear in characters and the model load can evict the chat model. Clicking again aborts/stops,
and the effect cleanup releases the object URL.

## Diagrams & charts in chat (`lib/diagrams.ts`)

**NOT a tool** — the model has nothing to execute server-side, it just writes source. It emits a
```mermaid fence (Mermaid diagram) or a ```chart fence (JSON Chart.js config); `lib/markdown.ts`
deliberately leaves those two languages **unhighlighted** so the raw source survives, and the
`diagramBlocks` Svelte action (used on the assistant prose in `ChatMessage.svelte`) draws them after
the HTML lands in the DOM.

Gated on `!isStreaming` — scanning mid-stream would parse a half-written diagram and burn the block.
Mermaid is `import()`ed on first use (it self-splits one chunk per diagram type, main bundle
unaffected) and runs `securityLevel: "strict"`; a render failure leaves the code block visible with
a one-line note instead of swallowing the answer. Rendered blocks get a **Source** toggle that
re-shows the fence.

`DEFAULT_DIAGRAM_PROMPT` (`lib/systemPrompt.ts`, `opts.diagrams`) is what tells the model it can
draw — it is on for every chat turn, so **keep it byte-stable or the KV prefix breaks**.

## Links open in a new tab (`lib/markdown.ts` `rehypeExternalLinks`)

Every rendered `http(s)` anchor gets `target="_blank"` + `rel="noopener noreferrer"`. The chat IS
the app — navigating in place tears down the playground and the SSE a streaming turn rides on.
In-app wiki citation chips (`href="#"`, `data-wiki-id`) are deliberately excluded.
