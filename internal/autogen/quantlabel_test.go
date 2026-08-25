package autogen

import "testing"

// The shares here are the ones measured off real files (see quantDominance):
// they are the regression, because the threshold is only meaningful against
// the two populations it separates.
func TestQuantLabelFrom(t *testing.T) {
	cases := []struct {
		name  string
		bytes map[uint32]int64
		want  string
	}{
		// A plain single-type quant, with the F32 norms every file carries.
		{"pure Q8_0", map[uint32]int64{8: 1000, 0: 40}, "Q8_0"},
		// Q4_K_M: the recipe lifts token_embd/output to Q6_K and is still Q4_K.
		{"Q4_K_M", map[uint32]int64{12: 71, 14: 29, 0: 1}, "Q4_K"},
		// A full SDXL checkpoint: its F16 text encoders are not a second quant.
		{"baked encoders", map[uint32]int64{8: 75, 1: 25}, "Q8_0"},
		// A hand-mixed file: no type speaks for it, so the label says so.
		{"hand mixed", map[uint32]int64{23: 55, 13: 19, 14: 14, 8: 10}, "IQ4_XS mix"},
		// Unsloth's dynamic quants spread even wider than a hand-mixed one.
		{"UD dynamic", map[uint32]int64{13: 29, 23: 29, 12: 26, 14: 11}, "Q5_K mix"},
		// An unquantized export is all filler and still has to name itself.
		{"bf16 export", map[uint32]int64{30: 500, 0: 20}, "BF16"},
		{"f32 only", map[uint32]int64{0: 500}, "F32"},
		// A type this build can neither name nor size makes the whole file
		// unjudgeable - better no answer than a confident wrong one.
		{"unknown type", map[uint32]int64{12: 900, 250: 0}, ""},
		{"empty", map[uint32]int64{}, ""},
	}
	for _, c := range cases {
		if got := quantLabelFrom(c.bytes); got != c.want {
			t.Errorf("%s: quantLabelFrom = %q, want %q", c.name, got, c.want)
		}
	}
}

// A tie must not depend on Go's randomized map order.
func TestQuantLabelFrom_TieIsStable(t *testing.T) {
	for i := 0; i < 50; i++ {
		if got := quantLabelFrom(map[uint32]int64{12: 100, 14: 100}); got != "Q4_K mix" {
			t.Fatalf("tie broke to %q", got)
		}
	}
}

// Every type ggmlTypeSize can weigh must also be nameable: a type in one table
// and not the other silently makes every file carrying it unlabelled.
func TestGgmlTypeTablesAgree(t *testing.T) {
	for typ := range ggmlTypeSize {
		if _, ok := ggmlTypeName[typ]; !ok {
			t.Errorf("ggml type %d is sized but not named", typ)
		}
	}
	for typ := range ggmlTypeName {
		if _, ok := ggmlTypeSize[typ]; !ok {
			t.Errorf("ggml type %d is named but not sized", typ)
		}
	}
}
