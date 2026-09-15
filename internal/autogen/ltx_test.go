package autogen

import (
	"strings"
	"testing"
)

// The LTX pair of autoencoders must classify from their OWN headers, and must
// not be confused with either of the two 3D VAE families already in the pool.
// All four tensor names below are read off the real bf16 files.
func TestAutogen_ClassifyLtxVaes(t *testing.T) {
	video := classifySafetensors(map[string]stTensor{
		"decoder.conv_in.conv.weight": {Shape: []int64{1024, 128, 3, 3, 3}},
		"encoder.conv_in.conv.weight": {Shape: []int64{128, 48, 3, 3, 3}},
	}, 1.4, "/m/ltx-2.5-video-vae-conv-bf16.safetensors")
	if video.Role != RoleVae || video.Family != VaeFamilyLtx {
		t.Fatalf("video vae = %s/%s, want vae/%s", video.Role, video.Family, VaeFamilyLtx)
	}
	if video.Width != 128 {
		t.Errorf("latent channels = %d, want 128", video.Width)
	}

	audio := classifySafetensors(map[string]stTensor{
		"audio_vae.decoder.conv_in.conv.weight": {Shape: []int64{512, 8, 3, 3}},
		"vocoder.conv_pre.weight":               {Shape: []int64{512, 128, 7}},
	}, 0.35, "/m/ltx-2.5-audio-vae-bf16.safetensors")
	if audio.Role != RoleAudioVae || audio.Family != VaeFamilyLtx {
		t.Fatalf("audio vae = %s/%s, want audio-vae/%s", audio.Role, audio.Family, VaeFamilyLtx)
	}

	// The two other video families must still classify as themselves: the
	// difference is one level of module wrapping (conv_in.CONV.weight), which is
	// exactly the kind of thing a loose match would swallow.
	wan := classifySafetensors(map[string]stTensor{
		"conv1.weight": {Shape: []int64{96, 16, 3, 3, 3}},
	}, 0.2, "/m/wan_2.1_vae.safetensors")
	if wan.Family != VaeFamilyWan3D {
		t.Errorf("wan vae = %s, want %s", wan.Family, VaeFamilyWan3D)
	}
	h3 := classifySafetensors(map[string]stTensor{
		"encoder.conv_in.weight": {Shape: []int64{1024, 3, 2, 8, 8}},
	}, 0.5, "/m/h3_video_vae.safetensors")
	if h3.Family != VaeFamilyVideo3D {
		t.Errorf("h3 vae = %s, want %s", h3.Family, VaeFamilyVideo3D)
	}
}

// Two audio VAEs on one disk must not cross-wire. They are unrelated networks
// over unrelated latents, so a mismatch is a failed load, not a worse soundtrack.
func TestAutogen_AudioVaeIsFamilyScoped(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/h3_audio_vae.safetensors", Role: RoleAudioVae, Family: VaeFamilyVideo3D},
		{Path: "/m/ltx-2.5-audio-vae-bf16.safetensors", Role: RoleAudioVae, Family: VaeFamilyLtx},
	}}
	if got := p.AudioVae(VaeFamilyLtx); got != "/m/ltx-2.5-audio-vae-bf16.safetensors" {
		t.Errorf("AudioVae(ltx) = %q", got)
	}
	if got := p.AudioVae(VaeFamilyVideo3D); got != "/m/h3_audio_vae.safetensors" {
		t.Errorf("AudioVae(video3d) = %q", got)
	}
}

