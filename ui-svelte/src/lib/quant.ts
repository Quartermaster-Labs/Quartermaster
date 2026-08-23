// How a quantization token is spelled — the UI half of internal/quant.
//
// MIRROR: QUANT_PATTERN below must stay byte-identical to `Pattern` in
// internal/quant/quant.go. TestPatternMirrorsUI in that package reads THIS file
// and fails the Go build's tests if the two drift, because they cannot drift
// harmlessly: the server derives a model's `quant` field from the Go copy while
// this table decides which ids collapse onto one row, so a token only one side
// knows makes the same model list twice under two different shapes.
//
// Written as families (Q/IQ, TQ, FP with its vendor prefix, BF/F) rather than an
// enumeration, so a new member of a known family — the next K-quant, the next
// 4-bit float — is matched the day it ships without an edit here. See the Go
// package doc for what is deliberately excluded (AWQ/GPTQ and friends).
export const QUANT_PATTERN = String.raw`I?Q\d+(?:_[A-Z0-9]+)*|TQ\d+(?:_[A-Z0-9]+)*|(?:MX|NV)?FP\d+(?:_[A-Z0-9]+)*|BF\d+|F\d+`;

// A whole '-'-separated part of an id that IS a quant. Never split an id on "_"
// to use this: the token contains "_" itself.
export const QUANT_RE = new RegExp(`^(?:${QUANT_PATTERN})$`, "i");

// Recipe markers that belong to the quant rather than the model name: unsloth
// writes "UD-Q4_K_XL" (dynamic), mradermacher "i1-Q4_K_M" (imatrix).
export const QUANT_PREFIX_RE = /^(?:UD|i1)$/i;

// Crumbs a quant leaves in a DISPLAY name. autogen strips the quant from the id
// only when it trails the filename, and prettifying splits what's left on "_":
// "ThinkingCap-Qwen3.6-27B-Q4_K_M-MTP" arrives named "Thinkingcap Qwen3.6 27b K
// M". The bare letters are the tail of a split K-quant, not tokens of their own.
export const CRUMB_RE = new RegExp(`^(?:UD|I1|K|M|S|L|X{1,2}[SLM]|${QUANT_PATTERN})$`, "i");
