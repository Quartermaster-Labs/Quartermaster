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
the global config knobs, in a category side-nav: **Appearance**, **General** (memory budget —
target VRAM / headroom / max RAM — **idle unload (ttl)** on its own row, the OOM guard, GPU usage,
and the Advanced disclosure), **KV cache** (fleet-wide KV type + the slot KV-cache disk-save
section), **Backends** (managed installs, the manual registry, LoRA folder) and **System**
(software update, Windows startup, network, models-folder watching, Quartermaster update checks, HF token). All
501-gated on `-generate`.

Three save shapes, and the difference is worth knowing before adding a knob:

- **Autosave (debounced ~900 ms)** — memory, guards. Cheap to get wrong, instantly visible.
- **Explicit Apply** — the Advanced sizer knobs, behind a "don't touch this" warning with a
  *Restore defaults* button (`DELETE /api/settings/advanced`). A debounced regen fired on a
  half-typed context ladder is exactly the failure the warning is about.
- **Explicit Save, no reload** — the System tab's network/updates/token block
  (`GET`/`PUT /api/settings/app`). These are read by the process at startup, not by the config
  generator: the page diffs the saved values against `running` (what the process actually bound)
  and names the fields still waiting on a restart. The HF token is write-only — the GET reports
  only that one is stored — and the page warns when `HF_TOKEN` in the environment is shadowing it.

The first two regenerate the config + hot-reload; the third cannot, because a bound socket cannot
be moved under a live server.

**Don't re-group the OOM guard card.** *Reserve (GB)* sits above the "shed idle models" toggle and
is never disabled by it, because the two are read by different halves of `vramGuard`: the toggle
gates the post-load watchdog alone, while the reserve is subtracted in `ceilingGB()` — the
**admission** ceiling, consulted on every spawn regardless. *Grace (s)* is the watchdog-only knob
and is correctly gated. Reserve is also not a second Headroom: headroom pads Quartermaster's own
size estimate and is charged always, at generate time; the reserve is held back for *other* apps
and only while one is growing past its idle baseline. See `internal/server/http-core.md`.

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

**Track a repo** (`components/TrackRepoModal.svelte`) adds a GitHub repo the built-in catalog
doesn't know about — a llama.cpp fork, an in-house engine — and it then renders as an ordinary
card, tagged "tracked", with edit / stop-tracking, sorted to the **top** of its group (the server
appends sources after the built-ins so a custom id can't shadow one, but these are the rows the user
came here to manage). The hard constraint is that **the user never
writes an asset pattern**: the modal fetches a real release (`getBackendSourceAssets`), the user
ticks the build they want, and the server derives the matching rule from that example
(`internal/backends/derive.go`). So every field here is a picker over fetched data, which is also
why the form cannot be filled in offline. Two consequences for anyone editing it:

- **Never add a pattern/regex input.** If matching is wrong the fix is re-picking an asset, not
  hand-editing a rule. The card shows `resolveBackendAsset`'s answer — the file name an install
  would fetch right now, and the closest asset when nothing matches — because a file name is
  something a user can judge and a derived regex is not.
- `suggestLabel` mirrors `backends.SuggestLabel` only so a freshly ticked row is named without a
  round trip; the server re-derives it, so drift is cosmetic. Companion files (llama.cpp's cudart
  zips) are picked per build from the same asset list.

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

**The server decides who is who.** Every model in the payload carries `modelKey` (one model = one
row) and `familyKey` (finetunes of one base), read out of the gguf's own header — the publisher's
`general.basename`/`size_label`/`finetune`, which survive any rename — with the id rules as the
fallback for the ~1/3 of files that carry no identity KVs. `buildRows` uses them verbatim and only
falls back to `baseKey`/`familyOf` when they are absent. **Do not re-derive them here.** Guessing
where a name ends and a quant begins was a losing game: every publisher spells a quant differently,
and one that no pattern reads (`Qwen3.8-27B-mix-q-k-mtp`) put one model on five rows.

