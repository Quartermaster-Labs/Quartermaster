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
	// VideoFamilyLtxAV is Lightricks LTX-2.x: ONE flat DiT over a joint
	// video+audio token stream, so the soundtrack is not a bolted-on second
	// tower but half the sequence. It patchifies with a LINEAR layer
	// (patchify_proj.weight) rather than H3's conv stem or Wan's conv3d, which
	// is why it needs its own marker rather than falling out of either.
	VideoFamilyLtxAV = "ltxav"
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
//
// LTX is the one family whose profile cannot be read off the weights. Its
// distilled and dev checkpoints are TENSOR-IDENTICAL: same arch, same shapes,
// same names, and they differ only in the schedule they were trained to run
// (distilled carries 8 predefined sigmas and is guidance-free, dev wants ~20
// euler steps at cfg 3.0). Nothing in the file distinguishes them, so the name
// is the only signal there is, the same compromise chroma/klein already make on
// the image side. A misread here is not subtle: 8 steps on dev is noise, and
// cfg 3.0 on distilled is a scorched, over-saturated clip.
func videoDefaultsFor(v videoInfo, name string) genDefaults {
	switch v.Kind {
	case VideoFamilyLtxAV:
		// 1280x704 at 121 frames is ~14k latent tokens by LTX's own ratios (it
		// compresses 32x spatially and 8x temporally, against the 8x/4x every
		// other family here uses), so it is LIGHTER per pixel than H3 at
		// 640x384 despite being four times the canvas. 121 is on the 8k+1 grid
		// and sits under the 153-frame ceiling the checkpoint's own
		// positional_embedding_max_pos imposes (20 latent frames).
		d := genDefaults{width: 1280, height: 704, frames: 121, fps: 24}
		if isDistilledName(name) {
			d.steps, d.cfg = 8, 1.0
		} else {
			d.cfg = 3.0
			d.sampler = "euler"
		}
		return d
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

// vaeTemporalTiling reports whether this family's VAE can actually decode in
// windows along TIME, which is a property of the VAE implementation and not of
// the flag.
//
// sd-server accepts --temporal-tiling for any model and silently falls back:
// "%s does not support temporal tiling for %s; processing the full temporal
// dimension". So an ungated flag is not an error, it is worse than one, a launch
// line that claims a VRAM lever the decode never applies. Emitting it only where
// it bites keeps the config honest about what is running.
//
// Only two implementations exist in the shipped binary, one per family:
// WanVAERunner::_compute_temporal_tiled (stateful, carrying causal conv state
// across windows) and LTXVideoVAE::decode_temporal_tiled_streaming. H3's VAE is
// a transformer autoencoder with no tiled decode path at all, which is the same
// structural difference that gives it its own family in the encoder pool.
func vaeTemporalTiling(v videoInfo) bool {
	switch v.Kind {
	case VideoFamilyWan, VideoFamilyLtxAV:
		return true
	}
	return false
}

// isDistilledName reports whether a checkpoint's id names it as the distilled
// (few-step, guidance-free) sibling of a family that ships both. Name matching
// is a last resort everywhere else in this package, and it is used here only
// because the two files are byte-for-byte the same SHAPE - see videoDefaultsFor.
func isDistilledName(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "distill") || strings.Contains(l, "turbo")
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
	case VideoFamilyLtxAV:
		// Two autoencoders, always both: the video half decodes the picture
		// latents, the audio half decodes the mel latents its joint token
		// stream also produced. They are picked by FAMILY rather than by the
		// enc.VideoVae/enc.AudioVae pins, which are one slot each and already
		// mean H3's pair on a box that has one - see fillEncoderSet. Override
		// .VaePath / .AudioVaePath remain the per-model escape hatch.
		vae = req("vae", pool.Vae(VaeFamilyLtx))
		// The text encoder is a Gemma-4-12B REPUBLISHED with LTX's caption
		// projection grafted on, so a stock Gemma of the same width would load
		// and then condition on nothing. The path hint is what separates them;
		// resolveComponents feeds it in as llmDefault.
		llm = req("llm", llmDefault)
		// Deliberately NOT emitting --embeddings-connectors: the 2.5 checkpoint
		// declares use_embeddings_connector and carries the connector layers
		// itself (video_embeddings_connector.* / audio_embeddings_connector.*),
		// so pointing the flag at an external file would load a second copy.
	case VideoFamilyWan:
		// Wan2.x decodes through the 3D causal VAE and conditions on umT5-XXL.
		// The Wan-2.1 and Qwen-Image VAEs are structurally identical, so the
		// path is the only signal available (the same hint the image arm uses).
		vae = req("vae", pool.Vae(VaeFamilyWan3D, "wan"))
		t5 = req("t5xxl", enc.T5)
	}
	if v.AudioOut {
		switch v.Kind {
		case VideoFamilyLtxAV:
			audioVae = req("audio_vae", pool.AudioVae(VaeFamilyLtx))
		default:
			audioVae = req("audio_vae", firstNonEmpty(enc.AudioVae, pool.AudioVae(VaeFamilyVideo3D)))
		}
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
