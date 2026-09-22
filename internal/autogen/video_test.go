package autogen

import (
	"os"
	"path/filepath"
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
		// cfg-scale is 7.0 (H3 conditions at 1.0) and --video-frames is 1.
		// 56 is on H3's 17k+5 alignment grid; 25 would become 39.
		"--cfg-scale 1",
		"--video-frames 56",
		"--fps 24",
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
	// H3 must NOT pin --steps. The base model is not distilled, so sd-server's
	// own 20 has to stand: the 4-step figure belongs to the turbo LoRAs, which
	// arrive per request and cannot be known at launch.
	if strings.Contains(out, "--steps") {
		t.Errorf("H3 must not pin --steps (base model is not distilled):\n%s", out)
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

	lines, _, _, _, _ := imageCmdLines(s, row, ov, "", "h3", 5120, videoInfo{Kind: VideoFamilyMinimaxH3, AudioOut: true})
	joined := lines.Effective
	for _, want := range []string{"--video-frames 49", "--fps 30", "--cfg-scale 1.5", "--audio-vae ov_audio.safetensors"} {
		if !strings.Contains(joined, want) {
			t.Errorf("override missing %q in: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--video-frames 56") {
		t.Errorf("family default should have been replaced: %s", joined)
	}
}

// An image model must emit exactly what it did before video existed: no frames,
// no fps, and only the overheads/defaults it always had.
func TestImageCmdLines_UntouchedByVideo(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4,
		Encoders: EncoderSet{FluxVae: "ae.safetensors", ClipL: "cl.safetensors", T5: "t5.gguf"}}
	row := GgufRow{FullPath: `D:\models\flux.gguf`, SizeGB: 6.0}

	lines, _, _, _, _ := imageCmdLines(s, row, &Override{}, "flux", "flux1-dev", 0, videoInfo{})
	joined := lines.Effective
	for _, unwant := range []string{"--video-frames", "--fps", "--audio-vae", "--cfg-scale", "--steps"} {
		if strings.Contains(joined, unwant) {
			t.Errorf("image cmd should not carry %q: %s", unwant, joined)
		}
	}
}

// The two long-clip VRAM levers are ON by default for a video model and emitted
// for nothing else. --vae-tiling caps the decode spatially, which is the whole
// story for a still; a clip's decode also grows along TIME, and only
// --temporal-tiling chunks that axis. --stream-layers frees the weight residency
// the sampler then spends on latents.
//
// The row here is deliberately sized to OFFLOAD (20GB against a 23GB budget),
// because --stream-layers has a precondition the other lever does not: sd.cpp
// ignores it outright unless the diffusion params live in RAM, which only
// --offload-to-cpu arranges. A resident-weights fixture would assert a flag the
// backend logs and discards. See TestStreamLayers_FollowsOffload.
//
// Wan is the family used here because it is the one whose VAE implements a tiled
// decode: see TestTemporalTiling_GatedOnVaeSupport for the other half.
func TestVideoVramLevers_DefaultOnForVideoOnly(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4,
		Encoders: EncoderSet{T5: "umt5.safetensors"}}
	row := GgufRow{FullPath: `D:\models\wan2.2-t2v.gguf`, SizeGB: 20.0}
	vid := videoInfo{Kind: VideoFamilyWan}

	lines, _, _, _, _ := imageCmdLines(s, row, &Override{}, "", "wan", 0, vid)
	joined := lines.Effective
	for _, want := range []string{"--temporal-tiling", "--stream-layers"} {
		if !strings.Contains(joined, want) {
			t.Errorf("video cmd missing default %q: %s", want, joined)
		}
	}

	// Each toggles off independently: they are separate knobs precisely because
	// --stream-layers trades PCIe traffic for VRAM and --temporal-tiling does not.
	off, _, _, _, _ := imageCmdLines(s, row, &Override{TemporalTiling: "off"}, "", "wan", 0, vid)
	j := off.Effective
	if strings.Contains(j, "--temporal-tiling") {
		t.Errorf("temporalTiling=off must suppress the flag: %s", j)
	}
	if !strings.Contains(j, "--stream-layers") {
		t.Errorf("temporalTiling=off must not touch --stream-layers: %s", j)
	}
	off2, _, _, _, _ := imageCmdLines(s, row, &Override{StreamLayers: "off"}, "", "wan", 0, vid)
	j2 := off2.Effective
	if strings.Contains(j2, "--stream-layers") {
		t.Errorf("streamLayers=off must suppress the flag: %s", j2)
	}
	if !strings.Contains(j2, "--temporal-tiling") {
		t.Errorf("streamLayers=off must not touch --temporal-tiling: %s", j2)
	}

	// An image model has no temporal axis to tile and no long-clip peak to
	// relieve, so neither flag belongs in its launch line at all.
	img := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4,
		Encoders: EncoderSet{FluxVae: "ae.safetensors", ClipL: "cl.safetensors", T5: "t5.gguf"}}
	imgRow := GgufRow{FullPath: `D:\models\flux.gguf`, SizeGB: 6.0}
	iLines, _, _, _, _ := imageCmdLines(img, imgRow, &Override{}, "flux", "flux1-dev", 0, videoInfo{})
	iJoined := iLines.Effective
	for _, unwant := range []string{"--temporal-tiling", "--stream-layers"} {
		if strings.Contains(iJoined, unwant) {
			t.Errorf("image cmd should not carry %q: %s", unwant, iJoined)
		}
	}
}

