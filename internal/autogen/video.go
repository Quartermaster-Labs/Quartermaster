package autogen

import (
	"fmt"
	"strings"
)

// Video generation is served by the SAME binary as image generation
// (stable-diffusion.cpp's sd-server), so this file is deliberately thin: it owns
// detection, the component role an image model does not have (--audio-vae), the
// per-family generation profile, and the YAML block. Everything else (budget,
// offload, placement, threads, LoRA dir) comes from imageCmdLines, which takes a
// videoInfo and branches only where video actually differs. Forking that builder
// would have duplicated ~95% identical argv and let the two drift.
//
// DETECTION IS STRUCTURAL, and that is not a style choice:
//
//   - MiniMax-H3's gguf carries ZERO metadata KVs. No general.architecture, no
//     general.type, nothing. The tensor table is the only thing in the file that
//     says what it is.
//   - The one arch string in circulation that reads "wan" belongs to
//     ERNIE-Image-Turbo, which is an IMAGE model (it takes the flux.2 arm in
//     resolveComponents). Routing arch=wan to video would have broken a model
//     that works today.
//
// See readTensorScan: video_patch_proj / final_layer.video_out mark H3, a 5-dim
// (conv3d) patch embedding marks the Wan2.x layout, and audio_patch_proj marks a
// model that also denoises a soundtrack latent.

// Video families, as named by tensorScan.videoKind.
const (
	VideoFamilyMinimaxH3 = "minimax_h3"
	VideoFamilyWan       = "wan"
)

// videoComputeOverheadGB approximates the non-weight VRAM a video generation
// needs on top of the resident diffusion weights. It is imageComputeOverheadGB's
// sibling and is deliberately much larger: the sampler holds a latent for every
// frame at once, and the 3D VAE decode at the end is the real peak (it
// reconstructs a whole temporal tile, not one picture).
//
// ponytail: flat, like the image number, so it does not scale with
// --video-frames or resolution. It only has to be in the right GB ballpark to
// flip the offload decision; --max-vram does the fine fitting inside sd.cpp.
// Raise it (or pin vramTargetGB) if a clip OOMs at decode.
const videoComputeOverheadGB = 4.0

// videoInfo is what the tensor scan concluded about a diffusion gguf's temporal
// nature. The zero value means "not a video model", which is what every image
// path passes.
type videoInfo struct {
	Kind     string // VideoFamily*, "" for an image model
	AudioOut bool   // also denoises an audio latent => needs --audio-vae
}

func (v videoInfo) is() bool { return strings.TrimSpace(v.Kind) != "" }

// videoInfoFrom lifts the scan result off a parsed gguf header.
func videoInfoFrom(meta Metadata) videoInfo {
	return videoInfo{Kind: meta.VideoKind, AudioOut: meta.HasAudioOut}
}

// IsVideoModel reports whether a gguf is a video-generation DiT. Checked BEFORE
// the image path in emitModel, because a video model's arch (when it declares
// one at all) is an image arch as far as isImageArch is concerned.
func IsVideoModel(meta Metadata) bool { return videoInfoFrom(meta).is() }

// genDefaults is the generation profile baked into a model's launch command,
// applied by sd-server whenever a request omits the field. A zero field means
// "emit nothing, keep sd-server's own default".
type genDefaults struct {
	steps   int
	cfg     float64
	sampler string
	width   int
	height  int
	frames  int
	fps     int
}

// videoDefaultsFor returns the family profile. An image model gets the zero
// profile, i.e. exactly the behaviour that existed before video did.
//
// The H3 numbers are from leejet's own reference command, and two of them are
// load-bearing rather than taste:
//
//   - cfg 1.0: H3 conditions at guidance 1.0. sd-server's built-in default is
//     7.0, so a model launched without this pin renders every clip wrong.
//   - frames 56: --video-frames defaults to 1, which is a single still. H3
//     aligns the count UP to the 17k+5 grid (5, 22, 39, 56, 73...), NOT to the
//     4n+1 grid every other video family uses, so 56 is exact and the old 25
//     would have silently become 39. sd.cpp: SDVersion-gated align_video_frames.
//
// steps is deliberately LEFT UNSET for H3. The base model is NOT distilled: it
// samples at sd-server's default 20, and the 4-step figure everyone quotes
// belongs to the turbo LoRAs (lightx2v et al), which arrive per REQUEST as
// `lora: [{path, multiplier}]` or as <lora:name:1.0> in the prompt. A launch
// flag cannot know whether a given request carries one, and pinning 4 here made
// every LoRA-less render mush.
func videoDefaultsFor(v videoInfo) genDefaults {
	switch v.Kind {
	case VideoFamilyMinimaxH3:
		return genDefaults{cfg: 1.0, width: 640, height: 384, frames: 56, fps: 24}
	case VideoFamilyWan:
		// Wan2.x is not distilled: leave steps/cfg at sd-server's defaults and
		// pin only what is unusable by default (one frame) or wrong for the
		// family's training resolution.
		return genDefaults{width: 832, height: 480, frames: 81, fps: 16}
	}
	return genDefaults{}
}

