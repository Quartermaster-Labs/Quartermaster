# autogen — VRAM sizing & load-plan math

How a model's `-ngl` / `--n-cpu-moe` / ctx / KV quant / checkpoint numbers are
derived. Read this before touching `plan.go`, `kvcost.go`, `generate_sizing.go`
or any `estVramGB` producer. Spawn-time re-sizing lives in
[`liveoffload.md`](liveoffload.md).

## Compute buffer

**Modeled, not a flat fudge.** `computeBufferGB` (`generate_sizing.go`) =
logits (`VocabSize*min(ub,computeLogitsTokens)*4`) + activations
(`ub*EmbeddingLength*~8*4`) + a fixed CUDA-ctx constant (`computeCudaCtxGB=0.3`,
charged only when `usingCudaGPU()`), scaled by `settings.computeBufFactor`.
Replaced a flat `ubSoloOh=0.17` that undercounted by >1 GB on large-vocab models
and drove VRAM spillover.

- **`computeLogitsTokens` is 1024, not 256.** llama.cpp sizes the output *tensor*
  by `n_outputs`, but the measured CUDA compute buffer still grows with the
  physical batch (output projection / cuBLAS workspace tiled over the ubatch). At
  256 the estimate was nearly ub-blind (+31 MB going 512→1024), so the sizer never
  charged the extra ub cost and over-committed — ~0.5 GB spill on Qwen3.6-35B-A3B/8 GB.
  1024 (== the common max ub) reproduces the measured +0.5 GB.
- **`VocabSize`** comes from `token_embd.weight`/`output.weight` tensor elems ÷
  `EmbeddingLength` (`gguf.go`); 0 dims → flat `computeFallbackGB`.
- **`DetectGpuCompute` must run before anything sizes a model** — hence its
  unconditional call in `cmd/quartermaster/quartermaster.go`. It used to sit inside the `-generate`
  branch, so a serve-only start sized a Vulkan box as CUDA.
- `effectiveUb` is shared by sizer and emit, so the charged ub matches the emitted `-ub`.

### Measure at PEAK or you will "prove" it is fat and plan into a spill

The model is CUDA-shaped, so scaling `computeBufFactor` down on Vulkan/ROCm looks
obviously right — it is a trap. An idle llama-server (loaded, one short prompt) holds
~1.9 GB less than the same process mid-generation: `--ctx-checkpoints` copies
(~0.18 GB each, 2 live), the exercised compute buffer, and the MTP draft context only
appear under a real prompt.

Measured, RX 7900 XTX (b10405-vulkan, Qwen3.6-27B UD-Q4_K_XL, vocab 248320 /
embd 5120 / ub 1024 / ctx 102400, `-ngl 99`): **idle** 20.51 GB dedicated (vs
20.37 GB modeled weights+KV+draft — making the 1.10 GB compute term look ~1.6 GB
over-charged) but **generating** 20.27 GB dedicated **+ 2.11 GB shared** = 22.38 GB
real, against a ~22.4 GB estimate. The estimate was right; the idle comparison was not.

This build prints no `compute buffer size` line at any verbosity, so per-process PDH
counters (`\GPU Process Memory(*)\Dedicated Usage` **and** `Shared Usage`) are the only
measurement — reading dedicated alone hides a spill completely, since demoted bytes
leave that counter.

### Windows over-commit degrades silently instead of failing

WDDM demotes allocations to system memory when a process exceeds its per-process budget
(below physical, and shrinking under pressure from other apps), so the adapter gauge
plateaus *under* the cap while the overflow moves to shared — 21.88/23.98 GB dedicated
while llama held 2.11 GB shared. Symptom: ~12× prefill collapse (644 t/s at `-ngl 64` →
53 t/s at `-ngl 99`, same model and build) plus desktop stutter as `dwm` gets evicted
(0.66 GB shared).

Consequences for the sizer: **fitting every layer is not the goal** — 64 layers with
~0.8 GB slack beats 65 layers with none by 12× — and the reachable ceiling is meaningfully
below physical, so a plan targeting ~100% of live free VRAM lands on the wrong side of the
line whenever desktop usage drifts a few hundred MB.

## Batch flags (`-ub` / `-b` / `--n-cpu-moe`)

Bench-validated, not folklore (Qwen3.5-35B-A3B IQ4_XS, 8 GB/32 GB rig):

- **`-ub` is prefill-only** (decode flat) and, *on MoE with CPU expert offload*, scales
  monotonically with **no plateau through 1024** — "smaller ub is faster" is false there.
  A bigger micro-batch amortises the per-batch PCIe expert fetch (**380 → 647 t/s** going
  512 → 1024 at `b=2048`). `computeBufferGB` charges its ~1 GB so the sizer won't
  over-allocate ctx and spill.
