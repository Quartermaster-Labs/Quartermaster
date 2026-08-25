package autogen

import "sort"

// tensorScan is everything one walk of the GGUF tensor-info section yields.
// The walk is not free (it reads a name + dims + type for every tensor in the
// file), so each new question about the weights is answered by adding a field
// here rather than by a second pass.
type tensorScan struct {
	expertShare float64
	vocabElems  int64
	diffKind    string
	bakedEnc    bool

	// typeBytes is on-disk bytes per ggml type id. A type missing from
	// ggmlTypeSize can't be weighed, so it is recorded with 0 bytes: its
	// PRESENCE still has to be visible to quantLabelFrom, which reports an
	// unweighable file as unlabelled rather than as the type it could measure.
	typeBytes map[uint32]int64
}

// ggmlTypeName spells a ggml type id the way llama-quantize names it. Ids are
// ggml's own enum (ggml/include/ggml.h), the same numbering ggmlTypeSize keys
// on — keep the two tables in step: a type that can be weighed but not named
// makes an unlabelled file, and one named but not weighed makes a wrong label.
var ggmlTypeName = map[uint32]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 6: "Q5_0", 7: "Q5_1",
	8: "Q8_0", 9: "Q8_1", 10: "Q2_K", 11: "Q3_K", 12: "Q4_K", 13: "Q5_K",
	14: "Q6_K", 15: "Q8_K", 16: "IQ2_XXS", 17: "IQ2_XS", 18: "IQ3_XXS",
	19: "IQ1_S", 20: "IQ4_NL", 21: "IQ3_S", 22: "IQ2_S", 23: "IQ4_XS",
	24: "I8", 25: "I16", 26: "I32", 27: "I64", 28: "F64", 29: "IQ1_M", 30: "BF16", 34: "TQ1_0", 35: "TQ2_0",
	39: "MXFP4", 40: "NVFP4", 41: "Q1_0",
}

// filler types carry no information about how a file was quantized: F32 is the
// norm/bias residue every quant leaves behind (and the type a converter writes
// a 1-D tensor as regardless), and the integer types are indices, not weights.
// Counting them would label a small model by its bias tensors.
var fillerType = map[uint32]bool{0: true, 24: true, 25: true, 26: true, 27: true}

// quantDominance is the share of weight bytes one type must hold to name the
// file by itself. Measured against a real model tree rather than guessed: a
// named recipe leaves its headline type 71-100% of the bytes (Q4_K_M sits at
// 71%, the rest Q6_K; a full SDXL checkpoint at 75%, the rest its F16 text
// encoders), while a file with no single answer spreads much wider - an
// unsloth UD-Q4_K_XL peaks at 45%, a hand-mixed one at 55%. 0.7 is the gap
// between those two populations, not a round number.
const quantDominance = 0.7

// quantLabelFrom names a file's quantization from what its tensors ACTUALLY
// are, rather than from its filename or its self-declared general.file_type
// (which lies: files shipped as "MXFP4" declare Q8_0, and a hand-mixed file
// declares whatever the last quantize pass set). The heaviest weight type wins;
// when nothing holds quantDominance of the bytes, the label says so ("IQ4_XS
// mix") because there is no single honest answer.
//
// Returns "" when the file cannot be judged - no weighable tensors, or a type
// present that this build can neither name nor size - so callers fall back to
// the name rules instead of showing a confident wrong answer.
func quantLabelFrom(typeBytes map[uint32]int64) string {
	var total int64
	var top uint32
	var topBytes int64
	seen := false
	for typ, b := range typeBytes {
		if _, ok := ggmlTypeName[typ]; !ok {
			return ""
		}
		if fillerType[typ] {
			continue
		}
		seen = true
		total += b
		// Ties break on the lower id (the coarser quant), so a file split
		// exactly between two types labels the same way on every read - map
		// iteration order is random.
		if b > topBytes || (b == topBytes && typ < top) {
			top, topBytes = typ, b
		}
	}
	switch {
	case !seen:
		// An all-F32/F16-of-1-D-tensors file: nothing but filler. Name it by
		// the filler anyway rather than reporting nothing.
		return fillerLabel(typeBytes)
	case total <= 0:
		return ""
	case float64(topBytes)/float64(total) >= quantDominance:
		return ggmlTypeName[top]
	}
	return ggmlTypeName[top] + " mix"
}

// fillerLabel names a file made only of types quantLabelFrom skips - an
// unquantized F32 export, or a tensor section of pure indices.
func fillerLabel(typeBytes map[uint32]int64) string {
	ids := make([]uint32, 0, len(typeBytes))
	for typ := range typeBytes {
		ids = append(ids, typ)
	}
	sort.Slice(ids, func(i, j int) bool {
		if typeBytes[ids[i]] != typeBytes[ids[j]] {
			return typeBytes[ids[i]] > typeBytes[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) == 0 {
		return ""
	}
	return ggmlTypeName[ids[0]]
}
