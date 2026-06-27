package autogen

import "testing"

// A UI-written exact-path override must win over a hand-authored glob row that
// also matches the same gguf, so first-match resolution emits the exact row's
// variants (regression: the glob shadowed the exact row, dropping a 100k
// variant the editor still showed).
func TestSidecarExactFirst_ExactBeatsGlob(t *testing.T) {
	path := "E:/Models/Qwen3.6-35B-A3B-MTP-GGUF/Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf"
	rows := []Override{
		{Match: "*Qwen3.6-35B-A3B-MTP-GGUF*"}, // glob, judge-only in the wild
		{Match: path, Variants: []VariantSpec{{Name: "100k", Ctx: 102400}}},
	}
	ordered := sidecarExactFirst(rows)
	row := GgufRow{FullPath: path, Quant: "Q4_K_XL"}
	got := ResolveOverride(row, ordered)
	if got == nil || len(got.Variants) != 1 || got.Variants[0].Name != "100k" {
		t.Fatalf("expected exact-path row (with 100k variant) to resolve, got %+v", got)
	}
}
