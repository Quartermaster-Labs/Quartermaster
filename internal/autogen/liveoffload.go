package autogen

// liveoffload.go re-derives a model's GPU/CPU layer placement from the VRAM
// free RIGHT NOW, at spawn time, instead of trusting the figure baked into the
// generated config. The config's -ngl/--n-cpu-moe are computed once (at generate
// time or startup autoVram); if another app grabs VRAM afterwards, loading a
// model with those stale flags can OOM. This adjusts the emitted argv against a
// live free-VRAM reading just before exec.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LiveOffloadArgs adjusts an emitted llama-server arg vector for the VRAM free
// right now (freeGB). When live free VRAM is tighter than the config assumed it
// pushes more layers/experts to CPU (raising --n-cpu-moe / lowering -ngl) so the
// load won't OOM. It only ever offloads MORE than the baked flags, never less,
// so a config that fits live VRAM (or a hand-pinned cpuOffload) is left untouched.
//
// If the model can't fit even at the planner's max offload it returns an error,
// so the caller refuses the spawn with a clear message instead of OOM-crashing.
//
// It fails open: a non-llama cmd, an unreadable gguf, no GPU telemetry
// (freeOK=false), or an estimate error all return args unchanged.
func LiveOffloadArgs(s Settings, args []string, freeGB float64, freeOK bool, logf func(string)) ([]string, error) {
	if !freeOK {
		return args, nil // no reading -> trust the baked plan
	}

	// sd-server (image gen) has no -m gguf and no -ngl; its only VRAM knob is
	// --max-vram, a runtime streaming budget. Pin it to live free VRAM minus a
	// headroom pad so a generation's peak (VAE decode, Kontext ref tokens) can't
	// overcommit shared VRAM and hang the desktop. Only ever TIGHTEN below the
	// baked value (never raise): ample free leaves the safe baked budget alone,
	// and a hand-pinned vramTargetGB stays respected.
	if maxVram, idx := argVal(args, "--max-vram"); idx >= 0 {
		return liveMaxVram(args, idx, maxVram, freeGB, logf), nil
	}

	model, _ := argVal(args, "-m", "--model")
	lower := strings.ToLower(model)

	// SAM (sam3_server) segmentation: model is *.ggml. Always force CPU. The
	// Vulkan backend on RX 7900 XTX numerically corrupts SAM inference — both the
	// PCS (text) detector scores AND the PVS (box/point) masks come back garbage
	// (scores collapse to ~0.0003, no object found), while CPU gives correct
	// results. SAM is tiny and coexists with a loaded LLM/image model (never
	// evicted), so the CPU cost (~14s/segment) is acceptable and the only backend
	// that actually works here. An explicit --no-gpu (user extraArgs) is a no-op.
	// ponytail: force CPU for all .ggml; revisit if a non-broken GPU backend
	// (CUDA, or a fixed Vulkan/ROCm build) is ever wired for SAM.
	if strings.HasSuffix(lower, ".ggml") {
		if hasFlag(args, "--no-gpu") {
			return args, nil
		}
		out := append(append([]string(nil), args...), "--no-gpu")
		if logf != nil {
			logf("dynoffload: sam -> --no-gpu (Vulkan SAM inference is numerically broken; CPU only)")
		}
		return out, nil
	}

	if !strings.HasSuffix(lower, ".gguf") {
		return args, nil // not a llama.cpp model load (sd-server, custom cmd, …)
	}
	nglStr, nglIdx := argVal(args, "-ngl", "--n-gpu-layers", "--gpu-layers")
	if nglIdx < 0 {
		return args, nil // no offload knob to tune
	}
	bakedNgl, _ := strconv.Atoi(nglStr)
	ncStr, ncIdx := argVal(args, "--n-cpu-moe")
	bakedNcpu := 0
	if ncIdx >= 0 {
		bakedNcpu, _ = strconv.Atoi(ncStr)
	}

	meta, err := ReadGgufMetadataCached(model)
	if err != nil {
		if logf != nil {
			logf(fmt.Sprintf("dynoffload: gguf read failed (%v); using baked flags", err))
		}
		return args, nil
	}

	// Budget = the TIGHTER of live free and the configured target. Live free is a
	// snapshot, and the other GPU clients on a desktop box are not static: dwm
	// alone swings by a GB or more with monitor/HDR/compositing load, and Discord,
	// Steam, a VR runtime or a browser can each take a few hundred MB AFTER the
	// model has already claimed everything the snapshot showed. Planning against
	// 100% of that snapshot is what makes a load that fit yesterday spill into
	// shared memory today: nothing is left for the growth, so the driver silently
	// demotes part of the model and throughput collapses.
	//
	// Capping at TargetVramGB makes the setting mean what it says on the load path
	// too (it used to bind only at generate time), so the split is a deliberate
	// decision instead of a race with whatever the desktop happened to hold at
	// spawn. Free still wins when it is the smaller of the two — a target above
	// what the card actually has left must not talk us into an OOM.
	budgetGB := liveBudgetGB(s, freeGB)
	if logf != nil && budgetGB < freeGB {
		logf(fmt.Sprintf("dynoffload: budget %.1fGB (targetVramGB, free=%.1fGB)", budgetGB, freeGB))
	}

	in := EstimateInput{
		Ctx:          atoiFlag(args, "-c", "--ctx-size"),
		KvK:          flagStr(args, "-ctk", "--cache-type-k"),
		KvV:          flagStr(args, "-ctv", "--cache-type-v"),
		KvInRam:      hasFlag(args, "--no-kv-offload"),
		Spec:         specTypes(args),
		RopeScaling:  flagStr(args, "--rope-scaling"),
		TargetVramGB: budgetGB, // EstimatePlan subtracts overhead via s
		// The compute buffer is sized by the physical batch, so the live guard has
		// to charge the -ub the argv actually launches with rather than re-deriving
		// the auto pick and mis-sizing a model with a pinned ub.
		Ub: atoiFlag(args, "-ub", "--ubatch-size"),
	}
	if c, ok := atoiFlagOK(args, "--ctx-checkpoints"); ok {
		in.CtxCheckpoints = &c
	} else {
		// No flag in the argv means llama-server's own 32, not our arch default.
		c := LlamaDefaultCtxCheckpoints
		in.CtxCheckpoints = &c
	}
	// Spacing scales each checkpoint's global-KV term, so the live re-estimate has
	// to charge the same step the baked argv runs with — and an argv with no -cms
	// runs at llama's 8192, not at the arch default we would have emitted.
	if step := atoiFlag(args, "-cms", "--checkpoint-min-step"); step > 0 {
		in.CheckpointMinStep = step
	} else {
		in.CheckpointMinStep = LlamaDefaultCheckpointMinStep
	}
	// A "-vision" twin loads a CLIP projector via --mmproj. The model gguf (-m)
	// carries no vision info, so EstimatePlan is projector-blind; charge the
	// projector's weights + CLIP compute reserve here so the live guard sizes the
	// twin like the baked plan did, instead of under-offloading and leaving too
	// little free VRAM once the projector + image buffers load.
	if mm, i := argVal(args, "--mmproj"); i >= 0 {
		if fi, statErr := os.Stat(mm); statErr == nil {
			in.MmprojGB = MmprojVramGB(mm, float64(fi.Size())/gib, s)
		} else if logf != nil {
			logf(fmt.Sprintf("dynoffload: --mmproj stat failed (%v); projector VRAM uncharged", statErr))
		}
	}
	// A separate draft gguf (-md: MTP sidecar or any DFlash drafter) has real
	// weights on disk; charge its actual size instead of the flat 0.34 GB
	// baked-in-MTP default so a big drafter doesn't get under-charged here.
	if md, i := argVal(args, "-md"); i >= 0 {
		if fi, statErr := os.Stat(md); statErr == nil {
			in.DraftGB = float64(fi.Size()) / gib
		} else if logf != nil {
			logf(fmt.Sprintf("dynoffload: -md stat failed (%v); draft VRAM under-charged", statErr))
		}
	}

	res, err := EstimatePlan(s, meta, in)
	if err != nil {
		if logf != nil {
			logf(fmt.Sprintf("dynoffload: estimate failed (%v); using baked flags", err))
		}
		return args, nil
	}

	// EstVramGB already folds in the overhead/compute reserve, so it IS the live
	// footprint. The planner already maxed out offload to fit; if it still can't,
	// even a fully CPU-offloaded load would OOM -> refuse.
	if res.EstVramGB > freeGB {
		return nil, fmt.Errorf("insufficient VRAM to load %s: needs ~%.1fGB at max CPU offload but only %.1fGB free - close other GPU apps and retry",
			filepath.Base(model), res.EstVramGB, freeGB)
	}

	// Admission floor. Without one, multi-resident loading always "succeeds":
	// the sizer just pushes the new model's layers/experts to CPU until it fits
	// whatever VRAM the already-resident models left over, and the box ends up
	// with everything loaded and everything crawling. Refuse instead — but only
	// when the BAKED plan was itself above the floor, so a model deliberately
	// configured to run mostly on CPU (a 35B MoE on a small card) still loads.
	if s.MinGpuFraction > 0 {
		live := gpuResidentFraction(meta, res.Ngl, res.NCpuMoe)
		baked := gpuResidentFraction(meta, bakedNgl, bakedNcpu)
		if live < s.MinGpuFraction && baked >= s.MinGpuFraction {
			return nil, fmt.Errorf("refusing to load %s: only %.0f%% of it would fit on the GPU right now (%.1fGB free, floor %.0f%%) - it would run at CPU speed; free VRAM or unload another model first",
				filepath.Base(model), live*100, freeGB, s.MinGpuFraction*100)
		}
	}

	// Only intervene when the live plan offloads MORE than the baked one. Ample
	// VRAM (res keeps everything on GPU) or a hand-pinned config is left as-is.
	if res.NCpuMoe <= bakedNcpu && res.Ngl >= bakedNgl {
		// Log the no-op too. Without this the guard is only visible when it fires,
		// so "the model loaded a layer short" can't be attributed: there's no way to
		// tell a low live reading from an over-conservative estimate without knowing
		// both numbers on a load that went fine.
		if logf != nil {
			logf(fmt.Sprintf("dynoffload: free=%.1fGB est=%.1fGB -> baked plan kept (-ngl %d --n-cpu-moe %d)",
				freeGB, res.EstVramGB, bakedNgl, bakedNcpu))
		}
		return args, nil
	}

	out := rewriteOffload(args, nglIdx, ncIdx, res.Ngl, res.NCpuMoe)
	if logf != nil {
		logf(fmt.Sprintf("dynoffload: free=%.1fGB -> -ngl %d->%d --n-cpu-moe %d->%d (est %.1fGB)",
			freeGB, bakedNgl, res.Ngl, bakedNcpu, res.NCpuMoe, res.EstVramGB))
	}
	return out, nil
}