- **The dense default is 512, not 1024** (`effectiveUb` keys on `meta.IsMoE`). A
  fully-GPU-resident dense model has no expert fetch to amortise, so the bigger batch buys
  nothing: on a 7900 XTX with Qwen3.8-27B-UD-Q4_K_XL at `b=2048`, pp2048 measured
  **682 / 679 / 667 t/s at ub 512 / 1024 / 2048** — flat-to-inverted, 512 ~2% ahead at
  every depth (d0 682 vs 667, d16k 543 vs 532, d65k 335 vs 327). Because
  `computeLogitsTokens` caps the vocab-scaled term at exactly 1024, halving ub halves both
  that term and activations: **~0.37 GB back** on a 27B/151k-vocab model, better spent as
  ctx. A fully-resident *MoE* is untested and conservatively keeps 1024.
- **`-b` is decoupled from `-ub`, fixed at 2048** (clamped `>=ub`, `<=ctx`). A logical
  batch above the physical one pipelines more micro-batches per `decode()`, overlapping
  CPU expert-fetch with GPU compute: **+20–38% prefill, plateau at 2048, zero extra VRAM**
  (the compute buffer is sized by ub only). Previously `-b == -ub`.
- The real decode lever is neither: the emitted MoE placement (`-ngl 99 --n-cpu-moe N`) is
  **~2× the decode of pure layer-offload** (`-ngl 12`).

## MoE expert share

**Two share tables.** `plan.go` has `moeExpertShare` (planner); `generate.go` has
`genMoeShare` (generation-side ctx sizing + `forceLowActiveMoE`), which adds `qwen35moe`.
Both are only a *fallback*: `effectiveShare` prefers the exact `Metadata.ExpertWeightShare`
derived from the tensor section, using the arch table only when that is 0.

`plan.go`'s `densePlacement` **intentionally diverges** from the PowerShell original
(it fixes a per-layer KV-reserve bug) — see the comment at `plan.go`.

## KV quant

**Defaults to `f16`, steps down only under VRAM pressure.** `defaultKvQuant` (`kvcost.go`)
is the single default picker used by `emitModel`, `EstimatePlan` and `RenderSoloCmd` (that
last one used to hardcode `q8_0` with no MoE branch, so the editor previewed a KV type the
config never emitted).

- Auto = `f16` for every arch; `q8_0` only when `f16` can't reach `denseMinCtx` inside the
  model's budget. A quantized KV's cost shows up in long-context recall and multi-turn tool
  use well before perplexity moves, so a shrunken window is the lesser loss.
- `settings.kvQuant` pins one type fleet-wide and skips the decision.
- **`bf16` is never the auto pick**: same 2 bytes as `f16` but spends mantissa bits on
  exponent range K/V activations don't need, and `f16` is the native flash-attention path.