// The hand-declared path has no tensor scan, so it cannot tell a video model
// from an image one: there the levers are explicit opt-in, not default-on.
func TestExtraImageVramLevers_OptIn(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4}

	plain := extraImageCmdLines(s, ExtraImageModel{
		Name: "h3-st", ModelPath: `D:\models\h3.safetensors`, ModelFlag: "--diffusion-model",
	}).Effective
	for _, unwant := range []string{"--temporal-tiling", "--stream-layers"} {
		if strings.Contains(plain, unwant) {
			t.Errorf("extra model must not default %q on: %s", unwant, plain)
		}
	}

	on := extraImageCmdLines(s, ExtraImageModel{
		Name: "h3-st", ModelPath: `D:\models\h3.safetensors`, ModelFlag: "--diffusion-model",
		TemporalTiling: "on", StreamLayers: "on",
	}).Effective
	for _, want := range []string{"--temporal-tiling", "--stream-layers"} {
		if !strings.Contains(on, want) {
			t.Errorf("extra model opt-in missing %q: %s", want, on)
		}
	}
}

// --temporal-tiling is emitted only for a family whose VAE can actually decode
// in windows along time. sd-server takes the flag from anyone and falls back
// with "does not support temporal tiling ...; processing the full temporal
// dimension", so an ungated flag would put a VRAM lever in the launch line that
// the decode never applies: not an error, just a lie about what is running.
func TestTemporalTiling_GatedOnVaeSupport(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4,
		Encoders: EncoderSet{VideoVae: "vae.safetensors", QwenLlm: "qwen.gguf"}}
	row := GgufRow{FullPath: `D:\models\h3.gguf`, SizeGB: 8.0}
	h3 := videoInfo{Kind: VideoFamilyMinimaxH3}

	// H3's VAE is a transformer autoencoder with no tiled decode path.
	def, _, _, _, _ := imageCmdLines(s, row, &Override{}, "", "h3", 5120, h3)
	j := def.Effective
	if strings.Contains(j, "--temporal-tiling") {
		t.Errorf("H3 VAE cannot tile along time, flag must not be emitted: %s", j)
	}
	// The other lever is untouched by the VAE question, but it has a precondition
	// of its own: this row fits resident (8GB against 23GB), so the weights are
	// not in RAM and sd.cpp would ignore --stream-layers. Absent is correct here.
	if strings.Contains(j, "--stream-layers") {
		t.Errorf("--stream-layers is ignored without offload, do not emit it: %s", j)
	}

	// An explicit "on" forces it anyway, so a backend build that gains support
	// needs an override row rather than a recompile.
	on, _, _, _, _ := imageCmdLines(s, row, &Override{TemporalTiling: "on"}, "", "h3", 5120, h3)
	if !strings.Contains(on.Effective, "--temporal-tiling") {
		t.Errorf("explicit temporalTiling=on must force the flag: %s", on.Effective)
	}
}

