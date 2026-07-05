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

// effectiveImageArch returns the arch to classify a gguf as a diffusion model:
// the declared general.architecture when it is a known image arch, else the
// tensor-name-sniffed DiffusionKind ("sdxl"/"sd1"). stable-diffusion.cpp's
// `convert` strips metadata KVs, so a converted SDXL UNet reports arch="" but
// its tensor names still identify it. Falls back to the declared arch (so the
// llama-path "# arch=..." comment still names an unknown arch).
func effectiveImageArch(meta Metadata) string {
	if isImageArch(meta.Architecture) {
		return meta.Architecture
	}
	if meta.DiffusionKind != "" {
		return meta.DiffusionKind
	}
	return meta.Architecture
}

// fullCheckpointArchs are diffusion families that stable-diffusion.cpp can only
// load (and version-detect) as an all-in-one checkpoint via -m, NOT as a bare
// --diffusion-model UNet plus external CLIPs. sd.cpp fixes the model version
// right after the UNet loads but before the CLIPs (stable-diffusion.cpp ~L650),
// and SDXL/SD is only recognized when the UNet and its 2nd text encoder are seen
// together — so a split load never detects them and dies "get sd version failed".
// Flux/SD3/Qwen/Z-Image self-identify from the UNet alone, so they keep the split
// path (external encoders wired from the pool).
var fullCheckpointArchs = map[string]bool{
	"sd1":              true,
	"sd2":              true,
	"sdxl":             true,
	"stable-diffusion": true,
}

// fluxGuidance returns the distilled guidance scale (--guidance) BFL recommends
// for a flux edit model, matched by name. ok=false means "leave the built-in
// 3.5" (plain flux-dev, schnell, chroma — none need an override).
func fluxGuidance(name string) (float64, bool) {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "kontext"):
		return 2.5, true
	case strings.Contains(n, "fill"):
		return 30, true
	}
	return 0, false
}

