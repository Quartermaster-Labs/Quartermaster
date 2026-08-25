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

// MIRROR: MIX_PATTERN / CRUMB_PATTERN must stay byte-identical to MixPattern /
// CrumbPattern in internal/quant/quant.go, guarded by the same mirror test.
//
// A hand-mixed quant ("Qwen3.8-27B-mix-q-k-mtp") has no canonical spelling -
// only the convention of saying "mix" and then listing the types that went in -
// so it is matched as a RUN of '-'-separated parts rather than one token: the
// marker plus the fragments after it, stopping at the build tag. A bare "-mix-"
// is deliberately not a quant: it is also an ordinary word in model names, and
// only "mix" followed by a fragment cannot be anything but a weight type.
export const MIX_PATTERN = String.raw`MIX(?:ED)?`;
// ONE fragment a mix run may absorb: the letters a K-quant recipe is spelled
// with once "-" has separated them.
export const CRUMB_PATTERN = String.raw`[QKSML]|X{1,2}[SLM]`;

export const MIX_RE = new RegExp(`^(?:${MIX_PATTERN})$`, "i");
export const CRUMB_PART_RE = new RegExp(`^(?:${CRUMB_PATTERN})$`, "i");

// Crumbs a quant leaves in a DISPLAY name. autogen strips the quant from the id
// only when it trails the filename, and prettifying splits what's left on "_":
// "ThinkingCap-Qwen3.6-27B-Q4_K_M-MTP" arrives named "Thinkingcap Qwen3.6 27b K
// M". The bare letters are the tail of a split K-quant, not tokens of their own.
export const CRUMB_RE = new RegExp(`^(?:UD|I1|${MIX_PATTERN}|${CRUMB_PATTERN}|${QUANT_PATTERN})$`, "i");
