# ui-svelte — playground tools, modes & attachments

What the chat model can call, what rewires how it answers, and what the user can hand it.
The chat surface itself is in [`playground-chat.md`](playground-chat.md).

**The recurring constraint here: every advertised tool and every prompt line sits in the
conversation's KV-stable prefix**, carried on every turn of every chat. That is why tools are
grouped behind few toggles rather than many, why a tool description never carries a date, and why
recall is by injection rather than by a read tool.

## Composer mode menu (`components/playground/ToolMenu.svelte`)

The ✨ (`Sparkles`) popover left of the chat composer holds the agent **modes** — things that
rewire how the assistant answers (Rewrite, Shopping assistant). Plain capability flags (Reasoning,
Web Search, QM Tools) are checkboxes in the Configs popover, not modes.

Items are a `ToolItem[]` (`chatHelpers.ts`) so adding one is a list entry, not markup. The button
**wears the active mode's own icon** (cart for shopping, pen for rewrite) and tints primary — no
corner badge; a dot there reads as an unread notification.

The only other composer icon is the paperclip: **always rendered** (the row must not reshuffle per
model) and **never disabled by the model** — documents need no vision, so `canAttach` (native
vision, or a `visionTwin` exists) only decides whether image extensions are in the picker's accept
list. There is **no vision toggle**: choosing an *image* auto-switches to the vision variant
(`swapToVision` sets + warm-loads the twin, toasts), and switching back is the model picker's job.
The swap fires on the picked file, not on opening the picker — otherwise attaching a PDF silently
changed the model.

## Help wiki (`lib/wiki.ts` + `components/WikiModal.svelte`)

One array of help articles is the single source for both the **Help** button (dashboard `Sidebar`
+ playground rail → `WikiModal`) and the `wiki_search` tool the chat models call. The tool is
always advertised in chat (local, no network), dispatched alongside `web_search`.

## Web-search providers (`lib/webSearch.ts` + `searchProvidersStore`)

Edited in the side-rail Settings → Search. Web search is an **ordered failover chain**, not one
endpoint — SearXNG first (local, keyless), then Brave / Tavily / DuckDuckGo / Google Programmable
Search, each tried only when the ones above it errored, timed out or returned nothing.

`ChatInterface` sends the chain on the turn payload (`searchProviders`,
`normalizeProviders(...).filter(providerReady)`); dispatch is server-side
(`internal/server/tools.md`).

- Row order **is** the failover order, edited with ↑/↓ rather than drag-and-drop.
- Keys are typed into local `$state` and written back by `saveProviders()` on change — binding them
  straight to the store would PUT a half-typed key on every keystroke.
- `providerReady` mirrors the Go `ready()` so an enabled-but-unconfigured row is flagged "needs
  setup" instead of silently costing a hop.
- The **Test** button probes that ONE row (`searchViaChain([row])`), never the chain — a chain test
  passes on the provider below the one being configured.
- `searxngUrlStore` survives as the legacy single-URL pref, kept in step with the SearXNG row.

## Assistant utility tools (`lib/assistantTools.ts`)

Five small tools a personal assistant needs constantly, all dispatched server-side
(`internal/server/{datetime,calc,units,weather,feed}.go`) and rendered as
`kind:"time" | "calc" | "units" | "weather" | "feed"` cards. Split into two exported groups **by KV
cost, not by category**:

- **`ALWAYS_TOOLS`** = `get_datetime` + `calculate` + `convert_units`. Local, no network, and each
  covers something a model is *reliably wrong* about from its own weights: it has no clock (so
  "next Friday" is dated from the training cutoff), it does arithmetic token by token (so a 4-digit
  multiplication is a guess), and it recalls conversion factors approximately. Always on, exactly
  like `wiki_search`.
- **`EXTRA_TOOLS`** = `get_weather` + `fetch_feed`. Both hit the network and only matter to some
  conversations, so they sit behind ONE **"Weather & Feeds"** checkbox in the Configs popover
  (`extraToolsStore`, default on) — one toggle, not two, to keep the prefix in two states rather
  than four.
- `DEFAULT_ASSISTANT_PROMPT` / `DEFAULT_EXTRA_TOOLS_PROMPT` (`lib/systemPrompt.ts`,
  `opts.assistant` / `opts.extras`) are the matching prompt lines. **They gate on the same flags as
  the tool list** — a prompt describing a tool that isn't advertised is what produces a call to a
  nonexistent function. Both are off in Rewrite mode along with the tools themselves.
- Only `calc`, `weather` and `feed` **replay** into history
  (`internal/server/playground.md`): `time` and `units` record a rendered label ("15.6 in → cm"),
  not their arguments, so replaying them would put a call in the transcript that could not have
  been made.

## Assistant memory (`lib/memoryTools.ts` + `stores/memories.ts`)

Standing facts about the user that survive a chat. Storage in `internal/server/memories.go`.

