package autogen

// Sizing math for the config emitter: pick the largest context that fits the
// VRAM budget for a profile, then derive the offload split (-ngl / --n-cpu-moe),
// KV checkpoint reserve, compute-buffer overhead, and mmproj/draft costs that
// the chosen context implies. Pure arithmetic over Metadata + Settings — no
// YAML, no command rendering.

import (
	"math"
)

const (
	// longCtxThreshold is the context at which a profile counts as long: the KV
	// cache dominates the footprint and the compute buffer is exercised over a
	// far bigger window, so the plan needs slack the short profiles don't.
	longCtxThreshold = 65536
	// longCtxHeadroomGB is the TOTAL safety slack a long profile should hold back.
	// Fitting a long context to the last megabyte is exactly the case where WDDM
	// demotes the overflow to shared memory instead of failing (see the package
	// doc), so the budget is shaved rather than spent. It is a floor, not an
	// addition — see longCtxTarget.
	longCtxHeadroomGB = 0.5
)

// longCtxTarget applies that headroom to a profile's budget, TOPPING UP whatever
// slack vramOverheadGB already holds back rather than stacking on top of it.
//
// Both knobs exist for the same reason (allocator/runtime slack so a tight fit
// doesn't get demoted to shared memory), and charging both cost a long profile a
// full extra 0.5 GB it never spent: on a 22.8 GB target with vramOverheadGB 0.5,
// a 100k dense profile sized against 22.3, then GetLoadPlan subtracted the 0.5
// again as cudaOverhead — 1.0 GB of pad, of which the estimate reported ~0.5 as
// used VRAM that the launch never allocated. So the card measured ~1 GB below the
// target and -ngl dropped a layer (and that layer's KV to RAM) for nothing.
//
// vramOverheadGB is already charged into every plan via prof.Overhead, so a long
// profile only needs the difference. With the default 0.5 this is now a no-op;
// raising vramOverheadGB shrinks it to zero rather than compounding.
//
// The headroom is charged HERE rather than inline at the ctx-tier call site in
// Generate, which is why the editor's preview and the baked config used to
// disagree: EstimatePlan flags IsLong on the same threshold but sized against the
// full target. Charging it here means every long profile — ctx tier, named
// variant, an explicit long Ctx on the model itself, and the preview — sizes
// identically.
func longCtxTarget(prof profile, vramOverheadGB float64) float64 {
	headroom := longCtxHeadroomGB - vramOverheadGB
	if headroom <= 0 {
		return prof.Target
	}
	if prof.IsLong && prof.Target > headroom {
		return prof.Target - headroom
	}
	return prof.Target
}

