# autogen — spawn-time placement guard (`liveoffload.go`)

Re-derives `-ngl` / `--n-cpu-moe` from free VRAM *right now*, so a stale baked plan
can't OOM. Generate-time math lives in [`sizing.md`](sizing.md).

## It is a spawn-time guard, NOT a regen

`LiveOffloadArgs` runs on every model spawn (wired by `server.WireDynamicOffload` →
router `SetSpawnArgs` → process `doStart`, only in `-generate` mode). It parses the
emitted argv (`-m`/`-c`/`-ctk`/`-ctv`/`--spec-type`/`--ctx-checkpoints`/`-cms`/`--mmproj`/
`-ngl`/`--n-cpu-moe`), reads the gguf (cached), and re-runs `EstimatePlan`.

- **It only ever offloads MORE** than the baked plan (raises `--n-cpu-moe` / lowers `-ngl`).
  Ample VRAM or a hand-pinned `cpuOffload` is left untouched.
- If `EstVramGB > freeGB` even at the planner's max offload it returns an error and the
  spawn is **refused** — a clean load failure, not an OOM crash.
- **Fails open**: non-`.gguf` cmd, no `-ngl`, unreadable gguf, or no GPU telemetry all pass
  the argv through unchanged.

## The budget is capped at `targetVramGB`

`TargetVramGB = liveBudgetGB(s, freeGB)` = **the tighter of live free and
`settings.targetVramGB`** (`EstVramGB` already folds overhead in, so it IS the live
footprint). The hard "can't fit at all" refusal still compares against raw free — a target
above what the card has left must not force an OOM.

The cap exists because the free reading is a *snapshot* and the other GPU clients on a
desktop are not static: measured on the RX 7900 XTX box, `dwm` alone held 2.08 GB and grew
after the load, with Discord/Steam/explorer/a VR runtime adding ~1.2 GB more. A plan sized to
100% of the spawn-time reading gets demoted to shared memory hours later — which is why the
same config "fit reliably" one day and stuttered the next. Capping at the target makes that
setting bind on the load path too (it used to apply only at generate time), which is what
makes the split deterministic day to day.

## Drafters and vision twins are charged

- A `-md` in the argv (separate MTP sidecar or DFlash drafter) is stat'd for its real
  on-disk size into `EstimateInput.DraftGB`, matching what generate-time bakes into the
  model's `Overhead` via `draftOverheadGB`. This previously used the flat 0.34 GB
  baked-in-MTP default regardless of the file's actual size, undercharging a
  multi-hundred-MB drafter.
- A `--mmproj` charges the projector's weights (its gguf size) **plus**
  `settings.visionOverheadGB` (default 1.0 GB) for the CLIP compute buffer — via
  `EstimateInput.MmprojGB` (`mmprojVramGB`), the same footprint generate-time bakes into the
  twin's `Overhead`. Before this, `EstimatePlan` was projector-blind (the `-m` gguf carries no
  vision info), so the guard sized a `-vision` twin as a bare LLM and under-offloaded, leaving
  too little free VRAM once the projector + image buffers loaded. Tune `visionOverheadGB`
  against the "CLIP … compute buffer size" llama prints for a real image.

## `minGpuFraction` is the admission floor