The key groups the row; the **label** is still id-derived, because a header key is built to compare
and not to read (`qwen2.5-vl-instruct-7b-instruct`). Same for `familyLabel` vs `family`.

The id-derived rules below are therefore the FALLBACK path, not the primary one — they still run
for every headerless file, so they still have to be right.

`lib/modelTable.ts` (pure, spec'd in `modelTable.test.ts`) does the rest. `quantOf` + `baseKey`
collapse every **quant** of a model onto one row, and within a quant the shortest id is the base
while its longer siblings become **variant** pills (same look as the playground `ModelSelector`).

`baseKey` truncates the id **at** the quant rather than splicing it out, because ctx tiers come
after it (`…-Q4_K_M-32k`) and splicing would give every tier its own row. Quant pills switch which
numbers the row shows; the ▸ expander lists one sub-row per quant so Size/Est VRAM/Est RAM are
comparable side by side.

Both `quantOf`/`baseKey` take the **first** quant-shaped part, not the last (mirrors the Go
`quantFromPath`), and fold a `UD`/`i1` recipe marker into the quant — or `-UD-` and a duplicated
trailing quant leak into the displayed name.

**A hand-mixed quant is a RUN of parts, not one token.** A gguf quantized per-tensor has no named
recipe, only its author's label — `Qwen3.8-27B-mix-q-k-mtp` — so `MIX_PATTERN`/`CRUMB_PATTERN`
(mirrored from `internal/quant`, and checked by the same Go mirror test) match the marker plus the
fragments after it and stop at the build tag: quant `MIX-Q-K`, base key `qwen3.8-27b`, i.e. another
pill on the model's own row next to `Q8_0` and `BF16`. A *bare* `-mix-` is deliberately not a quant
— it is also an ordinary word in a model name (`openhermes-mix-2.5`), and only `mix` followed by a
fragment cannot be anything else.

**Variants group on the gguf, not on the id.** `clusterModels` buckets the catalog by `family` —
the `-m` path the server already ships (`internal/server/family.go`) — and only then reads a quant
off the cluster's shortest id. That is the backstop for whatever the token rules never learn
(`q4km-clone`, `v3-control`): `baseKey` finds nothing to cut at and returns the whole id, so
without it every ctx tier and vision twin of one file stands alone (20 rows for 3 models in a real
catalog). The path says they are one file, and a row is one file. Two clusters merge into one quant entry only when they agree on a REAL token, so
two unparsed builds stay two entries under one family heading rather than being fused because
neither parsed. `QuantEntry.key` (quant, else the gguf) is what the pills key on — `quant` is `""`
for those and cannot be an identity.

**A pill may SHOW more than it may MERGE on.** The server also sends `quantLabel`, the weight type
read off the file's tensors (`IQ4_XS mix`) — honest, but computed, so `quantMergeKey` ignores it
and only `quantOf` falls back to it. A blank pill becomes a real answer; two unrelated hand-built
quants that happen to compute the same label still stay two entries, because a computed string is
not two files agreeing on a name.

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

**Checkboxes over tri-state overrides write a DELTA, never a value.** `mmap` (`"" | "on" | "off"`)
and `preserveThinking` (`null | false`) both have a generator default the user usually agrees
with — mmap follows the sizer's placement (on only where weights sit on the CPU, `--load-mode
none` otherwise), preserve-thinking is on wherever reasoning is. A binary checkbox that saves
`mmapOn ? "on" : "off"` stamps that default into the sidecar as an explicit pin, so the model
stops tracking its own placement the first time the modal is opened and saved for any unrelated
reason — which is exactly how a fleet ends up mmap-on while fully GPU-resident. So the save
compares against the inherited value (`mmapInheritOn` / `variantMmapInherit`, both read from the
launch args via `noNoMmap`) and writes `""`/`null` on agreement. Two consequences: read the
inherit state through `noNoMmap`, which understands `--load-mode`, never by grepping the retired
`--no-mmap`; and keep `variantFromDefault`'s mapping keyed on `=== false`, since `null` is now a
legitimate stored value that a truthiness test would flip to `false`.
