package autogen

import "testing"

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