// sizeProfile computes the context window and load plan for one profile,
// mirroring the dense / MoE / kv-in-ram / no-attn branches of Generate-Config.
//
// ckptGB is the context-checkpoint reserve this profile charged into the plan
// (0 when KV lives in RAM or the model has no attention dims). It is returned
// separately from kvReserve — which stays the pure KV cost the UI reports — so
// a caller that RE-derives the estimate for a pinned placement (estForOffload)
// charges the same checkpoint bytes GetLoadPlan already did. Dropping it there
// made a pinned/forced-offload estimate read ~ckptGB lower than the auto one
// for the same placement, so the numbers didn't reconcile (22.4 auto vs
// 21.8 + 0.3 pinned).
func sizeProfile(meta Metadata, s Settings, prof profile, perTokGB, kvConstGB float64, modelMax int, kvInRam bool) (ctx int, plan LoadPlan, kvReserve, ckptGB float64, err error) {
	target := longCtxTarget(prof, s.VramOverheadGB)
	overhead := prof.Overhead

	switch {
	case kvInRam && perTokGB > 0:
		kvReserve = 0.1
		plan, err = GetLoadPlan(meta, planOpt(target, s.MaxRamGB, kvReserve, overhead))
		if err != nil {
			return
		}
		ctxBudgetRam := s.MaxRamGB - plan.EstRamGB
		if ctxBudgetRam < 0.5 {
			ctxBudgetRam = 0.5
		}
		maxCtxRam := MaxCtxForBudget(ctxBudgetRam, perTokGB, kvConstGB)
		ctx = RoundedCtx(float64(min(modelMax, maxCtxRam)))
		if prof.Ctx != 0 {
			ctx = min(ctx, prof.Ctx)
		}
		kvReserve = KvReserveGB(ctx, perTokGB, kvConstGB)

	case perTokGB > 0:
		ckptCtxCeil := modelMax
		if prof.Ctx != 0 {
			ckptCtxCeil = min(ckptCtxCeil, prof.Ctx)
		}
		ckpt := checkpointReserveGB(prof, perTokGB, kvConstGB, ckptCtxCeil, meta.FullAttnInterval > 0)
		ckptGB = ckpt
		// Checkpoints live wherever the KV cache does. MoE keeps KV (and thus its
		// checkpoints) VRAM-resident even when expert weights spill to CPU via
		// --n-cpu-moe, so they're a flat VRAM overhead. Dense models keep the KV
		// of any CPU-offloaded layer in RAM, so the checkpoint cost is folded into
		// the per-layer KV reserve (placementCkpt) and split across the GPU/CPU
		// layers by densePlacement instead of charged whole to VRAM up front
		// (which drove -ngl toward 0 at partial offload).
		placementCkpt := 0.0
		if meta.IsMoE {
			overhead += ckpt
			if prof.Ctx != 0 {
				// Explicit ctx (a custom ctx tier / variant) is HARD: honor it and
				// let GetLoadPlan below trade expert layers (--n-cpu-moe) for the
				// larger KV reserve, instead of shrinking ctx to whatever VRAM is
				// free. "64k variant" means 64k context, capped only by modelMax.
				ctx = RoundedCtx(float64(min(modelMax, prof.Ctx)))
			} else {
				share := effectiveShare(meta, genMoeShareFor)
				nonExpert := meta.FileSizeGB * (1.0 - share)
				usableBase := target - 0.25 - overhead
				if meta.FileSizeGB <= usableBase {
					kvBudget := target - meta.FileSizeGB - overhead
					if kvBudget < 0.1 {
						kvBudget = 0.1
					}
					ctx = RoundedCtx(float64(min(modelMax, MaxCtxForBudget(kvBudget, perTokGB, kvConstGB))))
				} else {
					maxKvVram := target - nonExpert - overhead
					if maxKvVram < 0.1 {
						maxKvVram = 0.1
					}
					maxCtxVram := MaxCtxForBudget(maxKvVram, perTokGB, kvConstGB)
					ctx = RoundedCtx(float64(min(min(modelMax, s.MoeCtxTarget), maxCtxVram)))
				}
			}
		} else {
			ladder := s.DenseCtxLadder
			minCtx := s.DenseMinCtx
			if prof.Ctx != 0 {
				ladder = []int{prof.Ctx}
				minCtx = prof.Ctx
			}
			// Size ctx conservatively against the checkpoint cost (overhead+ckpt),
			// but keep ckpt out of overhead so placement can split it per-layer.
			d := GetDenseCtx(DenseCtxParams{
				ModelMax: modelMax, PerTokGB: perTokGB, KvConstGB: kvConstGB,
				FileSizeGB: meta.FileSizeGB, TargetVramGB: target, Overhead: overhead + ckpt,
				Ladder: ladder, MinCtx: minCtx, AllowOffload: prof.Ctx != 0,
			})
			ctx = d.Ctx
			if prof.Ctx != 0 {
				ctx = min(ctx, prof.Ctx)
			}
			placementCkpt = ckpt
		}
		kvReserve = KvReserveGB(ctx, perTokGB, kvConstGB)
		plan, err = GetLoadPlan(meta, planOpt(target, s.MaxRamGB, kvReserve+placementCkpt, overhead))
		if err != nil {
			return
		}

	default:
		ctx = RoundedCtx(float64(min(modelMax, 32768)))
		if prof.Ctx != 0 {
			ctx = min(ctx, prof.Ctx)
		}
		kvReserve = 0
		// No attention dims: planner uses its flat 1.0GB KV reserve default.
		plan, err = GetLoadPlan(meta, PlanOptions{TargetVramGB: target, MaxRamGB: s.MaxRamGB, CudaOverheadGB: overhead, cudaSet: true})
		if err != nil {
			return
		}
	}
	return
}

