package server

// Load-plan estimate: reconstructs an autogen.EstimateInput from a rendered
// launch command so the UI can show the VRAM/KV breakdown for a model exactly
// as it is currently configured, without re-deriving it from the override.

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// handleAPIModelEstimate previews the load plan (VRAM/RAM, ngl, n_cpu_moe,
// chosen ctx) for a candidate tuning without persisting anything. Query params:
// ctx, kvK, kvV, spec, ropeScaling (strings), kvInRam (bool), vram (float target
// GB), cpuOffload (int layers pinned to CPU), ctxCheckpoints, checkpointMinStep,
// ub (ints).
// Powers the editor's live memory estimate.
// cmdArgv splits a rendered launch command into argv exactly the way the
// process layer will, so a quoted path containing spaces ("C:\Program
// Files\...") survives as one token — strings.Fields would shred it and the
// flag after it would be misread. Returns nil for an unparseable command; the
// callers below then simply find no flags and fall back to their defaults.
func cmdArgv(cmd string) []string {
	return config.ParseCmd(cmd).Argv
}

// estimateInputFromCmd reconstructs the placement-relevant inputs from a
// model's rendered llama-server command so an estimate reflects the actually
// loaded variant (ctx, checkpoints, spec, kv) instead of re-sizing the solo
// profile with defaults. Unknown flags are ignored.
func estimateInputFromCmd(cmd string) autogen.EstimateInput {
	in := autogen.EstimateInput{}
	toks := cmdArgv(cmd)
	for i := 0; i < len(toks); i++ {
		next := func() (string, bool) {
			if i+1 < len(toks) {
				return toks[i+1], true
			}
			return "", false
		}
		switch toks[i] {
		case "-c", "--ctx-size":
			if v, ok := next(); ok {
				in.Ctx, _ = strconv.Atoi(v)
			}
		case "--ctx-checkpoints":
			if v, ok := next(); ok {
				if n, err := strconv.Atoi(v); err == nil {
					in.CtxCheckpoints = &n
				}
			}
		case "-cms", "--checkpoint-min-step":
			// Spacing scales each checkpoint's KV term, so the preview must charge
			// the same step the argv runs with.
			if v, ok := next(); ok {
				in.CheckpointMinStep, _ = strconv.Atoi(v)
			}
		case "--spec-type":
			// Chained spec backends (draft-mtp + ngram-map-k4v) appear as repeated
			// --spec-type; accumulate into the "+"-joined list the sizer expects.
			if v, ok := next(); ok {
				if in.Spec == "" {
					in.Spec = v
				} else {
					in.Spec += "+" + v
				}
			}
		case "--rope-scaling":
			// Decides whether the sizer may pick a ctx past the trained length, so
			// the preview must read it back off the argv like -c itself.
			if v, ok := next(); ok {
				in.RopeScaling = v
			}
		case "-ub", "--ubatch-size":
			// The compute buffer scales with the physical batch, so the preview must
			// charge the same ub the argv runs with.
			if v, ok := next(); ok {
				in.Ub, _ = strconv.Atoi(v)
			}
		case "-ctk":
			if v, ok := next(); ok {
				in.KvK = v
			}
		case "-ctv":
			if v, ok := next(); ok {
				in.KvV = v
			}
		case "--no-kv-offload":
			in.KvInRam = true
		case "-md", "--model-draft", "--spec-draft-model":
			if v, ok := next(); ok {
				if fi, err := os.Stat(v); err == nil {
					in.DraftGB = float64(fi.Size()) / (1 << 30)
				}
			}
		}
	}
	return in
}

// forcedOffloadFromCmd maps a rendered command's GPU/CPU layer split to the
// EstimateInput.CpuOffload the sizer's applyForcedOffload expects, so an estimate
// can reproduce the exact placement a running process launched with (incl. the
// spawn-time LiveOffloadArgs guard's extra offload) rather than re-deriving it.
// MoE: --n-cpu-moe N is the offload count directly. Dense: -ngl G of BlockCount
// layers => BlockCount-G on CPU. ok=false when the argv carries no placement flag
// (or dims are unknown) so the caller leaves the sizer to choose.
func forcedOffloadFromCmd(cmd string, meta autogen.Metadata) (int, bool) {
	toks := cmdArgv(cmd)
	ngl, nglSet := 0, false
	ncpu, ncpuSet := 0, false
	for i := 0; i+1 < len(toks); i++ {
		switch toks[i] {
		case "-ngl", "--n-gpu-layers", "--gpu-layers":
			if v, err := strconv.Atoi(toks[i+1]); err == nil {
				ngl, nglSet = v, true
			}
		case "--n-cpu-moe", "--cpu-moe":
			if v, err := strconv.Atoi(toks[i+1]); err == nil {
				ncpu, ncpuSet = v, true
			}
		}
	}
	if meta.IsMoE {
		if ncpuSet {
			return ncpu, true
		}
		return 0, false
	}
	blocks := int(meta.BlockCount)
	if nglSet && blocks > 0 {
		n := blocks - ngl
		if n < 0 {
			n = 0
		}
		return n, true
	}
	return 0, false
}

// mmprojPathFromCmd returns the "--mmproj" projector path in a rendered command,
// or "" when the command loads no projector (non-vision model).
func mmprojPathFromCmd(cmd string) string {
	toks := cmdArgv(cmd)
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == "--mmproj" {
			return toks[i+1]
		}
	}
	return ""
}

