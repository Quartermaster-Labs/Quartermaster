package autogen

import (
	"errors"
	"path/filepath"
	"testing"
)

// The Go key derivation must agree with the UI's (ui-svelte/src/lib/
// modelTable.ts): same axes, same answers, or a model groups one way in the
// table and inherits sidecars another way.
func TestModelBaseKeyAndFamilyKey(t *testing.T) {
	cases := []struct{ id, base, family string }{
		// Same model, two quants -> one base key.
		{"qwen3.6-27b-instruct-q4_k_m", "qwen3.6-27b-instruct", "qwen3.6-27b"},
		{"qwen3.6-27b-instruct-q8_0", "qwen3.6-27b-instruct", "qwen3.6-27b"},
		// Recipe prefixes belong to the quant.
		{"qwen3.6-27b-instruct-ud-q4_k_xl", "qwen3.6-27b-instruct", "qwen3.6-27b"},
		{"qwen3.6-27b-instruct-i1-q4_k_m", "qwen3.6-27b-instruct", "qwen3.6-27b"},
		// Finetunes, prefix and suffix, land on the same family.
		{"thinkingcap-qwen3.6-27b", "thinkingcap-qwen3.6-27b", "qwen3.6-27b"},
		{"qwen3.6-27b-uncensored-heretic-v2", "qwen3.6-27b-uncensored-heretic-v2", "qwen3.6-27b"},
		// A bare version number belongs to the name.
		{"gemma-4-12b-it-q6_k", "gemma-4-12b-it", "gemma-4-12b"},
		// MoE active-parameter tail stays in the family key.
		{"qwen3.6-35b-a3b-q4_k_m", "qwen3.6-35b-a3b", "qwen3.6-35b-a3b"},
		// Build tags trail the quant and are cut with it.
		{"qwen3.6-27b-q4_k_m-mtp-q4_k_m", "qwen3.6-27b", "qwen3.6-27b"},
		// No size token: its own family, so only an exact twin can donate.
		{"some-random-model", "some-random-model", "some-random-model"},
		// Discovery strips the quant but leaves the recipe marker behind, so the
		// orphan has to fold too - otherwise unsloth's UD quants form a family of
		// their own and never donate to the same model's plain quants.
		{"qwen3.8-27b-ud", "qwen3.8-27b", "qwen3.8-27b"},
		{"qwen3.8-27b-i1", "qwen3.8-27b", "qwen3.8-27b"},
		// MXFP4 is a quant token like any other.
		{"qwen3.8-27b-mxfp4", "qwen3.8-27b", "qwen3.8-27b"},
		// So is NVFP4 - and a publisher who trails the build tag rather than the
		// quant leaves it mid-id, where the cut still has to land on it.
		{"qwen3.8-27b-nvfp4-mtp-mid-high", "qwen3.8-27b", "qwen3.8-27b"},
	}
	for _, c := range cases {
		if got := ModelBaseKey(c.id); got != c.base {
			t.Errorf("ModelBaseKey(%q) = %q, want %q", c.id, got, c.base)
		}
		if got := FamilyKey(c.id); got != c.family {
			t.Errorf("FamilyKey(%q) = %q, want %q", c.id, got, c.family)
		}
	}
}

// Different parameter counts of one release are NOT one family: a 27B's drafter
// must never be handed to the 12B beside it.
func TestFamilyKey_SizeSeparatesRelease(t *testing.T) {
	if FamilyKey("qwen3.6-27b-instruct-q4_k_m") == FamilyKey("qwen3.6-12b-instruct-q4_k_m") {
		t.Fatal("27b and 12b share a family key")
	}
}

func TestSidecarCompatible(t *testing.T) {
	base := Metadata{Architecture: "qwen35", EmbeddingLength: 5120, BlockCount: 64, VocabSize: 248320}
	if !sidecarCompatible(base, base) {
		t.Fatal("identical headers reported incompatible")
	}
	// A vocab-extended finetune is the case that hard-fails a llama-server launch.
	extended := base
	extended.VocabSize = 248832
	if sidecarCompatible(base, extended) {
		t.Fatal("mismatched vocab accepted")
	}
	pruned := base
	pruned.BlockCount = 48
	if sidecarCompatible(base, pruned) {
		t.Fatal("mismatched block count accepted")
	}
	// An unparsed field is an unknown, not a match.
	unknown := base
	unknown.VocabSize = 0
	if sidecarCompatible(unknown, base) || sidecarCompatible(base, unknown) {
		t.Fatal("zero vocab accepted as a match")
	}
}

