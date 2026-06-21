package autogen

import (
	"math"
	"testing"
)

// checkpointReserveGB scales with the resolved checkpoint count: nil => the
// llama default (32), an explicit value is used verbatim, 0 disables, and a
// model with no VRAM-resident KV reserves nothing.
func TestCheckpointReserveGB(t *testing.T) {
	const ptg, kcg = 0.00002, 0.05
	per := kcg + ptg*float64(checkpointMinStep)

	// Large ctx ceiling: the count cap never binds, so the resolved count is used.
	const bigCtx = 1 << 20

	zero := 0
	if g := checkpointReserveGB(profile{CtxCheckpoints: &zero}, ptg, kcg, bigCtx); g != 0 {
		t.Errorf("disabled reserve=%v want 0", g)
	}
	// nil count => the arch-aware default; kcg>0 here (SWA/SSM) => 6.
	def := defaultCtxCheckpoints(kcg)
	if g := checkpointReserveGB(profile{}, ptg, kcg, bigCtx); math.Abs(g-float64(def)*per) > 1e-9 {
		t.Errorf("nil reserve=%v want %v (default %d checkpoints)", g, float64(def)*per, def)
	}
	eight := 8
	if g := checkpointReserveGB(profile{CtxCheckpoints: &eight}, ptg, kcg, bigCtx); math.Abs(g-8*per) > 1e-9 {
		t.Errorf("n=8 reserve=%v want %v", g, 8*per)
	}
	if g := checkpointReserveGB(profile{}, 0, 0, bigCtx); g != 0 {
		t.Errorf("no-kv reserve=%v want 0", g)
	}

	// Small ctx caps the count: a 4k ctx at min-step 256 holds at most 16
	// checkpoints, so an explicit 32 is clamped to 16.
	n32 := 32
	wantN := 4096 / checkpointMinStep
	if g := checkpointReserveGB(profile{CtxCheckpoints: &n32}, ptg, kcg, 4096); math.Abs(g-float64(wantN)*per) > 1e-9 {
		t.Errorf("ctx-capped reserve=%v want %v (%d checkpoints)", g, float64(wantN)*per, wantN)
	}
	// Ctx smaller than one min-step holds no checkpoints.
	if g := checkpointReserveGB(profile{CtxCheckpoints: &n32}, ptg, kcg, 100); g != 0 {
		t.Errorf("sub-step reserve=%v want 0", g)
	}
}

// sizeProfile must subtract the checkpoint VRAM from the budget: with the
// default 32 checkpoints enabled, a KV-limited dense model gets a smaller ctx
// than the same model with checkpoints disabled.
func TestSizeProfile_CheckpointsShrinkCtx(t *testing.T) {
	meta := Metadata{Architecture: "llama", BlockCount: 32, HeadCountKv: 8, KeyLength: 128, ValueLength: 128, FileSizeGB: 4}
	s := Settings{MaxRamGB: 32, DenseCtxLadder: []int{131072, 65536, 32768}, DenseMinCtx: 4096}
	const ptg, kcg = 0.00002, 0.0

	zero := 0
	many := 32
	off := profile{Name: "x", Target: 5, CtxCheckpoints: &zero}
	on := profile{Name: "x", Target: 5, CtxCheckpoints: &many}

	ctxOff, _, _, err := sizeProfile(meta, s, off, ptg, kcg, 131072, false)
	if err != nil {
		t.Fatal(err)
	}
	ctxOn, _, _, err := sizeProfile(meta, s, on, ptg, kcg, 131072, false)
	if err != nil {
		t.Fatal(err)
	}
	if !(ctxOn < ctxOff) {
		t.Errorf("ctx with checkpoints=%d should be < without=%d", ctxOn, ctxOff)
	}
}

// On a dense model that can't fit whole in VRAM, CPU-offloaded layers keep their
// KV (and checkpoint snapshots) in RAM. sizeProfile must fold the checkpoint
// reserve into the per-layer KV reserve so densePlacement splits it across the
// GPU/CPU layers, instead of charging it whole to VRAM up front (the old
// behaviour drove -ngl toward 0). The returned kvReserve stays the pure KV cost
// (checkpoints are reported separately), and the placement keeps more layers on
// the GPU than the whole-VRAM reservation would.
func TestSizeProfile_CheckpointsSplitAtPartialOffload(t *testing.T) {
	meta := Metadata{Architecture: "llama", BlockCount: 32, HeadCountKv: 8, KeyLength: 128, ValueLength: 128, FileSizeGB: 20}
	s := Settings{MaxRamGB: 64, DenseCtxLadder: []int{131072, 65536, 32768}, DenseMinCtx: 4096}
	const ptg, kcg = 0.0001, 0.2
	prof := profile{Name: "x", Target: 8, Overhead: 0.5} // tight budget -> partial offload

	ctx, plan, kvReserve, err := sizeProfile(meta, s, prof, ptg, kcg, 131072, false)
	if err != nil {
		t.Fatal(err)
	}

	// Reported kvReserve excludes the checkpoint cost (folded only into placement).
	if want := KvReserveGB(ctx, ptg, kcg); math.Abs(kvReserve-want) > 1e-9 {
		t.Errorf("kvReserve=%v want pure KV %v (checkpoints must not bloat the reported reserve)", kvReserve, want)
	}

	// Old behaviour: checkpoint charged whole to VRAM overhead.
	ckpt := checkpointReserveGB(prof, ptg, kcg, min(131072, ctx))
	oldPlan, err := GetLoadPlan(meta, planOpt(prof.Target, s.MaxRamGB, kvReserve, prof.Overhead+ckpt))
	if err != nil {
		t.Fatal(err)
	}
	if !(plan.Ngl > oldPlan.Ngl) {
		t.Errorf("split placement ngl=%d should exceed whole-VRAM ngl=%d (ckpt=%.2fGB)", plan.Ngl, oldPlan.Ngl, ckpt)
	}
}
