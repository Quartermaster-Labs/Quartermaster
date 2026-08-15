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

## SAM is always CPU

`LiveOffloadArgs` appends `--no-gpu` to every `.ggml`: the Vulkan SAM backend returns garbage
on RX 7900 XTX (both PCS text and PVS box/point) while CPU is correct. See
[`classes.md`](classes.md).
