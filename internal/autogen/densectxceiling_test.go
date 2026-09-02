package autogen

import "testing"

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
