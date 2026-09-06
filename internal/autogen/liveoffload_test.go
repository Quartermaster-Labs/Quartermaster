package autogen

import (
	"reflect"
	"testing"
)

func TestLiveOffload_Parsing(t *testing.T) {
	args := []string{
		"-m", "/models/qwen.gguf", "-ngl", "99", "-c", "8192",
		"-ctk", "q8_0", "-ctv", "q8_0", "--no-kv-offload",
		"--spec-type", "draft-mtp", "--ctx-checkpoints", "0",
		"--n-cpu-moe", "12",
	}

	if v, _ := argVal(args, "-m", "--model"); v != "/models/qwen.gguf" {
		t.Fatalf("model = %q", v)
	}
	if got := atoiFlag(args, "-c", "--ctx-size"); got != 8192 {
		t.Fatalf("ctx = %d", got)
	}
	if got := flagStr(args, "-ctk", "--cache-type-k"); got != "q8_0" {
		t.Fatalf("ctk = %q", got)
	}
	if !hasFlag(args, "--no-kv-offload") {
		t.Fatal("expected --no-kv-offload detected")
	}
	if got := specTypes(args); got != "draft-mtp" {
		t.Fatalf("spec = %q", got)
	}
	if n, ok := atoiFlagOK(args, "--ctx-checkpoints"); !ok || n != 0 {
		t.Fatalf("ctx-checkpoints = %d ok=%v", n, ok)
	}
	if _, idx := argVal(args, "--missing"); idx != -1 {
		t.Fatalf("absent flag idx = %d", idx)
	}
}

func TestLiveOffload_Rewrite(t *testing.T) {
	// MoE: --n-cpu-moe present -> bumped in place, -ngl untouched value rewritten.
	moe := []string{"-m", "x.gguf", "-ngl", "99", "--n-cpu-moe", "12"}
	_, nglIdx := argVal(moe, "-ngl")
	_, ncIdx := argVal(moe, "--n-cpu-moe")
	got := rewriteOffload(moe, nglIdx, ncIdx, 99, 40)
	want := []string{"-m", "x.gguf", "-ngl", "99", "--n-cpu-moe", "40"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("moe bump: got %v want %v", got, want)
	}

	// Dense: no --n-cpu-moe -> -ngl reduced, no flag appended (newNcpu 0).
	dense := []string{"-m", "x.gguf", "-ngl", "32"}
	_, nglIdx = argVal(dense, "-ngl")
	got = rewriteOffload(dense, nglIdx, -1, 10, 0)
	want = []string{"-m", "x.gguf", "-ngl", "10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dense reduce: got %v want %v", got, want)
	}

	// MoE without baked flag -> append --n-cpu-moe.
	bare := []string{"-m", "x.gguf", "-ngl", "99"}
	_, nglIdx = argVal(bare, "-ngl")
	got = rewriteOffload(bare, nglIdx, -1, 99, 8)
	want = []string{"-m", "x.gguf", "-ngl", "99", "--n-cpu-moe", "8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("moe append: got %v want %v", got, want)
	}

	// rewriteOffload must not mutate the input slice.
	if !reflect.DeepEqual(bare, []string{"-m", "x.gguf", "-ngl", "99"}) {
		t.Fatalf("input mutated: %v", bare)
	}
}