// llamaDefaultCtxCheckpoints is llama-server's --ctx-checkpoints default when
// the flag is omitted (PR #15293). Tuned for a multi-slot server; overkill for
// local single-user serving, so we override it with defaultCtxCheckpoints.
const llamaDefaultCtxCheckpoints = 32

// defaultCtxCheckpoints picks a sane checkpoint count for a model that doesn't
// set ctxCheckpoints itself.
//
// recurrent (FullAttnInterval>0: GatedDeltaNet/SSM hybrids) => 2. Checkpoint
// restore on these archs USED to land at the wrong position and spam
// "non-consecutive token position" + reprocess the whole prompt (upstream
// llama.cpp #21831), so we disabled them. That's now fixed upstream — measured on
// qwen3.6-27b: after clobbering the slot, returning to a prior 3524-token prompt
// restored 3520 tokens from a checkpoint (4 reprocessed), zero spam. Their KV is
// cheap (only the ~1/4 full-attn layers carry it), so a couple is nearly free.
//
// Otherwise kvConstGB > 0 means SWA: the KV window rolls, so prefix-cache reuse
// breaks on a context shift and checkpoints (which DO restore on SWA) are the
// only reuse path — worth a few, though each is pricey. Plain full-attention
// models keep a persistent KV that already covers linear chat, so they need only
// a couple for the occasional edit/branch.
//
// SWA gets 3, not more: a checkpoint costs kvConstGB + perTokGB*minStep, and on
// SWA the kvConstGB term (every local layer's whole window) dominates and is paid
// per snapshot — 6 snapshots reserved GBs on gemma-class models and crowded out
// the context. Rewind coverage is bought back by widening the spacing instead
// (defaultCheckpointMinStep), which only scales the cheap perTokGB term.
func defaultCtxCheckpoints(kvConstGB float64, recurrent bool) int {
	if recurrent {
		return 2
	}
	if kvConstGB > 0 {
		return 3
	}
	return 3
}

// defaultCheckpointMinStep picks the -cms (minimum prompt-token spacing between
// context checkpoints) for a model that doesn't pin one.
//
// SWA models get a wide 1024: with only 3 snapshots retained, spacing is what
// decides how far back a divergent prompt can be restored from, and on SWA the
// per-snapshot cost is dominated by the ctx-independent window state (kvConstGB),
// so quadrupling the step adds only perTokGB*768 per snapshot — far cheaper than
// paying kvConstGB again for extra snapshots. Everything else keeps llama's 256:
// their kvConstGB is ~0, so snapshots are cheap and tight spacing restores more
// of a diverging prompt.
func defaultCheckpointMinStep(kvConstGB float64, recurrent bool) int {
	if !recurrent && kvConstGB > 0 {
		return swaCheckpointMinStep
	}
	return checkpointMinStep
}

// swaCheckpointMinStep is the widened checkpoint spacing used for SWA models.
const swaCheckpointMinStep = 1024

// effectiveCheckpointMinStep resolves the spacing a profile will actually run
// with: an explicit pin wins, else the arch default.
func effectiveCheckpointMinStep(prof profile, kvConstGB float64, recurrent bool) int {
	if prof.CheckpointMinStep > 0 {
		return prof.CheckpointMinStep
	}
	return defaultCheckpointMinStep(kvConstGB, recurrent)
}

// checkpointMinStep mirrors llama-server's --checkpoint-min-step default â€” the
// minimum prompt-token spacing between context checkpoints. It is the floor for
// the per-checkpoint global-KV term; the spacing actually emitted (and charged)
// comes from effectiveCheckpointMinStep.
const checkpointMinStep = 256

// effectiveCtxCheckpoints resolves the checkpoint count a profile will actually
// run with: an explicit value (incl. 0 = disabled) when set, else the
// llama-server default.
func effectiveCtxCheckpoints(prof profile, def int) int {
	if prof.CtxCheckpoints != nil {
		return *prof.CtxCheckpoints
	}
	return def
}

