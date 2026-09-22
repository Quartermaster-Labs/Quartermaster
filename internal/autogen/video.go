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
// needs on top of the resident diffusion weights, AT THE FAMILY'S OWN DEFAULT
// PROFILE. It is imageComputeOverheadGB's sibling and is deliberately much
// larger: the sampler holds a latent for every frame at once, and the 3D VAE
// decode at the end is the real peak (it reconstructs a whole temporal tile,
// not one picture).
//
// This is a REFERENCE point, not the number the sizer uses: videoComputeOverhead
// scales it by how far the resolved profile sits from that default. Raise it (or
// pin vramTargetGB) if a clip OOMs at decode on DEFAULT framing.
const videoComputeOverheadGB = 4.0

// videoTemporalCompression is how many source frames one latent frame covers.
// Only the RATIO of two profiles in the SAME family is ever computed from it
// (see videoComputeOverhead), so it has to be right per family but never has to
// agree across them.
//
// LTX compresses 8x along time, the same 8 its frames%8==1 grid exposes. The
// others land on 4 (Wan's 4n+1 grid). H3 aligns to 17k+5 and is not a clean
// power of two, but 4 is the closest honest stand-in and the +1 slack below
// keeps the difference sub-percent at any length worth pricing.
func videoTemporalCompression(v videoInfo) int {
	if v.Kind == VideoFamilyLtxAV {
		return 8
	}
	return 4
}

// latentFrames is how many latent frames a source frame count occupies. Every
// family's grid is k*c+1: one keyframe plus k compressed windows.
func latentFrames(frames, comp int) int {
	if frames < 1 {
		frames = 1
	}
	if comp < 1 {
		comp = 1
	}
	return (frames-1)/comp + 1
}

// videoComputeOverhead prices the non-weight peak for the profile a model will
// ACTUALLY launch with, rather than the flat constant that used to stand here.
//
// The flat number was wrong in a way that only bites big models. A 22B LTX at
// Q4 is 14.6GB, and 14.6+4.0 fits inside a 24GB card's ~22.3GB budget, so the
// sizer concluded "resident", set offload=false, and graphBudget then handed
// --max-vram whatever was LEFT - 5.9GB - to run a 14k-token joint audio+video
// graph in. sd.cpp graph-cuts to fit, fails, and the driver backs the
// allocation with host memory: a silent spill into shared memory rather than an
// error. It gets worse the longer the clip, and LTX-2.5 is specified to 20s, so
// the flat constant is off by ~3x at the top of the model's own range.
//
// Scaling is RELATIVE to the family's own default profile, deliberately:
//
//   - a model left on its defaults prices EXACTLY as it did before, so this
//     cannot regress an H3/Wan placement that works today;
//   - per-family spatial compression cancels out of a same-family ratio
//     entirely, so no absolute calibration is needed - only
//     videoTemporalCompression, and only for the +1 slack.
//
// Tokens go as (W/c)*(H/c)*latentFrames, so area and latent length both enter
// linearly. Floored at the image overhead: a video model asked for one small
// still still owns a 3D VAE.
func videoComputeOverhead(v videoInfo, name string, def genDefaults) float64 {
	base := videoDefaultsFor(v, name)
	if base.width <= 0 || base.height <= 0 || base.frames <= 0 {
		return videoComputeOverheadGB
	}
	w, h, f := def.width, def.height, def.frames
	if w <= 0 {
		w = base.width
	}
	if h <= 0 {
		h = base.height
	}
	if f <= 0 {
		f = base.frames
	}
	comp := videoTemporalCompression(v)
	area := (float64(w) * float64(h)) / (float64(base.width) * float64(base.height))
	length := float64(latentFrames(f, comp)) / float64(latentFrames(base.frames, comp))
	if got := videoComputeOverheadGB * area * length; got > imageComputeOverheadGB {
		return got
	}
	return imageComputeOverheadGB
}

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
		// the checkpoint requires (frames % 8 == 1).
		//
		// 121 is a CONSERVATIVE default, NOT a ceiling. LTX-2.5 is specified for
		// 6-20 seconds, and past 10s it wants 720p/1080p at 24/25fps, which
		// 1280x704 @24 already is. An earlier version of this comment claimed a
		// 153-frame ceiling, read off positional_embedding_max_pos[0]=20 as "20
		// latent frames". That was wrong twice over:
		//
		//   - LTX's positional embedding is rope over fractional coordinates
		//     NORMALIZED by max_pos: get_fractional_positions divides the index
		//     grid BY it. max_pos is a divisor, not the length of a learned
		//     table, so there is no array to run off the end of. Past it you are
		//     extrapolating, which costs coherence, not a hard failure.
		//   - the 20 is almost certainly SECONDS. The same config carries
		//     audio_positional_embedding_max_pos [20], and the audio and video
		//     latents do not share a frame count, so an identical 20 on both
		//     only makes sense on a shared time axis - which is exactly what a
		//     joint AV transformer with use_audio_video_cross_attention needs.
		//     It matches the specified 20s maximum exactly.
		//
		// So clip length here is a VRAM question (see videoComputeOverhead), not
		// a model-capability one. Override.DefaultFrames 241 / 361 / 481 buys
		// 10 / 15 / 20 seconds at 24fps.
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

