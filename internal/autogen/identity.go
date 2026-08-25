package autogen

import (
	"regexp"
	"strings"
)

// Identity is what a gguf says it is, read out of its own header rather than
// out of its filename.
//
// The Models table groups on two keys: files sharing a ModelKey are the SAME
// model (one row, one pill per quant), and rows sharing a FamilyKey are
// finetunes of one base (one collapsible group). Deriving those from the name
// means guessing where the model stops and the quant starts, which is a losing
// game - every publisher spells a quant differently and some spell it in a way
// no pattern can read ("Qwen3.8-27B-mix-q-k-mtp"), so one model quietly becomes
// several rows. The header does not guess: convert_hf_to_gguf writes the source
// repo's own basename / size / finetune, and they survive any rename.
//
// Both keys are EMPTY when the header carries nothing usable - roughly a third
// of real files, since diffusion ggufs and hand-converted models write no
// identity KVs at all. That is not a failure: it is the caller's signal to fall
// back to the id-derived rules (ModelBaseKey / FamilyKey), which is all we ever
// had before. Keys are opaque - compare them, never parse or display them.
type Identity struct {
	ModelKey  string
	FamilyKey string

	// QuantLabel is the tensor-derived quantization ("Q4_K", "IQ4_XS mix"),
	// empty when the tensors can't be judged. It is a FALLBACK for display: a
	// filename that names its own quant keeps that spelling, because "UD-Q4_K_XL"
	// is what the user downloaded and "Q5_K mix" - though truer - is not.
	QuantLabel string
}

// identityJunk matches a KV value that identifies nothing: a bare commit hash
// (LFM2.5 ships general.finetune = "799e37a4...", Nanbeige a hex blob as its
// general.name), or a word describing where the file came from rather than
// which model it is. Left in the key, each of those splits a model's quants
// across rows as surely as a bad regex did - "Qwen3.8-27B-MXFP4" declares
// finetune "source" and is otherwise the same model as every other quant.
var (
	identityHexRe  = regexp.MustCompile(`^[0-9a-f]{16,}$`)
	identityJunkRe = regexp.MustCompile(`^(?:source|hf|gguf|converted|quantized|imatrix|main|model)$`)
	identityWordRe = regexp.MustCompile(`[^a-z0-9.]+`)
)

// normIdentity folds a header value to a comparable key fragment: case, spaces,
// underscores and dashes all stop mattering, so "Qwen2.5-Vl-7B-Instruct" and
// "qwen2.5_vl_7b_instruct" are one model. Returns "" for a value that says
// nothing about which model this is.
func normIdentity(s string) string {
	s = strings.Trim(identityWordRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if s == "" || identityHexRe.MatchString(s) || identityJunkRe.MatchString(s) {
		return ""
	}
	return s
}

// dropSizeLabel removes the parameter count from a basename. Publishers
// disagree about whether it belongs there - Qwen's own conversion writes
// basename "Qwen3.8" + size_label "27B" while unsloth's writes basename
// "Qwen3.8-27B" + the same size label - and the size is appended to the key
// separately, so leaving it in makes two spellings of one model.
func dropSizeLabel(base, size string) string {
	if base == "" || size == "" {
		return base
	}
	parts := strings.Split(base, "-")
	kept := parts[:0]
	for _, p := range parts {
		if p != size {
			kept = append(kept, p)
		}
	}
	return strings.Trim(strings.Join(kept, "-"), "-")
}

// identityFrom is the pure half of IdentityOf: header in, keys out.
//
// The row key is base + size + finetune and the family key is base + size, so
// every quant of one model lands on one row and every finetune of one base
// lands in one group. The finetune has to be IN the row key: Qwen3-4B-Instruct
// and Qwen3-4B-Thinking share a basename and a size, and are not the same model.
//
// A file with a size label and nothing else identifies nothing (a size is not a
// name), so both keys stay empty and the caller falls back to the id rules.
func identityFrom(m Metadata) Identity {
	id := Identity{QuantLabel: m.QuantLabel}
	size := normIdentity(m.SizeLabel)
	base := normIdentity(m.BaseName)
	if base == "" {
		// Some converters write only general.name, which is the same string with
		// the words title-cased and spaced ("Qwen3 4B Instruct 2507"). It carries
		// the finetune and size inline, so it can only serve as the whole key.
		if n := normIdentity(m.GeneralName); n != "" {
			return Identity{ModelKey: n, FamilyKey: dropSizeLabel(n, size), QuantLabel: m.QuantLabel}
		}
		return id
	}
	base = dropSizeLabel(base, size)
	if base == "" {
		return id
	}
	id.FamilyKey = strings.Trim(base+"-"+size, "-")
	id.ModelKey = strings.Trim(id.FamilyKey+"-"+normIdentity(m.FineTune), "-")
	return id
}

// IdentityOf reads a gguf's identity through the metadata cache, so the answer
// costs one map lookup after the first call per file. Never errors: an
// unreadable file identifies nothing, same as a header that says nothing.
func IdentityOf(path string) Identity {
	m, err := ReadGgufMetadataCached(path)
	if err != nil {
		return Identity{}
	}
	return identityFrom(m)
}
