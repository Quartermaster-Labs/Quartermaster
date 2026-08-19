package autogen

// estimate.go exposes the per-profile sizer as a one-shot preview so the web
// editor can show live VRAM/RAM use for a candidate tuning before it is saved.
// It mirrors the solo-profile path of emitModel without writing any config.

// EstimateInput is the subset of editor fields that affect placement/memory.
// Zero Ctx means "let the sizer pick". Zero TargetVramGB uses settings.
type EstimateInput struct {
	Ctx     int
	KvK     string
	KvV     string
	KvInRam bool
	Spec    string
	// RopeScaling ("linear"/"yarn") lets Ctx exceed the model's trained length —
	// the preview must know, or it silently sizes a clamped window and reports a
	// KV reserve the real launch won't have.
	RopeScaling    string
	TargetVramGB   float64
	CpuOffload     int  // >0 pins layers offloaded to CPU, overriding the sizer
	CtxCheckpoints *int // nil => llama default (32); 0 disables; reserves checkpoint VRAM
	// CheckpointMinStep pins -cms (checkpoint spacing in prompt tokens), which
	// scales each snapshot's global-KV term. 0 => the arch default (wide on SWA).
	CheckpointMinStep int
	DraftGB           float64 // separate MTP/DFlash draft gguf weights (GB); 0 => baked-in or none
	MmprojGB          float64 // "-vision" twin projector footprint (weights + CLIP reserve); 0 => none
	// Ub pins -ub (physical batch). The compute buffer scales with it — ~0.5 GB
	// between 512 and 1024 on a large-vocab model — so a preview that ignored a
	// per-model Override.Ub charged a buffer the real launch never has. 0 => the
	// same auto pick emit makes (effectiveUb).
	Ub int
	// DraftKind is the paired draft sidecar's kind ("mtp"/"dflash", "" for none),
	// as DraftSidecarForDir reports it. Only consulted when Spec is empty, to
	// resolve the same auto spec the emitter picks — see EstimatePlan.
	DraftKind string
}

// EstimateResult is the previewed load plan for a candidate tuning.
type EstimateResult struct {
	Ctx          int     `json:"ctx"`
	Ngl          int     `json:"ngl"`
	NCpuMoe      int     `json:"nCpuMoe"`
	EstVramGB    float64 `json:"estVramGB"`
	EstRamGB     float64 `json:"estRamGB"`
	TargetVramGB float64 `json:"targetVramGB"`
	MaxRamGB     float64 `json:"maxRamGB"`
	KvReserveGB  float64 `json:"kvReserveGB"`
	// CheckpointGB is the VRAM reserved for context checkpoints (included in
	// EstVramGB via overhead, broken out so the UI can attribute it separately
	// from model weights).
	CheckpointGB float64 `json:"checkpointGB"`
	// DraftGB is the VRAM charged for the speculative draft / MTP nextn layer
	// (baked-in ~0.34 GB or a separate draft gguf's weights + pad). Folded into
	// EstVramGB via overhead; broken out so the UI can attribute it separately
	// from the main model weights. 0 when no draft-mtp spec is active.
	DraftGB float64 `json:"draftGB"`
	// ComputeBufGB is the GPU compute buffer (logits + activations + the CUDA
	// context constant when a CUDA GPU is in use). MmprojGB is a "-vision" twin's
	// projector weights + CLIP reserve (0 for non-vision). OverheadGB is the global
	// vramOverheadGB safety headroom. All three are folded into EstVramGB via
	// overhead; broken out so the UI can label them separately from the model
	// weights instead of lumping everything non-KV into "Weights".
	ComputeBufGB float64 `json:"computeBufGB"`
	MmprojGB     float64 `json:"mmprojGB"`
	OverheadGB   float64 `json:"overheadGB"`
	RamExceeded  bool    `json:"ramExceeded"`
	IsMoE        bool    `json:"isMoE"`
}

