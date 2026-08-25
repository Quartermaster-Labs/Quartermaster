package autogen

import "testing"

// Every case here is a REAL header off a real gguf. The rules exist to survive
// what publishers actually write, so invented inputs would prove nothing.
func TestIdentityFrom(t *testing.T) {
	cases := []struct {
		name             string
		meta             Metadata
		wantRow, wantFam string
	}{
		{
			// The bug this replaced regexes for: three unrelated spellings of one
			// model, all landing on one row. Only the header knows.
			"unsloth Qwen3.8",
			Metadata{BaseName: "Qwen3.8-27B", SizeLabel: "27B"},
			"qwen3.8-27b", "qwen3.8-27b",
		}, {
			// Qwen's own conversion writes the size OUT of the basename, and calls
			// the finetune "source". Same model, same row.
			"Qwen3.8 from source",
			Metadata{BaseName: "Qwen3.8", SizeLabel: "27B", FineTune: "source", GeneralName: "Qwen3.8 27B Source"},
			"qwen3.8-27b", "qwen3.8-27b",
		}, {
			// Finetunes must NOT collapse: Instruct and Thinking share a basename
			// and a size and are different models - one row each, one family.
			"Qwen3-4B Instruct",
			Metadata{BaseName: "Qwen3", SizeLabel: "4B", FineTune: "Instruct"},
			"qwen3-4b-instruct", "qwen3-4b",
		}, {
			"Qwen3-4B Thinking",
			Metadata{BaseName: "Qwen3", SizeLabel: "4B", FineTune: "Thinking"},
			"qwen3-4b-thinking", "qwen3-4b",
		}, {
			// An abliterated finetune keeps the base's family, so it groups under it.
			"a finetune of Qwen3.8",
			Metadata{BaseName: "Qwen3.8-27B", SizeLabel: "27B", FineTune: "Aggressive"},
			"qwen3.8-27b-aggressive", "qwen3.8-27b",
		}, {
			// Case and separators are noise: one model, however it is punctuated.
			"punctuation",
			Metadata{BaseName: "Qwen2.5_VL_7B_Instruct", SizeLabel: "7B", FineTune: "Instruct"},
			"qwen2.5-vl-instruct-7b-instruct", "qwen2.5-vl-instruct-7b",
		}, {
			// LFM2.5 ships its git commit as the finetune; left in, it would split
			// the model's quants across a row each.
			"a commit hash for a finetune",
			Metadata{BaseName: "LFM2.5", SizeLabel: "2.7B", FineTune: "799e37a4e60bdaae12c03b982cd0b0a531d87047"},
			"lfm2.5-2.7b", "lfm2.5-2.7b",
		}, {
			// Only general.name survives some conversions.
			"name only",
			Metadata{GeneralName: "Gemma 4 E2B", SizeLabel: "4.6B"},
			"gemma-4-e2b", "gemma-4-e2b",
		}, {
			// Nanbeige's general.name is a hex blob, and a size label alone names
			// nothing - so the file identifies nothing and the caller falls back.
			"junk name, size only",
			Metadata{GeneralName: "F56Ec5A9650268Aa098496734743C25Ea778Bd2D", SizeLabel: "4.2B"},
			"", "",
		}, {
			// Most diffusion ggufs and every hand conversion look like this.
			"no identity at all",
			Metadata{},
			"", "",
		},
	}
	for _, c := range cases {
		got := identityFrom(c.meta)
		if got.ModelKey != c.wantRow || got.FamilyKey != c.wantFam {
			t.Errorf("%s: identityFrom = (%q, %q), want (%q, %q)", c.name, got.ModelKey, got.FamilyKey, c.wantRow, c.wantFam)
		}
	}
}

// The quant label rides along untouched - identityFrom judges names, not weights.
func TestIdentityFrom_CarriesQuantLabel(t *testing.T) {
	if got := identityFrom(Metadata{QuantLabel: "IQ4_XS mix"}).QuantLabel; got != "IQ4_XS mix" {
		t.Errorf("QuantLabel = %q", got)
	}
}
