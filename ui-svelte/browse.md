# ui-svelte — `/browse`, the model browser

`routes/Browse.svelte`, client in `lib/hubApi.ts`, server in `internal/server/hubapi.go`
(see `internal/server/hubapi.md`). Search Hugging Face, read the repo page, pick a quant,
download it into the models folder.

**Its own page, not a Models category tab** — acquiring a model is a task, not a view of the
local catalog — but it carries the **same category tabs as the Models page**
(`MODEL_CATEGORIES`, same ids and order), so what you can browse and what you already have are
one vocabulary. The tab is sent as `kind=` and narrowed **hub-side** (`searchFilters` in
`internal/hub/hf.go` pairs `gguf` with the pipeline tag, ANDed by HF): filtering a 30-row page
client-side would leave most tabs empty.

**It opens already browsing.** An empty query is a valid search (the hub answers it with its own
top-by-downloads listing), so the page lands on content instead of an empty box demanding a
query. The one button next to the box is a **refresh**, not a "Search": the box already searches,
and a button whose verb flipped between Search and Browse was two names for "re-run whatever
this list currently is".

**The box searches as it is typed**, debounced 300ms (`onQueryInput`) — under the gap between
words, over the gap between letters, so a phrase costs one round trip and not a dozen against a
rate-limited hub. Enter still searches immediately (it cancels the pending timer), and
`lastSearched` suppresses a re-ask when the typing settles back on the text already showing.
This is why `runSearch` carries **no "already searching" guard**: a search starting while one is
in flight is now the normal case, and dropping it would leave an older query's results on screen.
`searchSeq` is what makes that safe — only the newest response may land, and only it may clear
the spinner.

## Filters and sort

A **Filters** popover (a `.seg` button carrying a count of how many knobs are off their default,
so an active filter is never invisible behind a closed menu). It also holds the **Sort**
(Popular / Liked / Newest) — a category of listing belongs beside the knobs deciding which repos
are listed, and as a toolbar control it was three permanent buttons for a choice made once.

- **Trendy** — **ON by default**. Only repos *published* in the last `TRENDY_DAYS` (14), which
  paired with the sort is "popular releases that are new"; without it the page is a permanent
  all-time chart where a model released this week never appears. Judged on the repo's **creation**
  date, never its last edit (that is what "Updated within" answers). The cost is real and is why
  the empty state is trendy-aware: a deliberate text search mostly finds repos older than two
  weeks, so an empty list names Trendy and offers one click to search the whole hub, rather than
  reporting that the hub has nothing.
- **Max size** (`MAX_PARAMS_B`, 120B default) — an unfiltered top-downloads page is mostly
  frontier-size repos a single-GPU box can't run, which buries everything it can. The cap is
  server-side and parsed from the repo **name** via `hub.ParamsB`, so a repo that doesn't state
  its size is **shown, not hidden** — which is why it's an adjustable knob and not a silent filter.
- **Min downloads**, **Updated within**.

Those three numeric knobs are **sliders over a stop table**, not button rows: `lib/logScale.ts`
holds `SIZE_STOPS` / `DOWNLOAD_STOPS` / `AGE_STOPS`, and `components/LogSlider.svelte` binds the
range input to the **index** into one, so the *spacing* is the table's. The tables are geometric
(each stop ~a constant factor above the last), because every one of these spans orders of
magnitude and on a linear track the whole useful part — 8B vs 14B, a day vs a week — is a few
pixels at the far left. They are curated round numbers rather than a generated `exp()` curve so
every landing point is a figure a person would have typed. `Infinity` is the "no limit" stop on
the two caps and is converted to/from the `0` the filter state and API use at the component
boundary (`capValue`/`capStop`); the downloads table is a *floor*, so its permissive end is `0`
on the left and it has no infinite stop. `LogSlider` commits on `onchange` (release), not
`oninput` — Max size re-runs the hub search, so committing per stop crossed would fire a request
for every stop a drag passes; the readout still tracks the thumb mid-drag off local state.

Size and trendy are **hub-side** (changing either re-runs the search); downloads and recency
are **client-side** over what came back, since HF's list endpoint filters by neither.
`ageDays` returns `null` rather than `0` for a repo stating no date — "unknown" and "touched
today" are opposite answers to a recency filter. A filter set that hides everything renders an
"all N results are hidden by your filters" row with a Reset, not an empty list that reads as
"the hub has nothing".

## No page-size knob, no result cap — the list loads as it scrolls

`onResultsScroll` within 240px of the bottom → `loadMore`, appending and deduping by id, paging by
the server's `nextSkip` (which counts the **hub's** rows, not the surviving ones). `PAGE_SIZE` is
per-round-trip only.

Two things a naive infinite scroll gets wrong here and this one handles:

- A page can be emptied entirely by the filters, which appends nothing, fires no further scroll
  event and would stall — so one trigger pulls up to `MAX_AUTO_PAGES` until something lands.
- A list too short to scroll can never ask for more, so `fillViewport()` tops it up until it
  overflows its pane or the hub runs out.

A `searchSeq` counter drops a page still in flight from a superseded query.

The category tab row carries an **open-models-folder** button at its right end (`revealFolder()` →
`POST /api/hub/reveal`) — the footer line that merely *named* the path was a string to read and
retype, and is gone.

## Layout: results left, detail right

Publisher avatar via `components/HubAvatar.svelte` → `getAuthorAvatar` → `/api/hub/avatar`, then
`/api/imgproxy`. **Three** paths land on the same default tile — no avatar for that namespace, a
failed lookup, and an `onerror` after the image loaded — and it is drawn while the lookup is
still in flight, so rows never reflow and a broken-image glyph never appears. The avatar cache
holds the **promise**, so thirty rows by `unsloth` make one request.

