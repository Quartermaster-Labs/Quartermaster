package autogen

import (
	"math"
	"testing"
)

// A pinned CPU offload must be priced with the SAME bytes the auto plan priced.
// The whole model + KV + checkpoint reserve is conserved: whatever leaves VRAM
// shows up in RAM, so the pinned run's estVram+estRam has to equal the whole-fit
// run's estVram. It didn't: estForOffload dropped sizeProfile's checkpoint
// reserve, so the editor read "22.4 GB, no RAM" on the slider and "21.7 GB +
// 0.3 GB RAM" once the placement was pinned — a 0.3 GB hole that reconciled to
// nothing.
func TestEstimatePlan_ForcedOffloadConservesCheckpointReserve(t *testing.T) {
	meta := Metadata{
		Architecture: "llama", BlockCount: 32,
		HeadCountKv: 8, KeyLength: 128, ValueLength: 128,
		ContextLength: 131072, EmbeddingLength: 4096,
		FileSizeGB: 12,
	}
	s := Settings{
		TargetVramGB: 24, VramOverheadGB: 0.5, MaxRamGB: 64,
		DenseCtxLadder: []int{131072, 65536, 32768}, DenseMinCtx: 4096,
	}
	many := 16
	in := EstimateInput{Ctx: 32768, KvK: "q8_0", KvV: "q8_0", CtxCheckpoints: &many}

	auto, err := EstimatePlan(s, meta, in)
	if err != nil {
		t.Fatal(err)
	}
	if auto.EstRamGB != 0 || auto.Ngl < int(meta.BlockCount) {
		t.Fatalf("setup: expected a whole-model VRAM fit, got ngl=%d estRam=%v", auto.Ngl, auto.EstRamGB)
	}
	if auto.CheckpointGB <= 0 {
		t.Fatalf("setup: expected a non-zero checkpoint reserve, got %v", auto.CheckpointGB)
	}

	in.CpuOffload = 4
	pinned, err := EstimatePlan(s, meta, in)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Ngl != int(meta.BlockCount)-4 {
		t.Fatalf("pinned ngl=%d want %d", pinned.Ngl, int(meta.BlockCount)-4)
	}
	if pinned.EstRamGB <= 0 {
		t.Fatalf("pinned estRam=%v want the offloaded layers' cost", pinned.EstRamGB)
	}
	// round2 on both halves, so allow a cent of slack.
	if got, want := pinned.EstVramGB+pinned.EstRamGB, auto.EstVramGB; math.Abs(got-want) > 0.02 {
		t.Errorf("pinned estVram %.2f + estRam %.2f = %.2f, want the auto plan's total %.2f (checkpoint reserve dropped?)",
			pinned.EstVramGB, pinned.EstRamGB, got, want)
	}
}
