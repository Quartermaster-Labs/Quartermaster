package autogen

// pins.go turns a model's custom launch arguments into sizing constraints.
//
// Composition (customargs.go) already makes a pinned flag win at spawn: the
// generated occurrence is dropped and the user's text is appended last, and
// llama-server keeps the last flag. But the plan around it was still sized as
// if the pin did not exist: `-c 5000` overrode the emitted -c while the baked
// estVramGB, the checkpoint reserve and the router's admission figure still
// described the window the sizer had picked, and the editor's memory panel
// showed the other launch. So the sizer reads the pins back and plans around
// them; the emitted flags then say the same thing twice instead of contradicting
// each other.
//
// The text itself is never rewritten here. A pinned knob the sizer does not
// model (samplers, -cram, --no-op-offload, ...) is ignored: it still wins at
// spawn, it just does not move the plan.

import (
	"strconv"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// Pins holds the custom launch-argument values the sizer can honor. Every field
// is optional: nil / "" means the text did not pin that knob, so the generator
// keeps owning it and its auto-pick runs unchanged.
type Pins struct {
	// Ctx is llama-server's -c, the TOTAL KV pool (--kv-unified makes every slot
	// share one). profile.Ctx is the per-slot window, so applying a pin divides
	// this by the slot count — see ApplyToProfile.
	Ctx               *int
	Ngl               *int   // -ngl / --n-gpu-layers (dense layers kept on GPU)
	NCpuMoe           *int   // --n-cpu-moe / -ncmoe (MoE expert layers on CPU)
	KvK, KvV          string // -ctk / -ctv cache types
	KvInRam           bool   // --no-kv-offload / -nkvo
	Parallel          *int   // --parallel / -np server slots
	Ub                *int   // -ub / --ubatch-size physical batch
	Spec              string // --spec-type chain, "+"-joined as the emitter writes it
	RopeScaling       string // --rope-scaling
	CtxCheckpoints    *int   // --ctx-checkpoints (0 disables the checkpoint cache)
	CheckpointMinStep *int   // -cms / --checkpoint-min-step
}

// Empty reports whether the text pinned nothing the sizer understands.
func (p Pins) Empty() bool {
	return p.Ctx == nil && p.Ngl == nil && p.NCpuMoe == nil && p.KvK == "" && p.KvV == "" &&
		!p.KvInRam && p.Parallel == nil && p.Ub == nil && p.Spec == "" && p.RopeScaling == "" &&
		p.CtxCheckpoints == nil && p.CheckpointMinStep == nil
}

// PinsFromArgs reads the sizer-relevant flags out of custom launch text. It
// never fails: unparseable text pins nothing (validation reports that), a
// non-numeric value is skipped rather than fatal, and the last occurrence of a
// repeated flag wins, matching what llama-server itself would use.
func PinsFromArgs(text string) Pins {
	var p Pins
	if strings.TrimSpace(text) == "" {
		return p
	}
	toks, err := config.SanitizeCommand(text)
	if err != nil {
		return p
	}
	var spec []string
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if !strings.HasPrefix(tok, "-") {
			continue
		}
		name, inline, hasInline := SplitFlagToken(tok)
		d, ok := LookupFlag(name)
		if !ok {
			continue
		}
		val := ""
		switch {
		case hasInline:
			val = inline
		case d.Value && i+1 < len(toks):
			i++
			val = toks[i]
		}
		switch d.Knob {
		case "ctx":
			p.Ctx = pinPositiveInt(val)
		case "ngl":
			p.Ngl = pinInt(val)
		case "nCpuMoe":
			p.NCpuMoe = pinInt(val)
		case "kvK":
			p.KvK = pinToken(val)
		case "kvV":
			p.KvV = pinToken(val)
		case "kvInRam":
			p.KvInRam = true
		case "parallel":
			p.Parallel = pinPositiveInt(val)
		case "ub":
			p.Ub = pinPositiveInt(val)
		case "spec":
			if v := pinToken(val); v != "" {
				spec = append(spec, v)
			}
		case "ropeScaling":
			p.RopeScaling = pinToken(val)
		case "ctxCheckpoints":
			p.CtxCheckpoints = pinInt(val)
		case "checkpointMinStep":
			p.CheckpointMinStep = pinPositiveInt(val)
		}
	}
	if len(spec) > 0 {
		p.Spec = strings.Join(spec, "+")
	}
	return p
}