func TestLiveOffload_NoGpuReadingIsNoop(t *testing.T) {
	args := []string{"-m", "x.gguf", "-ngl", "99", "--n-cpu-moe", "12"}
	out, err := LiveOffloadArgs(Settings{}, args, 0, false, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(out, args) {
		t.Fatalf("expected unchanged, got %v", out)
	}
}

func TestLiveOffload_NonLlamaCmdIsNoop(t *testing.T) {
	// A custom cmd with no --max-vram and no .gguf model -> left alone.
	args := []string{"--model", "/models/flux.safetensors", "-ngl", "10"}
	out, err := LiveOffloadArgs(Settings{}, args, 4, true, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(out, args) {
		t.Fatalf("expected unchanged, got %v", out)
	}
}

func TestLiveOffload_SdMaxVram(t *testing.T) {
	base := []string{"sd-server", "--diffusion-model", "flux.gguf", "--max-vram", "6.5", "--vae-tiling"}

	// Tight live VRAM: 5.5 free - 1.0 headroom = 4.5 < 6.5 baked -> tighten.
	out, err := LiveOffloadArgs(Settings{}, base, 5.5, true, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if v, _ := argVal(out, "--max-vram"); v != "4.5" {
		t.Errorf("--max-vram = %q, want 4.5", v)
	}

	// Ample live VRAM: 8.0 - 1.0 = 7.0 >= 6.5 baked -> leave the baked budget.
	out, err = LiveOffloadArgs(Settings{}, base, 8.0, true, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(out, base) {
		t.Errorf("ample VRAM should not loosen baked budget, got %v", out)
	}

	// Barely any free VRAM: clamps to the floor, never negative, never refused.
	out, _ = LiveOffloadArgs(Settings{}, base, 1.2, true, nil)
	if v, _ := argVal(out, "--max-vram"); v != "0.5" {
		t.Errorf("--max-vram = %q, want floor 0.5", v)
	}

	// No GPU reading -> baked plan trusted.
	out, _ = LiveOffloadArgs(Settings{}, base, 0, false, nil)
	if !reflect.DeepEqual(out, base) {
		t.Errorf("no reading should be a noop, got %v", out)
	}
}

func TestLiveOffload_BudgetCap(t *testing.T) {
	// Target below live free binds: the spawn snapshot is not a safe budget on a
	// desktop, where dwm/Discord/a VR runtime grow into VRAM after the load.
	if got := liveBudgetGB(Settings{TargetVramGB: 20.5}, 22.5); got != 20.5 {
		t.Fatalf("target should cap free: got %v want 20.5", got)
	}
	// Target above what the card actually has left must not talk us into an OOM.
	if got := liveBudgetGB(Settings{TargetVramGB: 22.8}, 18.0); got != 18.0 {
		t.Fatalf("free should win when tighter: got %v want 18.0", got)
	}
	// Unset target leaves live free as the only bound (hand-written config).
	if got := liveBudgetGB(Settings{}, 12.0); got != 12.0 {
		t.Fatalf("unset target: got %v want 12.0", got)
	}
}

// The whole point of the ctx trim: when the live budget lands a hair under what
// the baked plan assumed, the sizer's answer is to spill a layer (a dense
// model's weights AND its KV to RAM, crossed every token) to buy back a few
// tenths of a GB. Giving up a block of context buys the same VRAM and costs
// only window, so trimCtxForPlacement has to find a smaller ctx that keeps the
// baked placement rather than letting the placement rewrite happen.
func TestLiveOffload_TrimCtxKeepsBakedPlacement(t *testing.T) {
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
	in := EstimateInput{Ctx: 32768, KvK: "q8_0", KvV: "q8_0", TargetVramGB: 24}

	baked, err := EstimatePlan(s, meta, in)
	if err != nil {
		t.Fatal(err)
	}
	if baked.Ngl < int(meta.BlockCount) {
		t.Fatalf("setup: expected a whole-model fit at 24GB, got ngl=%d", baked.Ngl)
	}

	// Walk the budget down until the sizer first gives up a layer. That is the
	// "64/65" the user reported, reproduced without guessing a magic number.
	var short EstimateResult
	tight := in
	for b := 23.9; b > 12; b -= 0.1 {
		tight.TargetVramGB = b
		short, err = EstimatePlan(s, meta, tight)
		if err != nil {
			t.Fatal(err)
		}
		if short.Ngl < baked.Ngl {
			break
		}
	}
	if short.Ngl >= baked.Ngl {
		t.Fatalf("setup: never lost a layer down to 12GB (ngl=%d)", short.Ngl)
	}

	ctx, trimmed, ok := trimCtxForPlacement(s, meta, tight, 1, baked.Ngl, baked.NCpuMoe)
	if !ok {
		t.Fatalf("no trim found at %.1fGB (ngl would drop %d->%d)", tight.TargetVramGB, baked.Ngl, short.Ngl)
	}
	if ctx >= in.Ctx || ctx%4096 != 0 {
		t.Fatalf("trimmed ctx = %d, want a smaller multiple of 4096", ctx)
	}
	if ctx < int(float64(in.Ctx)*ctxTrimFloorFrac) {
		t.Fatalf("trimmed ctx %d is below the %.0f%% floor", ctx, ctxTrimFloorFrac*100)
	}
	if trimmed.Ngl < baked.Ngl || trimmed.NCpuMoe > baked.NCpuMoe {
		t.Fatalf("trim did not restore the baked placement: ngl=%d ncpumoe=%d", trimmed.Ngl, trimmed.NCpuMoe)
	}
}

// The step is per-slot: with --parallel 4 the total -c has to move in 4x4096
// blocks, or each slot ends up holding a non-multiple of the sizer's block.
func TestLiveOffload_TrimStepsWholeSlots(t *testing.T) {
	if got := argSlots([]string{"-m", "x.gguf", "-np", "4"}); got != 4 {
		t.Fatalf("slots = %d want 4", got)
	}
	if got := argSlots([]string{"-m", "x.gguf"}); got != 1 {
		t.Fatalf("slots default = %d want 1", got)
	}

	args := []string{"-m", "x.gguf", "-c", "32768", "-ngl", "99"}
	_, idx := argVal(args, "-c", "--ctx-size")
	out := rewriteCtx(args, idx, 24576)
	if !reflect.DeepEqual(out, []string{"-m", "x.gguf", "-c", "24576", "-ngl", "99"}) {
		t.Fatalf("rewriteCtx: %v", out)
	}
	if args[3] != "32768" {
		t.Fatalf("rewriteCtx mutated its input: %v", args)
	}
}
