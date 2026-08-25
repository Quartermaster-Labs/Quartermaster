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
// One shape is not a single token at all: a gguf quantized per-tensor carries
// only the label its author gave it ("mix-q-k"), so it is matched as a RUN of
// '-'-separated parts - see MixPattern.
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
	// ggufExtRe strips the file extension before a name is split into parts.
	ggufExtRe = regexp.MustCompile(`(?i)\.gguf$`)
)

// FromName extracts the quant token from a gguf file name, upper-cased, or ""
// when the name carries none. The FIRST match wins: what follows a quant is a
// build tag ("-MTP", "-MID-HIGH", "-00001-of-00002"), never a second weight type.
func FromName(name string) string {
	base := ggufExtRe.ReplaceAllString(name, "")
	// A named recipe is found by regex (it can be bounded by "_" or "." as well
	// as "-"); a hand-mixed one spans several '-' parts and has to be walked. Run
	// both and take whichever starts EARLIER in the name, because first-wins is
	// what makes "…-Q6_K-mtp" a Q6_K and "…-mixed-q6_k-q4_k_m-mtp" a mix of two.
	tok, at := "", -1
	if mm := InNameRe.FindStringSubmatchIndex(name); mm != nil {
		tok, at = strings.ToUpper(name[mm[2]:mm[3]]), mm[2]
	}
	parts := strings.Split(base, "-")
	for i, off := 0, 0; i < len(parts); i++ {
		if isMixStart(parts, i) && (at < 0 || off < at) {
			return TokenAt(parts, i)
		}
		off += len(parts[i]) + 1 // + the '-' that follows it
	}
	return tok
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
		if isMixStart(parts, i) {
			return i
		}
	}
	return -1
}

// MixPattern matches the marker that opens a HAND-MIXED quant: a gguf built by
// llama-quantize with per-tensor types rather than one named recipe, which its
// author then labels by hand ("Qwen3.8-27B-mix-q-k-mtp.gguf"). There is no
// canonical spelling for such a build - only the convention of saying "mix" and
// then listing the types that went in - so the token is matched as a RUN: the
// marker plus the fragments that follow it, stopping at the first part that is
// neither (the build tag).
//
// A bare "-mix-" is deliberately NOT a quant. The marker only counts when at
// least one fragment follows it, because "mix" is also an ordinary word in model
// names ("openhermes-mix-2.5") and cutting an id there would strand half the
// name; "mix-q-k" cannot be anything but a weight type.
const MixPattern = `MIX(?:ED)?`

// CrumbPattern matches ONE fragment a mix run may absorb: the letters a K-quant
// recipe is spelled with once the '-' has separated them. Full quant tokens are
// absorbed too (see runEnd), so "mix-q4_k_m-q6_k" reads as one token.
const CrumbPattern = `[QKSML]|X{1,2}[SLM]`

var (
	mixRe   = regexp.MustCompile(`(?i)^(?:` + MixPattern + `)$`)
	crumbRe = regexp.MustCompile(`(?i)^(?:` + CrumbPattern + `)$`)
)

// runEnd returns the index one past the last part of the token that starts at
// parts[i], which is i+1 for every token but a mix run (and i+2 when i is a
// UD/i1 marker sitting in front of its token).
func runEnd(parts []string, i int) int {
	switch {
	case i < 0 || i >= len(parts):
		return i
	case PrefixRe.MatchString(parts[i]) && i+1 < len(parts):
		return i + 2
	case !mixRe.MatchString(parts[i]):
		return i + 1
	}
	j := i + 1
	for j < len(parts) && (crumbRe.MatchString(parts[j]) || TokenRe.MatchString(parts[j])) {
		j++
	}
	return j
}

// isMixStart reports whether a mix run begins at parts[i] - the marker AND at
// least one fragment after it.
func isMixStart(parts []string, i int) bool {
	return mixRe.MatchString(parts[i]) && runEnd(parts, i) > i+1
}

// TokenAt renders the whole token that starts at parts[i], upper-cased.
func TokenAt(parts []string, i int) string {
	end := runEnd(parts, i)
	if i < 0 || end <= i {
		return ""
	}
	return strings.ToUpper(strings.Join(parts[i:end], "-"))
}

// FromParts extracts the quant token from an already-split name, "" when there
// is none. Unlike PartIndex it will match at index 0, because it is fed FILE
// names, where a file may be named for nothing but its weight type.
func FromParts(parts []string) string {
	for i := range parts {
		if TokenRe.MatchString(parts[i]) {
			if i > 0 && PrefixRe.MatchString(parts[i-1]) {
				return TokenAt(parts, i-1)
			}
			return TokenAt(parts, i)
		}
		if isMixStart(parts, i) {
			return TokenAt(parts, i)
		}
	}
	return ""
}
