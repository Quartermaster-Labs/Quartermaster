// Package quant is the single source of truth for how a quantization token is
// spelled in a gguf file name and in the model id derived from it.
//
// It exists because that answer used to live in four hand-maintained copies -
// autogen's discovery regex, autogen's family keys, the server's /v1/models
// metadata, and the Models table in the UI - and every quant the list had not
// heard of broke all four at once in the same way: the token stops looking like
// a quant, so the id never gets cut at it, and every ctx tier and vision twin of
// ONE model is presented as a model of its own (NVFP4 did exactly this).
//
// The pattern is deliberately written as FAMILIES rather than an enumeration.
// llama.cpp adds weight types faster than a list gets updated, and the families
// have been stable for years:
//
//	Q / IQ  Q4_0, Q4_K_M, Q2_K_S, IQ2_XXS, IQ4_XS, Q1_0 - plus the ik_llama.cpp
//	        fork's IQ4_KS / IQ2_KL / IQ4_KSS, which are the same shape
//	TQ      ternary: TQ1_0, TQ2_0
//	FP      FP8, FP16, and the 4-bit float formats with their vendor prefix:
//	        MXFP4 (OCP microscaling, E8M0 scale), MXFP4_MOE, NVFP4 (NVIDIA, E4M3)
//	BF / F  BF16, F16, F32
//
// A new member of any of those families is matched the day it ships. Only a
// genuinely new FAMILY (a vendor prefix nobody uses yet) needs an edit here -
// and then it needs it in exactly one place, with the TS mirror guarded by
// TestPatternMirrorsUI.
//
// Not included, on purpose: AWQ, GPTQ, EXL2/EXL3, W4A16 and friends. They label
// safetensors checkpoints for vLLM/ExLlama, and discovery only walks ggufs, so
// admitting them would buy nothing and risk swallowing a real name part.
package quant

import (
	"regexp"
	"strings"
)

// Pattern matches ONE quant token, unanchored and without flags, so callers can
// anchor it or embed it in a larger expression. Keep it in sync with
// QUANT_PATTERN in ui-svelte/src/lib/quant.ts.
const Pattern = `I?Q\d+(?:_[A-Z0-9]+)*|TQ\d+(?:_[A-Z0-9]+)*|(?:MX|NV)?FP\d+(?:_[A-Z0-9]+)*|BF\d+|F\d+`

// PrefixPattern matches a recipe marker that belongs to the quant rather than to
// the model name, always written immediately before the token: unsloth's dynamic
// "UD-Q4_K_XL", mradermacher's imatrix "i1-Q4_K_M".
const PrefixPattern = `UD|i1`

var (
	// TokenRe matches a whole '-'-separated part of an id that IS a quant.
	// Never split an id on "_" to use this: the token contains "_" itself.
	TokenRe = regexp.MustCompile(`(?i)^(?:` + Pattern + `)$`)
	// PrefixRe matches a whole part that is a recipe marker.
	PrefixRe = regexp.MustCompile(`(?i)^(?:` + PrefixPattern + `)$`)
	// InNameRe finds a quant inside a gguf FILE name, bounded by a separator
	// before and a separator or the extension after, so a name that merely
	// contains something quant-shaped does not trip it.
	InNameRe = regexp.MustCompile(`(?i)[-_.](` + Pattern + `)(?:[._-]|\.gguf$)`)
)

// FromName extracts the quant token from a gguf file name, upper-cased, or ""
// when the name carries none. The FIRST match wins: what follows a quant is a
// build tag ("-MTP", "-MID-HIGH", "-00001-of-00002"), never a second weight type.
func FromName(name string) string {
	if mm := InNameRe.FindStringSubmatch(name); mm != nil {
		return strings.ToUpper(mm[1])
	}
	return ""
}

// PartIndex returns the index of the first quant-shaped part of an already-split
// id, folding a UD/i1 marker in front of it into the token, or -1 when there is
// none. Index 0 is never returned - an id that IS a quant has no base left.
func PartIndex(parts []string) int {
	for i := 1; i < len(parts); i++ {
		if TokenRe.MatchString(parts[i]) {
			if i > 1 && PrefixRe.MatchString(parts[i-1]) {
				return i - 1
			}
			return i
		}
	}
	return -1
}