// checkpointReserveGB estimates the extra VRAM a profile's context checkpoints
// consume. llama-server keeps up to --ctx-checkpoints KV snapshots so a diverging
// prompt can be restored instead of reprocessed. Each snapshot holds the
// ctx-independent window/recurrent state (kvConstGB) plus roughly one
// checkpoint-min-step worth of global KV. Left unaccounted, the default 32
// snapshots silently overflow VRAM into sysmem and tank decode speed.
//
// The count is capped by how many checkpoints can actually exist: at min-step
// spacing a context of ctxCeil tokens holds at most ctxCeil/step snapshots, so a
// small pinned ctx (e.g. a 4k judge) reserves far fewer than the 32 default.
// Returns 0 when checkpoints are disabled or the model has no VRAM-resident KV.
func checkpointReserveGB(prof profile, perTokGB, kvConstGB float64, ctxCeil int, recurrent bool) float64 {
	n := effectiveCtxCheckpoints(prof, defaultCtxCheckpoints(kvConstGB, recurrent))
	if n <= 0 || (perTokGB <= 0 && kvConstGB <= 0) {
		return 0
	}
	step := effectiveCheckpointMinStep(prof, kvConstGB, recurrent)
	if ctxCeil > 0 {
		if maxN := ctxCeil / step; maxN < n {
			n = maxN
		}
	}
	if n <= 0 {
		return 0
	}
	perCheckpoint := kvConstGB + perTokGB*float64(step)
	return float64(n) * perCheckpoint
}

// planOpt builds PlanOptions with explicit reserve + overhead set.
func planOpt(target, maxRam, kvReserve, overhead float64) PlanOptions {
	return PlanOptions{
		TargetVramGB:     target,
		MaxRamGB:         maxRam,
		KvCacheReserveGB: kvReserve,
		CudaOverheadGB:   overhead,
		kvReserveSet:     true,
		cudaSet:          true,
	}
}

// forceLowActiveMoE recomputes the expert split for low-active MoE models that
// the planner's PCIe-thrash crossover wrongly fell back to naive -ngl on. Keeps
// dense+attention on GPU (-ngl 99) with experts on CPU.
func forceLowActiveMoE(meta Metadata, plan LoadPlan, prof profile, kvReserve float64) (ngl, ncpuMoe int) {
	ngl, ncpuMoe = plan.Ngl, plan.NCpuMoe
	if !(meta.IsMoE && ncpuMoe == 0 && ngl < 99) {
		return
	}
	share := effectiveShare(meta, genMoeShareFor)
	reserve := 1.0
	if kvReserve > 0 {
		reserve = kvReserve
	}
	usable := prof.Target - reserve - prof.Overhead
	nonExpert := meta.FileSizeGB * (1.0 - share)
	perMoeLayer := (meta.FileSizeGB * share) / float64(meta.BlockCount)
	moeOnGpu := math.Floor((usable - nonExpert) / perMoeLayer)
	if moeOnGpu > float64(meta.BlockCount) {
		moeOnGpu = float64(meta.BlockCount)
	}
	if moeOnGpu < 0 {
		moeOnGpu = 0
	}
	ncpuMoe = int(math.Max(0, float64(meta.BlockCount)-moeOnGpu))
	ngl = 99
	return
}

// applyForcedOffload overrides the auto placement with a user-pinned number of
// layers pushed to CPU (Override.CpuOffload). MoE models offload expert layers
// (--n-cpu-moe n, GPU stays -ngl 99); dense models drop GPU layers
// (-ngl = blocks-n). n is clamped to [0, blockCount].
func applyForcedOffload(meta Metadata, n int) (ngl, ncpuMoe int) {
	blocks := int(meta.BlockCount)
	if n < 0 {
		n = 0
	}
	if blocks > 0 && n > blocks {
		n = blocks
	}
	if meta.IsMoE {
		return 99, n
	}
	ngl = blocks - n
	if ngl < 0 {
		ngl = 0
	}
	return ngl, 0
}

