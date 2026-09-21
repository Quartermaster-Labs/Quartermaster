package autogen

import (
	"fmt"
	"os"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
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
	// Generic marker for a GGUF that declares general.type=diffusion but whose
	// general.architecture doesn't self-identify as image (e.g. HiDream-O1 reports
	// arch "qwen"). effectiveImageArch maps such a file to "diffusion". No
	// resolveComponents arm → nothing auto-wired; components come from an override.
	"diffusion": true,
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
	// A non-image arch that still declares general.type=diffusion (HiDream-O1
	// reports arch "qwen") — classify it image via the generic "diffusion" marker.
	if strings.EqualFold(strings.TrimSpace(meta.GeneralType), "diffusion") {
		return "diffusion"
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

// fileGB is a component file's on-disk size in GiB, or 0 when it cannot be
// stat'd. A missing file is charged nothing on purpose: the emitted config still
// has to be produced for a box whose components are not downloaded yet, and
// over-charging a path that does not exist would shrink --max-vram for a model
// that is about to be reported as broken anyway (see the missing-roles WARNING).
func fileGB(p string) float64 {
	p = strings.TrimSpace(p)
	if p == "" {
		return 0
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / (1 << 30)
}

// graphBudget is the number handed to --max-vram, which is NOT a VRAM cap.
//
// sd-server: "maximum VRAM budget in GiB for graph-cut segmented execution". It
// is the size one MERGED graph segment may reach, so a larger value fuses more
// nodes into a single allocation. Handing it the whole card is therefore not the
// safe direction, it is the dangerous one: the weights are already sitting in
// that VRAM, so sd.cpp merges against space that does not exist and the
// allocation fails at sampling, not at load (params load lazily: --eager-load is
// off by default, so a model reports "ready" seconds before it tries this).
//
// That is a 24GB card OOMing on a 2.7GiB compute buffer while nominally holding
// a 22.3GB budget, which is what LTX-2.5 (14.6GB resident + two VAEs) hit.
//
// So the budget is what is LEFT once everything we told sd-server to keep on the
// GPU is paid for:
//
//   - offload=false: the diffusion weights are resident, so they come off.
//   - offload=true: --offload-to-cpu puts them in RAM and --vae-on-cpu follows,
//     so the card is free for the graph and the whole budget stands. This is
//     also the only configuration in which --stream-layers does anything.
//   - the text encoder is on CPU by default (te=cpu) and charged only when an
//     override pins it back onto the GPU.
//
// Floored at 1 GiB rather than 0, because 0 has a meaning of its own here
// ("disables graph splitting"), which is the opposite of what a card this tight
// wants.
func graphBudget(budget float64, row GgufRow, comp imageComponents, ov *Override, offload bool) float64 {
	if offload {
		return budget
	}
	resident := row.SizeGB
	if ov == nil || ov.VaeOnCpu != "on" {
		resident += fileGB(comp.vae) + fileGB(comp.audioVae)
	}
	if ov != nil && ov.TeOnCpu == "off" {
		resident += fileGB(comp.clipL) + fileGB(comp.clipG) + fileGB(comp.t5) + fileGB(comp.llm) + fileGB(comp.llmVision)
	}
	if g := budget - resident; g >= 1 {
		// One decimal: the YAML comment prints this number too, and a raw
		// float64 subtraction renders as 7.689999999999999.
		return float64(int(g*10)) / 10
	}
	return 1
}

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

// resolveLoraDir picks the `--lora-model-dir` for one diffusion model: the
// per-model override, else settings.loraDirs[category], else the fleet-wide
// settings.loraDir, else the directory the model file itself sits in. The last
// fallback is what makes a LoRA dropped next to its base checkpoint
// (D:/LLM/Models/flux1/<lora>.safetensors) show up in /sdapi/v1/loras with no
// config at all — sd-server's default is the process cwd, which never contains
// anything useful here.
//
// category splits image from video, because sd-server takes ONE directory and a
// video LoRA and an SDXL LoRA are not interchangeable: a single fleet-wide
// folder forces whoever keeps them apart (every ComfyUI layout does) to fall
// back to per-model overrides. The per-category entry is therefore checked
// first and loraDir remains the fallback for a single-tree setup.
//
// Only the DIRECTORY is a launch flag; which LoRA (and at what strength) is
// per-request — `lora: [{path, multiplier}]` on /sdapi/v1/{txt2img,img2img},
// where path is the file's name inside this dir.
func resolveLoraDir(s Settings, ovDir, category, modelPath string) string {
	if d := strings.TrimSpace(ovDir); d != "" {
		return d
	}
	if d := strings.TrimSpace(s.LoraDirs[category]); d != "" {
		return d
	}
	if d := strings.TrimSpace(s.LoraDir); d != "" {
		return d
	}
	p := strings.ReplaceAll(strings.TrimSpace(modelPath), "\\", "/")
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return ""
}

// loraCategory maps the emitter's image/video split onto the UI category ids
// settings.loraDirs is keyed by, so the two cannot drift apart by spelling.
func loraCategory(isVideo bool) string {
	if isVideo {
		return "video"
	}
	return "image"
}

// imageComponents are the resolved VAE / text-encoder file paths for one
// diffusion model. Empty = not attached (the family doesn't need it, or it's
// missing from the pool — see resolveComponents' missing list).
type imageComponents struct{ vae, clipL, clipG, t5, llm, llmVision, audioVae string }

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
func resolveComponents(enc EncoderSet, ov *Override, arch, name string, pool *EncoderPool, condHidden int64, vid videoInfo) (c imageComponents, missing []string) {
	a := strings.ToLower(strings.TrimSpace(arch))
	n := strings.ToLower(name)
	// Fill every blank pool field from what is actually on disk before the arch
	// arms read it, so a machine that declared no settings.encoders at all still
	// wires a complete command.
	enc = fillEncoderSet(enc, pool)
	// The text encoder is picked by MATCHING WIDTHS, not by name: the DiT states
	// the encoder hidden size it was trained against, and each candidate reports
	// its own. A declared EncoderSet.QwenLlm of that width wins outright, because
	// width is not an identity: Qwen3-4B-Instruct-2507 and Qwen3-VL-4B-Instruct
	// are both 2560 wide, and only the declaration says which one a given DiT was
	// trained against. Without one the scan's pick is used, since a single global
	// field cannot be right for two models that want different encoders (Z-Image
	// wants Qwen3-4B where LongCat wants Qwen2.5-VL-7B). A per-model Override
	// still wins over both, below.
	wantVision := wantsVisionEncoder(a, n, ov)
	preferLlm := enc.QwenLlm
	if vid.Kind == VideoFamilyLtxAV && !pool.llmCandidate(preferLlm, condHidden, wantVision) {
		// LTX-2.x does not condition on a stock LLM. Its encoder is a
		// Gemma-4-12B republished WITH the caption projection baked in, and a
		// plain Gemma-3-12B is the same 3840 wide, so the width match alone is a
		// coin flip that pool.Llm resolves by file size - which the stock model
		// usually wins. The path hint breaks the tie.
		//
		// The test is "does the declared pin APPLY here", not "is one declared":
		// settings.encoders.qwenLlm is a single global field, and pool.Llm honours
		// it only when it clears the same width and vision gates. A pin aimed at
		// an image model (Qwen3-4B, 2560 wide) is silently ignored for LTX, so
		// treating its mere presence as an answer suppressed this hint in the one
		// configuration that needs it: a populated encoders block, which is what
		// every real install has.
		if h := pool.LlmHinted("ltx"); h != "" {
			preferLlm = h
		}
	}
	autoLlm, autoVision := pool.Llm(condHidden, wantVision, preferLlm)
	if autoLlm == "" && wantVision {
		// No vision-capable encoder of that width: fall back to a text-only one
		// rather than emitting nothing, and let the missing projector show up as
		// the model producing unconditioned output rather than as a dead server.
		autoLlm, autoVision = pool.Llm(condHidden, false, preferLlm)
	}
	llmDefault := autoLlm
	if llmDefault == "" && !pool.knowsLlm(enc.QwenLlm) {
		// A declared encoder the scan could not classify (kept outside the models
		// root, say) is still the user's explicit choice.
		//
		// A pin the scan DID classify is a different case: Llm already rejected it
		// on width or vision, so substituting it here would wire a DiT to an
		// encoder of the wrong shape and emit no warning, because req() only warns
		// on an empty path. settings.encoders.qwenLlm is one global field shared by
		// every diffusion model on the box, so that is not a hypothetical: it is
		// what a Z-Image pin does to any model that wants a different encoder.
		// Leaving it empty lets the missing-llm warning fire instead.
		llmDefault = enc.QwenLlm
	}
	if wantVision {
		c.llmVision = autoVision
	}
	req := func(role, path string) string {
		if path == "" {
			missing = append(missing, role)
		}
		return path
	}
	switch {
	// Video DiTs come first and are matched STRUCTURALLY (vid is read off the
	// tensor table, not the arch string): MiniMax-H3 carries no metadata at all,
	// and the arch that does say "wan" belongs to ERNIE-Image-Turbo, an image
	// model that must keep taking the flux.2 arm below.
	case vid.is():
		c.vae, c.audioVae, c.t5, c.llm = videoComponents(vid, enc, pool, llmDefault, &missing)
	case strings.Contains(n, "chroma"): // flux-derived, CLIP stripped → T5 only
		c.vae = req("vae", enc.FluxVae)
		c.t5 = req("t5xxl", enc.T5)
	// Flux.2 Klein reports general.architecture "flux" too (verified against a
	// real gguf header — sd.cpp didn't give it its own arch tag), so arch alone
	// can't tell it apart from flux.1; name-detect like chroma. Klein drops
	// clip_l/t5 for an LLM encoder and needs its own 32-ch-latent VAE,
	// incompatible with flux.1's fluxVae.
	//
	// WHICH LLM is not fixed across the family and is NOT decided here: klein 4B
	// wants Qwen3-4B, klein 9B wants Qwen3-8B, and flux.2-dev wants Mistral-Small-3.
	// All three fall out of the width match, because flux.2 states a caption width
	// of 3x its encoder hidden size (klein 9B: txt_in 12288 = 3 x 4096) and
	// captionFactor divides that out. Nothing needs adding here for a new variant.
	case strings.Contains(n, "klein") || strings.Contains(n, "flux2") || strings.Contains(n, "flux-2"):
		c.vae = req("vae", enc.Flux2Vae)
		c.llm = req("llm", llmDefault)
	// LongCat-Image / LongCat-Image-Edit ship their DiT in BFL (flux) tensor
	// layout, so every converter tags them general.architecture "flux" (and
	// stduhpf's quants carry no KVs at all: the double_blocks sniff in
	// readTensorScan lands them here too). They are not flux.1: clip_l/t5 are
	// gone in favour of a Qwen2.5-VL LLM encoder, while the VAE IS flux.1's ae.
	// Name-detect like chroma. The encoder and (for the edit variant) its vision
	// projector come from the scan, matched on the caption width the DiT states
	// (condHidden 3584 = Qwen2.5-VL-7B). --flow-shift is NOT wired: it is a
	// sampling knob with no structural tell, so LongCat-Edit still wants
	// `extraArgs: "--flow-shift 3.16"` on its override row (exp(1.15), its
	// scheduler's base_shift == max_shift, so unlike flux.1 it does not vary
	// with canvas size).
	case strings.Contains(n, "longcat"):
		c.vae = req("vae", enc.FluxVae)
		c.llm = req("llm", llmDefault)
	case a == "flux" || strings.HasPrefix(a, "flux"):
		c.vae = req("vae", enc.FluxVae)
		c.clipL = req("clip_l", enc.ClipL)
		c.t5 = req("t5xxl", enc.T5)
	// SD1/SD2/SDXL are NOT handled here: they load as -m full checkpoints
	// (isFullCheckpointArch), so imageCmdLines never calls resolveComponents for
	// them — sd.cpp can't version-detect a split SDXL (bare UNet + external CLIPs).
	case strings.HasPrefix(a, "lumina") || a == "z_image" || strings.Contains(n, "z-image"):
		c.vae = req("vae", enc.ZimageVae)
		c.llm = req("llm", llmDefault)
	// Qwen-Image lineage (Qwen-Image / Qwen-Image-Edit / Qwen-Rapid) and the
	// Wan-derived DiTs (Krea, ERNIE-Image) condition on an LLM and carry no
	// CLIP/T5 at all. Their VAEs split by arch: the wan/qwen_image 3D causal VAE
	// for the former, flux.2's 32-channel AE for ERNIE.
	//
	// The Wan-2.1 and Qwen-Image 20B VAEs are structurally IDENTICAL (same 194
	// tensors, every dimension equal), so nothing in either file distinguishes
	// them and the model name is the only signal available: hence the hints.
	// Wrong pick here is a colour-shifted decode, not a crash.
	//
	// Qwen-Image 2.1's RGBA VAE is in the same family but is 64-channel, and
	// THAT cross-wire is a bad decode rather than a tint, so it is excluded by
	// latent width before the hints ever run (see wan3dLatent).
	case a == "qwen_image" || a == "wan" || strings.Contains(n, "qwen-image") || strings.Contains(n, "qwen_image"):
		if a == "wan" && !strings.Contains(n, "wan") {
			// ERNIE-Image reports arch "wan" but is a flux.2-latent model.
			c.vae = req("vae", firstNonEmpty(enc.Flux2Vae, pool.Vae(VaeFamilyFlux2)))
		} else {
			c.vae = req("vae", pool.VaeOfWidth(VaeFamilyWan3D, wan3dLatent(a, condHidden), wan3dHints(n)...))
		}
		c.llm = req("llm", llmDefault)
	}
	// A projector with no encoder to attach it to is incoherent argv: sd-server
	// would take --llm_vision with no --llm. Only the arms above decide whether
	// this model has an LLM encoder at all.
	if c.llm == "" {
		c.llmVision = ""
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
		set("audio_vae", ov.AudioVaePath, &c.audioVae)
		// An explicit projector path always wins; when the encoder itself was
		// overridden the auto-paired projector belonged to a DIFFERENT file, so
		// drop it unless the override supplies its own.
		if ov.TextEncoderPath != "" && ov.LlmVisionPath == "" {
			c.llmVision = projectorBeside(ov.TextEncoderPath, pool)
		}
		if ov.LlmVisionPath != "" {
			c.llmVision = ov.LlmVisionPath
		}
		if ov.LlmVision == "off" {
			c.llmVision = ""
		}
		if ov.LlmVision == "on" && c.llmVision == "" {
			c.llmVision = projectorBeside(c.llm, pool)
		}
	}
	return c, missing
}

// imageCmdLines builds the sd-server argv (exe first) for a diffusion gguf,
// applying the per-model Override knobs plus the arch-wired component pool. Shared
// by emitImageModel (YAML emit) and RenderSoloCmd (editor launch-parameters
// preview), so the box matches a save. Also returns the resolved VRAM budget and
// offload decision for the YAML comment, and any required-but-missing encoder roles.
func imageCmdLines(s Settings, row GgufRow, ov *Override, arch, name string, condHidden int64, vid videoInfo) (lines []string, budget, graph float64, offload bool, missing []string) {
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
	// A video generation holds several frames of latent plus a 3D VAE decode
	// buffer, so its non-weight peak is several times an image's. Charging it
	// the image number would leave a model resident that cannot actually decode.
	overhead := imageComputeOverheadGB
	if vid.is() {
		overhead = videoComputeOverheadGB
	}
	offload = row.SizeGB+overhead > budget
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
	//
	// Full-checkpoint archs bake their encoders/VAE — wire nothing external.
	// Split archs draw component files from the shared pool (per-model override
	// wins). Resolved BEFORE the argv literal because --max-vram is priced off
	// what these components weigh, not just off the diffusion gguf.
	fullCkpt := isFullCheckpointArch(arch)
	modelFlag := "--diffusion-model"
	if fullCkpt {
		modelFlag = "-m"
	}
	var comp imageComponents
	if !fullCkpt {
		comp, missing = resolveComponents(s.Encoders, ov, arch, name, encoderPoolFor(s.RootList()), condHidden, vid)
	}
	graph = graphBudget(budget, row, comp, ov, offload)
	// Per-model backend pick from the config editor (Override.Backend) or the
	// ★Default image entry; fall back to the legacy derived exe.
	sdExe := imageExe(s, ov)
	lines = []string{
		sdExe,
		fmt.Sprintf("%s %s", modelFlag, modelPath),
		"-l 127.0.0.1",
		"--listen-port ${PORT}",
		fmt.Sprintf("--max-vram %g", graph),
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
	// The soundtrack decoder of an audio-capable video model. Without it the DiT
	// still denoises an audio latent it then has no way to turn into samples.
	if p := imageArg(comp.audioVae); p != "" {
		lines = append(lines, "--audio-vae "+p)
	}
	// The vision tower of the text encoder, needed by edit pipelines that
	// condition on a reference image. Auto-paired to the chosen --llm (its
	// sibling mmproj), never hand-typed. Missing, most edit models do not error:
	// they report "vision disabled" and emit an image unrelated to the
	// reference, so a wrong-looking result is the only symptom. Qwen-Image 2.1
	// is the exception and refuses the prompt outright ("editing requires
	// Qwen3-VL vision weights"), which is the better behaviour of the two.
	if p := imageArg(comp.llmVision); p != "" {
		lines = append(lines, "--llm_vision "+p)
	}
	var ovLoraDir string
	if ov != nil {
		ovLoraDir = ov.LoraDir
	}
	if p := imageArg(resolveLoraDir(s, ovLoraDir, loraCategory(vid.is()), modelPath)); p != "" {
		lines = append(lines, "--lora-model-dir "+p)
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
	// Video-only VRAM levers. --vae-tiling above chunks the decode spatially; a
	// clip's decode also grows along TIME with --video-frames and only
	// --temporal-tiling chunks that axis. --stream-layers streams the diffusion
	// weights against --max-vram rather than pinning them resident, which hands
	// that headroom to the sampler.
	//
	// Both are gated on vid.is(): --temporal-tiling applies to a video VAE decode
	// and nothing else, and an image model has no long-clip peak for
	// --stream-layers to relieve, so on the image path both would be noise in the
	// launch line at best.
	//
	// --temporal-tiling is gated a second time, on the FAMILY, because only some
	// video VAEs implement a tiled decode and the rest accept the flag and ignore
	// it (see vaeTemporalTiling). An explicit "on" still forces it through, so a
	// build that adds support for a family needs an override row rather than a
	// recompile.
	switch {
	case ov != nil && ov.TemporalTiling == "on":
		lines = append(lines, "--temporal-tiling")
	case vid.is() && vaeTemporalTiling(vid) && (ov == nil || ov.TemporalTiling != "off"):
		lines = append(lines, "--temporal-tiling")
	}
	// --stream-layers is additionally gated on OFFLOAD, and that is not caution,
	// it is the flag's actual precondition. sd.cpp: "--stream-layers has no
	// effect unless diffusion params backend is cpu; ignoring" - streaming the
	// weights in means they have to live somewhere to stream FROM. Only
	// --offload-to-cpu puts them there ("place the weights in RAM to save VRAM,
	// and automatically load them into VRAM when needed"), so emitting it beside
	// resident weights logged an ignore line and bought nothing, while reading in
	// the launch args like a VRAM lever that was already pulled. An explicit "on"
	// still forces it, as the escape hatch for a build that widens the rule.
	switch {
	case ov != nil && ov.StreamLayers == "on":
		lines = append(lines, "--stream-layers")
	case vid.is() && offload && (ov == nil || ov.StreamLayers != "off"):
		lines = append(lines, "--stream-layers")
	}
	lines = append(lines, fmt.Sprintf("-t %d", threads))
	// Park components on CPU via the sd-server --backend spec. te=cpu is the default
	// (the text encoder runs once per generation, cheapest to keep off the GPU, and
	// its VRAM is NOT in row.SizeGB — leaving it resident lets a "fits" diffusion
	// model thrash the whole graph through --max-vram). "off" keeps it on the GPU.
	// vae=cpu is opt-in: the bf16 VAE decodes on the GPU by default, but some
	// backends whiten it — CPU is the safe (slower) fallback.
	var beParts []string
	if ov == nil || ov.TeOnCpu != "off" {
		beParts = append(beParts, "te=cpu")
	}
	if ov != nil && ov.VaeOnCpu == "on" {
		beParts = append(beParts, "vae=cpu")
	}
	if len(beParts) > 0 {
		lines = append(lines, "--backend "+strings.Join(beParts, ","))
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
	// Generation defaults applied when a request omits them. An image model
	// starts from nothing (sd-server's own defaults stand); a video model starts
	// from its family profile, because two of those numbers are not preferences:
	// --video-frames defaults to 1 (a single still), and H3 conditions at
	// cfg-scale 1.0 while sd-server's built-in default is 7.0. Per-model
	// overrides still win over both.
	def := videoDefaultsFor(vid, name)
	if ov != nil {
		if ov.DefaultSteps > 0 {
			def.steps = ov.DefaultSteps
		}
		if ov.DefaultCfg > 0 {
			def.cfg = ov.DefaultCfg
		}
		if ov.DefaultSampler != "" {
			def.sampler = ov.DefaultSampler
		}
		if ov.DefaultWidth > 0 {
			def.width = ov.DefaultWidth
		}
		if ov.DefaultHeight > 0 {
			def.height = ov.DefaultHeight
		}
		if ov.DefaultFrames > 0 {
			def.frames = ov.DefaultFrames
		}
		if ov.DefaultFps > 0 {
			def.fps = ov.DefaultFps
		}
	}
	if def.steps > 0 {
		lines = append(lines, fmt.Sprintf("--steps %d", def.steps))
	}
	if def.cfg > 0 {
		lines = append(lines, fmt.Sprintf("--cfg-scale %g", def.cfg))
	}
	if def.sampler != "" {
		lines = append(lines, "--sampling-method "+def.sampler)
	}
	if def.width > 0 {
		lines = append(lines, fmt.Sprintf("--width %d", def.width))
	}
	if def.height > 0 {
		lines = append(lines, fmt.Sprintf("--height %d", def.height))
	}
	if def.frames > 0 {
		lines = append(lines, fmt.Sprintf("--video-frames %d", def.frames))
	}
	if def.fps > 0 {
		lines = append(lines, fmt.Sprintf("--fps %d", def.fps))
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines, budget, graph, offload, missing
}

// mergeImageVariant overlays an image variant onto its base override: the
// variant inherits every base field it leaves empty (component paths, placement)
// and overrides only what it sets. Unlisted is the variant's own (a variant is a
// distinct served id). Variants is cleared to avoid re-emitting.
func mergeImageVariant(base Override, v VariantSpec) Override {
	o := base
	o.Variants = nil
	o.Unlisted = v.Unlisted
	// Component paths resolve through the shared sentinel rule: empty inherits
	// the model-wide path, "none" drops the encoder for this preset only.
	mergeInheritImageStrings(&o, &v)
	if v.RefEdit != "" {
		o.RefEdit = v.RefEdit
	}
	if v.LlmVision != "" {
		o.LlmVision = v.LlmVision
	}
	if v.OffloadToCpu != "" {
		o.OffloadToCpu = v.OffloadToCpu
	}
	if v.TeOnCpu != "" {
		o.TeOnCpu = v.TeOnCpu
	}
	if v.VaeOnCpu != "" {
		o.VaeOnCpu = v.VaeOnCpu
	}
	if v.VaeTiling != "" {
		o.VaeTiling = v.VaeTiling
	}
	if v.TemporalTiling != "" {
		o.TemporalTiling = v.TemporalTiling
	}
	if v.StreamLayers != "" {
		o.StreamLayers = v.StreamLayers
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
	if v.DefaultFrames > 0 {
		o.DefaultFrames = v.DefaultFrames
	}
	if v.DefaultFps > 0 {
		o.DefaultFps = v.DefaultFps
	}
	if v.ExtraArgs != "" {
		o.ExtraArgs = v.ExtraArgs
	}
	return o
}

// extraImageBudget resolves the --max-vram for an extra image model (its own
// VramTargetGB, else the settings target minus overhead; floored at 1).
func extraImageBudget(s Settings, m ExtraImageModel) float64 {
	budget := s.TargetVramGB - s.VramOverheadGB
	if m.VramTargetGB > 0 {
		budget = m.VramTargetGB
	}
	if budget < 1 {
		budget = 1
	}
	return budget
}

// imageExe picks the sd-server binary this image model launches: its own backend
// pin when it has one, else the class default the ★ picks, else the legacy slot.
// Only an auto model follows a later default switch. The class guard keeps a
// stray non-image id from emitting the wrong launcher.
func imageExe(s Settings, ov *Override) string {
	if rb := resolveBackend(s, ov, "image"); rb.Exe != "" && kindServesClass(rb.Kind, "image") {
		return rb.Exe
	}
	return s.SdServerExe
}

// extraImageCmdLines builds the sd-server argv for one hand-declared extra image
// model (safetensors DiT). Shared by config emit and the editor's cmd preview so
// both render identically — the gguf-scan RenderSoloCmd path can't serve these
// (a safetensors DiT has no gguf header to arch-detect from).
func extraImageCmdLines(s Settings, m ExtraImageModel) []string {
	threads := s.Threads
	if m.Threads > 0 {
		threads = m.Threads
	}
	modelFlag := "-m"
	if strings.TrimSpace(m.ModelFlag) != "" {
		modelFlag = strings.TrimSpace(m.ModelFlag)
	}
	lines := []string{
		imageExe(s, &Override{Backend: m.Backend}),
		fmt.Sprintf("%s %s", modelFlag, imageArg(m.ModelPath)),
		"-l 127.0.0.1",
		"--listen-port ${PORT}",
		fmt.Sprintf("--max-vram %g", extraImageBudget(s, m)),
	}
	if p := imageArg(m.VaePath); p != "" {
		lines = append(lines, "--vae "+p)
	}
	if p := imageArg(m.ClipLPath); p != "" {
		lines = append(lines, "--clip_l "+p)
	}
	if p := imageArg(m.ClipGPath); p != "" {
		lines = append(lines, "--clip_g "+p)
	}
	if p := imageArg(m.T5Path); p != "" {
		lines = append(lines, "--t5xxl "+p)
	}
	if p := imageArg(m.LlmPath); p != "" {
		lines = append(lines, "--llm "+p)
	}
	// Hand-declared extras are scanned by nothing, so there is no videoInfo to
	// read a category off (the same blind spot --temporal-tiling is opted into
	// by hand below). They take the "image" entry; a video extra that needs the
	// other tree sets its own loraDir, which wins over both.
	if p := imageArg(resolveLoraDir(s, m.LoraDir, "image", m.ModelPath)); p != "" {
		lines = append(lines, "--lora-model-dir "+p)
	}
	if m.DiffusionFa != "off" {
		lines = append(lines, "--diffusion-fa")
	}
	if m.VaeTiling != "off" {
		lines = append(lines, "--vae-tiling")
	}
	// Opt-in, unlike the discovered path above: an extra model is hand-declared
	// and never scanned, so there is nothing here that knows it is a video model.
	if m.TemporalTiling == "on" {
		lines = append(lines, "--temporal-tiling")
	}
	if m.StreamLayers == "on" {
		lines = append(lines, "--stream-layers")
	}
	lines = append(lines, fmt.Sprintf("-t %d", threads))
	var beParts []string
	if m.TeOnCpu != "off" {
		beParts = append(beParts, "te=cpu")
	}
	if m.VaeOnCpu == "on" {
		beParts = append(beParts, "vae=cpu")
	}
	if len(beParts) > 0 {
		lines = append(lines, "--backend "+strings.Join(beParts, ","))
	}
	if m.OffloadToCpu == "on" {
		lines = append(lines, "--offload-to-cpu", "--vae-on-cpu")
	}
	if m.DefaultSteps > 0 {
		lines = append(lines, fmt.Sprintf("--steps %d", m.DefaultSteps))
	}
	if m.DefaultCfg > 0 {
		lines = append(lines, fmt.Sprintf("--cfg-scale %g", m.DefaultCfg))
	}
	if sm := strings.TrimSpace(m.DefaultSampler); sm != "" {
		lines = append(lines, "--sampling-method "+sm)
	}
	if m.DefaultWidth > 0 {
		lines = append(lines, fmt.Sprintf("--width %d", m.DefaultWidth))
	}
	if m.DefaultHeight > 0 {
		lines = append(lines, fmt.Sprintf("--height %d", m.DefaultHeight))
	}
	if extra := strings.TrimSpace(m.ExtraArgs); extra != "" {
		lines = append(lines, extra)
	}
	return lines
}

// RenderExtraImageCmd renders the full sd-server command for one extra image
// model as a single line (the editor's cmd-preview endpoint uses this).
func RenderExtraImageCmd(s Settings, m ExtraImageModel) string {
	return strings.Join(extraImageCmdLines(s, m), " ")
}

// FindExtraImageModel returns the settings entry whose model path matches p
// (the editor resolves a model to its -m/--diffusion-model gguf path).
func FindExtraImageModel(s Settings, p string) (ExtraImageModel, bool) {
	for _, m := range s.ExtraImageModels {
		if config.PathEqual(m.ModelPath, p) {
			return m, true
		}
	}
	return ExtraImageModel{}, false
}

// ExtraImageAsOverride projects an ExtraImageModel's editable fields into an
// Override so the config editor can seed its image form from the hand-declared
// settings entry when no sidecar override exists yet (without this, opening an
// unedited extra model shows blank fields and the first save wipes the base).
func ExtraImageAsOverride(m ExtraImageModel) Override {
	return Override{
		Match:           m.ModelPath,
		VaePath:         m.VaePath,
		ClipLPath:       m.ClipLPath,
		ClipGPath:       m.ClipGPath,
		T5Path:          m.T5Path,
		TextEncoderPath: m.LlmPath,
		LoraDir:         m.LoraDir,
		TeOnCpu:         m.TeOnCpu,
		VaeOnCpu:        m.VaeOnCpu,
		VaeTiling:       m.VaeTiling,
		TemporalTiling:  m.TemporalTiling,
		StreamLayers:    m.StreamLayers,
		DiffusionFa:     m.DiffusionFa,
		OffloadToCpu:    m.OffloadToCpu,
		DefaultSteps:    m.DefaultSteps,
		DefaultCfg:      m.DefaultCfg,
		DefaultSampler:  m.DefaultSampler,
		DefaultWidth:    m.DefaultWidth,
		DefaultHeight:   m.DefaultHeight,
		VramTargetGB:    m.VramTargetGB,
		Threads:         m.Threads,
		ExtraArgs:       m.ExtraArgs,
		Unlisted:        m.Unlisted,
		PromptEnhancer:  m.PromptEnhancer,
	}
}

// ApplyOverrideToExtraImage overlays a matched Override's image fields onto a
// hand-declared ExtraImageModel so UI config edits (the sidecar override) reach
// the safetensors models the gguf scan never sees. The Override is the UI's
// COMPLETE snapshot (seeded from ExtraImageAsOverride), so every UI-modelled
// field is taken authoritatively — including the "" tri-state defaults, so a
// toggle flipped back ON (DiffusionFa "") clears a base "off". Structural fields
// (Name/ModelPath/ModelFlag) always stay.
// ponytail: authoritative replace, not field-merge — correct because a UI save
// writes the full field set; a broad glob file override matching a safetensors
// model would flatten its toggles to defaults, but none is authored that way.
func ApplyOverrideToExtraImage(m ExtraImageModel, ov *Override) ExtraImageModel {
	if ov == nil {
		return m
	}
	m.VaePath = ov.VaePath
	m.ClipLPath = ov.ClipLPath
	m.ClipGPath = ov.ClipGPath
	m.T5Path = ov.T5Path
	m.LlmPath = ov.TextEncoderPath
	m.LoraDir = ov.LoraDir
	m.Backend = ov.Backend
	m.TeOnCpu = ov.TeOnCpu
	m.VaeOnCpu = ov.VaeOnCpu
	m.VaeTiling = ov.VaeTiling
	m.TemporalTiling = ov.TemporalTiling
	m.StreamLayers = ov.StreamLayers
	m.DiffusionFa = ov.DiffusionFa
	m.OffloadToCpu = ov.OffloadToCpu
	m.DefaultSteps = ov.DefaultSteps
	m.DefaultCfg = ov.DefaultCfg
	m.DefaultSampler = ov.DefaultSampler
	m.DefaultWidth = ov.DefaultWidth
	m.DefaultHeight = ov.DefaultHeight
	m.ExtraArgs = ov.ExtraArgs
	m.Unlisted = ov.Unlisted
	m.PromptEnhancer = ov.PromptEnhancer
	if ov.Threads > 0 {
		m.Threads = ov.Threads
	}
	if ov.VramTargetGB > 0 {
		m.VramTargetGB = ov.VramTargetGB
	}
	return m
}

// emitExtraImageModels writes an sd-server block for each Settings.ExtraImageModel
// (safetensors DiTs autogen's gguf scan can't see). Unlike emitImageModel these
// wire components verbatim from explicit paths — no arch detection, no VRAM
// planner. A matching override (sidecar UI edit or file rule) is overlaid so the
// config editor can tune these. Names are deduped against emitted models via seen.
func emitExtraImageModels(b *strings.Builder, s Settings, overrides []Override, seen map[string]bool, emitted *[]string) {
	for _, m := range s.ExtraImageModels {
		name := strings.TrimSpace(m.Name)
		if strings.TrimSpace(m.ModelPath) == "" || name == "" || seen[name] {
			continue
		}
		seen[name] = true

		ov := ResolveOverride(GgufRow{FullPath: m.ModelPath}, overrides)
		m = ApplyOverrideToExtraImage(m, ov)
		lines := extraImageCmdLines(s, m)

		fmt.Fprintf(b, "\n  # extra image model (safetensors, sd-server, max-vram=%gGB) - hand-declared, no gguf scan\n", extraImageBudget(s, m))
		fmt.Fprintf(b, "  %q:\n", name)
		b.WriteString("    cmd: >\n")
		for _, line := range lines {
			fmt.Fprintf(b, "      %s\n", line)
		}
		fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
		writeSingleDeviceEnv(b, s, imageExe(s, &Override{Backend: m.Backend}))
		writeEstVram(b, extraImageBudget(s, m))
		b.WriteString("    checkEndpoint: /\n")
		writePromptEnhancer(b, m.PromptEnhancer, enhancerByID(s.PromptEnhancers))
		if m.Unlisted {
			b.WriteString("    unlisted: true\n")
		}
		b.WriteString("    capabilities:\n")
		if IsReferenceEditModel(name, ov) {
			b.WriteString("      in: [text, image]\n")
		} else {
			b.WriteString("      in: [text]\n")
		}
		b.WriteString("      out: [image]\n")
		*emitted = append(*emitted, name)
	}
}

func emitImageModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name, arch string, bakedEnc bool, condHidden int64, emitted *[]string) {
	lines, budget, graph, offload, missing := imageCmdLines(s, row, ov, arch, name, condHidden, videoInfo{})

	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (image model, sd-server, budget=%gGB, max-vram=%gGB, offload=%t)\n", arch, row.SizeGB, budget, graph, offload)
	// SD/SDXL served as -m full checkpoints: if this gguf has no baked encoders it
	// is a bare UNet, which sd.cpp cannot load standalone (no split path for SDXL).
	if isFullCheckpointArch(arch) && !bakedEnc {
		fmt.Fprintf(b, "  # WARNING: %s looks UNet-only (no baked text encoder) - sd.cpp can't load SD/SDXL split; supply an all-in-one checkpoint\n", name)
	}
	if len(missing) > 0 {
		fmt.Fprintf(b, "  # WARNING: %s needs encoder(s) [%s] that aren't in settings.encoders - generation will fail until declared\n", name, strings.Join(missing, ", "))
	}
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range lines {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	writeSingleDeviceEnv(b, s, imageExe(s, ov))
	// Admission estimate = the whole budget this model was sized against, NOT the
	// --max-vram it launches with: that one is only the graph-cut headroom left
	// once the resident weights are paid for, so charging the scheduler that
	// number would book a 14GB model as if it were the 7GB of slack around it.
	// True peak during sampling/VAE decode runs above it, which is why the
	// scheduler additionally refuses to spawn anything while a render is in
	// flight (see FIFO.imageRenderInFlight) rather than trusting this number
	// alone.
	writeEstVram(b, budget)
	// sd-server has no /health; the webui root returns 200 once loaded.
	b.WriteString("    checkEndpoint: /\n")
	// The enhancer is not a launch flag - sd-server has no such concept. It rides
	// the model entry so the playground can read it off /api/models and offer the
	// rewrite before the render, which is the only place a rewrite is reviewable.
	writePromptEnhancer(b, ovPromptEnhancer(ov), enhancerByID(s.PromptEnhancers))
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	// A reference-edit model declares an image INPUT, which is what tells the
	// playground to send the source as a reference (extra_images) instead of an
	// img2img base. That distinction is not cosmetic: the img2img route scales
	// the step count by the denoise strength, so an 8-step distill silently ran
	// 4 steps and produced artifacted output.
	if IsReferenceEditModel(name, ov) {
		b.WriteString("      in: [text, image]\n")
	} else {
		b.WriteString("      in: [text]\n")
	}
	b.WriteString("      out: [image]\n")
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}

// wan3dHints orders the two indistinguishable 3D causal VAEs by what the model
// name says its lineage is. Wan/Krea take wan_2.1_vae, everything else in the
// Qwen-Image family takes qwen_image_vae.
// wan3dLatent is the VAE latent width a Wan-3D-family image DiT expects, or 0
// for "no opinion" (match any, the behaviour every other model here relies on).
//
// It cannot be read off the DiT. Qwen-Image's img_in is [64, hidden] for BOTH
// lines: the 20B patchifies a 16-channel latent 2x2 into 64, while 2.1 consumes
// a 64-channel latent whole. The caption projection is what separates them, and
// the file states it: the 20B line is 3584 wide (Qwen2.5-VL-7B) and pairs with
// the 16-channel VAE, 2.1 is 4096 (Qwen3-VL-8B) and pairs with the 64-channel
// RGBA one. Krea2 is arch qwen_image too and sits at 2560, i.e. 16-channel.
//
// Scoped to qwen_image on purpose: Wan VIDEO checkpoints share this family and
// do not share this rule (Wan 2.2 TI2V ships a 48-channel VAE), so they keep
// passing 0 and picking by hint.
func wan3dLatent(arch string, condHidden int64) int64 {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(arch)), "qwen_image") {
		return 0
	}
	if condHidden == 4096 {
		return 64
	}
	return 16
}

func wan3dHints(name string) []string {
	if strings.Contains(name, "krea") || strings.Contains(name, "wan") {
		return []string{"wan_2.1", "wan2.1", "wan"}
	}
	return []string{"qwen_image", "qwen-image"}
}

// firstNonEmpty returns the first non-blank of its arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
