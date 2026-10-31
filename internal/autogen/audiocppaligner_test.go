package autogen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectQwen3Aligner(t *testing.T) {
	dir := t.TempDir()
	aligner := filepath.Join(dir, "Qwen3-ForcedAligner-0.6B-GGUF", "qwen3-forced-aligner-0.6b-q8_0.gguf")
	// Named like an aligner, but the header says otherwise: not taken.
	decoy := filepath.Join(dir, "decoy", "aligned-chat-q8_0.gguf")
	for _, p := range []string{aligner, decoy} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGgufKVs(t, aligner, [][2]any{{"general.architecture", "audiocpp"}, {"audiocpp.model_spec.family", "qwen3_forced_aligner"}})
	writeGgufKVs(t, decoy, [][2]any{{"general.architecture", "llama"}})

	rows := []GgufRow{
		{FullPath: decoy, FileName: filepath.Base(decoy), SizeGB: 4},
		{FullPath: aligner, FileName: filepath.Base(aligner), SizeGB: 0.9},
	}
	got := detectQwen3Aligner(rows)
	if got == nil || got.path != filepath.ToSlash(aligner) || got.sizeGB != 0.9 {
		t.Fatalf("detectQwen3Aligner = %+v, want %s at 0.9GB", got, aligner)
	}
	if detectQwen3Aligner(rows[:1]) != nil {
		t.Fatal("a non-aligner header was taken for the aligner")
	}
}

func TestEmitAudioCppModel_ASRWithAligner(t *testing.T) {
	// A source-build layout: the binary three levels below the checkout root
	// that holds assets/.
	root := t.TempDir()
	exe := filepath.Join(root, "build", "hip", "bin", "audiocpp_server.exe")
	vad := filepath.Join(root, "assets", "framework", "models", "silero_vad")
	for _, d := range []string{filepath.Dir(exe), vad} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := audioCppSettings("vulkan")
	s.Backends[0].Path = exe
	s.qwen3Aligner = &audioCppAligner{path: "D:/models/qwen3-forced-aligner-0.6b-q8_0.gguf", sizeGB: 0.9}

	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, audioCppRow("/models/speech/qwen3-asr-1.7b-q8_0.gguf"), &Override{}, "qwen3-asr-1.7b-q8_0", Metadata{Architecture: "audiocpp", AudioFamily: "qwen3_asr"}, &emitted)
	out := b.String()

	for _, want := range []string{
		`qwen3_asr.forced_aligner_path: "D:/models/qwen3-forced-aligner-0.6b-q8_0.gguf"`,
		`qwen3_asr.vad_model_path: "` + filepath.ToSlash(vad) + `"`,
		// 0.2 (row) + 0.4 (asr overhead) + 0.9 (aligner).
		"estVramGB: 1.5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted YAML missing %q:\n%s", want, out)
		}
	}
}

func TestEmitAudioCppModel_ASRWithoutAligner(t *testing.T) {
	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, audioCppSettings("vulkan"), audioCppRow("/models/speech/qwen3-asr-1.7b-q8_0.gguf"), &Override{}, "qwen3-asr-1.7b-q8_0", Metadata{Architecture: "audiocpp", AudioFamily: "qwen3_asr"}, &emitted)
	out := b.String()
	if strings.Contains(out, "forced_aligner_path") || strings.Contains(out, "vad_model_path") {
		t.Errorf("emitted timestamp options with no aligner on disk:\n%s", out)
	}
	if !strings.Contains(out, "no word timestamps") {
		t.Errorf("missing the no-aligner note:\n%s", out)
	}
	if !strings.Contains(out, "estVramGB: 0.6") {
		t.Errorf("estimate changed with no aligner:\n%s", out)
	}
}