// EstimatePlan sizes one candidate tuning against the given settings + gguf
// metadata, returning the chosen ctx and the resulting VRAM/RAM estimate. It
// reuses sizeProfile/forceLowActiveMoE so the preview matches what a save would
// actually emit for the solo profile.
func EstimatePlan(s Settings, meta Metadata, in EstimateInput) (EstimateResult, error) {
	// KV quant: the fleet default (f16, stepping down to q8_0 only under VRAM
	// pressure) unless a valid matched override is given — mirrors emitModel, so
	// the preview's KV reserve matches what a save would emit.
	// An empty spec means AUTO, not "no drafter" — the emitter runs every model
	// through effectiveSpec, which hands an MTP-capable gguf draft-mtp+ngram-mod
	// and charges its 0.34 GB (or the sidecar's real weights). The preview used to
	// read "" as no spec and charge 0, so a model left on auto spec previewed
	// 0.34 GB lighter than it bakes. On a tight budget that is the whole margin:
	// Qwen3.8-27B at 106k previewed -ngl 99 / 0 GB RAM and emitted -ngl 64 / 0.32.
	spec := in.Spec
	if spec == "" {
		spec = effectiveSpec(meta, nil, in.DraftKind)
	}
	estTarget := s.TargetVramGB
	if in.TargetVramGB > 0 {
		estTarget = in.TargetVramGB
	}
	// Only charge the sidecar when the active spec will actually attach it as -md
	// (see matchedDraftSizeGB): a dir can pair a DFlash drafter to a model that
	// runs on a baked-in MTP head, and that drafter never loads.
	draftGB := matchedDraftSizeGB(spec, in.DraftKind, in.DraftGB)
	kvDef := defaultKvQuant(s, meta, estTarget, s.VramOverheadGB+draftOverheadGB(spec, draftGB))
	// The editor sends kvK/kvV independently, so a form that resolves only one
	// side must not sink the preview back to the fleet default — see
	// resolveKvPair.
	kvK, kvV := resolveKvPair(in.KvK, in.KvV, kvDef, kvDef)

	perTokGB, kvConstGB := 0.0, 0.0
	if m := GetKvCostModel(meta, kvK, kvV); m.OK {
		perTokGB, kvConstGB = m.SlopeGB, m.ConstGB
	}

	// Rope scaling lifts the trained-length ceiling; without it this is nativeCtx.
	modelMax := ropeCeiling(meta, in.RopeScaling, in.Ctx)

	target := s.TargetVramGB
	if in.TargetVramGB > 0 {
		target = in.TargetVramGB
	}

	// Draft overhead: baked-in MTP nextn layer ~0.34 GB (KV+compute). A separate
	// draft file (Gemma-4's MTP sidecar, or any DFlash drafter — always separate)
	// instead charges its real on-disk weights + a small KV/compute pad, so big
	// drafts scale up rather than under-counting at 0.34.
	specOh := draftOverheadGB(spec, draftGB)

	prof := profile{
		Name:     "estimate",
		Target:   target,
		Overhead: s.VramOverheadGB + specOh,
		Ctx:      in.Ctx,
		Spec:     spec,
		KvK:      kvK,
		KvV:      kvV,
		IsLong:   in.Ctx >= longCtxThreshold,

		CtxCheckpoints:    in.CtxCheckpoints,
		CheckpointMinStep: in.CheckpointMinStep,
	}
	// Charge the ub the launch will actually run with: effectiveUb reads the
	// override's pinned value, and passing nil here made the preview size a
	// different compute buffer than emit did for the same model.
	computeBufGB := computeBufferGB(meta, effectiveUb(prof, &Override{Ub: in.Ub}, prof.Ctx, target), s.ComputeBufFactor)
	prof.Overhead += computeBufGB
	prof.Overhead += in.MmprojGB // "-vision" projector weights + CLIP compute reserve

	ctx, plan, kvReserve, planCkptGB, err := sizeProfile(meta, s, prof, perTokGB, kvConstGB, modelMax, in.KvInRam)
	if err != nil {
		return EstimateResult{}, err
	}
	// Checkpoints live wherever the KV cache does. When KV is in RAM they cost
	// no VRAM, so report 0 to stay consistent with EstVramGB (which sizeProfile
	// only inflates in the VRAM-KV branch).
	checkpointGB := 0.0
	if !in.KvInRam {
		ckptCtxCeil := modelMax
		if prof.Ctx != 0 {
			ckptCtxCeil = min(ckptCtxCeil, prof.Ctx)
		}
		checkpointGB = checkpointReserveGB(prof, perTokGB, kvConstGB, ckptCtxCeil, meta.FullAttnInterval > 0)
	}

	ngl, ncpuMoe := forceLowActiveMoE(meta, plan, prof, kvReserve)
	if in.CpuOffload > 0 {
		ngl, ncpuMoe = applyForcedOffload(meta, in.CpuOffload)
	}
	// Any placement that differs from the one GetLoadPlan priced has to be
	// re-priced: forceLowActiveMoE rewrites -ngl/--n-cpu-moe in place and used to
	// leave plan.EstVramGB describing the placement it just discarded.
	if ngl != plan.Ngl || ncpuMoe != plan.NCpuMoe {
		plan.EstVramGB, plan.EstRamGB = estForOffload(meta, prof, kvReserve, planCkptGB, ngl, ncpuMoe)
		plan.RamExceeded = s.MaxRamGB > 0 && plan.EstRamGB > s.MaxRamGB
	}

	// Dense checkpoints are split GPU/RAM by the layer placement (see sizeProfile),
	// so the VRAM portion is only the GPU-resident fraction. Scale the reported
	// figure to match EstVramGB. MoE keeps KV (and checkpoints) fully in VRAM.
	if checkpointGB > 0 && !meta.IsMoE && meta.BlockCount > 0 {
		gpuFrac := float64(min(ngl, int(meta.BlockCount))) / float64(meta.BlockCount)
		if gpuFrac < 0 {
			gpuFrac = 0
		}
		checkpointGB *= gpuFrac
	}

	return EstimateResult{
		Ctx:          ctx,
		Ngl:          ngl,
		NCpuMoe:      ncpuMoe,
		EstVramGB:    plan.EstVramGB,
		EstRamGB:     plan.EstRamGB,
		TargetVramGB: target,
		MaxRamGB:     s.MaxRamGB,
		KvReserveGB:  kvReserve,
		CheckpointGB: checkpointGB,
		DraftGB:      specOh,
		ComputeBufGB: computeBufGB,
		MmprojGB:     in.MmprojGB,
		OverheadGB:   s.VramOverheadGB,
		RamExceeded:  plan.RamExceeded,
		IsMoE:        meta.IsMoE,
	}, nil
}