- Validation is `ValidKvPair`: llama's `kv_cache_types` (`f32 f16 bf16 q8_0 q5_1 q5_0 q4_1
  q4_0`) minus `iq4_nl` (no FA kernel), K and V matched. Anything else — including a typo —
  resets to the default rather than being emitted and killing the spawn. A per-model/variant
  override only takes effect if it is itself valid and matched.

`GetKvCostModel` gives KV size as `Slope*ctx + Const` GB, SWA/hybrid-SSM aware; `Const` is 0
for plain attention. `GetDenseCtx` is the speed-first dense context picker (avoid offloading
just to grow ctx).

## Context checkpoints

**Checkpoints reserve VRAM and shrink ctx.** Beyond emitting the `--ctx-checkpoints` count,
`checkpointReserveGB` (`generate_sizing.go`) reserves VRAM for them inside `sizeProfile`
(split across GPU/CPU KV reserve at partial offload), so a model with checkpoints gets a
smaller usable ctx — asserted by `checkpoint_test.go`. Cost is `n * (kvConstGB + perTokGB*step)`:
**`kvConstGB` is paid per snapshot**, which is why SWA (gemma) is the expensive case and
recurrent/plain (`kvConstGB` ~0) is nearly free.

**Count and spacing are tuned together.** Because the const term is per-snapshot but the
spacing term is not, rewind coverage is bought with a **wider `--checkpoint-min-step`, not
more snapshots**:

- `defaultCheckpointMinStep` emits `-cms 1024` for SWA (`swaCheckpointMinStep`), keeps
  llama's 256 elsewhere; emitted only when it differs from llama's default.
- `defaultCtxCheckpoints`: SWA 3 (was 6 — 6 × the whole window state reserved GBs on
  gemma-class models and crowded out ctx), plain attention 3 at default spacing.
- **Recurrent/hybrid emit 2** (`meta.FullAttnInterval > 0` — GatedDeltaNet/SSM, e.g. Qwen3.6).
  Was `0` (checkpoint restore landed at the wrong position → `non-consecutive token position`
  spam + full reprocess, llama.cpp #21831) but **fixed in the current build** (genVersion v18,
  2026-07-17): verified on qwen3.6-27b — after clobbering the slot with a divergent prompt,
  returning to a prior 3524-tok prompt restored 3520 tok from a checkpoint (4 reprocessed),
  zero spam; ckpt=0 fully reprocessed. Hybrid KV is cheap (~1/4 layers full-attn) so 2 is
  nearly free.
- The **emitted flag and the charged reserve share one resolver** (`effectiveCheckpointMinStep`)
  via `profile.CheckpointMinStep`, so they can't drift. A pinned `Override.CheckpointMinStep` /
  `VariantSpec.CheckpointMinStep` still wins (variants **inherit** the model-wide spacing,
  matching the effOv merge at emit — unlike `ctxCheckpoints`, which is standalone per variant).
  `LiveOffloadArgs` and the config-editor estimate both parse `-cms`.

This is the **in-RAM same-process** checkpoint path, separate from the server's slot-cache **disk**
path. On hybrids the disk path's *exact* save/restore works (measured 2026-08-19: 32,032 of 32,057
tokens reused across a process restart, prefill 60.6s → 0.35s); only its *partial-prefix seeding*
stays gated — see `internal/server/slotcache.md` (`seedSkip`). Repro is `kvcache_probe.py append`,
not `swap` (`swap` resends an identical prompt, which needs a rewind and always looks like a miss
on these archs).

## Speculative decoding / drafters

**DFlash drafters auto-detect and link like MTP sidecars, but DFlash is never auto-selected.**

- `discover.go`'s `dflashFileRe` matches "dflash" anywhere in a filename (publishers use it
  as an infix, unlike MTP's `mtp-` prefix) and pairs it to the same-dir target via
  `GgufRow.DraftPath`/`DraftKind`/`DraftSizeGB` — same `draftByDir` pass as MTP, different
  regex + kind.
- `effectiveSpec` defaults to the free baked-MTP head chained with model-less ngram
  (`meta.IsMTP` → `draft-mtp+ngram-mod`, benched better than mtp alone), else plain `ngram-mod`.
- A paired DFlash file takes effect **only** via an explicit `spec: draft-dflash` override.
  It used to auto-default when decode was GPU-bound (+17% on a short flat-prompt MoE bench),
  but real multi-turn use at the 100k tier craters: DFlash's resident draft weights *and* its
  own full-context KV compound against a tight budget as the session grows, hitting an
  oversubscription cliff mtp can't (mtp has no separate weights or KV). Still a legitimate
  opt-in for short-lived/fresh-context work.
- `buildCmdLines` emits `-md`/`-ngld 99` and defaults `--spec-draft-n-max` to **5** for dflash
  (n-max sweep: ~15% reasoning tg over n=3/4, ties n=6) vs **2** for MTP; `SpecDraftNMax`
  overrides either.
- **Never opt in on dense/CPU-bound models:** on Dense-27B it merely ties mtp on decode, costs
  1.43 GB resident, and the draft's own prefill craters pp by −55%.
- Any separate draft file is charged via `draftOverheadGB` = real on-disk size + 0.1 GB pad
  (the flat 0.34 GB is only for a baked-in MTP nextn layer with no file).

## RoPE scaling

**The only thing that lifts the trained-ctx ceiling, and it derives its own factor.** Every
sizing path goes through `ropeCeiling(meta, ov.RopeScaling, ov.Ctx)` (`rope.go`):
`""`/`"none"` clamp to the trained length (a bigger window over untrained positions is
garbage, not more context); `linear`/`yarn` raise the ceiling to the requested ctx bounded by
`maxRopeFactor` (8×).

Four call sites: `generate.go` per model **and** per profile (a variant can enable it alone),
`generate_cmd.go` preview, `estimate.go` — plus `EstimateInput.RopeScaling`, so the cogwheel
preview and the spawn-time `LiveOffloadArgs` re-sizer size the same window the launch gets.

`buildCmdLines` also emits a **derived** `--rope-scale` = `ropeFactor(meta, ctx)` (ratio
rounded UP to a half step) whenever a scaling type is set with no explicit factor — a bare
`--rope-scaling yarn` leaves llama.cpp on the gguf's own factor (1.0 unless the publisher
fine-tuned for extension), i.e. the flag is on and does nothing. An explicit `ropeScale`
always wins.

## Emitted footprint (`estVramGB` / `estRamGB`)

**The sizer's footprint is emitted, not just a comment.** Every sizing emitter writes a
per-model `estVramGB:` line (`writeEstVram`, `generate_emit.go`) from the same number the
estimate already computed — `plan.EstVramGB` for LLMs, the `--max-vram` cap for image, the
vllm pool for vllm, size+overhead for embedding/qwentts.

That field is the **admission input** for the router's VRAM-budget-aware multi-load
(`internal/router/group.go`), paired with the top-level `vramBudgetGB:` (`emitVramBudget`,
= `settings.targetVramGB`, withheld when `settings.multiResident: false`).

- **CPU-resident classes deliberately emit nothing**: ASR (parakeet), SAM and TTS.cpp run on
  the CPU, so charging them GPU budget would evict a chat model for something that never
  wanted VRAM. Absent = unknown, which the router reads as "fall back to the static group
  policy" — never as 0 GB.
- **Consequence: a new emitter that puts weights on the GPU must call `writeEstVram`** or its
  model can never be co-resident.
- `writeEstRam` writes the companion `estRamGB:` (the weights/KV share that does *not* fit on
  the GPU) — purely informational, surfaced as the Models table's Est RAM column so a partial
  offload's system-memory cost is visible before loading. Nothing in the router reads it.
