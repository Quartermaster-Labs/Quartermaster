# internal/server — slot KV cache (fork)

`slotcache*.go` + `kvcacheapi.go`. Persists a llama-server **slot's KV** to disk so an expensive
prefill isn't thrown away when a live slot is reused. A model serves `--parallel N` slots (N=1 by
default); any new request can evict a resident conversation. This subsystem snapshots the KV before
it's lost and restores it — instead of reprefilling — when that conversation, or one sharing its
preamble, returns.

| File | Role |
|---|---|
| `slotcache.go` | The state machine: `middleware`, `onSwitch`, `restoreOnLoad`, `saveOnEvict`, `ensurePreambleSeed`, `synthPrefill`, plus the `/slots` HTTP calls. |
| `slotcache_anchor.go` | How a request becomes a cache key: `sessionAnchor` (conversation id + stable system+tools preamble), `normalizeTimestamps`, `preambleHash`/`preambleKey`. |
| `slotcache_disk.go` | Snapshot-directory layout and the pruning passes: `fileName`/`splitFileName`, `enforceCaps` (LRU by mtime), `prunePreambleFiles`, `dropStalePreambles`, `bestSeed`. Guarded by `diskMu`. |
| `slotcache_slots.go` | Multi-slot mechanics: `sk`/`modelOf`/`slotIndexOf` (bookkeeping keys), `slotCount` (reads `-np/--parallel` off the configured cmd), `slotStates` (one `GET /slots` scrape), `acquire` (conversation → slot assignment) and `pinSlot` (`id_slot` body injection). |
| `slotcache_stats.go` | Observability: `kvCounters`, the `kvEvent` ring, the pending-confirm queue (`pushAwait`/`confirmReuse`) pairing a restore with llama-server's reported reuse, and the `stats()` snapshot. Own `statsMu`. |
| `kvcacheapi.go` | `GET /api/kvcache` — the monitoring snapshot (counters, recent events, on-disk files) for the Observe → KV Cache tab. |

## When it's active

Two gates: `cfg.SlotCache.Enable` (global; `dir`/`minSaveTokens`/`maxDiskGB`/`maxSessions`) **and**
per-model `participates(model)`, true only when the model's cmd carries `--slot-save-path`.
Non-participating models are left alone; a disabled cache is a branchless no-op middleware.

**Wiring.** `slotCache.middleware` sits in the model-dispatch chain. Cross-swap persistence also
needs two router process hooks: **pre-stop → `saveOnEvict`** and **post-start → `restoreOnLoad`**
(after Ready, before the triggering request is served). Without those hooks the cold path is dead —
if restore never fires, check they're called.

## Two file categories

1. **Conversation snapshots** — `model__<key>.bin` (+ a `.meta` preamble sidecar), one per chat.
   Keyed by `sessionAnchor`: the `X-Conversation-Id` header if sent (preferred — survives
   compaction, no opening collisions), else `sha256(firstSystem + firstUser)`. LRU-bounded by
   `enforceCaps`.
2. **Preamble caches** — `model__preamble_<hash>.bin`: one system+tools-only KV per
   `(model, preamble)`, i.e. per agent/environment, seeding *every* cold/warm load sharing that
   preamble. `hash = sha256("preamble\x00"+preamble)[:16]`,
   `preamble = system + "\x00tools\x00" + toolsJSON`. Differentiation is **purely content** —
   identical bytes share one file, whatever harness sent them.

## Save path (conversation snapshots)

- **WARM** (`onSwitch`, model loaded): a new conversation arrives → save the outgoing one if it's
  worth it, restore the incoming one if it's on disk.
- **COLD** (`saveOnEvict`): evicting A to load B kills A's process with no A request to trigger a
  save — the pre-stop hook snapshots it.

"Worth saving" = live KV ≥ `minSaveTokens`. **Cost is the only gate**, with no turn-count gate: a
single-turn chat with a long answer is still expensive to reprefill.

## Restore / seed path (preamble caches + Tier-1)

On a load with no exact conversation file, warm (`onSwitch`) and cold (`restoreOnLoad`) try, in
order:

1. **`ensurePreambleSeed`** — restore this agent's `preamble_<hash>.bin` (`preamble-hit`), else
   **mint** it: `synthPrefill` POSTs a system+tools-only `max_tokens:1` chat (llama-server can only
   save the *whole* live slot, so a clean preamble-only KV needs a synthetic prefill while the slot
   is safe to clobber), then saves the resident KV (`preamble-mint`). Gated on a non-empty system
   prompt **and** `len(preamble) ≥ seedMinPrefixBytes` (2 KB).
2. **`bestSeed`** (Tier-1 fallback) — a prior session sharing a ≥2 KB leading preamble prefix,
   chosen to **minimize over-restore**: tail-free preamble caches first, then longest shared prefix,
   then smallest `.bin`. Over-restore (a sibling conversation whose tail diverges) is wasted I/O on
   plain attention and *harmful* on hybrid/recurrent — un-rewindable layers emit `non-consecutive
   token position N after M` plus a full reprocess. Recurrent/hybrid models skip the cache entirely;
   see `recurrentSkip` below.