## The model card is rendered inline, and it is written by a stranger

`renderMarkdown` runs with `allowDangerousHtml: true`, so the card goes through
`lib/hubMarkdown.ts` first: frontmatter stripped, repo-relative URLs resolved against
`…/resolve/main/…`, then `sanitizeHTML` — parsed in an **inert `DOMParser` document** (assigning
to a live `innerHTML` would already have fired `<img onerror>`), tags dropped by blocklist,
attributes kept by **allowlist**, non-http `href`/`src` removed, every image re-pointed at
`/api/imgproxy`.

Its spec is the only one in the project running under `// @vitest-environment jsdom` — a DOM
sanitizer has to be tested against a DOM, and a regex-based one is how sanitizers get bypassed.

`width`/`height` survive the allowlist **on an image and only as a plain number** — that is how
a card sizes its own badges, and stripping them rendered a 20px shield at its 1200px natural size.

Card styling is scoped **plain CSS against the theme variables, not `@apply`** (Tailwind v4 needs
a `@reference` inside a component `<style>`, and nothing else here does it). A card is written
for a full-width hub page, so: images capped on both axes; an all-image paragraph (badge row /
hero shot) laid out as a centred flex wrap; `align="center"` honoured (the attribute survives
sanitising but does nothing on its own); tables get their own scroller; and nothing is allowed
past `max-width: 100%` — one stray inline width scrolls the whole column and takes the results
list with it.

## The file table

`groupFiles()` (rows are `FileOption`) collapses every shard of a multi-part GGUF onto ONE row —
a lone shard is not a model, so offering shard 2 of 3 as its own download only produces a broken
folder.

**Every row is labelled with the file's whole name**, taken from its group key, so a sharded set
shows the shared name minus the `-00001-of-00003`. It used to show only the quant tag a regex
picked out of that name, which read tidily until it didn't: a miss was silent (an unknown recipe
marker or suffix mislabelled the row instead of failing), and two files could reduce to the same
tag — `mmproj-F16.gguf` rendered as a bare "F16", identical in the table to the model's own F16
weights.

### "downloaded" is a content check, not a name check

A row carries a green **downloaded** badge when every shard is on disk at the size the hub reports.
That test on its own was wrong in one common case: a publisher re-uploads a quant **in place**,
under the same filename, and a rebuild at the same quant lands within bytes of the old one — so
the new revision read as already downloaded, the button rendered as a disabled tick, and the only
way to get the update was to go rename or delete the file by hand.

So the server records the hub's content id for each file it downloads (`.quartermaster-hub.json`
in the repo folder; see [`internal/hub/CLAUDE.md`](../internal/hub/CLAUDE.md)) and compares it on
the next visit. `HubFile.stale` means *on disk, but not what the repo is serving now*: the row
gets an amber **update available** badge and a live button, and `rowState()` ranks `stale` above
`local` for exactly that reason. `groupFiles` ORs `stale` across shards where it ANDs `local` —
the set is one model, so one superseded shard makes the whole thing the old revision — and the
job then refetches only the shards whose id actually moved.

Both ids have to be known for that call, so a **hand-copied** file — one we have no record of —
is never called stale. That is what the second button on a `downloaded` row is for: it passes
`force`, which refetches regardless. It covers what the server cannot see (a file swapped outside
quartermaster, or one downloaded before the manifest existed) and means no row can ever again
strand the user with a manual fix. Either way the old copy stays in place until the new file is
renamed over it, so a canceled update leaves a working model behind.

The server-side `HubFile.projector` flag still drives three things a name can't: a `projector`
badge; sorting projectors **below** the models (as the smallest file in a repo one otherwise
sorted first, where it read as the cheap option); and "companion" in place of the fits/spills
chip, since a projector is charged on top of whichever file you pick rather than sized on its own.

### The fit column answers with context, not just yes/no

"fits, 92k context" / "fits, max context" / "partly on CPU, 32k context" — because a quant that
fits is only useful at a window you can actually use.

- It comes from `estimateHubFile()` → `GET /api/hub/estimate`, which Range-fetches that file's
  GGUF header server-side and runs the real sizer against the configured VRAM target (see
  `internal/server/hubapi.md`).
- `verdictFor()` — size-only, and a **hint rather than an estimate** — is what the cell shows
  until the header lands, or permanently if it can't be read, so the column never blocks on a
  network call. The `title` says which of the two is on screen.
- `sizeRepo()` kicks the sizings off when a repo is opened: through a small worker pool
  (`SIZE_CONCURRENCY` 5 — serial filled in one row per second and the table was still settling
  long after the user had read it, while firing a dozen at once just makes them queue on the
  browser's per-host connection budget; server-side the concurrent rows collapse into ONE header
  fetch per model). It skips projectors, aborts if the user opened a different repo mid-flight,
  and runs **only on the `llm` tab** — the planner is LLM-shaped (layers, KV, expert share), so
  asking it about a diffusion or TTS gguf would produce a confidently wrong number.
- The model's own config page remains the authority once the file is on disk.

## Downloads and errors

Browse draws **no download progress of its own** — the status rail's Downloads menu is the single
downloads manager (see [`dashboard.md`](dashboard.md)), and a page-local strip duplicating it was
removed. Browse reads the shared store only to know whether the open repo is already being pulled.

The whole page 501-gates to an "unavailable" state (`available = false`). Errors are rendered
**verbatim**: the server writes them to be read ("accept the license on the model page", "not
enough free disk"), so `hubFetch` surfaces the body rather than a status code.
