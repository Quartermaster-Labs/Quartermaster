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
	// No .gguf model -> left alone (sd-server / custom cmd).
	args := []string{"--model", "/models/flux.safetensors", "-ngl", "10"}
	out, err := LiveOffloadArgs(Settings{}, args, 4, true, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(out, args) {
		t.Fatalf("expected unchanged, got %v", out)
	}
}
