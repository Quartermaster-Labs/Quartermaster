package autogen

import (
	"fmt"
	"strings"
)

// imageArchs are general.architecture values that mark an all-in-one diffusion
// (image-generation) GGUF, served by sd-server instead of llama-server.
//
// ponytail: list unverified against real files — extend it when you put an image
// GGUF on disk. An undetected one still emits as a (broken) llama model whose
// YAML "# arch=<x>" comment names the arch to add here.
var imageArchs = map[string]bool{
	"flux":             true,
	"sd1":              true,
	"sd3":              true,
	"sdxl":             true,
	"stable-diffusion": true,
	"qwen_image":       true,
	"z_image":          true,
	"lumina":           true, // Lumina2 / Lumina-Next; Z-Image-Turbo reports "lumina2"
	"wan":              true,
}

// isImageArch reports whether arch identifies a diffusion model. Matches exact
// names plus a prefix fallback so versioned archs ("flux.1", "sd3.5") still hit.
func isImageArch(arch string) bool {
	a := strings.ToLower(strings.TrimSpace(arch))
	if a == "" {
		return false
	}
	if imageArchs[a] {
		return true
	}
	for k := range imageArchs {
		if strings.HasPrefix(a, k) {
			return true
		}
	}
	return false
}

// imageComputeOverheadGB approximates the non-weight VRAM a generation needs on
// top of the resident diffusion weights: activations plus the VAE decode buffer
// (decoding a ~1024px latent is the peak). It is what decides whether the
// weights fit alongside compute or have to be offloaded.
//
// ponytail: flat estimate, not a per-resolution model — the real peak scales
// with image size and step count. Raise it (or set a per-model vramTargetGB)
// if generations OOM; it only needs to be in the right GB ballpark to flip the
// offload decision, since --max-vram does the fine-grained fitting in sd.cpp.
const imageComputeOverheadGB = 1.5

// emitImageModel writes an sd-server YAML entry for a diffusion GGUF. The
// capabilities in:[text] out:[image] block is what makes /v1/models report
// image_generation=true, so the UI's Image tab and playground list the model.
//
// Placement is auto-estimated like the LLM sizer instead of hand-tuned per
// model: the budget is TargetVramGB-VramOverheadGB (or a per-model vramTargetGB
// override), and if the diffusion weights plus a compute pad don't fit it, the
// weights are offloaded to RAM (--offload-to-cpu, paged to VRAM on use) and the
// external text encoder is parked on CPU (--backend te=cpu — it runs once per
// generation, so it is the cheapest component to keep off the GPU). --max-vram
// is always passed as the streaming budget; sd.cpp graph-cuts to fit it.
// --diffusion-fa and --vae-tiling are near-free VRAM savers, always on.
//
// Flags are grounded in sd-server --help (stable-diffusion.cpp f440ad9): note
// the listen flags are -l / --listen-port (NOT --host/--port), and the readiness
// probe is "/" (the embedded webui) since sd-server has no /health.
func emitImageModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name, arch string, emitted *[]string) {
	modelPath := strings.ReplaceAll(row.FullPath, "\\", "/")

	// Budget mirrors the LLM sizer: target minus headroom. A per-model
	// vramTargetGB override replaces it wholesale (manual escape hatch).
	budget := s.TargetVramGB - s.VramOverheadGB
	threads := s.Threads
	if ov != nil {
		if ov.VramTargetGB > 0 {
			budget = ov.VramTargetGB
		}
		if ov.Threads > 0 {
			threads = ov.Threads
		}
	}
	if budget < 1 {
		budget = 1
	}

	// Offload when resident weights + compute peak can't fit the budget. The
	// VAE/encoder are external (not in row.SizeGB); te=cpu covers the encoder,
	// --offload-to-cpu pages the diffusion weights.
	offload := row.SizeGB+imageComputeOverheadGB > budget

	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (image model, sd-server, max-vram=%gGB, offload=%t)\n", arch, row.SizeGB, budget, offload)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	// GGUF diffusion quants are standalone diffusion weights, so they load via
	// --diffusion-model (not -m, which expects an all-in-one checkpoint). The VAE
	// and text encoder are separate files — supply them through extraArgs
	// (--vae ... --llm ... / --clip_l / --t5xxl), since their paths/names vary per
	// model family and can't be discovered.
	lines := []string{
		s.SdServerExe,
		fmt.Sprintf("--diffusion-model %s", modelPath),
		"-l 127.0.0.1",
		"--listen-port ${PORT}",
		fmt.Sprintf("--max-vram %g", budget),
		"--diffusion-fa",
		// --vae-tiling is load-bearing, not just a quality knob: it caps the VAE
		// decode VRAM spike. Decoding a full latent whole OOMs/hangs intermittently
		// on a tight (8GB) card, so keep it always on. Quality is steps/cfg, not this.
		"--vae-tiling",
		fmt.Sprintf("-t %d", threads),
	}
	// A diffusion-only GGUF (loaded via --diffusion-model) always has an external
	// text encoder + VAE supplied through extraArgs (--llm/--clip/--t5/--vae). Their
	// VRAM is NOT in row.SizeGB, so the offload estimate above can't see it. Park the
	// encoder on CPU unconditionally: it runs once per generation (cheapest component
	// to keep off the GPU), and leaving it resident lets a "fits" diffusion model
	// thrash the whole graph through --max-vram. This is the te=cpu the override used
	// to hand-set in extraArgs — keep it OUT of extraArgs now to avoid a dup --backend.
	lines = append(lines, "--backend te=cpu")
	if offload {
		lines = append(lines, "--offload-to-cpu")
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	for _, line := range lines {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// sd-server has no /health; the webui root returns 200 once loaded.
	b.WriteString("    checkEndpoint: /\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	if ov != nil && len(ov.Aliases) > 0 {
		b.WriteString("    aliases:\n")
		for _, al := range ov.Aliases {
			fmt.Fprintf(b, "      - %q\n", al)
		}
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      in: [text]\n")
	b.WriteString("      out: [image]\n")
	*emitted = append(*emitted, name)
}
