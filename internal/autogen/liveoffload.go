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
	if !strings.HasSuffix(strings.ToLower(model), ".gguf") {
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

	in := EstimateInput{
		Ctx:          atoiFlag(args, "-c", "--ctx-size"),
		KvK:          flagStr(args, "-ctk", "--cache-type-k"),
		KvV:          flagStr(args, "-ctv", "--cache-type-v"),
		KvInRam:      hasFlag(args, "--no-kv-offload"),
		Spec:         specTypes(args),
		TargetVramGB: freeGB, // RAW live free; EstimatePlan subtracts overhead via s
	}
	if c, ok := atoiFlagOK(args, "--ctx-checkpoints"); ok {
		in.CtxCheckpoints = &c
	}
	// A "-vision" twin loads a CLIP projector via --mmproj. The model gguf (-m)
	// carries no vision info, so EstimatePlan is projector-blind; charge the
	// projector's weights + CLIP compute reserve here so the live guard sizes the
	// twin like the baked plan did, instead of under-offloading and leaving too
	// little free VRAM once the projector + image buffers load.
	if mm, i := argVal(args, "--mmproj"); i >= 0 {
		if fi, statErr := os.Stat(mm); statErr == nil {
			in.MmprojGB = mmprojVramGB(float64(fi.Size())/gib, s)
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
		return nil, fmt.Errorf("insufficient VRAM to load %s: needs ~%.1fGB at max CPU offload but only %.1fGB free — close other GPU apps and retry",
			filepath.Base(model), res.EstVramGB, freeGB)
	}

	// Only intervene when the live plan offloads MORE than the baked one. Ample
	// VRAM (res keeps everything on GPU) or a hand-pinned config is left as-is.
	if res.NCpuMoe <= bakedNcpu && res.Ngl >= bakedNgl {
		return args, nil
	}

	out := rewriteOffload(args, nglIdx, ncIdx, res.Ngl, res.NCpuMoe)
	if logf != nil {
		logf(fmt.Sprintf("dynoffload: free=%.1fGB -> -ngl %d->%d --n-cpu-moe %d->%d (est %.1fGB)",
			freeGB, bakedNgl, res.Ngl, bakedNcpu, res.NCpuMoe, res.EstVramGB))
	}
	return out, nil
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
