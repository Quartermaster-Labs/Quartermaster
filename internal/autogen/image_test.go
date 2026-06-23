package autogen

import (
	"strings"
	"testing"
)

func TestIsImageArch(t *testing.T) {
	for _, a := range []string{"flux", "FLUX", "flux.1", "sd3", "sd3.5", "qwen_image", "z_image", " stable-diffusion "} {
		if !isImageArch(a) {
			t.Errorf("isImageArch(%q) = false, want true", a)
		}
	}
	for _, a := range []string{"", "qwen3moe", "llama", "gemma4"} {
		if isImageArch(a) {
			t.Errorf("isImageArch(%q) = true, want false", a)
		}
	}
}

func TestEmitImageModel(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 7, VramOverheadGB: 0.5, Threads: 7}

	// Big model (6.5GB + 1.5 compute > 6.5 budget) → offload path.
	big := GgufRow{FullPath: `C:\models\flux.gguf`, SizeGB: 6.5}
	emitImageModel(&b, s, big, &Override{}, "flux-q4", "flux", &emitted)
	out := b.String()
	for _, want := range []string{"sd-server", "--diffusion-model C:/models/flux.gguf", "--listen-port ${PORT}", "--max-vram 6.5", "--diffusion-fa", "--vae-tiling", "--offload-to-cpu", "--backend te=cpu", "offload=true", "checkEndpoint: /", "out: [image]", "in: [text]", "ttl: 600"} {
		if !strings.Contains(out, want) {
			t.Errorf("emit missing %q:\n%s", want, out)
		}
	}
	if len(emitted) != 1 || emitted[0] != "flux-q4" {
		t.Errorf("emitted = %v, want [flux-q4]", emitted)
	}

	// Small model (2GB + 1.5 < 6.5 budget) → fits resident, no offload flags.
	var b2 strings.Builder
	var em2 []string
	small := GgufRow{FullPath: `C:\models\sd15.gguf`, SizeGB: 2.0}
	emitImageModel(&b2, s, small, &Override{}, "sd15-q4", "sd1", &em2)
	out2 := b2.String()
	if strings.Contains(out2, "--offload-to-cpu") || strings.Contains(out2, "offload=true") {
		t.Errorf("small model should not offload:\n%s", out2)
	}
	// vae-tiling is always on (caps the VAE decode VRAM spike), even when resident.
	if !strings.Contains(out2, "--vae-tiling") {
		t.Errorf("small model should still vae-tile:\n%s", out2)
	}
	// te=cpu is unconditional (external encoder always parked on CPU), even when the
	// diffusion weights fit resident.
	if !strings.Contains(out2, "--backend te=cpu") {
		t.Errorf("small model should still set te=cpu:\n%s", out2)
	}
}