func (s *Server) handleAPIModelEstimate(w http.ResponseWriter, r *http.Request) {
	realID, gguf, cmd, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "loading settings failed: "+err.Error())
		return
	}
	meta, err := autogen.ReadGgufMetadataCached(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "reading gguf metadata failed: "+err.Error())
		return
	}

	q := r.URL.Query()
	// actual=true: seed from the loaded command so the preview reflects the variant
	// that's really running. Prefer the RUNNING cmd (post spawn-time LiveOffloadArgs
	// guard) over the config cmd: the guard can offload MORE layers than the baked
	// plan against live free VRAM, so the config cmd's -ngl is pre-guard and would
	// disagree with the staging area. Pin the estimate to the running placement so
	// the settings menu matches what's actually loaded. Otherwise (config editor,
	// unloaded, or an edited field) start blank so the sizer re-derives placement.
	var in autogen.EstimateInput
	if q.Get("actual") == "true" {
		seedCmd := cmd
		if rc, running := s.local.LaunchedCmd(realID); running && rc != "" {
			seedCmd = rc
		}
		in = estimateInputFromCmd(seedCmd)
		// Pin the actual GPU/CPU layer split from the running argv so EstimatePlan
		// reports the loaded placement instead of re-sizing it against the budget.
		if n, ok := forcedOffloadFromCmd(seedCmd, meta); ok {
			in.CpuOffload = n
		}
	}
	// Explicit query params override the seed (the config editor's form fields).
	if v := q.Get("kvK"); v != "" {
		in.KvK = v
	}
	if v := q.Get("kvV"); v != "" {
		in.KvV = v
	}
	if v := q.Get("spec"); v != "" {
		in.Spec = v
	}
	if v := q.Get("kvInRam"); v != "" {
		in.KvInRam = v == "true"
	}
	if v := q.Get("ctxCheckpoints"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			in.CtxCheckpoints = &n
		}
	}
	if v := q.Get("checkpointMinStep"); v != "" {
		in.CheckpointMinStep, _ = strconv.Atoi(v)
	}
	if v := q.Get("ub"); v != "" {
		in.Ub, _ = strconv.Atoi(v)
	}
	if v := q.Get("ropeScaling"); v != "" {
		// Decides whether the sizer may pick a ctx past the trained length; without
		// it an editor preview of a rope-extended window silently sizes the clamped
		// one and reports a KV reserve the launch won't have.
		in.RopeScaling = v
	}
	if v := q.Get("ctx"); v != "" {
		in.Ctx, _ = strconv.Atoi(v)
	}
	if v := q.Get("vram"); v != "" {
		in.TargetVramGB, _ = strconv.ParseFloat(v, 64)
	} else {
		// No explicit budget: mirror EnsureConfig's autoVram so the preview sizes
		// against the same live free-VRAM budget the config was baked with.
		// Otherwise the estimate uses the larger static targetVramGB and reports a
		// bigger ctx (e.g. 128k) than the config's actual -c (e.g. 98k).
		autogen.ResolveAutoVram(&gf.Settings, nil)
	}
	if v := q.Get("cpuOffload"); v != "" {
		in.CpuOffload, _ = strconv.Atoi(v)
	}
	// A paired draft sidecar (MTP/DFlash gguf in the model's dir) costs real VRAM
	// once the active spec is a draft backend. The config-editor path starts blank
	// (no -md in the cmd to stat), so seed DraftGB from the sidecar's on-disk size
	// — otherwise draftOverheadGB charges only its flat 0.1 GB pad and the estimate
	// bar under-reports the drafter's weights (0.4-1.3 GB here). Harmless for
	// non-draft specs: draftOverheadGB ignores DraftGB unless spec is draft-*.
	// DraftKind travels with it: an empty spec means AUTO, and EstimatePlan needs
	// the sidecar's kind to resolve the same spec the emitter would (a paired mtp
	// gguf makes an otherwise-plain model draft-capable). Without it a model left
	// on auto spec previews without the drafter's VRAM.
	if in.DraftGB == 0 {
		if _, kind, sizeGB := autogen.DraftSidecarForDir(filepath.Dir(gguf)); sizeGB > 0 {
			in.DraftGB = sizeGB
			in.DraftKind = kind
		}
	}
	// A "-vision" twin loads an mmproj projector whose weights + CLIP compute
	// buffer cost VRAM the bare-LLM sizer is blind to (the -m gguf carries no
	// vision info). Charge the same footprint generate-time bakes into the twin's
	// Overhead (mmprojVramGB) so the editor bar and the status-rail breakdown size
	// the vision load correctly — otherwise the sizer picks an unaffordably large
	// ctx and the projector's VRAM is misattributed to the CUDA slice.
	if in.MmprojGB == 0 {
		if mp := mmprojPathFromCmd(cmd); mp != "" {
			if fi, err := os.Stat(mp); err == nil {
				// Same footprint generate-time bakes: projector weights + the
				// per-projector CLIP compute buffer (modeled from mmproj hparams,
				// flat VisionOverheadGB fallback).
				in.MmprojGB = autogen.MmprojVramGB(mp, float64(fi.Size())/(1<<30), gf.Settings)
			}
		}
	}

	res, err := autogen.EstimatePlan(gf.Settings, meta, in)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "estimate failed: "+err.Error())
		return
	}
	writeJSON(w, res)
}

// --- Global settings editor (dashboard GPU-memory card) ---