// pinToken returns v unless it is empty or looks like the next flag (a missing
// value at the end of the text), so a wrong `--ctx-size --ub 512` cannot pin a
// flag name as a value.
func pinToken(v string) string {
	if v == "" || strings.HasPrefix(v, "-") {
		return ""
	}
	return v
}

func pinInt(v string) *int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &n
}

func pinPositiveInt(v string) *int {
	n := pinInt(v)
	if n == nil || *n <= 0 {
		return nil
	}
	return n
}

// ApplyToOverride folds the pins the whole model shares into a copy of ov, so
// the code that runs before profiles are built (the KV cost model, the ctx
// ceiling, effectiveUb, the slot count, the checkpoint knobs) sees them. Ctx
// and the layer placement are per-profile and applied by ApplyToProfile; the
// custom text fields are passed through untouched, because the text is still
// appended verbatim and still wins at spawn.
func (p Pins) ApplyToOverride(ov Override) Override {
	if p.Empty() {
		return ov
	}
	if p.KvK != "" {
		ov.KvK = p.KvK
	}
	if p.KvV != "" {
		ov.KvV = p.KvV
	}
	if p.KvInRam {
		ov.KvInRam = true
	}
	if p.Parallel != nil {
		ov.Parallel = EffectiveParallel(&Override{Parallel: *p.Parallel})
	}
	if p.Ub != nil {
		ov.Ub = *p.Ub
	}
	if p.Spec != "" {
		ov.Spec = p.Spec
	}
	if p.RopeScaling != "" {
		ov.RopeScaling = p.RopeScaling
	}
	if p.CtxCheckpoints != nil {
		ov.CtxCheckpoints = p.CtxCheckpoints
	}
	if p.CheckpointMinStep != nil {
		ov.CheckpointMinStep = *p.CheckpointMinStep
	}
	return ov
}

// ApplyToProfile folds the pins one profile's own text carries into its sizing
// inputs. slots is the profile's effective parallel count: -c addresses the
// shared pool, while profile.Ctx is the per-slot window, so the pin divides.
func (p Pins) ApplyToProfile(prof *profile, meta Metadata, slots int) {
	if p.Empty() {
		return
	}
	if p.Ctx != nil {
		per := *p.Ctx / max(slots, 1)
		if per < 1 {
			per = 1
		}
		prof.Ctx = per
		prof.IsLong = per >= longCtxThreshold
		// RoundedCtx floors a window to a 4096 multiple because the sizer picks
		// a window; a pin is a decision, so `-c 5000` must preview and plan as
		// 5000, not 4096.
		prof.CtxExact = true
	}
	if p.KvK != "" {
		prof.KvK = p.KvK
	}
	if p.KvV != "" {
		prof.KvV = p.KvV
	}
	if p.Ub != nil {
		prof.Ub = *p.Ub
	}
	if p.Spec != "" {
		prof.Spec = p.Spec
	}
	if p.CtxCheckpoints != nil {
		prof.CtxCheckpoints = p.CtxCheckpoints
	}
	if p.CheckpointMinStep != nil {
		prof.CheckpointMinStep = *p.CheckpointMinStep
	}
	if n, ok := p.cpuOffload(meta); ok {
		prof.CpuOffload = n
		// 0 is a real pin here (`-ngl <blocks>` = every layer on GPU, or
		// `--n-cpu-moe 0` = no expert offload): profile.CpuOffload alone cannot
		// say that, because its zero value means "let the sizer decide".
		prof.CpuOffloadSet = true
	}
	// A named variant with its own text layers these over the model-wide shape
	// at emit time, so the sizing reads have to see them there too.
	if v := prof.Variant; v != nil {
		if p.Parallel != nil {
			v.Parallel = EffectiveParallel(&Override{Parallel: *p.Parallel})
		}
		if p.KvInRam {
			v.KvInRam = true
		}
		if p.RopeScaling != "" {
			v.RopeScaling = p.RopeScaling
		}
	}
}