// estForOffload recomputes the VRAM/RAM estimate for a forced placement so the
// generated header comment (and the editor preview) reflect the pinned offload
// rather than the auto sizer's numbers. Mirrors the cost model in plan.go.
//
// ckptGB is sizeProfile's checkpoint reserve and MUST be charged here too, the
// same way sizeProfile charged it into the plan: flat VRAM on MoE (KV and its
// snapshots stay GPU-resident under --n-cpu-moe), folded into the per-layer
// reserve on dense (a CPU layer keeps its KV — and its checkpoint share — in
// RAM). Omitting it silently under-reported every pinned placement by the
// checkpoint reserve, so estVram + estRam no longer summed to the auto plan's
// total and the config baked an optimistic estVramGB for the router to admit
// against.
func estForOffload(meta Metadata, prof profile, kvReserve, ckptGB float64, ngl, ncpuMoe int) (estVram, estRam float64) {
	size := meta.FileSizeGB
	blocks := float64(meta.BlockCount)
	overhead := prof.Overhead
	if blocks <= 0 {
		return prof.Target, 0
	}
	if meta.IsMoE {
		share := effectiveShare(meta, genMoeShareFor)
		nonExpert := size * (1.0 - share)
		expertGpuFrac := (blocks - float64(ncpuMoe)) / blocks
		estVram = nonExpert + size*share*expertGpuFrac + kvReserve + ckptGB + overhead
		estRam = size * share * (float64(ncpuMoe) / blocks)
		return round(estVram, 2), round(estRam, 2)
	}
	gpuFrac := float64(ngl) / blocks
	if gpuFrac > 1 {
		gpuFrac = 1
	}
	estVram = gpuFrac*(size+kvReserve+ckptGB) + overhead
	estRam = (1 - gpuFrac) * (size + kvReserve + ckptGB)
	return round(estVram, 2), round(estRam, 2)
}

// Empirical compute-graph constants. With flash attention on (the default), the
// CUDA compute buffer is dominated by the logits/output tensor plus a handful of
// n_ubatch*n_embd activation copies; computeCudaCtxGB covers the fixed CUDA
// runtime + cuBLAS workspace. The activation-copy count is a coarse fit, so
// Settings.ComputeBufFactor scales the whole analytic term for per-build/arch
// calibration against the "compute buffer size" llama logs.
//
// computeLogitsTokens caps the vocab-scaled term at n_vocab*THIS*4. The output
// TENSOR is sized by n_outputs (~1 in prefill) — but empirically the CUDA compute
// buffer for a large-vocab model still grows with the physical batch (output-
// projection / cuBLAS workspace tiled over the ubatch). Measured on Qwen3.6-35B-A3B
// (vocab 248320, embd 2048) on an 8GB card: at ub=512 the model fits its budget, at
// ub=1024 it spills ~0.5GB into shared memory. A 256 cap made the estimate nearly
// ub-blind (+31MB from 512->1024) so the sizer never charged the extra ub cost and
// over-committed VRAM. Cap at 1024 so the term scales with ub across the useful
// range (vocab*1024*4 ~1.0GB, giving the observed +0.5GB from 512->1024) and stops
// the overfill. ponytail: ceiling is 1024 (== the common max ub); revisit if ub>1024
// is used, and dial Settings.ComputeBufFactor down if it over-offloads other models.
const (
	computeActCopies    = 8.0
	computeCudaCtxGB    = 0.3
	computeLogitsTokens = 1024.0
	computeFallbackGB   = 0.17 // vocab/embd dims missing => prior flat estimate
)

// Do NOT scale this model down on Vulkan/ROCm without measuring PEAK, not idle.
// An idle process (post-load, one short prompt) holds ~1.9GB less than the same
// process mid-generation: context checkpoints, the exercised compute buffer, and
// the MTP draft context only materialize under a real prompt. Measured on an RX
// 7900 XTX (b10405-vulkan, Qwen3.6-27B UD-Q4_K_XL, ctx 102400, -ngl 99): idle
// 20.51GB dedicated, but generating 20.27GB dedicated + 2.11GB SHARED = 22.38GB
// real footprint against a ~22.4GB estimate. Comparing the idle figure against a
// peak-modeling estimate makes the compute term look ~1.6GB fat when it is not,
// and a factor derived that way plans straight into a driver spill.