// --stream-layers is emitted only where it does something. sd.cpp: "--stream-layers
// has no effect unless diffusion params backend is cpu; ignoring", and the only
// thing that puts the diffusion params in RAM is --offload-to-cpu. So the flag
// tracks the offload decision rather than the video/image one, and a video model
// that fits resident must not carry it.
//
// This is not cosmetic. The flag reads in a launch line like a VRAM lever that
// has already been pulled, so emitting it beside resident weights is how a clip
// that OOMs looks like it was already doing everything it could.
func TestStreamLayers_FollowsOffload(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", TargetVramGB: 24, VramOverheadGB: 1, Threads: 4,
		Encoders: EncoderSet{T5: "umt5.safetensors"}}
	vid := videoInfo{Kind: VideoFamilyWan}

	// 8GB + the 4GB video compute pad fits a 23GB budget: weights stay resident.
	fits := GgufRow{FullPath: `D:\models\wan-small.gguf`, SizeGB: 8.0}
	j := strings.Join(mustLines(imageCmdLines(s, fits, &Override{}, "", "wan", 0, vid)), " ")
	if strings.Contains(j, "--stream-layers") {
		t.Errorf("resident weights: sd.cpp ignores --stream-layers, do not emit it: %s", j)
	}
	if strings.Contains(j, "--offload-to-cpu") {
		t.Fatalf("fixture no longer exercises the resident path: %s", j)
	}

	// 20GB + 4GB does not: --offload-to-cpu puts the params in RAM, which is the
	// one configuration where streaming them back is a real lever.
	big := GgufRow{FullPath: `D:\models\wan-big.gguf`, SizeGB: 20.0}
	j2 := strings.Join(mustLines(imageCmdLines(s, big, &Override{}, "", "wan", 0, vid)), " ")
	if !strings.Contains(j2, "--offload-to-cpu") {
		t.Fatalf("fixture no longer exercises the offload path: %s", j2)
	}
	if !strings.Contains(j2, "--stream-layers") {
		t.Errorf("offloaded weights: --stream-layers must be emitted: %s", j2)
	}

	// An explicit "on" still forces it through, the escape hatch for a backend
	// build that widens the rule.
	j3 := strings.Join(mustLines(imageCmdLines(s, fits, &Override{StreamLayers: "on"}, "", "wan", 0, vid)), " ")
	if !strings.Contains(j3, "--stream-layers") {
		t.Errorf("explicit streamLayers=on must force the flag: %s", j3)
	}
}

// mustLines drops imageCmdLines' sizing returns, which these tests read off the
// rendered argv instead.
func mustLines(cmd ComposedCmd, _, _ float64, _ bool, _ []string) []string { return cmd.Lines }

// --max-vram is a graph-cut budget, not a VRAM cap, so it prices the headroom
// left AFTER everything we told sd-server to keep on the card. Handing it the
// whole budget while the weights sit in that same VRAM is what let a 24GB card
// fail a 2.7GiB compute-buffer allocation on LTX-2.5.
func TestGraphBudget_PricesHeadroomNotTheCard(t *testing.T) {
	row := GgufRow{SizeGB: 14.6}
	comp := imageComponents{}

	// Resident weights come off the top.
	if got := graphBudget(22.3, row, comp, nil, false); got != 7.7 {
		t.Errorf("resident: graphBudget = %v, want 7.7", got)
	}
	// Offloaded weights do not: they are in RAM, so the card is the graph's.
	if got := graphBudget(22.3, row, comp, nil, true); got != 22.3 {
		t.Errorf("offloaded: graphBudget = %v, want 22.3", got)
	}
	// No headroom left hands the question to sd-server rather than guessing a
	// small positive number: -1 is the RESERVE form ("live free VRAM, sparing
	// 1 GiB"). Neither 0 (which means "disable graph splitting") nor a literal
	// 1 (a one-gigabyte graph budget, which GGML_ASSERTs on a video model) is
	// the answer, and the planner knows where the params actually landed.
	if got := graphBudget(14.0, row, comp, nil, false); got != -1 {
		t.Errorf("over budget: graphBudget = %v, want the -1 reserve form", got)
	}
}

