# ui-svelte — the operator dashboard

Everything served on the main listen port. The model browser has its own doc
([`browse.md`](browse.md)); the playground is on its own port
([`playground-chat.md`](playground-chat.md)).

Routing is hash-based (`svelte-spa-router`); the router table lives in `App.svelte`.

## `/` — Dashboard (`routes/Dashboard.svelte`)

Landing page: the shared live-models panel (`components/ActiveModelsPanel.svelte` — launch
params + `InferenceFeedback` for whatever is loaded) plus the quick-load picker, ranked by
per-**family** load tally so loading any variant floats the family up. No GPU/activity
duplication of the StatusRail/Observe; the config knobs live in Settings.

## Downloads menu (`components/DownloadsMenu.svelte`, state in `stores/hubJobs.ts`)

**Not a route** — one icon on the StatusRail with a live count, opening a panel, the way a
browser does it. Monitoring a download is a glance, not a destination; it was a page and a
sidebar row before that (both deleted), which cost permanent nav real estate for something
idle almost always.

The panel lists active jobs with a bar, a rate and an ETA, plus the last 8 finished/failed/
canceled with where they landed — each finished job's path is a **button** that opens that folder
(`revealFolder(j.dir)` → `POST /api/hub/reveal`), and the panel foots with an **Open models
folder** link. Both replaced printed paths and a "finished downloads are in the config already —
see the Models page" line: a path you can't click is a string to retype, and the sentence
restated what the Models page already shows.

