package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

// A dense model trained past 128k must be sized to its own ceiling when the KV
// cache for that window fits the budget. The default ladder used to top out at
// 131072, which pinned every 256k/512k/1M model there and left the VRAM those
// extra tokens would have used unallocated.
func TestGetDenseCtx_DefaultLadderReachesModelMax(t *testing.T) {
	var s Settings
	s.applyDefaults()

	// Roomy budget: ~0.6 GB of KV per 128k tokens, 40 GB target, 8 GB weights.
	p := DenseCtxParams{
		ModelMax: 262144, PerTokGB: 0.6 / 131072, KvConstGB: 0.1,
		FileSizeGB: 8, TargetVramGB: 40, Overhead: 1.5,
		Ladder: s.DenseCtxLadder, MinCtx: s.DenseMinCtx,
	}
	got := GetDenseCtx(p)
	if got.Ctx != 262144 {
		t.Errorf("ctx = %d (%s), want the model max 262144", got.Ctx, got.Note)
	}
	if got.Note != "model-max" {
		t.Errorf("note = %q, want %q", got.Note, "model-max")
	}
}

// An explicitly configured ladder is still a ceiling the sizer honors.
func TestGetDenseCtx_ExplicitLadderStillCaps(t *testing.T) {
	p := DenseCtxParams{
		ModelMax: 262144, PerTokGB: 0.6 / 131072, KvConstGB: 0.1,
		FileSizeGB: 8, TargetVramGB: 40, Overhead: 1.5,
		Ladder: []int{65536, 32768}, MinCtx: 32768,
	}
	if got := GetDenseCtx(p); got.Ctx != 65536 {
		t.Errorf("ctx = %d (%s), want the ladder top 65536", got.Ctx, got.Note)
	}
}

// No ladder at all is not a crash and not a 0-token window: the model's trained
// ceiling is the only cap left.
func TestGetDenseCtx_EmptyLadderUsesModelMax(t *testing.T) {
	p := DenseCtxParams{
		ModelMax: 262144, PerTokGB: 0.6 / 131072, KvConstGB: 0.1,
		FileSizeGB: 8, TargetVramGB: 40, Overhead: 1.5,
		MinCtx: 32768,
	}
	if got := GetDenseCtx(p); got.Ctx != 262144 {
		t.Errorf("ctx = %d (%s), want the model max 262144", got.Ctx, got.Note)
	}
}

// An install made before the ceiling moved has the old ladder written into its
// generate file AND its UI settings sidecar, so raising the default alone is
// invisible: both copies have to be migrated or the 128k cap survives forever.
func TestSettings_LegacyCtxLadderMigrates(t *testing.T) {
	s := Settings{DenseCtxLadder: []int{131072, 65536, 32768}}
	s.applyDefaults()
	if got := s.DenseCtxLadder[0]; got != 1048576 {
		t.Errorf("ladder top = %d, want the current default 1048576 (ladder=%v)", got, s.DenseCtxLadder)
	}

	// A ladder that is NOT the old shipped default is a deliberate cap: keep it.
	keep := Settings{DenseCtxLadder: []int{131072, 32768}}
	keep.applyDefaults()
	if len(keep.DenseCtxLadder) != 2 || keep.DenseCtxLadder[0] != 131072 {
		t.Errorf("hand-set ladder was rewritten: %v", keep.DenseCtxLadder)
	}
}

// The whole point, on a real file: gemma-4-12B-it-qat is trained to 262144 and
// was being loaded at 131072 with ~10 GB of a 22.8 GB budget unspent.
func TestEstimatePlan_Gemma4_12B_ReachesTrainedCeiling(t *testing.T) {
	if realModelsRoot == "" {
		t.Skip("no local model tree")
	}
	p := filepath.Join(realModelsRoot, "unsloth", "gemma-4-12B-it-qat-GGUF", "gemma-4-12B-it-qat-UD-Q4_K_XL.gguf")
	if _, err := os.Stat(p); err != nil {
		t.Skip("gemma-4-12B-it-qat not present")
	}
	meta, err := ReadGgufMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContextLength != 262144 {
		t.Skipf("this build of the model is trained to %d, not 262144", meta.ContextLength)
	}
	s := Settings{TargetVramGB: 22.8, VramOverheadGB: 0.5, MaxRamGB: 24}
	s.applyDefaults()
	est, err := EstimatePlan(s, meta, EstimateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if est.Ctx != 262144 {
		t.Errorf("ctx = %d (%.2f GB est), want the trained 262144", est.Ctx, est.EstVramGB)
	}
}
