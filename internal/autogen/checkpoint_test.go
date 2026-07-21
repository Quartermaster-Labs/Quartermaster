package autogen

import (
	"math"
	"testing"
)

// Recurrent/hybrid models (FullAttnInterval>0) get 2 (upstream #21831 restore
// fixed, verified on qwen3.6-27b); SWA (kvConst>0, non-recurrent) and plain
// attention both get 3. SWA buys its rewind coverage back with a wider spacing
// instead of more (expensive) snapshots.
func TestDefaultCtxCheckpoints_Recurrent(t *testing.T) {
	if n := defaultCtxCheckpoints(0.05, true); n != 2 {
		t.Errorf("recurrent checkpoints = %d, want 2", n)
	}
	if n := defaultCtxCheckpoints(0.05, false); n != 3 {
		t.Errorf("SWA default got %d want 3", n)
	}
	if n := defaultCtxCheckpoints(0, false); n != 3 {
		t.Errorf("plain default got %d want 3", n)
	}
}

// Spacing is arch-keyed: SWA widens to swaCheckpointMinStep so 3 snapshots still
// cover a broad rewind range; recurrent and plain attention keep llama's 256. An
// explicit pin always wins.
func TestCheckpointMinStep(t *testing.T) {
	if s := defaultCheckpointMinStep(0.05, false); s != swaCheckpointMinStep {
		t.Errorf("SWA step = %d, want %d", s, swaCheckpointMinStep)
	}
	if s := defaultCheckpointMinStep(0.05, true); s != checkpointMinStep {
		t.Errorf("recurrent step = %d, want %d", s, checkpointMinStep)
	}
	if s := defaultCheckpointMinStep(0, false); s != checkpointMinStep {
		t.Errorf("plain step = %d, want %d", s, checkpointMinStep)
	}
	if s := effectiveCheckpointMinStep(profile{CheckpointMinStep: 512}, 0.05, false); s != 512 {
		t.Errorf("pinned step = %d, want 512", s)
	}
}

// checkpointReserveGB scales with the resolved checkpoint count: nil => the
// llama default (32), an explicit value is used verbatim, 0 disables, and a
// model with no VRAM-resident KV reserves nothing.
func TestCheckpointReserveGB(t *testing.T) {
	// kcg>0 with recurrent=false is SWA, so the charged spacing is the wide one.
	const ptg, kcg = 0.00002, 0.05
	per := kcg + ptg*float64(swaCheckpointMinStep)

	// Large ctx ceiling: the count cap never binds, so the resolved count is used.
	const bigCtx = 1 << 20

	zero := 0
	if g := checkpointReserveGB(profile{CtxCheckpoints: &zero}, ptg, kcg, bigCtx, false); g != 0 {
		t.Errorf("disabled reserve=%v want 0", g)
	}
	// nil count => the arch-aware default; kcg>0 here (SWA) => 3.
	def := defaultCtxCheckpoints(kcg, false)
	if g := checkpointReserveGB(profile{}, ptg, kcg, bigCtx, false); math.Abs(g-float64(def)*per) > 1e-9 {
		t.Errorf("nil reserve=%v want %v (default %d checkpoints)", g, float64(def)*per, def)
	}
	eight := 8
	if g := checkpointReserveGB(profile{CtxCheckpoints: &eight}, ptg, kcg, bigCtx, false); math.Abs(g-8*per) > 1e-9 {
		t.Errorf("n=8 reserve=%v want %v", g, 8*per)
	}
	if g := checkpointReserveGB(profile{}, 0, 0, bigCtx, false); g != 0 {
		t.Errorf("no-kv reserve=%v want 0", g)
	}

	// Small ctx caps the count: a 4k ctx at the SWA min-step holds at most
	// 4096/step checkpoints, so an explicit 32 is clamped to that.
	n32 := 32
	wantN := 4096 / swaCheckpointMinStep
	if g := checkpointReserveGB(profile{CtxCheckpoints: &n32}, ptg, kcg, 4096, false); math.Abs(g-float64(wantN)*per) > 1e-9 {
		t.Errorf("ctx-capped reserve=%v want %v (%d checkpoints)", g, float64(wantN)*per, wantN)
	}
	// Ctx smaller than one min-step holds no checkpoints.
	if g := checkpointReserveGB(profile{CtxCheckpoints: &n32}, ptg, kcg, 100, false); g != 0 {
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
	ckpt := checkpointReserveGB(prof, ptg, kcg, min(131072, ctx), false)
	oldPlan, err := GetLoadPlan(meta, planOpt(prof.Target, s.MaxRamGB, kvReserve, prof.Overhead+ckpt))
	if err != nil {
		t.Fatal(err)
	}
	if !(plan.Ngl > oldPlan.Ngl) {
		t.Errorf("split placement ngl=%d should exceed whole-VRAM ngl=%d (ckpt=%.2fGB)", plan.Ngl, oldPlan.Ngl, ckpt)
	}
}