// liveBudgetGB is the VRAM the spawn-time sizer is allowed to plan into: the
// tighter of the live free reading and the configured TargetVramGB. See the
// call site for why the raw free snapshot is not a safe budget on a desktop.
// TargetVramGB <= 0 (unset) leaves the free reading as the only bound.
func liveBudgetGB(s Settings, freeGB float64) float64 {
	if s.TargetVramGB > 0 && s.TargetVramGB < freeGB {
		return s.TargetVramGB
	}
	return freeGB
}

// gpuResidentFraction estimates how much of a model's weight ends up on the GPU
// under a given placement, as 0..1.
//
// Dense: the offloaded layer share (-ngl of the block count). MoE: -ngl is 99
// and the real lever is --n-cpu-moe, which moves N blocks' EXPERT tensors to
// CPU — so the CPU share is the expert-weight fraction scaled by N/blocks, and
// the dense trunk stays on the GPU. Unknown block count => 1 (no opinion, which
// keeps the floor from firing on a model it can't measure).
func gpuResidentFraction(meta Metadata, ngl, ncpuMoe int) float64 {
	blocks := int(meta.BlockCount)
	if blocks <= 0 {
		return 1
	}
	if meta.IsMoE && ncpuMoe > 0 {
		share := effectiveShare(meta, genMoeShareFor)
		onCPU := share * float64(min(ncpuMoe, blocks)) / float64(blocks)
		return 1 - onCPU
	}
	if ngl >= blocks {
		return 1
	}
	if ngl <= 0 {
		return 0
	}
	return float64(ngl) / float64(blocks)
}

