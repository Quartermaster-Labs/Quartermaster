package autogen

// liveoffload.go re-derives a model's GPU/CPU layer placement from the VRAM
// free RIGHT NOW, at spawn time, instead of trusting the figure baked into the
// generated config. The config's -ngl/--n-cpu-moe are computed once (at generate
// time or startup autoVram); if another app grabs VRAM afterwards, loading a
// model with those stale flags can OOM. This adjusts the emitted argv against a
// live free-VRAM reading just before exec.

import (
	"fmt"
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
