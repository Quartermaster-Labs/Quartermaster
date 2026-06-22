package autogen

// estimate.go exposes the per-profile sizer as a one-shot preview so the web
// editor can show live VRAM/RAM use for a candidate tuning before it is saved.
// It mirrors the solo-profile path of emitModel without writing any config.

// EstimateInput is the subset of editor fields that affect placement/memory.
// Zero Ctx means "let the sizer pick". Zero TargetVramGB uses settings.
type EstimateInput struct {
	Ctx            int
	KvK            string
	KvV            string
	KvInRam        bool
	Spec           string
	TargetVramGB   float64
	CpuOffload     int  // >0 pins layers offloaded to CPU, overriding the sizer
	CtxCheckpoints *int // nil => llama default (32); 0 disables; reserves checkpoint VRAM
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
	RamExceeded  bool    `json:"ramExceeded"`
	IsMoE        bool    `json:"isMoE"`
}

// EstimatePlan sizes one candidate tuning against the given settings + gguf
// metadata, returning the chosen ctx and the resulting VRAM/RAM estimate. It
// reuses sizeProfile/forceLowActiveMoE so the preview matches what a save would
// actually emit for the solo profile.
func EstimatePlan(s Settings, meta Metadata, in EstimateInput) (EstimateResult, error) {
	// KV quant: forced matched q8_0 unless a valid matched override is given
	// (mirrors emitModel).
	kvK, kvV := "q8_0", "q8_0"
	if in.KvK != "" {
		kvK = in.KvK
	}
	if in.KvV != "" {
		kvV = in.KvV
	}
	if kvK != kvV || kvK == "iq4_nl" || kvV == "iq4_nl" {
		kvK, kvV = "q8_0", "q8_0"
	}

	perTokGB, kvConstGB := 0.0, 0.0
	if m := GetKvCostModel(meta, kvK, kvV); m.OK {
		perTokGB, kvConstGB = m.SlopeGB, m.ConstGB
	}

	modelMax := 32768
	if meta.ContextLength > 0 {
		modelMax = int(meta.ContextLength)
	}

	target := s.TargetVramGB
	if in.TargetVramGB > 0 {
		target = in.TargetVramGB
	}

	specOh := 0.0
	if in.Spec == "draft-mtp" {
		specOh = 0.34
	}

	prof := profile{
		Name:     "estimate",
		Target:   target,
		Overhead: s.VramOverheadGB + specOh,
		Ctx:      in.Ctx,
		Spec:     in.Spec,
		KvK:      kvK,
		KvV:      kvV,
		IsLong:   in.Ctx >= 65536,

		CtxCheckpoints: in.CtxCheckpoints,
	}
	prof.Overhead += computeBufferGB(meta, effectiveUb(prof, nil), s.ComputeBufFactor)

	ctx, plan, kvReserve, err := sizeProfile(meta, s, prof, perTokGB, kvConstGB, modelMax, in.KvInRam)
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
		checkpointGB = checkpointReserveGB(prof, perTokGB, kvConstGB, ckptCtxCeil)
	}

	ngl, ncpuMoe := forceLowActiveMoE(meta, plan, prof, kvReserve)
	if in.CpuOffload > 0 {
		ngl, ncpuMoe = applyForcedOffload(meta, in.CpuOffload)
		plan.EstVramGB, plan.EstRamGB = estForOffload(meta, prof, kvReserve, ngl, ncpuMoe)
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
		RamExceeded:  plan.RamExceeded,
		IsMoE:        meta.IsMoE,
	}, nil
}