// LTX's text encoder is a Gemma-4-12B republished WITH the caption projection
// baked in. A stock Gemma-3-12B is the same 3840 wide and is usually the larger
// file, so width alone picks the wrong one: the path hint is the tiebreak.
func TestAutogen_LtxPrefersProjectedEncoder(t *testing.T) {
	pool := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/ltx/video-vae.safetensors", Role: RoleVae, Family: VaeFamilyLtx, Width: 128},
		{Path: "/m/ltx/audio-vae.safetensors", Role: RoleAudioVae, Family: VaeFamilyLtx},
		{Path: "/m/gemma3-12b-Q8_0.gguf", Role: RoleLlm, Width: 3840, SizeGB: 13},
		{Path: "/m/elix3r/gemma4-12b-with-proj-ltx-2.5-Q5_K_M.gguf", Role: RoleLlm, Width: 3840, SizeGB: 9.5},
	}}
	vid := videoInfo{Kind: VideoFamilyLtxAV, AudioOut: true}
	c, missing := resolveComponents(EncoderSet{}, nil, "ltxv", "ltx-2.5-22b-distilled-transformer-Q4_K_M", pool, 3840, vid)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if !strings.Contains(c.llm, "with-proj") {
		t.Errorf("llm = %q, want the with-proj encoder, not the bigger stock Gemma", c.llm)
	}
	if c.vae != "/m/ltx/video-vae.safetensors" || c.audioVae != "/m/ltx/audio-vae.safetensors" {
		t.Errorf("vae/audio = %q/%q", c.vae, c.audioVae)
	}
	// No CLIP and no T5: LTX conditions on the projected LLM alone.
	if c.clipL != "" || c.t5 != "" {
		t.Errorf("ltx must take neither clip nor t5: %+v", c)
	}

	// An explicit pin still beats the hint: settings.encoders is the user's word.
	c2, _ := resolveComponents(EncoderSet{QwenLlm: "/m/gemma3-12b-Q8_0.gguf"}, nil, "ltxv", "ltx-2.5-dev", pool, 3840, vid)
	if c2.llm != "/m/gemma3-12b-Q8_0.gguf" {
		t.Errorf("declared qwenLlm should win, got %q", c2.llm)
	}
}

// Distilled and dev are TENSOR-IDENTICAL, so the profile is name-derived. This
// is the one place in the package where that is acceptable, and getting it wrong
// is loud: 8 steps on dev is noise, cfg 3.0 on distilled is scorched.
func TestAutogen_LtxDistilledVsDev(t *testing.T) {
	vid := videoInfo{Kind: VideoFamilyLtxAV, AudioOut: true}

	d := videoDefaultsFor(vid, "ltx-2.5-22b-distilled-transformer-Q4_K_M")
	if d.steps != 8 || d.cfg != 1.0 {
		t.Errorf("distilled = steps %d cfg %v, want 8 / 1.0", d.steps, d.cfg)
	}
	dev := videoDefaultsFor(vid, "ltx-2.5-22b-dev-transformer-Q4_K_M")
	if dev.steps != 0 || dev.cfg != 3.0 {
		t.Errorf("dev = steps %d cfg %v, want unpinned / 3.0", dev.steps, dev.cfg)
	}
	// Framing is shared, and 121 is on the 8k+1 grid under the 153-frame
	// positional-embedding ceiling.
	for _, g := range []genDefaults{d, dev} {
		if g.width != 1280 || g.height != 704 || g.frames != 121 || g.fps != 24 {
			t.Errorf("framing = %dx%d %df @%d, want 1280x704 121f @24", g.width, g.height, g.frames, g.fps)
		}
		if (g.frames-1)%8 != 0 || g.frames > 153 {
			t.Errorf("frames %d is off LTX's 8k+1 grid or past the 153 ceiling", g.frames)
		}
	}
	// The other families must be untouched by the new name argument.
	if h3 := videoDefaultsFor(videoInfo{Kind: VideoFamilyMinimaxH3}, "minimax-h3-distilled"); h3.steps != 0 || h3.frames != 56 {
		t.Errorf("h3 profile changed: %+v", h3)
	}
}