// imageVramHeadroomGB is the VRAM kept free above sd-server's --max-vram budget,
// so the desktop compositor + un-modeled spikes have room and can't hard-hang
// Windows. imageVramFloorGB is the smallest budget we'll set: below it sd.cpp
// just pages nearly everything from RAM (slow but safe), never a load failure.
const (
	imageVramHeadroomGB = 1.0
	imageVramFloorGB    = 0.5
)

// liveMaxVram rewrites the --max-vram value (at index idx) to freeGB minus the
// headroom pad, but only when that is TIGHTER than the baked budget. Ample free
// VRAM, an unparseable value, or a baked budget already under the live ceiling
// all return args unchanged.
func liveMaxVram(args []string, idx int, baked string, freeGB float64, logf func(string)) []string {
	bakedGB, err := strconv.ParseFloat(baked, 64)
	if err != nil {
		return args // leave a hand-written non-numeric budget alone
	}
	budget := freeGB - imageVramHeadroomGB
	if budget >= bakedGB {
		return args // enough headroom at the baked budget; don't loosen it
	}
	if budget < imageVramFloorGB {
		budget = imageVramFloorGB
	}
	out := append([]string(nil), args...)
	out[idx] = strconv.FormatFloat(budget, 'f', 1, 64)
	if logf != nil {
		logf(fmt.Sprintf("dynoffload: sd --max-vram %s->%s (free=%.1fGB, %.1fGB headroom)",
			baked, out[idx], freeGB, imageVramHeadroomGB))
	}
	return out
}