**Recall is by injection, not by a tool** — `memoryBlock($memories)` renders `- [<id>] <text>` lines
into the system prompt of every turn, newest-updated first, cut at `MEMORY_BLOCK_LIMIT` (8k chars)
with a count of what was omitted rather than a silent drop. A `memory_read` tool would sit in the
KV-stable prefix and still only fire when the model thought to call it; injected facts are always in
front of it. The trade runs the other way too: **a write changes the system prompt, so it
invalidates the KV prefix of every chat** — which is why only `MEMORY_TOOLS` (`memory_save`,
`memory_delete`) are advertised, and why `DEFAULT_MEMORY_PROMPT` (`opts.memory`) tells the model to
save lasting facts only and to replace via `id` instead of accumulating near-duplicates.

**On** by default (`memoryStore`, a `userPref`), off in Rewrite mode. The Configs checkbox gates the
tools, the prompt line and the block together.

Writes are **not** approval-gated (unlike `quartermaster_configure`) — they are frequent and
reversible; visibility is the `kind:"memory"` chat card plus **Settings → Memory**, where the user
adds/edits/deletes entries (each row labelled "Remembered by the assistant" vs "Added by you"). The
card handler re-fetches (`loadMemories()`) so a save shows up in the panel without a reload.
`"memory"` does **not** replay into history — the recorded query is the fact, not the call's
arguments, and a replayed `memory_save` reads as a second save.

## Click-through answer wizard (`lib/askBlock.ts` + `playground/AskWizard.svelte`)

A mode that has to pin down a brief (shopping stage 1) ends its turn with a ```ask fenced JSON block
instead of a numbered list. `splitAsk` lifts it out of the prose — including mid-stream, where an
unterminated fence is cut and replaced by a "Writing your options…" shimmer so half-typed JSON is
never shown — and `AskWizard` steps through the questions **one at a time**, options stacked
vertically (single-choice advances on click, multi needs Next, Skip sends "no preference").

Picks are composed into a plain `Label: value` user message and sent normally — no server
involvement, since asking already ends the turn.

- An option that *means* "other" (`isOtherOption` — "Other", "Other (please specify)", "Something
  else", "None of the above") is **not** an answer: models write one into `options` instead of
  setting `allowOther`, and letting it advance the step sends a question answered with the word
  "Other". Picking one reveals + focuses the free-text field and holds the step; typing replaces the
  chip at send time.
- Earlier answers carry forward — a currency picked in step 1 becomes the prefix on a later budget
  field, and price options the model wrote before it knew the currency are **converted, not
  annotated**: `lib/currency.ts` `fetchFxRate()` hits `GET /api/fx`, the same 6h-cached server lookup
  `convert_currency` uses (**prefetched as soon as the currency question is answered**, debounced
  300 ms because the answer can come from the free-text field — fetching on arrival at the budget
  step instead puts a live upstream round trip in front of the user). `convertMoneyLabel()` rewrites
  each bracket ("Under $500" → "3,450 DKK"; ranges converted once with the code at the end,
  `niceRoundAmount` so a bracket isn't false-precise). The sent value keeps both figures ("3,450 DKK
  (converted from Under $500)").
- **A rate is never computed client-side**; if the lookup fails the options are **dropped** in favour
  of the text field rather than shown in a currency the user doesn't spend.
- Only offered on the **last finished** assistant message (`onAskAnswer` is undefined otherwise), and
  a malformed block parses to `null` and falls through to an ordinary code fence rather than
  swallowing the answer.

## Shopping assistant

`lib/systemPrompt.ts` `DEFAULT_SHOPPING_PROMPT` + `shoppingStore`/`shoppingPrefsStore`. A
prompt-staged buying helper, not a separate mode: brief (ask only what changes the outcome, restate
as `**Brief:**`) → research (search, then `fetch_page` the real product pages) → report (short
verdict + what could not be verified, then a ```products block).

### Report cards (`lib/productBlock.ts` + `playground/ProductReport.svelte`)

Stage 3 ends in a ```products fence of JSON
(`{pick, products:[{name, price, shop, image, url, specs, why, badge, cite}]}`) rendered as a card
grid — same lift-it-out-of-the-prose contract as ```ask (`splitProducts`, unterminated fence →
"Building your comparison…" shimmer, malformed → falls through to an ordinary code fence). Split runs
on `ask.cleaned` so a turn carrying both keeps each fence out of the other's prose. Read-only, so it
renders on **every** assistant turn, not just the last.