// videoComponents wires the component files a video DiT needs, drawing from the
// same pool the image arms use. It returns the paths and appends to missing any
// role the family REQUIRES that nothing supplied (surfaced as a YAML WARNING, so
// a half-wired model is visible rather than silently broken).
//
// llmDefault is the width-matched text encoder resolveComponents already picked:
// H3 states condHidden 5120 on condition_proj.weight, which is Qwen3-VL-32B.
func videoComponents(v videoInfo, enc EncoderSet, pool *EncoderPool, llmDefault string, missing *[]string) (vae, audioVae, t5, llm string) {
	req := func(role, path string) string {
		if path == "" {
			*missing = append(*missing, role)
		}
		return path
	}
	switch v.Kind {
	case VideoFamilyMinimaxH3:
		// H3's video VAE is a transformer autoencoder, its own family in the
		// pool (VaeFamilyVideo3D) precisely because it shares no tensor shape
		// with the 2D AEs or with Wan's 3D causal VAE.
		vae = req("vae", firstNonEmpty(enc.VideoVae, pool.Vae(VaeFamilyVideo3D)))
		llm = req("llm", llmDefault)
	case VideoFamilyWan:
		// Wan2.x decodes through the 3D causal VAE and conditions on umT5-XXL.
		// The Wan-2.1 and Qwen-Image VAEs are structurally identical, so the
		// path is the only signal available (the same hint the image arm uses).
		vae = req("vae", pool.Vae(VaeFamilyWan3D, "wan"))
		t5 = req("t5xxl", enc.T5)
	}
	if v.AudioOut {
		audioVae = req("audio_vae", firstNonEmpty(enc.AudioVae, pool.AudioVae()))
	}
	return vae, audioVae, t5, llm
}

// emitVideoModel writes an sd-server YAML entry for a video-generation GGUF.
// Same server, same budget math and placement as an image model: what differs is
// the component set (an --audio-vae), the generation profile, the larger compute
// overhead, and the declared capabilities, which is what routes the playground to
// the Video tab and the async job API rather than to /sdapi txt2img.
func emitVideoModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name, arch string, vid videoInfo, condHidden int64, emitted *[]string) {
	lines, budget, offload, missing := imageCmdLines(s, row, ov, arch, name, condHidden, vid)

	archNote := strings.TrimSpace(arch)
	if archNote == "" {
		// H3 declares no arch at all; say so rather than emitting "arch=" and
		// leaving the reader of the config to guess what happened.
		archNote = "(none declared)"
	}
	fmt.Fprintf(b, "\n  # arch=%s family=%s size=%gGB (video model, sd-server, max-vram=%gGB, offload=%t)\n", archNote, vid.Kind, row.SizeGB, budget, offload)
	if len(missing) > 0 {
		fmt.Fprintf(b, "  # WARNING: %s needs component(s) [%s] that aren't in settings.encoders - generation will fail until declared\n", name, strings.Join(missing, ", "))
	}
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range lines {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	writeSingleDeviceEnv(b, s, imageExe(s, ov))
	// Admission estimate = the --max-vram cap sd-server is told to stay inside,
	// the same accepted under-charge the image path makes: the true peak during
	// 3D VAE decode runs above it, and the scheduler's in-flight guard is what
	// actually keeps a second process from spawning underneath a render.
	writeEstVram(b, budget)
	b.WriteString("    checkEndpoint: /\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      in: [text]\n")
	if vid.AudioOut {
		b.WriteString("      out: [video, audio]\n")
	} else {
		b.WriteString("      out: [video]\n")
	}
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}