// computeBufferGB estimates the GPU compute buffer (logits + activations + CUDA
// runtime) for a given physical batch (ub). This lives on the GPU regardless of
// CPU expert offload, so it is charged as flat VRAM overhead.
func computeBufferGB(meta Metadata, ub int, factor float64) float64 {
	if factor <= 0 {
		factor = 1.0
	}
	embd := float64(meta.EmbeddingLength)
	vocab := float64(meta.VocabSize)
	if embd <= 0 || vocab <= 0 || ub <= 0 {
		return computeFallbackGB
	}
	logits := vocab * math.Min(float64(ub), computeLogitsTokens) * 4.0
	acts := float64(ub) * embd * computeActCopies * 4.0
	// The fixed CUDA-context constant is a CUDA-runtime cost; only charge it when a
	// CUDA (NVIDIA) GPU is actually in use. On Vulkan/ROCm (AMD/Intel) the runtime
	// context buffer differs, so charging the CUDA figure over-counts.
	// ponytail: Vulkan/ROCm get 0 here rather than their own constant — add a
	// per-backend value if a non-CUDA build's context buffer proves to matter.
	ctxOh := 0.0
	if usingCudaGPU() {
		ctxOh = computeCudaCtxGB
	}
	return ctxOh + factor*(logits+acts)/gib
}

// clipComputeBufferGB models the CLIP vision tower's peak GPU compute buffer from
// the mmproj's own hparams, so a "-vision" twin's reserve scales per projector
// rather than a flat pad. The vision graph runs standard (non-flash) attention, so
// the n_head × n_patches² score matrix dominates, plus per-patch FFN+hidden
// activations, × a graph factor for ggml's intermediate copies. n_patches is the
// base image_size/patch_size grid. Qwen-VL dynamic resolution can exceed the base
// tile on a large image, but the spawn-time offload guard REFUSES an over-budget
// load rather than OOMing, so sizing for the base (typical) tile trades a little
// big-image headroom for not permanently over-reserving. Returns 0 when the gguf
// carries no vision dims (caller falls back to the flat VisionOverheadGB).
func clipComputeBufferGB(m Metadata) float64 {
	if m.VisionImageSize <= 0 || m.VisionPatchSize <= 0 || m.VisionEmbd <= 0 {
		return 0
	}
	grid := m.VisionImageSize / m.VisionPatchSize
	nPatch := float64(grid * grid)
	heads := float64(m.VisionHeads)
	if heads <= 0 {
		heads = 1
	}
	ffn := float64(m.VisionFFN)
	if ffn <= 0 {
		ffn = 4 * float64(m.VisionEmbd)
	}
	const bytesF32, graphFactor = 4.0, 1.3
	kq := heads * nPatch * nPatch * bytesF32                 // attention score matrix (peak)
	act := nPatch * (ffn + float64(m.VisionEmbd)) * bytesF32 // ffn + hidden activations
	return graphFactor * (kq + act) / gib
}

// MmprojVramGB is a "-vision" twin's total projector VRAM footprint: the projector
// gguf's own weights (fileSizeGB — resident on GPU by default) plus its CLIP
// compute buffer. The buffer is modeled per-projector from the mmproj hparams
// (clipComputeBufferGB, read via ReadGgufMetadataCached) when mmprojPath is
// available; otherwise it falls back to the flat s.VisionOverheadGB. Charged at
// BOTH generate time (baked plan) and spawn time (LiveOffloadArgs via
// EstimateInput.MmprojGB), so the live guard sizes the twin against the same
// footprint the config assumed.
func MmprojVramGB(mmprojPath string, fileSizeGB float64, s Settings) float64 {
	buf := s.VisionOverheadGB
	if mmprojPath != "" {
		if mm, err := ReadGgufMetadataCached(mmprojPath); err == nil {
			if b := clipComputeBufferGB(mm); b > 0 {
				buf = b
			}
		}
	}
	return fileSizeGB + buf
}

// draftOverheadGB returns the VRAM overhead to charge for the active spec
// chain's draft model. A baked-in MTP nextn layer with no separate weights
// file is a flat ~0.34 GB (KV+compute). A separate draft gguf — an MTP
// sidecar (Gemma-4) or any DFlash block-diffusion drafter, which is always a
// separate file — charges its real on-disk weight size plus a small
// KV/compute pad instead, so large drafts scale up rather than under-counting.
func draftOverheadGB(spec string, draftSizeGB float64) float64 {
	switch {
	case specHas(spec, "draft-dflash"):
		return draftSizeGB + 0.1
	case specHas(spec, "draft-mtp"):
		if draftSizeGB > 0 {
			return draftSizeGB + 0.1
		}
		return 0.34
	default:
		return 0
	}
}