`repairProductUrls` (applied in `ChatMessage`, against the turn's `fetch_page` cards) swaps a card's
search/category URL — `isListingUrl` — for the page actually fetched for that product: models fill
the field with the shop search they ran, which makes the user redo the search the assistant already
did. Matching is deliberately strict — **every** significant word of the product name must be in the
page title, and a two-page tie is left alone, because a card pointing at the wrong product is worse
than one pointing at a search.

Tolerant parser: alternate field names (`title`/`cost`/`store`/`link`/`features`/`reason`), non-http
`image`/`url` dropped (a relative or `javascript:` URL is broken or hostile), nameless entries
dropped, `[n]` citation markers stripped from every field (models staple them onto URLs, where
they're a dead link — the card has its own cite chip).

### Pictures and prefs

Pictures need the model to have *been given* a URL: `fetch_page` harvests up to 3 per page
(`internal/server/tools.md`, `pickImages` — og:image first), and the card loads them through
`proxiedImage()` → `GET /api/imgproxy` rather than hotlinking, since shop CDNs refuse a foreign
`Referer`. A failed load falls back to a monogram, never a broken-image glyph.

Standing prefs (country/currency/shops) are a single free-text line in the Configs popover,
deliberately NOT per-chat. Turning shopping on force-enables `fetch_page`: a price from a search
snippet is not a price.

### Currency

Asked for in stage 1 (never assumed — euros are not a default) and prices are quoted exactly as the
page states them. `lib/currency.ts` `CONVERT_CURRENCY_TOOL` is advertised **only in shopping mode**
(outside it, converting money is a rare aside a search answers). Rates are fetched server-side by
`internal/server/currency.go` and rendered as a `kind:"currency"` tool card. The converted figure
goes *beside* the page's own price, never in place of it.

## File attachments (`lib/attachments.ts` + the composer's doc chips)

The paperclip takes text/code, PDF, DOCX and audio, not just images.

**Extraction runs in the browser and the result is folded into the user's own message** as a
`<file name="…" note="…">…</file>` block (`buildFileBlock`/`splitFileBlocks`, `</file>` in the body
escaped, an unterminated block left as prose). So the transcript IS the storage, and history,
tool-call replay, compaction and the KV prefix needed no server change at all. That is also why the
planned server-side `pdftotext`/`mutool` exec-per-request was dropped: it would have added a binary,
an installer component and a PATH hunt for the same string.

`ChatMessage` re-splits a user turn and renders the files as collapsible chips above the prose;
**editing is disabled** on a message carrying one (same rule as images — an edit rewrites the whole
string and would dump raw document text into the textarea).

### Per-format extraction

- **PDF** is pdf.js, `import()`ed on first use so it stays its own chunk (same pattern as mermaid);
  the worker is `?url`-imported, which is why `internal/server/ui.go` pins `.mjs` to
  `text/javascript` — Windows' registry has no row for it and `http.ServeContent` served the worker
  as `text/plain`. A PDF yielding almost no text **throws**: this is extraction, not OCR, and a
  silent empty attachment is worse than an error.
- **DOCX** is read with a ~60-line central-directory zip walk + `DecompressionStream("deflate-raw")`
  rather than a jszip/mammoth dependency tree. The data offset comes from the **local** header's
  extra length (`readZipEntry`; using the central one reads garbage out of a real docx, and the test
  fixture is built to differ there).
- **Audio** posts to `/v1/audio/transcriptions` via `pickTranscribeModel`, which prefers an
  **already-ready** ASR model and toasts before swapping, since loading one evicts the chat model.

### Size is checked at attach time, not at send

`estimateTokens` (~4 chars/token) against `docBudgetTokens` = 40% of the window (live `n_ctx`, else
the catalog's `ctx`), rejected naming both figures, and re-checked against what the other attachments
already claim. An unknown ctx enforces nothing rather than guessing. Send is blocked while any file
is still being read, so a half-extracted set can't go out.

## Playground libs (`src/lib/`)

| Module | What |
|---|---|
| `webSearch.ts` | web-search tool + provider chain. `formatSearchResults` stamps **today's date** into the result *header* so a time-sensitive query gets re-run with the real year — the date stays out of the tool *description*, which sits in the KV-stable prefix. Ported server-side in `internal/server/turnstools.go`, where searches actually run. |
| `fetchPage.ts` | `FETCH_PAGE_TOOL` — the `fetch_page` contract, fetched server-side; advertised whenever web search or shopping is on. |
| `youtube.ts` | `YOUTUBE_TOOL` / `YOUTUBE_SEARCH_TOOL` / `YOUTUBE_COMMENTS_TOOL`, all three always advertised, fetched server-side. Search and channel-listing are ONE tool on purpose: they return the same shape, and a weak local model choosing between two near-identical tools mostly chooses wrong. Plus link unfurling — `extractYouTubeIds` finds video links in a message, `fetchYouTubeMeta` resolves them through `/api/youtube/meta` with a module-level per-id promise cache, rendered by `playground/YouTubeEmbed.svelte`. |
| `chatCompact.ts` | auto-compaction + conversation title gen |
| `wordDiff.ts` | rewrite diff |
| `reasoning.ts` | Harmony/reasoning parsing, `thinkSummary`, `activityLabel` |
| `inferenceAuth.ts` | `refreshInferenceKey`/`inferenceHeaders` — auto-attach API key to playground inference |
| `modelUtils.ts` | `modelCategory`/`MODEL_CATEGORIES`, drives the Models tabs + key scoping |