// metaFn builds a header reader over a path->Metadata table for the tests below.
func metaFn(m map[string]Metadata) func(string) (Metadata, error) {
	return func(p string) (Metadata, error) {
		if md, ok := m[p]; ok {
			return md, nil
		}
		return Metadata{}, errors.New("no header")
	}
}

func p(parts ...string) string { return filepath.Join(parts...) }

// The headline case: a drafter downloaded next to ONE quant reaches the other
// quant and a finetune of the same base, while a model that ships its own
// drafter keeps it.
func TestInheritSidecars_DraftAcrossQuantsAndFinetunes(t *testing.T) {
	q4 := p("root", "q4", "qwen3.6-27b-q4_k_m.gguf")
	q8 := p("root", "q8", "qwen3.6-27b-q8_0.gguf")
	tune := p("root", "tune", "qwen3.6-27b-heretic-q4_k_m.gguf")
	own := p("root", "own", "thinkingcap-qwen3.6-27b-q4_k_m.gguf")
	other := p("root", "other", "gemma-4-12b-q4_k_m.gguf")

	md := Metadata{Architecture: "qwen35", EmbeddingLength: 5120, BlockCount: 64, VocabSize: 248320}
	rows := []GgufRow{
		{ID: "qwen3.6-27b-q4_k_m", BaseID: "qwen3.6-27b", Quant: "Q4_K_M", FullPath: q4,
			DraftPath: p("root", "q4", "mtp-qwen3.6-27b.gguf"), DraftKind: "mtp", DraftSizeGB: 0.5},
		{ID: "qwen3.6-27b-q8_0", BaseID: "qwen3.6-27b", Quant: "Q8_0", FullPath: q8},
		{ID: "qwen3.6-27b-heretic-q4_k_m", BaseID: "qwen3.6-27b-heretic", Quant: "Q4_K_M", FullPath: tune},
		{ID: "thinkingcap-qwen3.6-27b-q4_k_m", BaseID: "thinkingcap-qwen3.6-27b", Quant: "Q4_K_M", FullPath: own,
			DraftPath: p("root", "own", "mtp-thinkingcap.gguf"), DraftKind: "mtp", DraftSizeGB: 0.4},
		{ID: "gemma-4-12b-q4_k_m", BaseID: "gemma-4-12b", Quant: "Q4_K_M", FullPath: other},
	}
	metas := map[string]Metadata{q4: md, q8: md, tune: md, own: md}
	metas[other] = Metadata{Architecture: "gemma3", EmbeddingLength: 3840, BlockCount: 48, VocabSize: 262144}

	inheritSidecarsWith(rows, metaFn(metas))

	if rows[1].DraftPath != rows[0].DraftPath || rows[1].DraftKind != "mtp" || rows[1].DraftSizeGB != 0.5 {
		t.Fatalf("other quant did not inherit: %+v", rows[1])
	}
	if rows[2].DraftPath != rows[0].DraftPath {
		t.Fatalf("finetune did not inherit: %q", rows[2].DraftPath)
	}
	// A finetune shipping its own drafter keeps it — dir-local always wins.
	if filepath.Base(rows[3].DraftPath) != "mtp-thinkingcap.gguf" {
		t.Fatalf("own drafter overwritten: %q", rows[3].DraftPath)
	}
	// A different model entirely gets nothing.
	if rows[4].DraftPath != "" {
		t.Fatalf("unrelated model inherited a drafter: %q", rows[4].DraftPath)
	}
}