After any restore, `awaitConfirm[model]` is set; the **next** request's upstream `cached_tokens`
(`confirmReuse`, from the metrics monitor) is the proof the KV was actually reused (`confirm` /
`confirm-miss`), not merely loaded.

## Pruning (three mechanisms)

- **`enforceCaps`** — LRU by mtime within `maxDiskGB` / `maxSessions`. Preamble caches are
  **exempt** (sticky shared seeds).
- **`prunePreambleFiles`** — backstop: keep the newest `maxPreambleGenerations` (3) per model.
- **`dropStalePreambles`** — on mint, delete a prior preamble that is the **same agent apart from a
  small dynamic span** (`supersedesPreamble`: shared prefix + suffix, non-matching middle ≤
  `preambleDynDeltaMax` 512 B). Catches a daily date bump without nuking a different agent sharing
  identical tools. It needs the full preamble in `.meta`, so preamble sidecars are stored
  **uncapped** (conversation `.meta` stays capped at `metaMaxBytes`).

## Gotchas

- **We mutate the forwarded prompt (timestamp normalization).** `sessionAnchor` strips time-of-day
  from ISO datetimes in the **system prompt** (`normalizeTimestamps`/`isoTimeOfDay`) and rewrites
  the re-attached body, so upstream sees the date-only form. Otherwise an agent stamping the wall
  clock into its preamble re-mints a multi-hundred-MB preamble KV every run. **System prompt
  only** — user messages keep their timestamps, bare dates are untouched. Always on when the slot
  cache participates. This is *not* the same as `promptcanon.go`, which is always-on for every chat
  request regardless of participation (see [`http-core.md`](http-core.md)).
- **We own the slot mapping.** On a multi-slot model llama-server would otherwise pick the slot
  itself (longest common prefix) and never tell us, while our save/restore has to name one
  (`POST /slots/<id>?action=`). So `acquire` assigns the slot — sticky to the conversation, then any
  free slot, then LRU **skipping slots mid-generation**, then plain LRU — claims it under `stateMu`
  before any I/O, and `pinSlot` writes `id_slot` into the forwarded body so upstream honours it.
  Bookkeeping keys are `model` for slot 0 and `model#N` above it, so single-slot models key exactly
  as before. Caveat: a route that re-parses the body into fresh params (Anthropic `/v1/messages`)
  can drop `id_slot`, degrading the pin to a hint — visible as confirm-miss, never a wrong answer.
- **Three locks.** `stateMu` guards bookkeeping maps and is never held across I/O; `slotMu` is
  **per model slot** (`lockSlot`) so a multi-GB save on slot 0 can't block a request landing on
  slot 1; `diskMu` serializes
  directory-wide prune passes. Lock order `slotMu` → `stateMu`, `slotMu` → `diskMu`; `statsMu`
  nests into none. A single global lock here made a multi-GB save for model A block every other
  model — regression test `TestSlotCache_SaveDoesNotBlockOtherModels`.
- **Stats lock.** `record()` uses `statsMu` so it is callable inside any `stateMu`/`slotMu` section
  without reentrancy.
- **Cold mint template mismatch.** `synthPrefill` always mints via OpenAI `/v1/chat/completions`; a
  harness on a different upstream template (Anthropic `/v1/messages`) may tokenize differently →
  `confirm-miss`, no correctness harm. Upgrade path: mint via the request's own endpoint.
- **Anthropic system.** `sessionAnchor` falls back to the top-level `"system"` field when there is
  no system-role message.
- **Recurrent / hybrid: `recurrentSkip`** — save, exact restore AND partial-prefix seeding are all a
  **no-op**. Whole-slot restore reuses **0 tokens** on GatedDeltaNet/SSM (the state is restorable
  only at its exact saved length → full reprocess; llama.cpp #21831), so `middleware` bails before
  reading the body. Detection: `newSlotCache`'s `recurrent` predicate reads the gguf
  (`autogen.ReadGgufMetadataCached`) and treats `FullAttnInterval > 0` as recurrent. **SWA (Gemma)
  and plain attention are NOT gated** — their restore does reuse. Ground truth is the KV Cache tab's
  confirm-hit/miss ratio: if a new arch shows `confirm-miss` waste, extend
  `recurrent`/`recurrentSkip`. Repro: `scripts/kvcache_probe.py switch` (warm) / `swap`
  (cross-process). Drop the guards in `middleware`/`saveOnEvict`/`restoreOnLoad` only if #21831
  lands. The *in-RAM same-process* checkpoint path is separate and already on
  (`internal/autogen/sizing.md`, `--ctx-checkpoints 2`).
- **Warm-slot skip (`preamble-warm`).** `onSwitch` does NOT restore the disk preamble when the slot
  already holds that exact preamble live — that would clobber valid live state, and skipping lets
  upstream reuse the prefix natively. The disk preamble earns its keep on a genuinely cold load,
  **plain-attention models only**.