// resolveGenDefaults is the family profile with the per-model Override applied,
// i.e. the generation settings the model will actually launch with.
//
// Split out of imageCmdLines because it now has TWO readers inside that one
// function and the order between them matters: the video compute overhead is
// priced off this result (so it must resolve BEFORE the offload decision), while
// the --steps/--width/--video-frames argv is emitted at the very end. Resolving
// it twice would let a DefaultFrames override change the emitted clip length
// without changing the placement that clip was sized for, which is precisely the
// mismatch that makes a render spill into shared memory.
func resolveGenDefaults(v videoInfo, name string, ov *Override) genDefaults {
	def := videoDefaultsFor(v, name)
	if ov == nil {
		return def
	}
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
	return def
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
		//
		// The "-conv" hint is load-bearing, not cosmetic. LTX-2.5 publishes TWO
		// video decoders of the same family and latent width, and with no hint
		// pickByHint falls back to the first path in sorted order, which is the
		// SLOW one ("...-vae-bf16" sorts before "...-vae-conv-bf16"). Measured on
		// a 24GB card, same prompts, only the decoder swapped:
		//
		//	0.4MP @  7s	 76s ->	32s
		//	1.0MP @  5s	376s ->	53s
		//	0.3MP @ 13s	437s ->	45s
		//
		// The ratio is 2.4x on the one case that was NOT already spilling and up
		// to 9.7x on the worst, which is the shape of a memory cliff being
		// removed rather than a faster kernel: the conv decoder's peak is flat in
		// the latent grid, so the decode stops overflowing into host memory.
		// A box holding only one of the two still gets it, hint or no hint.
		vae = req("vae", pool.Vae(VaeFamilyLtx, "-conv"))
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
	cmd, budget, graph, offload, missing := imageCmdLines(s, row, ov, arch, name, condHidden, vid)
	lines := cmd.Lines

	archNote := strings.TrimSpace(arch)
	if archNote == "" {
		// H3 declares no arch at all; say so rather than emitting "arch=" and
		// leaving the reader of the config to guess what happened.
		archNote = "(none declared)"
	}
	fmt.Fprintf(b, "\n  # arch=%s family=%s size=%gGB (video model, sd-server, budget=%gGB, max-vram=%gGB, offload=%t)\n", archNote, vid.Kind, row.SizeGB, budget, graph, offload)
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
	// Admission estimate = the budget this model was sized against, not the
	// --max-vram it launches with (that is only the graph headroom left once the
	// resident weights are paid for),
	// the same accepted under-charge the image path makes: the true peak during
	// 3D VAE decode runs above it, and the scheduler's in-flight guard is what
	// actually keeps a second process from spawning underneath a render.
	writeEstVram(b, budget)
	b.WriteString("    checkEndpoint: /\n")
	// Same wiring as the image path, and it rides the model entry for the same
	// reason: the rewrite is not a launch flag (sd-server has no such concept), so
	// the playground reads it off /api/models and offers the rewrite BEFORE the job
	// is posted, which is the only place a rewrite is reviewable.
	//
	// The two directions read t2v / i2v on this path: the Video tab's edit
	// direction is a FIRST-FRAME reference, not an img2img base. Video wants the
	// rewrite more than images do - LTX is trained on single-paragraph audio-visual
	// captions of 150-220 words and degrades on a short prompt, so a bare prompt is
	// out of distribution rather than merely vague - and pays less for it: the
	// enhancer swaps in once, ahead of a render measured in minutes.
	writeEnhancerBlock(b, s, row.ID, ovPromptEnhancer(ov), ovPromptEnhancerEdit(ov), ovEnhancerPrompt(ov), ovEnhancerEditPrompt(ov))
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
