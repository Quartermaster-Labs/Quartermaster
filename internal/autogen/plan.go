package autogen

import (
	"fmt"
	"math"
	"strings"
)

// moeExpertShare is the fraction of total weight in expert tensors, per arch.
// Approximate; mirrors the PowerShell planner table.
var moeExpertShare = map[string]float64{
	"gemma4":   0.88,
	"qwen3":    0.90,
	"qwen3moe": 0.90,
	"llama":    0.80,
	"lfm2":     0.78,
	"lfm2moe":  0.78,
}

const moeShareDefault = 0.85

// moeShareFor returns the expert-weight share for an architecture.
func moeShareFor(arch string) float64 {
	if s, ok := moeExpertShare[strings.ToLower(arch)]; ok {
		return s
	}
	return moeShareDefault
}

// effectiveShare prefers the exact expert-weight share derived from the gguf
// tensor section; it falls back to a per-arch heuristic only when the tensor
// section could not be sized (ExpertWeightShare == 0).
func effectiveShare(meta Metadata, fallback func(string) float64) float64 {
	if meta.ExpertWeightShare > 0 {
		return meta.ExpertWeightShare
	}
	return fallback(meta.Architecture)
}

// LoadPlan is the chosen llama-server placement plus diagnostics.
type LoadPlan struct {
	Ngl          int
	NCpuMoe      int
	EstVramGB    float64
	EstRamGB     float64
	TargetVramGB float64
	MaxRamGB     float64
	BlockCount   int
	Architecture string
	IsMoE        bool
	FileSizeGB   float64
	RamExceeded  bool
	Reasons      []string
}

// PlanOptions are the budgets fed to GetLoadPlan. Zero values fall back to the
// PowerShell defaults (KvCacheReserveGB=1.0, CudaOverheadGB=0.5).
type PlanOptions struct {
	TargetVramGB     float64
	MaxRamGB         float64 // 0 = no RAM check
	KvCacheReserveGB float64
	CudaOverheadGB   float64
	kvReserveSet     bool // internal: distinguish an explicit 0 reserve
	cudaSet          bool
}

// densePlacement chooses -ngl for a model placed layer-by-layer. Each
// GPU-resident layer carries BOTH its weight slice and its KV-cache slice in
// VRAM; layers left on the CPU keep their KV in RAM. So the full-ctx KV reserve
// is spread across blocks (kvReserve/blocks per layer) rather than subtracted
// whole from the VRAM budget up front. The old "usableVram = target - kvReserve"
// form reserved every layer's KV in VRAM even for CPU-resident layers, which
// drove ngl toward 0 while the reserved VRAM sat unused (e.g. a 24B dense at 64k
// ctx pinned to -ngl 0 on a 7GB budget). Diverges from the PowerShell
// Get-LlamaLoadPlan, which shares the same flaw.
func densePlacement(size, kvReserve float64, blocks int, target, cudaOverhead float64) (ngl int, estVram, estRam float64) {
	perLayer := (size + kvReserve) / float64(blocks)
	budget := target - cudaOverhead
	if perLayer > 0 && budget > 0 {
		ngl = int(math.Floor(budget / perLayer))
	}
	if ngl > blocks+1 {
		ngl = blocks + 1
	}
	if ngl < 0 {
		ngl = 0
	}
	gpuFrac := float64(ngl) / float64(blocks)
	if gpuFrac > 1 {
		gpuFrac = 1
	}
	estVram = gpuFrac*(size+kvReserve) + cudaOverhead
	estRam = (1 - gpuFrac) * (size + kvReserve)
	return
}