// LTX's VAE has a real streaming tiled decode
// (LTXVideoVAE::decode_temporal_tiled_streaming), so the flag is honest for it.
// H3's is still a transformer autoencoder with no tiled path, and the flag is a
// lie there.
func TestAutogen_LtxTemporalTiling(t *testing.T) {
	if !vaeTemporalTiling(videoInfo{Kind: VideoFamilyLtxAV}) {
		t.Error("LTX's VAE implements temporal tiling")
	}
	if vaeTemporalTiling(videoInfo{Kind: VideoFamilyMinimaxH3}) {
		t.Error("H3 has no tiled decode path")
	}
}

// End to end: the emitted YAML has to carry both autoencoders, the projected
// encoder, the distilled schedule, and the audio capability the Video tab reads.
func TestAutogen_EmitVideoModel_Ltx(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 24, VramOverheadGB: 1, Threads: 7}
	row := GgufRow{FullPath: `D:\models\ltx-2.5-22b-distilled-transformer-Q4_K_M.gguf`, SizeGB: 15.7}
	vid := videoInfo{Kind: VideoFamilyLtxAV, AudioOut: true}
	ov := &Override{
		VaePath:         "D:/m/ltx-video-vae.safetensors",
		AudioVaePath:    "D:/m/ltx-audio-vae.safetensors",
		TextEncoderPath: "D:/m/gemma4-12b-with-proj.gguf",
	}

	emitVideoModel(&b, s, row, ov, "ltx-2.5-22b-distilled-Q4_K_M", "ltxv", vid, 3840, &emitted)
	out := b.String()
	for _, want := range []string{
		"--diffusion-model D:/models/ltx-2.5-22b-distilled-transformer-Q4_K_M.gguf",
		"--vae D:/m/ltx-video-vae.safetensors",
		"--audio-vae D:/m/ltx-audio-vae.safetensors",
		"--llm D:/m/gemma4-12b-with-proj.gguf",
		"--steps 8",
		"--cfg-scale 1",
		"--video-frames 121",
		"--fps 24",
		"--temporal-tiling",
		"out: [video, audio]",
		"family=ltxav",
		"arch=ltxv",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emit missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("complete component set should not warn:\n%s", out)
	}
	// The embeddings connector is IN the 2.5 checkpoint (use_embeddings_connector
	// with the connector layers present), so pointing the flag at an external
	// file would load a second, unrelated copy.
	if strings.Contains(out, "--embeddings-connectors") {
		t.Errorf("connector is baked into the checkpoint, must not be wired:\n%s", out)
	}
}

// LTX states its encoder width ONLY in a diffusers config json blob: the caption
// projection lives in the encoder file, not the DiT, so no tensor here carries
// 3840 and condHiddenFrom returns 0.
func TestAutogen_CaptionChannelsFromConfig(t *testing.T) {
	if got := captionChannelsFrom(`{"transformer":{"caption_channels":3840,"cross_attention_dim":4096}}`); got != 3840 {
		t.Errorf("caption_channels = %d, want 3840", got)
	}
	// Anything that is not such a blob costs one failed match and nothing else.
	for _, s := range []string{"", "not json", `{"transformer":{}}`, `{"caption_channels":`} {
		if got := captionChannelsFrom(s); got != 0 {
			t.Errorf("captionChannelsFrom(%q) = %d, want 0", s, got)
		}
	}
}

// The LTX encoder parses as a perfectly good Gemma chat model, so without the
// filename rule it is SERVED as one: a strictly worse Gemma than the stock file
// beside it, carrying a projection head no chat client will ever use.
func TestAutogen_WithProjIsNotServed(t *testing.T) {
	if !encoderFileRe.MatchString("gemma4-12b-with-proj-ltx-2.5-Q5_K_M.gguf") {
		t.Error("with-proj encoder should be skipped as a diffusion component")
	}
	// The rule must stay narrow: an ordinary chat gguf is untouched.
	for _, n := range []string{"gemma3-12b-it-Q8_0.gguf", "Qwen3-4B-Instruct-2507-Q6_K.gguf", "projector-test.gguf"} {
		if encoderFileRe.MatchString(n) {
			t.Errorf("%q should still be served as a model", n)
		}
	}
}