`LiveOffloadArgs` only ever offloads *more*, so without a floor a tight VRAM situation
degrades every load to CPU speed instead of refusing one ("everything loads, everything
crawls").

`gpuResidentFraction` scores the placement — `-ngl`/`BlockCount` dense,
`1 - share×(n-cpu-moe/blocks)` for MoE, where `share` is the expert-weight share — and the
spawn is refused when the live plan falls under `settings.minGpuFraction` (default 0.5)
**while the baked plan was above it**. That second condition matters: a model whose
*generated* plan is already CPU-heavy (deliberate `cpuOffload`, a model bigger than the card)
is never blocked — only one degraded by what is currently resident. Negative = explicit opt-out.

## The split ratio is re-derived at spawn

`retuneTensorSplit`. Everything else in `LiveOffloadArgs` adjusts HOW MUCH of a model goes to
the GPU; this decides WHERE it lands.

A baked `--tensor-split` was computed at generate time from each card's **idle** free VRAM. By
spawn time another model, or a game, can be sitting on one card only, and the stale ratio still
sends that card its full share of the layers: the total fits the pooled budget and the load OOMs
anyway. So the ratio and `--main-gpu` are rebuilt from the live per-device reading, charging
`EstimateResult.FixedGB` (the whole non-splittable share: compute buffer, CUDA context,
projector, headroom) to the main device exactly as generate does.

The live reading is deliberately NOT the idle high-water mark `SampleGpuSet` applies: the sizer
wants an idle budget, the spawn guard wants the truth. `Server.liveGpuSet` builds it from the
perf monitor's most recent sample and hands it over on `Settings.Gpus`, so this costs no extra
probe. No device set (no telemetry) or no `--tensor-split` in the argv leaves the argv untouched.

## SAM is always CPU

`LiveOffloadArgs` appends `--no-gpu` to every `.ggml`: the Vulkan SAM backend returns garbage
on RX 7900 XTX (both PCS text and PVS box/point) while CPU is correct. See
[`classes.md`](classes.md).

## The runtime half: `vramguard.go` (server)

The spawn guard only runs at exec, which leaves two holes: the router's admission
(`groupSwapper.budgetEviction`) charges a *static* budget that ignores what a game is holding
on the card, and nothing reacts *after* a load (a resident model is silently demoted into
shared memory when another app starts, with no error and no log). `vramguard` (`internal/server/vramguard.go`)
closes both from one sample taken on the perf monitor's own cadence (>=5s), so the admission
path stays a lock-free atomic read.

- **Live VRAM ceiling (admission).** The guard attributes the POOLED used memory of every
  eligible adapter (`pooledGPUStat`, same eligibility rule as the sizer via
  `autogen.EligibleGpuStats`) between
  quartermaster's own children and everyone else, and publishes a ceiling via the router's
  `SetLiveVramBudget` probe (`router.LiveVramFn`); `budgetEviction` admits against
  `min(vramBudgetGB, ceiling)`. The ceiling is measured as **foreign growth over an idle
  baseline**, not as raw free VRAM:

  ```
  excess  = max(0, foreignMB − foreignFloorMB)      // floor = idle low-water mark
  ceiling = vramBudgetGB − excess − oomGuardReserveGB   // and exactly vramBudgetGB when excess == 0
  ```

  The baseline matters: `total − foreign − reserve` reduces algebraically to `used > total −
  reserve` once you compare it against the resident set's summed `estVramGB`, so it fires on a
  perfectly healthy box whose card is legitimately full of *our own* well-planned models. The
  compositor's ~1.5 GB is already priced into `targetVramGB` and every `estVramGB` carries its
  own overhead pad — double-counting them would evict for nothing. Tracking excess over the
  idle floor means an untroubled box sees **zero** behaviour change, and only a *new* foreign
  claim (a game, a browser hitting the GPU) moves the ceiling. A missing or untrustworthy
  reading (no telemetry, or per-process attribution that can't see our own children — counting
  them as foreign would collapse the ceiling and evict everything) leaves the static budget in
  force.
- **Post-load watchdog (eviction).** When the resident set's summed `estVramGB` no longer fits
  the ceiling and stays over it for the grace period, idle models are unloaded — largest-first
  until the set fits — so the driver doesn't silently demote them. Only *idle* models are ever
  touched: a model with a request in flight (or still starting) is excluded, since a failed
  request is worse than a slow one, as are `persistent` members and CPU-only models (no
  `estVramGB`, nothing to reclaim). If nothing idle can be shed, the guard logs and accepts
  the degradation.
- **Refusal memo.** "Insufficient VRAM" spawn refusals are memoised per model for 30s, so a
  client retrying in a loop doesn't pay the full ~4s post-eviction reclaim probe on every
  retry. The memo is invalidated by time or by free VRAM coming back, whichever lands first —
  the moment another app releases the card the next request loads normally. A served model's
  memo is cleared on successful spawn, and memoised refusals carry a "(cached; …)" note so the
  log can tell a cached no from a freshly-probed one.

Three settings (in `overrides.go`, all defaulted in `applyDefaults`):

- `oomGuardReserveGB` (default **1.0**) — extra VRAM held back once foreign use is *already*
  above the idle floor, for the other client's continued growth: a game that just claimed 8 GB is
  still allocating. Charged only when `excess > 0`, so it never shrinks an untroubled box's
  budget. Negative = explicit opt-out (admit into the raw leftovers).
- `oomGuardEvict` (default **on**) — enables the post-load watchdog; `false` leaves resident
  models alone and accepts the silent shared-memory degradation.
- `oomGuardGraceSec` (default **30**) — how long the resident set must be over the ceiling
  before the watchdog sheds anything, since VRAM pressure is spiky (a shader compile, a video
  decode) and unloading on a transient spike costs a full reload for nothing.

The idle floor itself is a low-water mark over the sampled foreign readings (the same shape as
`Server.systemVramMB`), so the desktop compositor's steady baseline is learned rather than
guessed, and it follows foreign usage back *down* when the other app exits — otherwise the first
game of the session would keep the budget tight until restart.