// The header gate outranks the name gate: a family sibling whose tokenizer grew
// must not be handed a drafter that would abort its launch.
func TestInheritSidecars_HeaderMismatchRefused(t *testing.T) {
	donor := p("root", "a", "qwen3.6-27b-q4_k_m.gguf")
	want := p("root", "b", "qwen3.6-27b-vocabgrown-q4_k_m.gguf")
	rows := []GgufRow{
		{BaseID: "qwen3.6-27b", Quant: "Q4_K_M", FullPath: donor,
			DraftPath: p("root", "a", "mtp.gguf"), DraftKind: "mtp", DraftSizeGB: 0.5,
			MmprojPath: p("root", "a", "mmproj.gguf"), MmprojSizeGB: 0.9},
		{BaseID: "qwen3.6-27b-vocabgrown", Quant: "Q4_K_M", FullPath: want},
	}
	inheritSidecarsWith(rows, metaFn(map[string]Metadata{
		donor: {Architecture: "qwen35", EmbeddingLength: 5120, BlockCount: 64, VocabSize: 248320},
		want:  {Architecture: "qwen35", EmbeddingLength: 5120, BlockCount: 64, VocabSize: 249344},
	}))
	if rows[1].DraftPath != "" || rows[1].MmprojPath != "" {
		t.Fatalf("incompatible sibling inherited: draft=%q mmproj=%q", rows[1].DraftPath, rows[1].MmprojPath)
	}
}

// A projector shipped with the base model reaches a finetune that has none, and
// an exact-quant twin of the same model is preferred over a finetune's copy.
func TestInheritSidecars_MmprojDonorPreference(t *testing.T) {
	md := Metadata{Architecture: "qwen3vl", EmbeddingLength: 4096, BlockCount: 40, VocabSize: 151936}
	sameModel := p("root", "base-q8", "qwen3-vl-8b-q8_0.gguf")
	tuneDonor := p("root", "tune", "qwen3-vl-8b-roleplay-q4_k_m.gguf")
	want := p("root", "base-q4", "qwen3-vl-8b-q4_k_m.gguf")
	rows := []GgufRow{
		{BaseID: "qwen3-vl-8b-roleplay", Quant: "Q4_K_M", FullPath: tuneDonor,
			MmprojPath: p("root", "tune", "mmproj.gguf"), MmprojSizeGB: 1.1},
		{BaseID: "qwen3-vl-8b", Quant: "Q8_0", FullPath: sameModel,
			MmprojPath: p("root", "base-q8", "mmproj.gguf"), MmprojSizeGB: 1.2},
		{BaseID: "qwen3-vl-8b", Quant: "Q4_K_M", FullPath: want},
	}
	inheritSidecarsWith(rows, metaFn(map[string]Metadata{sameModel: md, tuneDonor: md, want: md}))
	if rows[2].MmprojPath != rows[1].MmprojPath {
		t.Fatalf("preferred the finetune's projector over the same model's: %q", rows[2].MmprojPath)
	}
	if rows[2].MmprojSizeGB != 1.2 {
		t.Fatalf("MmprojSizeGB = %v, want the donor's 1.2", rows[2].MmprojSizeGB)
	}
}

// Inheritance is by pair, not by bundle: a model can borrow a projector from one
// sibling and a drafter from another, and a row with a header the parser could
// not read is left alone rather than guessed at.
func TestInheritSidecars_UnreadableHeaderSkipped(t *testing.T) {
	donor := p("root", "a", "qwen3.6-27b-q4_k_m.gguf")
	want := p("root", "b", "qwen3.6-27b-q8_0.gguf")
	rows := []GgufRow{
		{BaseID: "qwen3.6-27b", Quant: "Q4_K_M", FullPath: donor,
			DraftPath: p("root", "a", "mtp.gguf"), DraftKind: "mtp", DraftSizeGB: 0.5},
		{BaseID: "qwen3.6-27b", Quant: "Q8_0", FullPath: want},
	}
	// Only the donor parses; the recipient's header is unreadable.
	inheritSidecarsWith(rows, metaFn(map[string]Metadata{
		donor: {Architecture: "qwen35", EmbeddingLength: 5120, BlockCount: 64, VocabSize: 248320},
	}))
	if rows[1].DraftPath != "" {
		t.Fatalf("inherited without a header to check: %q", rows[1].DraftPath)
	}
}