// rewriteOffload returns a copy of args with -ngl set to newNgl and --n-cpu-moe
// set to newNcpu (appending the flag when it wasn't present and the value is
// nonzero). nglIdx is the index of the -ngl value; ncIdx the --n-cpu-moe value
// index, or -1 when absent.
func rewriteOffload(args []string, nglIdx, ncIdx, newNgl, newNcpu int) []string {
	out := append([]string(nil), args...)
	out[nglIdx] = strconv.Itoa(newNgl)
	switch {
	case ncIdx >= 0:
		out[ncIdx] = strconv.Itoa(newNcpu)
	case newNcpu > 0:
		out = append(out, "--n-cpu-moe", strconv.Itoa(newNcpu))
	}
	return out
}

// argVal returns the value following the first matching flag and the index of
// that value (-1 if absent). Args are already shlex-split, so flag and value are
// adjacent tokens.
func argVal(args []string, names ...string) (string, int) {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1], i + 1
			}
		}
	}
	return "", -1
}

func flagStr(args []string, names ...string) string {
	v, _ := argVal(args, names...)
	return v
}

func atoiFlag(args []string, names ...string) int {
	n, _ := atoiFlagOK(args, names...)
	return n
}

func atoiFlagOK(args []string, names ...string) (int, bool) {
	v, idx := argVal(args, names...)
	if idx < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// specTypes joins every --spec-type value with "+", matching the form the
// generator emits and that specHas() scans.
func specTypes(args []string) string {
	var specs []string
	for i, a := range args {
		if a == "--spec-type" && i+1 < len(args) {
			specs = append(specs, args[i+1])
		}
	}
	return strings.Join(specs, "+")
}
