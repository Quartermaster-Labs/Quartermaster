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
// imageArg normalizes a component path to forward slashes and quotes it when it
// contains spaces, so it survives the whitespace-split command rendering.
func imageArg(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return ""
	}
	if strings.ContainsAny(p, " \t") {
		return fmt.Sprintf("%q", p)
	}
	return p
}

// imageCmdLines builds the sd-server argv (exe first) for a diffusion gguf,
// applying the per-model Override knobs. Shared by emitImageModel (YAML emit) and
// RenderSoloCmd (editor launch-parameters preview), so the box matches a save.
// Also returns the resolved VRAM budget and offload decision for the YAML comment.
func imageCmdLines(s Settings, row GgufRow, ov *Override) (lines []string, budget float64, offload bool) {
	modelPath := strings.ReplaceAll(row.FullPath, "\\", "/")

	// Budget mirrors the LLM sizer: target minus headroom. A per-model
	// vramTargetGB override replaces it wholesale (manual escape hatch).
	budget = s.TargetVramGB - s.VramOverheadGB
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
	// --offload-to-cpu pages the diffusion weights. A per-model override pins it.
	offload = row.SizeGB+imageComputeOverheadGB > budget
	if ov != nil {
		switch ov.OffloadToCpu {
		case "on":
			offload = true
		case "off":
			offload = false
		}
	}

	// GGUF diffusion quants are standalone diffusion weights, so they load via
	// --diffusion-model (not -m, which expects an all-in-one checkpoint). The VAE
	// and text encoder are separate files supplied as component-path overrides
	// (--vae / --llm / --clip_l / --t5xxl); their names vary per family.
	lines = []string{
		s.SdServerExe,
		fmt.Sprintf("--diffusion-model %s", modelPath),
		"-l 127.0.0.1",
		"--listen-port ${PORT}",
		fmt.Sprintf("--max-vram %g", budget),
	}
	if ov != nil {
		if p := imageArg(ov.VaePath); p != "" {
			lines = append(lines, "--vae "+p)
		}
		if p := imageArg(ov.ClipLPath); p != "" {
			lines = append(lines, "--clip_l "+p)
		}
		if p := imageArg(ov.ClipGPath); p != "" {
			lines = append(lines, "--clip_g "+p)
		}
		if p := imageArg(ov.T5Path); p != "" {
			lines = append(lines, "--t5xxl "+p)
		}
		if p := imageArg(ov.TextEncoderPath); p != "" {
			lines = append(lines, "--llm "+p)
		}
	}
	// --diffusion-fa is a near-free VRAM saver, on unless turned off.
	if ov == nil || ov.DiffusionFa != "off" {
		lines = append(lines, "--diffusion-fa")
	}
	// --vae-tiling is load-bearing, not just a quality knob: it caps the VAE
	// decode VRAM spike. Decoding a full latent whole OOMs/hangs intermittently
	// on a tight (8GB) card, so keep it on by default. Quality is steps/cfg, not this.
	if ov == nil || ov.VaeTiling != "off" {
		lines = append(lines, "--vae-tiling")
	}
	lines = append(lines, fmt.Sprintf("-t %d", threads))
	// Park the text encoder on CPU by default: it runs once per generation (cheapest
	// component to keep off the GPU), and its VRAM is NOT in row.SizeGB, so leaving
	// it resident lets a "fits" diffusion model thrash the whole graph through
	// --max-vram. "off" keeps it on the GPU for a model that has the headroom.
	if ov == nil || ov.TeOnCpu != "off" {
		lines = append(lines, "--backend te=cpu")
	}
	if offload {
		lines = append(lines, "--offload-to-cpu")
		// VAE decode is a single end-of-generation VRAM spike that --vae-tiling
		// only caps, not removes. On a tight card (offload=true, ~1.5GB VRAM
		// headroom) with a token-heavy model like Kontext (reference image ~2x
		// the sequence), that spike overcommits shared VRAM and hard-hangs
		// Windows. The VAE runs once per image, so parking it on CPU is nearly
		// free and removes the crash. te=cpu only moves the text encoders.
		lines = append(lines, "--vae-on-cpu")
	}
	// Generation defaults applied when a request omits them.
	if ov != nil {
		if ov.DefaultSteps > 0 {
			lines = append(lines, fmt.Sprintf("--steps %d", ov.DefaultSteps))
		}
		if ov.DefaultCfg > 0 {
			lines = append(lines, fmt.Sprintf("--cfg-scale %g", ov.DefaultCfg))
		}
		if ov.DefaultSampler != "" {
			lines = append(lines, "--sampling-method "+ov.DefaultSampler)
		}
		if ov.DefaultWidth > 0 {
			lines = append(lines, fmt.Sprintf("--width %d", ov.DefaultWidth))
		}
		if ov.DefaultHeight > 0 {
			lines = append(lines, fmt.Sprintf("--height %d", ov.DefaultHeight))
		}
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines, budget, offload
}

// mergeImageVariant overlays an image variant onto its base override: the
// variant inherits every base field it leaves empty (component paths, placement)
// and overrides only what it sets. Unlisted is the variant's own (a variant is a
// distinct served id). Variants is cleared to avoid re-emitting.
func mergeImageVariant(base Override, v VariantSpec) Override {
	o := base
	o.Variants = nil
	o.Unlisted = v.Unlisted
	if v.VaePath != "" {
		o.VaePath = v.VaePath
	}
	if v.ClipLPath != "" {
		o.ClipLPath = v.ClipLPath
	}
	if v.ClipGPath != "" {
		o.ClipGPath = v.ClipGPath
	}
	if v.T5Path != "" {
		o.T5Path = v.T5Path
	}
	if v.TextEncoderPath != "" {
		o.TextEncoderPath = v.TextEncoderPath
	}
	if v.OffloadToCpu != "" {
		o.OffloadToCpu = v.OffloadToCpu
	}
	if v.TeOnCpu != "" {
		o.TeOnCpu = v.TeOnCpu
	}
	if v.VaeTiling != "" {
		o.VaeTiling = v.VaeTiling
	}
	if v.DiffusionFa != "" {
		o.DiffusionFa = v.DiffusionFa
	}
	if v.VramTargetGB > 0 {
		o.VramTargetGB = v.VramTargetGB
	}
	if v.Threads > 0 {
		o.Threads = v.Threads
	}
	if v.DefaultSteps > 0 {
		o.DefaultSteps = v.DefaultSteps
	}
	if v.DefaultCfg > 0 {
		o.DefaultCfg = v.DefaultCfg
	}
	if v.DefaultSampler != "" {
		o.DefaultSampler = v.DefaultSampler
	}
	if v.DefaultWidth > 0 {
		o.DefaultWidth = v.DefaultWidth
	}
	if v.DefaultHeight > 0 {
		o.DefaultHeight = v.DefaultHeight
	}
	if v.ExtraArgs != "" {
		o.ExtraArgs = v.ExtraArgs
	}
	return o
}

func emitImageModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name, arch string, emitted *[]string) {
	lines, budget, offload := imageCmdLines(s, row, ov)

	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (image model, sd-server, max-vram=%gGB, offload=%t)\n", arch, row.SizeGB, budget, offload)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range lines {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// sd-server has no /health; the webui root returns 200 once loaded.
	b.WriteString("    checkEndpoint: /\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      in: [text]\n")
	b.WriteString("      out: [image]\n")
	*emitted = append(*emitted, name)
}