func isFullCheckpointArch(arch string) bool {
	a := strings.ToLower(strings.TrimSpace(arch))
	if fullCheckpointArchs[a] {
		return true
	}
	for k := range fullCheckpointArchs {
		if strings.HasPrefix(a, k) {
			return true
		}
	}
	return false
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

// imageComponents are the resolved VAE / text-encoder file paths for one
// diffusion model. Empty = not attached (the family doesn't need it, or it's
// missing from the pool — see resolveComponents' missing list).
type imageComponents struct{ vae, clipL, clipG, t5, llm string }

// resolveComponents decides which component files a bare diffusion GGUF needs
// from its architecture, draws them from the shared Settings.Encoders pool, then
// lets an explicit per-model Override path win. It returns the resolved paths
// plus the role names an arch REQUIRES that neither pool nor override supplied
// (surfaced as a WARNING so a misconfigured model is visible, not silently broken).
//
// name disambiguates families an arch tag can't: Chroma reports arch "flux" but
// removes the CLIP encoder (T5 only). SDXL/SD1 GGUFs are typically full
// checkpoints (CLIP+VAE baked), so their VAE is optional (wired only if declared),
// never required.
//
// ponytail: sd3 / qwen_image arms omitted — no such model on disk yet. Add an arm
// (and any new EncoderSet field) when one lands; the "# arch=..." YAML comment on
// the fallthrough names the arch to wire.
func resolveComponents(enc EncoderSet, ov *Override, arch, name string) (c imageComponents, missing []string) {
	a := strings.ToLower(strings.TrimSpace(arch))
	n := strings.ToLower(name)
	req := func(role, path string) string {
		if path == "" {
			missing = append(missing, role)
		}
		return path
	}
	switch {
	case strings.Contains(n, "chroma"): // flux-derived, CLIP stripped → T5 only
		c.vae = req("vae", enc.FluxVae)
		c.t5 = req("t5xxl", enc.T5)
	case a == "flux" || strings.HasPrefix(a, "flux"):
		c.vae = req("vae", enc.FluxVae)
		c.clipL = req("clip_l", enc.ClipL)
		c.t5 = req("t5xxl", enc.T5)
	// SD1/SD2/SDXL are NOT handled here: they load as -m full checkpoints
	// (isFullCheckpointArch), so imageCmdLines never calls resolveComponents for
	// them — sd.cpp can't version-detect a split SDXL (bare UNet + external CLIPs).
	case strings.HasPrefix(a, "lumina") || a == "z_image" || strings.Contains(n, "z-image"):
		c.vae = req("vae", enc.ZimageVae)
		c.llm = req("llm", enc.QwenLlm)
	}
	// Explicit per-model override wins over the arch-wired pool default, and clears
	// the role from the missing list (an override can supply what the pool lacks).
	if ov != nil {
		set := func(role, ovPath string, dst *string) {
			if ovPath == "" {
				return
			}
			*dst = ovPath
			for i, m := range missing {
				if m == role {
					missing = append(missing[:i], missing[i+1:]...)
					break
				}
			}
		}
		set("vae", ov.VaePath, &c.vae)
		set("clip_l", ov.ClipLPath, &c.clipL)
		set("clip_g", ov.ClipGPath, &c.clipG)
		set("t5xxl", ov.T5Path, &c.t5)
		set("llm", ov.TextEncoderPath, &c.llm)
	}
	return c, missing
}

// imageCmdLines builds the sd-server argv (exe first) for a diffusion gguf,
// applying the per-model Override knobs plus the arch-wired component pool. Shared
// by emitImageModel (YAML emit) and RenderSoloCmd (editor launch-parameters
// preview), so the box matches a save. Also returns the resolved VRAM budget and
// offload decision for the YAML comment, and any required-but-missing encoder roles.
func imageCmdLines(s Settings, row GgufRow, ov *Override, arch, name string) (lines []string, budget float64, offload bool, missing []string) {
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

	// Most diffusion GGUFs are standalone diffusion weights loaded via
	// --diffusion-model, with VAE/text-encoder supplied as separate component
	// files (--vae / --llm / --clip_l / --t5xxl). SD1/SD2/SDXL are the exception:
	// sd.cpp can't version-detect them split, so they must be an all-in-one
	// checkpoint loaded via -m (encoders + VAE baked in, none wired externally).
	fullCkpt := isFullCheckpointArch(arch)
	modelFlag := "--diffusion-model"
	if fullCkpt {
		modelFlag = "-m"
	}
	lines = []string{
		s.SdServerExe,
		fmt.Sprintf("%s %s", modelFlag, modelPath),
		"-l 127.0.0.1",
		"--listen-port ${PORT}",
		fmt.Sprintf("--max-vram %g", budget),
	}
	// Full-checkpoint archs bake their encoders/VAE — wire nothing external.
	// Split archs draw component files from the shared pool (per-model override wins).
	var comp imageComponents
	if !fullCkpt {
		comp, missing = resolveComponents(s.Encoders, ov, arch, name)
	}
	if p := imageArg(comp.vae); p != "" {
		lines = append(lines, "--vae "+p)
	}
	if p := imageArg(comp.clipL); p != "" {
		lines = append(lines, "--clip_l "+p)
	}
	if p := imageArg(comp.clipG); p != "" {
		lines = append(lines, "--clip_g "+p)
	}
	if p := imageArg(comp.t5); p != "" {
		lines = append(lines, "--t5xxl "+p)
	}
	if p := imageArg(comp.llm); p != "" {
		lines = append(lines, "--llm "+p)
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
	// Distilled guidance for flux edit models. The /sdapi route only reads
	// cfg_scale (→ guidance.txt_cfg); it has NO per-request key for distilled
	// guidance, so it stays at whatever the server launched with. Bake BFL's
	// per-model default here: Kontext=2.5, Fill=30 (plain flux-dev keeps the
	// built-in 3.5; schnell/chroma are guidance-free → nothing emitted).
	if g, ok := fluxGuidance(name); ok {
		lines = append(lines, fmt.Sprintf("--guidance %g", g))
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
	return lines, budget, offload, missing
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

func emitImageModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name, arch string, bakedEnc bool, emitted *[]string) {
	lines, budget, offload, missing := imageCmdLines(s, row, ov, arch, name)

	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (image model, sd-server, max-vram=%gGB, offload=%t)\n", arch, row.SizeGB, budget, offload)
	// SD/SDXL served as -m full checkpoints: if this gguf has no baked encoders it
	// is a bare UNet, which sd.cpp cannot load standalone (no split path for SDXL).
	if isFullCheckpointArch(arch) && !bakedEnc {
		fmt.Fprintf(b, "  # WARNING: %s looks UNet-only (no baked text encoder) — sd.cpp can't load SD/SDXL split; supply an all-in-one checkpoint\n", name)
	}
	if len(missing) > 0 {
		fmt.Fprintf(b, "  # WARNING: %s needs encoder(s) [%s] that aren't in settings.encoders — generation will fail until declared\n", name, strings.Join(missing, ", "))
	}
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
