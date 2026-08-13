package autogen

import "math"

// rope.go — RoPE context extension.
//
// A model's trained context (`general.context_length`) is a hard ceiling for the
// sizer: everything else in here caps the chosen ctx at `modelMax`, because
// asking a model for positions it was never trained on returns coherent-looking
// garbage. Setting a rope scaling type is the user overriding that judgement, so
// two things follow from one knob:
//
//  1. the ceiling lifts to the requested ctx (bounded by maxRopeFactor), and
//  2. `--rope-scale` is derived from how far past native the ctx went, because
//     llama.cpp otherwise takes the factor from gguf metadata — 1.0 on a model
//     that was never fine-tuned for extension — and the extra context silently
//     does nothing at all.
//
// The user can still pin `ropeScale` explicitly; the derivation only fills a
// zero.

// maxRopeFactor bounds the extension. Past ~4x even YaRN degrades badly, and the
// cap mostly exists so a mis-typed ctx can't ask for a KV reserve the sizer then
// tries to fit.
const maxRopeFactor = 8

// ropeExtends reports whether a scaling type actually stretches the context.
// "none" is a valid override meaning "disable the model's baked-in scaling", and
// "" is auto — neither grants extra ctx.
func ropeExtends(kind string) bool { return kind == "linear" || kind == "yarn" }

// nativeCtx is the model's trained context length, with the same 32k fallback
// the sizer uses when the gguf header doesn't declare one.
func nativeCtx(meta Metadata) int {
	if meta.ContextLength > 0 {
		return int(meta.ContextLength)
	}
	return 32768
}

// ropeCeiling is the largest ctx the sizer may pick for this model: the trained
// length, unless rope scaling is on and a larger ctx was explicitly requested.
func ropeCeiling(meta Metadata, ropeScaling string, reqCtx int) int {
	n := nativeCtx(meta)
	if !ropeExtends(ropeScaling) || reqCtx <= n {
		return n
	}
	return min(reqCtx, n*maxRopeFactor)
}

// ropeFactor is the --rope-scale needed to reach ctx on a model trained for
// nativeCtx. 0 when no scaling is needed. Rounded UP to a half step: a factor
// below the true ratio leaves the tail of the window on untrained positions,
// which is exactly the failure the flag exists to avoid.
func ropeFactor(meta Metadata, ctx int) float64 {
	n := nativeCtx(meta)
	if ctx <= n {
		return 0
	}
	return math.Min(math.Ceil(float64(ctx)/float64(n)*2)/2, maxRopeFactor)
}
