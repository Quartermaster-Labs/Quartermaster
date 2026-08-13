package autogen

import (
	"strings"
	"testing"
)

// The trained length is the ceiling unless rope scaling is explicitly on: a bare
// ctx override past context_length must still clamp, since a longer window over
// untrained positions is garbage, not more context.
func TestRopeCeiling(t *testing.T) {
	meta := Metadata{ContextLength: 131072}
	cases := []struct {
		scaling string
		ctx     int
		want    int
	}{
		{"", 262144, 131072},          // no scaling -> clamp
		{"none", 262144, 131072},      // "none" disables scaling, grants nothing
		{"yarn", 262144, 262144},      // opted in -> ceiling follows the request
		{"linear", 262144, 262144},    // linear too
		{"yarn", 65536, 131072},       // asking for less never lowers the ceiling
		{"yarn", 1 << 24, 131072 * 8}, // bounded by maxRopeFactor
	}
	for _, c := range cases {
		if got := ropeCeiling(meta, c.scaling, c.ctx); got != c.want {
			t.Errorf("ropeCeiling(%q, %d) = %d, want %d", c.scaling, c.ctx, got, c.want)
		}
	}
	// No declared context_length falls back to the sizer's 32k assumption.
	if got := ropeCeiling(Metadata{}, "", 0); got != 32768 {
		t.Errorf("ropeCeiling(no ctxlen) = %d, want 32768", got)
	}
}

// The factor rounds UP, or the tail of the window lands on untrained positions.
func TestRopeFactor(t *testing.T) {
	meta := Metadata{ContextLength: 131072}
	cases := []struct {
		ctx  int
		want float64
	}{
		{131072, 0},   // at native: no scaling needed
		{65536, 0},    // below native
		{262144, 2},   // exactly 2x
		{163840, 1.5}, // 1.25x -> next half step
		{140000, 1.5},
	}
	for _, c := range cases {
		if got := ropeFactor(meta, c.ctx); got != c.want {
			t.Errorf("ropeFactor(%d) = %v, want %v", c.ctx, got, c.want)
		}
	}
}

// A scaling type with no explicit ropeScale must emit a DERIVED --rope-scale:
// llama.cpp otherwise takes the factor from gguf metadata (1.0 on a model never
// fine-tuned for extension) and the extra ctx does nothing.
func TestBuildCmdLines_ropeScaleDerived(t *testing.T) {
	s := Settings{}
	meta := Metadata{ContextLength: 32768}
	row := GgufRow{FullPath: "/m.gguf"}
	prof := profile{Name: "solo"}

	got := strings.Join(buildCmdLines(s, meta, row, prof, 65536, 99, 0, "f16", "f16", false,
		&Override{RopeScaling: "yarn", Ctx: 65536}), " ")
	if !strings.Contains(got, "--rope-scaling yarn") || !strings.Contains(got, "--rope-scale 2") {
		t.Fatalf("derived rope scale missing: %s", got)
	}

	// An explicit factor wins and is never doubled up.
	got = strings.Join(buildCmdLines(s, meta, row, prof, 65536, 99, 0, "f16", "f16", false,
		&Override{RopeScaling: "yarn", Ctx: 65536, RopeScale: 3}), " ")
	if !strings.Contains(got, "--rope-scale 3") || strings.Count(got, "--rope-scale") != 1 {
		t.Fatalf("explicit rope scale not respected: %s", got)
	}

	// At/below the trained length there is nothing to scale, so no flag.
	got = strings.Join(buildCmdLines(s, meta, row, prof, 32768, 99, 0, "f16", "f16", false,
		&Override{RopeScaling: "yarn", Ctx: 32768}), " ")
	if strings.Contains(got, "--rope-scale ") {
		t.Fatalf("rope scale emitted at native ctx: %s", got)
	}
}

// EstimatePlan must honour the lifted ceiling too, or the cogwheel's live
// preview reports the KV reserve of a window the launch won't actually use.
func TestEstimatePlan_ropeExtendsCtx(t *testing.T) {
	s := Settings{TargetVramGB: 24, VramOverheadGB: 1}
	meta := Metadata{
		Architecture: "qwen3", ContextLength: 32768, BlockCount: 32,
		EmbeddingLength: 4096, HeadCount: 32, HeadCountKv: 8,
		KeyLength: 128, ValueLength: 128, FileSizeGB: 4,
	}

	clamped, err := EstimatePlan(s, meta, EstimateInput{Ctx: 65536})
	if err != nil {
		t.Fatalf("estimate (clamped): %v", err)
	}
	if clamped.Ctx > 32768 {
		t.Fatalf("ctx %d exceeded trained length without rope scaling", clamped.Ctx)
	}

	extended, err := EstimatePlan(s, meta, EstimateInput{Ctx: 65536, RopeScaling: "yarn"})
	if err != nil {
		t.Fatalf("estimate (extended): %v", err)
	}
	if extended.Ctx != 65536 {
		t.Fatalf("rope-extended ctx = %d, want 65536", extended.Ctx)
	}
	if extended.EstVramGB <= clamped.EstVramGB {
		t.Fatalf("extended ctx must cost more VRAM: %.2f vs %.2f", extended.EstVramGB, clamped.EstVramGB)
	}
}
