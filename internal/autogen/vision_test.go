package autogen

import (
	"os"
	"strings"
	"testing"
)

// clipComputeBufferGB models the CLIP vision compute buffer from mmproj hparams.
// Verified against the real Qwen3.6-27B mmproj-F16 (image 768 / patch 16 / embd
// 1152 / ffn 4304 / heads 16): base-tile n_patches = (768/16)^2 = 2304, KQ =
// 16*2304^2*4 ~0.34 GB dominates, total ~0.5 GB — well under the old flat 1.0 pad.
func TestClipComputeBufferGB(t *testing.T) {
	qwen := Metadata{VisionImageSize: 768, VisionPatchSize: 16, VisionEmbd: 1152, VisionFFN: 4304, VisionHeads: 16}
	got := clipComputeBufferGB(qwen)
	if got < 0.4 || got > 0.65 {
		t.Errorf("Qwen3-VL mmproj: got %.3f GB, want ~0.5 GB", got)
	}

	// Quadratic in patch count: doubling the grid (halving patch_size) ~4x's the
	// dominant KQ term, so the buffer grows well past linear.
	dense := qwen
	dense.VisionPatchSize = 8 // grid 96 -> 9216 patches vs 2304
	if d := clipComputeBufferGB(dense); d < 3*got {
		t.Errorf("halving patch_size should >3x the buffer: got %.3f vs base %.3f", d, got)
	}

	// Missing vision dims => 0 (caller falls back to the flat VisionOverheadGB).
	if z := clipComputeBufferGB(Metadata{}); z != 0 {
		t.Errorf("no vision dims: got %.3f, want 0", z)
	}
}

// The vision twin's projector is only worth its VRAM while it costs neither
// layer placement nor a meaningful slice of the context window; otherwise the
// sizer parks the CLIP tower on the CPU (--no-mmproj-offload).
func TestCpuMmprojWins(t *testing.T) {
	full := LoadPlan{Ngl: 99}
	spilled := LoadPlan{Ngl: 60}
	moeFull := LoadPlan{Ngl: 99, NCpuMoe: 0}
	moeSpilled := LoadPlan{Ngl: 99, NCpuMoe: 12}

	tests := []struct {
		name             string
		gpuPlan, cpuPlan LoadPlan
		gpuCtx, cpuCtx   int
		want             bool
	}{
		// Placement: the projector pushed dense layers / MoE experts off the GPU.
		// That tax is per token, so the CPU encode always wins — even at equal ctx.
		{"dense layers displaced", spilled, full, 32768, 32768, true},
		{"moe experts displaced", moeSpilled, moeFull, 32768, 32768, true},
		// Window: same placement, but the projector ate more than a quarter of it.
		{"window halved", full, full, 16384, 32768, true},
		{"window barely dented", full, full, 28672, 32768, false},
		// The projector fit in the slack — keep the fast GPU encode.
		{"projector free", full, full, 32768, 32768, false},
	}
	for _, tc := range tests {
		if got := cpuMmprojWins(tc.gpuPlan, tc.gpuCtx, tc.cpuPlan, tc.cpuCtx); got != tc.want {
			t.Errorf("%s: cpuMmprojWins = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The spawn-time guard must agree with the baked plan: an argv carrying
// --no-mmproj-offload has no projector in VRAM, so re-estimating it must not
// charge one (which would offload text layers the config meant to keep on GPU).
func TestLiveOffload_NoMmprojOffloadUncharged(t *testing.T) {
	args := []string{"llama-server", "--mmproj", "C:/models/mmproj.gguf", "--no-mmproj-offload"}
	if !hasFlag(args, "--no-mmproj-offload") {
		t.Fatal("hasFlag missed --no-mmproj-offload")
	}
	if _, i := argVal(args, "--mmproj"); i < 0 {
		t.Fatal("argVal missed --mmproj")
	}
}

// The per-model projector dropdown (Override.Mmproj) pins what the auto
// fallback would otherwise decide: "ram" always emits --no-mmproj-offload,
// "gpu" never does, and "none" removes the vision twin entirely. Gated on the
// real models tree — it needs a model that actually ships a projector.
func TestAutogen_Generate_MmprojPin(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	gen := func(mode string) string {
		t.Helper()
		gf := GenerateFile{
			Settings:  Settings{ModelsRoot: realModelsRoot},
			Overrides: []Override{{Match: "*", Mmproj: mode}},
		}
		gf.Settings.applyDefaults()
		out, err := Generate(gf, "T")
		if err != nil {
			t.Fatalf("generate(%q): %v", mode, err)
		}
		return out
	}

	if auto := gen(""); !strings.Contains(auto, "-vision\":") {
		t.Skip("no model in the tree ships an mmproj; nothing to pin")
	}
	if ram := gen("ram"); !strings.Contains(ram, "--no-mmproj-offload") {
		t.Error(`mmproj "ram": expected --no-mmproj-offload on the vision twin`)
	}
	if gpu := gen("gpu"); strings.Contains(gpu, "--no-mmproj-offload") {
		t.Error(`mmproj "gpu": projector pinned to VRAM must not emit --no-mmproj-offload`)
	}
	none := gen("none")
	if strings.Contains(none, "-vision\":") {
		t.Error(`mmproj "none": expected no vision twin`)
	}
	if strings.Contains(none, "--mmproj ") {
		t.Error(`mmproj "none": expected no --mmproj flag anywhere`)
	}
}