// GetLoadPlan derives -ngl and --n-cpu-moe from a VRAM budget and gguf metadata.
// Port of Get-LlamaLoadPlan.ps1.
func GetLoadPlan(meta Metadata, opt PlanOptions) (LoadPlan, error) {
	kvReserve := opt.KvCacheReserveGB
	if !opt.kvReserveSet && kvReserve == 0 {
		kvReserve = 1.0
	}
	cudaOverhead := opt.CudaOverheadGB
	if !opt.cudaSet && cudaOverhead == 0 {
		cudaOverhead = 0.5
	}

	arch := strings.ToLower(meta.Architecture)
	size := meta.FileSizeGB
	blocks := int(meta.BlockCount)
	isMoE := meta.IsMoE

	if blocks <= 0 {
		return LoadPlan{}, fmt.Errorf("load plan: invalid block_count=%d in metadata", blocks)
	}
	if size <= 0 {
		return LoadPlan{}, fmt.Errorf("load plan: invalid file size for %s", meta.Path)
	}

	usableVram := math.Max(0.0, opt.TargetVramGB-kvReserve-cudaOverhead)

	ngl := 99
	ncpuMoe := 0
	var reasons []string
	reasons = append(reasons, fmt.Sprintf("file=%.2fGB blocks=%d arch=%s moe=%v target_vram=%.1fGB usable_vram=%.1fGB",
		size, blocks, arch, isMoE, opt.TargetVramGB, usableVram))

	var estVram, estRam float64

	switch {
	case size <= usableVram:
		reasons = append(reasons, "fits whole-model in VRAM budget -> -ngl 99")
		estVram = size + kvReserve + cudaOverhead
		estRam = 0.0

	case isMoE:
		share := effectiveShare(meta, moeShareFor)
		nonExpertTotal := size * (1.0 - share)
		usableForExperts := usableVram - nonExpertTotal
		if usableForExperts <= 0 {
			// Dense path can't even fit; fall back to -ngl reduction.
			ngl, estVram, estRam = densePlacement(size, kvReserve, blocks, opt.TargetVramGB, cudaOverhead)
			ncpuMoe = 0
			reasons = append(reasons, fmt.Sprintf("MoE share=%.2f but non-expert weight %.2fGB exceeds usable VRAM -> dense fallback -ngl=%d", share, nonExpertTotal, ngl))
		} else {
			perMoeLayer := (size * share) / float64(blocks)
			moeLayersOnGpu := math.Floor(usableForExperts / perMoeLayer)
			if moeLayersOnGpu > float64(blocks) {
				moeLayersOnGpu = float64(blocks)
			}
			ncpuMoeCandidate := int(math.Max(0, float64(blocks)-moeLayersOnGpu))

			// Crossover: when more than ~half the blocks need CPU experts, the
			// per-token PCIe round-trip cost exceeds the dense-on-GPU saving, so
			// naive -ngl (one boundary transition) wins.
			crossoverFrac := 0.5
			if float64(ncpuMoeCandidate) > float64(blocks)*crossoverFrac {
				ngl, estVram, estRam = densePlacement(size, kvReserve, blocks, opt.TargetVramGB, cudaOverhead)
				ncpuMoe = 0
				reasons = append(reasons, fmt.Sprintf("MoE share=%.2f but ncpumoe candidate %d/%d exceeds %.0f%% crossover -> naive -ngl=%d (avoid PCIe thrash)", share, ncpuMoeCandidate, blocks, crossoverFrac*100, ngl))
			} else {
				ncpuMoe = ncpuMoeCandidate
				ngl = 99
				expertGpuFrac := float64(blocks-ncpuMoe) / float64(blocks)
				estVram = nonExpertTotal + (size * share * expertGpuFrac) + kvReserve + cudaOverhead
				estRam = (size * share) * (float64(ncpuMoe) / float64(blocks))
				reasons = append(reasons, fmt.Sprintf("MoE share=%.2f non_expert=%.2fGB per_moe_layer=%.3fGB -> --n-cpu-moe=%d", share, nonExpertTotal, perMoeLayer, ncpuMoe))
			}
		}

	default:
		ngl, estVram, estRam = densePlacement(size, kvReserve, blocks, opt.TargetVramGB, cudaOverhead)
		ncpuMoe = 0
		reasons = append(reasons, fmt.Sprintf("dense per_layer=%.3fGB(w)+%.3fGB(kv) -> -ngl=%d/%d", size/float64(blocks), kvReserve/float64(blocks), ngl, blocks))
	}

	ramExceeded := false
	if opt.MaxRamGB > 0 && estRam > opt.MaxRamGB {
		ramExceeded = true
		reasons = append(reasons, fmt.Sprintf("WARN estimated RAM %.2fGB exceeds MaxRamGB %.1fGB", estRam, opt.MaxRamGB))
	}

	return LoadPlan{
		Ngl:          ngl,
		NCpuMoe:      ncpuMoe,
		EstVramGB:    round(estVram, 2),
		EstRamGB:     round(estRam, 2),
		TargetVramGB: opt.TargetVramGB,
		MaxRamGB:     opt.MaxRamGB,
		BlockCount:   blocks,
		Architecture: arch,
		IsMoE:        isMoE,
		FileSizeGB:   size,
		RamExceeded:  ramExceeded,
		Reasons:      reasons,
	}, nil
}