- **Pause and Cancel are different operations and are drawn as such.** Pause keeps every byte
  (`paused` is a **non-terminal** phase — `isUnfinishedJob` keeps the row in the active section
  and in the rail's badge, while `isRunningJob` excludes it so the poller stops rather than
  ticking against a job that isn't moving). Cancel **deletes what was downloaded**, so it arms
  an **inline confirmation naming the amount about to be discarded** — inline rather than
  `window.confirm()` because the byte count is the fact the decision turns on, and a native
  dialog can't show it (and can be suppressed by the browser). Escape backs out of the armed
  confirm before it closes the panel.
- Every verb refreshes the store immediately: after a pause nothing is polling, so the row
  proving it worked would otherwise never arrive.
- Jobs live in a **store, not in the component**, because a 40 GB pull outlives whatever page
  started it — otherwise the menu and Browse would be two pollers of one list.
- Progress is **polled from `/api/hub/jobs` at 1.5 s only while a job runs**, exactly like
  `ManagedBackends.svelte` — no new SSE type, and an idle dashboard makes no requests. The menu
  fires one refresh on mount so a pull started before a reload still shows a count without
  anyone opening it, and polling stops itself when nothing is running.
- The **rate is computed client-side** (the server sends a byte counter) and smoothed
  `0.7·last + 0.3·instant`: a raw between-polls delta on a chunked transfer gives an ETA
  swinging between 4 and 40 minutes — worse than none, which is also why it is blank until the
  samples land.
- The panel is `position: fixed`, not absolute: the rail is an `overflow-x-auto` strip and would
  clip it.
- There is no "clear history" — the list is in the server process and empties on restart.

## Settings (`components/SettingsModal.svelte` wrapping `routes/Settings.svelte`)

**Not a route** — opened as a modal from the Sidebar (same pattern as Help/`WikiModal`). Holds
the global config knobs: memory budget (target VRAM / headroom / max RAM), **idle unload (ttl)**,
and the experimental slot KV-cache disk-save section. All 501-gated on `-generate`; each save
regenerates the config + hot-reloads.

### Managed backend installs (`components/ManagedBackends.svelte`)

Rendered above the manual registry in Settings → Backends. Downloads a backend build from its
upstream GitHub release via `/api/backends/*` (`getBackendCatalog`/`getBackendReleases`/
`installBackend`/`getBackendJobs`/`activateBackend`/`uninstallBackend` in `stores/api.ts`).

Tabbed by registry **class** (`lib/backends.ts` `backendClass`) — Text / Image / Upscale / … /
Tools — so llama.cpp and vLLM share one tab, exactly the set competing for one ★ default;
unknown kinds land in "Other", helper binaries (`kind: ""`) in "Tools", so a new catalog entry
needs no UI change. A component flagged `manual` (vLLM — Python wheels, no executable) renders
its `setup` text instead of install controls. GPU flavour preselected from the server's
`suggested`, overridable; releases load lazily (the catalog call never hits GitHub, so the tab
opens offline); progress **polls `/api/backends/jobs` only while a job runs**. Installed versions
kept side by side with Use (rollback) / Remove.

Does **not** replace the manual rows below it — tts-server / sam3-server / a ROCm sd-server have
no installable release; managed rows appear there read-only, tagged "installed".

## `/models`, `/models/:category` — Models

`routes/Models.svelte` → `components/ModelsPanel.svelte`. The whole catalog as **ONE spreadsheet
page**: ONE toolbar row (category tabs counted from `MODEL_CATEGORIES` on the left; All/Loaded/
Idle + folder picker + ID↔Name + show-unlisted pushed right) over `components/ModelsTable.svelte`.

The row **wraps, never scrolls** — a scrollbar under the tabs hides categories behind a drag.
Search is not on that row: it lives in the table's **Model column header**, the column it filters,
as an icon that expands into the input (`/` opens it, Escape clears + collapses) and collapses
back only while empty, so an active filter can never hide behind an icon.

`:category` is the **initial** tab only (deep link / old bookmark); switching tabs afterwards is
page state, not navigation, so the sidebar has a single Models entry with no sub-menu. No
`ActiveModelsPanel` here — the Dashboard owns live models. Peer models stay a read-only chip list
below the table.

### Two grouping axes, one row per model

`lib/modelTable.ts` (pure, spec'd in `modelTable.test.ts`) does all of it. `quantOf` + `baseKey`
collapse every **quant** of a model onto one row, and within a quant the shortest id is the base
while its longer siblings become **variant** pills (same look as the playground `ModelSelector`).

`baseKey` truncates the id **at** the quant rather than splicing it out, because ctx tiers come
after it (`…-Q4_K_M-32k`) and splicing would give every tier its own row. Quant pills switch which
numbers the row shows; the ▸ expander lists one sub-row per quant so Size/Est VRAM/Est RAM are
comparable side by side.

Both `quantOf`/`baseKey` take the **first** quant-shaped part, not the last (mirrors the Go
`quantFromPath`), and fold a `UD`/`i1` recipe marker into the quant — or `-UD-` and a duplicated
trailing quant leak into the displayed name.

### A third axis: family

`familyOf` is the finetune detector — it reduces a base key to `<model><param count>`, so
`thinkingcap-qwen3.6-27b` and `qwen3.6-27b-uncensored-heretic-v2` cluster under one `qwen3.6-27b`
heading. Keyed on the parameter count because it is the one token a tuner never rewrites.

**Finetunes keep their own rows**: they carry different weights, sizes and behaviour, and folding
them together would let "load Qwen3.6-27B" quietly start an uncensored tune. `groupFamilies`
clusters already-sorted rows **without reordering** — a family takes the position of its
best-ranked member.

A heading says where a family starts and nothing said where it **ended**, so a family is drawn as
a **rail**: one `rowspan`ed cell between the ★ and Model columns, tinted, carrying the family name
as vertical (`writing-mode: vertical-rl`) text. The line marks both ends and costs no row of
vertical space — a heading row did neither. `familySpan` counts expanded quant sub-rows, so the
rail can't fall short of the family it labels.

`stripQuantCrumbs` cleans the display name for the same reason `quantOf` takes the first match:
autogen scrubs the id but not the `name`, and prettifying `…-Q4_K_M-MTP` splits the leftovers into
words ("Thinkingcap Qwen3.6 27b K M"). Trailing crumbs only, never down to an empty string.

### Columns, sorting, striping

Columns are ★ / Name / Quant / Size / Est VRAM / Est RAM only — backend and everything else live
in the cogwheel (`ModelConfigModal`).

- No cell rules: **alternating bands** separate rows (parity assigned across the whole table, not
  per family, so striping doesn't restart mid-list).
- A **loaded row is lit** (left accent + tinted background, green ready / amber transitional) and
  `sortRows` floats loaded models above whatever sort is active, in both directions — under
  **favorites**, which pin above everything (★ column, keyed by ROW not model id, so a pin
  survives switching quant or variant).
- Header clicks cycle **asc → desc → none** (`nextSort`): "none" is a real state returning the
  catalog's own order, so there is no reset button.
- Sort key/dir, favorites and the display toggles are `persistentStore` prefs.

### The header sticks on the CELLS, not on `<thead>`

`position: sticky` on a `thead`/`tr` is unreliable under `border-collapse: collapse`, and its
collapsed bottom border scrolls away with the body regardless. So every `<th>` carries
`sticky top-0 z-20`, its own **opaque** `bg-surface` (rows must not show through), and the rule as
an `inset 0 -1px 0` shadow.

The scroll container is the table's own `flex-1 min-h-0 overflow-auto` wrapper, which only has a
height because the chain `h-screen → main overflow-auto → h-full → panel h-full` is unbroken —
break any link and the page scrolls instead, taking the header with it.

**No blank header cells.** The three non-sortable columns are labelled like the sortable ones minus
the button (`headCls`): ★ carries a Star glyph (+ `sr-only` text), the family rail carries `FAM`,
the action column `Actions`. A blank header reads as a rendering bug, and these are exactly the
columns whose purpose isn't self-evident.

### Where the numbers come from

Quant, Size and Est RAM are **server-side additions**: `internal/server/modelmeta.go` derives quant
+ on-disk size (shard-aware, `sync.Map`-cached since `modelStatus` re-renders on every SSE tick)
from the model's gguf path, and `estRamGB` is emitted per model by autogen beside `estVramGB` — so
it only appears after a config regen.

## `/observe`, `/logs`, `/activity`, `/performance` — Observe

`routes/Observe.svelte`: a single tabbed page with **Activity**, **Logs**, **Performance** and
**Context** tabs. Context (`Context.svelte`) is itself sub-tabbed: **KV Cache** (`KvCache.svelte`)
+ **Prompt Canonicalization** (`Canon.svelte`). Legacy `/logs`, `/activity`, `/performance`
deep-links preselect the matching tab.

## `/api-keys` — API Keys (`routes/ApiKeys.svelte`)

Create / scope / reveal / delete inference API keys. Only when the server runs with `-generate`.

## `/test` — Playground stub (`routes/PlaygroundStub.svelte`)

The dashboard no longer hosts the playground; this page links out to the standalone playground app
on `playgroundPort`.

## Fork-specific dashboard UI

Added on top of upstream: the per-model config editor (`ModelConfigModal.svelte`), the live-models
panel (`ActiveModelsPanel.svelte`, Dashboard only), the global Settings modal, live metrics &
activity (`ActivityStats.svelte`, `StatusRail.svelte`, `InferenceFeedback.svelte`, live-token
readout), the VRAM gauge, the request/response inspector (`CaptureDialog.svelte`), and the API-key
page.

**`ModelConfigModal.svelte`** does dynamic ctx / VRAM-target / variant tuning. The ctx slider's
ceiling is the model's **trained** length, lifted to 4× by the **RoPE** checkbox beside it
(`toggleRope` sets `adv.ropeScaling = "yarn"`; the Go sizer derives `--rope-scale` from the ctx
picked). Past-native is marked on the track with a tick at the trained length plus a
warning-coloured thumb/readout, and turning the toggle off pulls ctx back under the ceiling instead
of letting the server silently clamp it. Its pure launch-command↔form helpers live beside it in
`modelCmdForm.ts`: `parseCmdFields`/`parseImageCmdFields`, the flag sets the form owns,
`specHas`/`specToggle`, `fmtCtx`/`nglDisplay`/`parseCtx`. Anything that reads component state stays
in the `.svelte`.