// sd-server's planner demotes a video VAE's params to RAM even with headroom to
// spare, and on a 3D VAE that is paid once per TILE rather than once per image:
// measured at 1280x704x73, decode was 85.0s with the params in RAM and 44.6s
// with them resident, sampling unchanged. So a video model that already fits
// restates the plan without the vae=cpu clause.
func TestVideoVaeResidentPlan_KeepsAResidentVaeOnTheGpu(t *testing.T) {
	dir := t.TempDir()
	vae := filepath.Join(dir, "vae.safetensors")
	if err := os.WriteFile(vae, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "sd-server-no-such-file")
	seedBackendDevCache(t, exe, []BackendDevice{
		{ID: "ROCm0", Name: "AMD Radeon RX 7900 XTX", TotalGB: 23.98},
		{ID: "ROCm1", Name: "AMD Radeon(TM) Graphics", TotalGB: 12.17},
	})
	s := Settings{Gpus: GpuSet{{Index: 0, Name: "AMD Radeon RX 7900 XTX", TotalGB: 23.95, FreeGB: 23}}}
	comp := imageComponents{vae: vae}
	ltx := videoInfo{Kind: VideoFamilyLtxAV}
	row := GgufRow{SizeGB: 14.6}

	got := videoVaeResidentPlan(exe, s, row, comp, nil, ltx, 23.0, false)
	want := `--backend "diffusion=ROCm0,te=ROCm0,vae=ROCm0" --params-backend "te=cpu"`
	if got != want {
		t.Errorf("videoVaeResidentPlan = %q, want %q", got, want)
	}

	// Every other caller keeps auto-fit. An image VAE decodes once per
	// generation, so streaming its weights is a rounding error and not worth
	// disabling the planner for.
	if p := videoVaeResidentPlan(exe, s, row, comp, nil, videoInfo{}, 23.0, false); p != "" {
		t.Errorf("image model got a plan %q, want auto-fit left alone", p)
	}
	// A model that offloads has no resident DiT to plan around, and the offload
	// path has its own VAE handling.
	if p := videoVaeResidentPlan(exe, s, row, comp, nil, ltx, 23.0, true); p != "" {
		t.Errorf("offloading model got a plan %q, want auto-fit left alone", p)
	}
	// Pinning the VAE spends from the same pot the decode graph draws on, so a
	// card with no room left must not trade one failure for another: sd-server
	// priced that graph at 9062.89 MB against 8162.31 MB available and refused.
	if p := videoVaeResidentPlan(exe, s, row, comp, nil, ltx, 18.0, false); p != "" {
		t.Errorf("no-headroom card got a plan %q, want auto-fit left alone", p)
	}
	// A second card changes who should decide. auto-fit can put the text encoder
	// in GPU1's VRAM, which this plan cannot express, and offload=false was
	// decided against the whole set's budget.
	two := Settings{Gpus: GpuSet{
		{Index: 0, Name: "AMD Radeon RX 7900 XTX", TotalGB: 23.95, FreeGB: 23},
		{Index: 1, Name: "AMD Radeon RX 7900 XTX", TotalGB: 23.95, FreeGB: 23},
	}}
	if p := videoVaeResidentPlan(exe, two, row, comp, nil, ltx, 23.0, false); p != "" {
		t.Errorf("multi-GPU box got a plan %q, want auto-fit left alone", p)
	}
	// VaeOnCpu is the escape hatch for a backend that whitens the VAE; it wins.
	if p := videoVaeResidentPlan(exe, s, row, comp, &Override{VaeOnCpu: "on"}, ltx, 23.0, false); p != "" {
		t.Errorf("VaeOnCpu=on got a plan %q, want the VAE left on the CPU", p)
	}
	// Refusal when the backend does not confirm the device by name: a placement
	// built on a guess is worse than the planner's own answer.
	if p := videoVaeResidentPlan(filepath.Join(dir, "unprobed"), s, row, comp, nil, ltx, 23.0, false); p != "" {
		t.Errorf("unprobed backend got a plan %q, want auto-fit left alone", p)
	}
}
