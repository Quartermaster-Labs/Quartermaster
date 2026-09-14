package autogen

import (
	"strings"
	"testing"
)

// Detection has to survive the two traps described in video.go: a model with no
// metadata at all must still be found, and arch "wan" must NOT be treated as
// video (it is ERNIE-Image-Turbo, an image model).
func TestIsVideoModel(t *testing.T) {
	h3 := Metadata{VideoKind: VideoFamilyMinimaxH3, HasAudioOut: true}
	if !IsVideoModel(h3) {
		t.Error("MiniMax-H3 (no arch, video tensors) should classify as video")
	}
	ernie := Metadata{Architecture: "wan"}
	if IsVideoModel(ernie) {
		t.Error("arch=wan alone is ERNIE-Image-Turbo, an IMAGE model, not video")
	}
	if !isImageArch(ernie.Architecture) {
		t.Error("arch=wan must keep taking the image path")
	}
}

func TestEmitVideoModel_MinimaxH3(t *testing.T) {
	var b strings.Builder
	var emitted []string
	enc := EncoderSet{VideoVae: "h3_video_vae.safetensors", AudioVae: "h3_audio_vae.safetensors", QwenLlm: "qwen3vl32b.gguf"}
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 24, VramOverheadGB: 1, Threads: 7, Encoders: enc}
	row := GgufRow{FullPath: `D:\models\minimax_h3-Q4_K_M.gguf`, SizeGB: 8.0}
	vid := videoInfo{Kind: VideoFamilyMinimaxH3, AudioOut: true}

	// Arch is empty on purpose: H3's gguf carries no KVs whatsoever.
	emitVideoModel(&b, s, row, &Override{}, "minimax-h3", "", vid, 5120, &emitted)
	out := b.String()

	for _, want := range []string{
		"sd-server",
		"--diffusion-model D:/models/minimax_h3-Q4_K_M.gguf",
		"--vae h3_video_vae.safetensors",
		"--audio-vae h3_audio_vae.safetensors",
		"--llm qwen3vl32b.gguf",
		// Both of these are load-bearing, not preferences: the built-in
		// cfg-scale is 7.0 (H3 aborts above 1.0) and --video-frames is 1.
		"--cfg-scale 1",
		"--video-frames 25",
		"--fps 24",
		"--steps 4",
		"out: [video, audio]",
		"family=minimax_h3",
		"(none declared)",
		"checkEndpoint: /",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emit missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("complete component set should not warn:\n%s", out)
	}
	// 8GB weights + 4GB video compute overhead fits a 23GB budget.
	if strings.Contains(out, "--offload-to-cpu") {
		t.Errorf("model that fits should not offload:\n%s", out)
	}
	if len(emitted) != 1 || emitted[0] != "minimax-h3" {
		t.Errorf("emitted = %v, want [minimax-h3]", emitted)
	}
}

// A video model whose components are not on disk must SAY so in the config
// rather than emit a command that fails at the first request.
func TestEmitVideoModel_MissingComponents(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 8, VramOverheadGB: 1, Threads: 4}
	row := GgufRow{FullPath: `D:\models\h3.gguf`, SizeGB: 8.0}

	emitVideoModel(&b, s, row, &Override{}, "h3", "", videoInfo{Kind: VideoFamilyMinimaxH3, AudioOut: true}, 5120, &emitted)
	out := b.String()
	for _, want := range []string{"WARNING", "vae", "llm", "audio_vae"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing-component emit should name %q:\n%s", want, out)
		}
	}
	// 8GB + 4GB video overhead blows a 7GB budget.
	if !strings.Contains(out, "--offload-to-cpu") {
		t.Errorf("oversized video model should offload:\n%s", out)
	}
}

// An audio-less video family wires no --audio-vae and declares no audio out.
func TestEmitVideoModel_WanNoAudio(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 24, VramOverheadGB: 1, Threads: 7,
		Encoders: EncoderSet{T5: "umt5.gguf"}}
	row := GgufRow{FullPath: `D:\models\wan22.gguf`, SizeGB: 9.0}

	emitVideoModel(&b, s, row, &Override{}, "wan22", "wan", videoInfo{Kind: VideoFamilyWan}, 0, &emitted)
	out := b.String()
	if strings.Contains(out, "--audio-vae") {
		t.Errorf("Wan has no audio branch, should wire no audio vae:\n%s", out)
	}
	if !strings.Contains(out, "out: [video]") {
		t.Errorf("Wan should declare video-only output:\n%s", out)
	}
	for _, want := range []string{"--t5xxl umt5.gguf", "--video-frames 81", "--fps 16"} {
		if !strings.Contains(out, want) {
			t.Errorf("emit missing %q:\n%s", want, out)
		}
	}
	// Wan is not distilled, so steps/cfg stay at sd-server's own defaults.
	if strings.Contains(out, "--cfg-scale") || strings.Contains(out, "--steps") {
		t.Errorf("Wan should not pin steps/cfg:\n%s", out)
	}
}

// Per-model overrides still win over the family profile.
func TestVideoDefaults_OverrideWins(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4}
	row := GgufRow{FullPath: `D:\models\h3.gguf`, SizeGB: 8.0}
	ov := &Override{DefaultFrames: 49, DefaultFps: 30, DefaultCfg: 1.5, AudioVaePath: "ov_audio.safetensors"}

	lines, _, _, _ := imageCmdLines(s, row, ov, "", "h3", 5120, videoInfo{Kind: VideoFamilyMinimaxH3, AudioOut: true})
	joined := strings.Join(lines, " ")
	for _, want := range []string{"--video-frames 49", "--fps 30", "--cfg-scale 1.5", "--audio-vae ov_audio.safetensors"} {
		if !strings.Contains(joined, want) {
			t.Errorf("override missing %q in: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--video-frames 25") {
		t.Errorf("family default should have been replaced: %s", joined)
	}
}

// An image model must emit exactly what it did before video existed: no frames,
// no fps, and only the overheads/defaults it always had.
func TestImageCmdLines_UntouchedByVideo(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4,
		Encoders: EncoderSet{FluxVae: "ae.safetensors", ClipL: "cl.safetensors", T5: "t5.gguf"}}
	row := GgufRow{FullPath: `D:\models\flux.gguf`, SizeGB: 6.0}

	lines, _, _, _ := imageCmdLines(s, row, &Override{}, "flux", "flux1-dev", 0, videoInfo{})
	joined := strings.Join(lines, " ")
	for _, unwant := range []string{"--video-frames", "--fps", "--audio-vae", "--cfg-scale", "--steps"} {
		if strings.Contains(joined, unwant) {
			t.Errorf("image cmd should not carry %q: %s", unwant, joined)
		}
	}
}