// pinsForProfile folds the custom launch arguments that apply to one profile
// into its sizing inputs. A named variant with its own text sizes with that
// text's pins; every other profile inherits the model-wide text (already folded
// into ov by ApplyToOverride, and re-applied here so ctx/placement land on the
// profile itself).
func pinsForProfile(prof *profile, ov Override, meta Metadata) {
	text := ov.CustomArgsText()
	if v := prof.Variant; v != nil {
		text = inheritStr(v.CustomArgs, text)
	}
	pins := PinsFromArgs(text)
	if pins.Empty() {
		return
	}
	slots := profileParallel(*prof, ov)
	if pins.Parallel != nil {
		slots = EffectiveParallel(&Override{Parallel: *pins.Parallel})
	}
	pins.ApplyToProfile(prof, meta, slots)
}

// ApplyToEstimate folds pins over a preview request's parameters. It is the
// /estimate counterpart of ApplyToProfile: the web editor's candidate tuning
// must describe the launch the text pins, and the text is the last word just as
// it is at spawn.
func (p Pins) ApplyToEstimate(in *EstimateInput, meta Metadata) {
	if p.Empty() || in == nil {
		return
	}
	if p.Parallel != nil {
		in.Parallel = EffectiveParallel(&Override{Parallel: *p.Parallel})
	}
	if p.Ctx != nil {
		per := *p.Ctx / max(in.Parallel, 1)
		if per < 1 {
			per = 1
		}
		in.Ctx = per
		in.CtxExact = true
	}
	if p.KvK != "" {
		in.KvK = p.KvK
	}
	if p.KvV != "" {
		in.KvV = p.KvV
	}
	if p.KvInRam {
		in.KvInRam = true
	}
	if p.Ub != nil {
		in.Ub = *p.Ub
	}
	if p.Spec != "" {
		in.Spec = p.Spec
	}
	if p.RopeScaling != "" {
		in.RopeScaling = p.RopeScaling
	}
	if p.CtxCheckpoints != nil {
		in.CtxCheckpoints = p.CtxCheckpoints
	}
	if p.CheckpointMinStep != nil {
		in.CheckpointMinStep = *p.CheckpointMinStep
	}
	if n, ok := p.cpuOffload(meta); ok {
		in.CpuOffload = n
		in.CpuOffloadSet = true
	}
}

// cpuOffload maps -ngl / --n-cpu-moe onto the sizer's "layers on CPU" forcing
// input, using the same convention as applyForcedOffload: on a dense model the
// count is layers OFF the GPU, on a MoE model it is EXPERT layers pinned to the
// CPU. A knob that does not apply to the model's shape (--n-cpu-moe on a dense
// gguf) is ignored rather than guessed at, and so is -ngl on a MoE model, where
// placement is expressed as an expert split the number cannot be mapped onto.
func (p Pins) cpuOffload(meta Metadata) (int, bool) {
	if meta.IsMoE {
		if p.NCpuMoe == nil {
			return 0, false
		}
		n := max(*p.NCpuMoe, 0)
		if meta.BlockCount > 0 {
			n = min(n, int(meta.BlockCount))
		}
		return n, true
	}
	if p.Ngl == nil || meta.BlockCount <= 0 {
		return 0, false
	}
	n := int(meta.BlockCount) - max(*p.Ngl, 0)
	if n < 0 {
		n = 0
	}
	return n, true
}
