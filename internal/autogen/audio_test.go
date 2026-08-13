package autogen

import (
	"strings"
	"testing"
)

func TestIsTTSModel(t *testing.T) {
	cases := []struct {
		arch string
		file string
		want bool
	}{
		{"qwen3-tts", "qwen-talker-1.7b-base-Q8_0.gguf", true}, // arch hit
		{"qwen3", "qwen-talker-0.6b-base-Q4_K_M.gguf", true},   // bare LM arch, name fallback
		{"qwentts-codec", "qwen-tokenizer-12hz-Q8_0.gguf", true},
		{"kokoro", "Kokoro_no_espeak_Q8_0.gguf", true}, // TTS.cpp, arch hit
		{"", "Kokoro_espeak_f16.gguf", true},           // TTS.cpp, name fallback
		{"", "Parler_TTS_Mini_Q5_0.gguf", true},
		{"qwen3", "Qwen3.6-35B-A3B-Q4_K_S.gguf", false}, // ordinary chat LM
		{"llama", "some-llm.gguf", false},
		{"llama", "Diamond-Coder-7B-Q4_K_M.gguf", false}, // "dia" must not misroute
	}
	for _, c := range cases {
		if got := IsTTSModel(Metadata{Architecture: c.arch}, c.file); got != c.want {
			t.Errorf("IsTTSModel(%q, %q) = %v, want %v", c.arch, c.file, got, c.want)
		}
	}
}

func TestEmitTTSModel(t *testing.T) {
	s := Settings{TtsServerExe: "tts-server", TtlSec: 600}
	row := GgufRow{
		FullPath:    "/models/tts/qwen-talker-1.7b-base-Q8_0.gguf",
		FileName:    "qwen-talker-1.7b-base-Q8_0.gguf",
		SizeGB:      1.0,
		CodecPath:   "/models/tts/qwen-tokenizer-12hz-Q8_0.gguf",
		CodecSizeGB: 0.5,
	}
	meta := Metadata{Architecture: "qwen3-tts"}
	var b strings.Builder
	var emitted []string
	emitTTSModel(&b, s, row, &Override{}, "qwen-talker-1.7b-base-q8_0", meta, &emitted)
	out := b.String()

	for _, want := range []string{
		"tts-server",
		"--model /models/tts/qwen-talker-1.7b-base-Q8_0.gguf",
		"--codec /models/tts/qwen-tokenizer-12hz-Q8_0.gguf",
		"--port ${PORT}",
		"checkEndpoint: /health",
		"out: [audio]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted YAML missing %q:\n%s", want, out)
		}
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted = %v, want 1 entry", emitted)
	}
	// qwentts registers voices from a clip; the playground's clone UI is gated on
	// this capability, not on whether the model reported named speakers.
	if !strings.Contains(out, "voiceClone: true") {
		t.Errorf("qwentts emit should declare voiceClone:\n%s", out)
	}
	// qwentts accepts whatever model name the request carries, so the rewrite is
	// TTS.cpp-only — emitting it here would just hide the real id from the logs.
	if strings.Contains(out, "useModelName") {
		t.Errorf("qwentts emit should not rewrite the model name:\n%s", out)
	}

	// Missing codec surfaces a WARNING and no --codec flag.
	row.CodecPath, row.CodecSizeGB = "", 0
	var b2 strings.Builder
	var em2 []string
	emitTTSModel(&b2, s, row, &Override{}, "talker-nocodec", meta, &em2)
	if !strings.Contains(b2.String(), "WARNING") || strings.Contains(b2.String(), "--codec") {
		t.Errorf("missing-codec emit should warn and omit --codec:\n%s", b2.String())
	}
}

// A TTS.cpp model must render TTS.cpp's own flags (--model-path, no codec, no
// voices dir) and pick the ttscpp registry entry even though a qwentts entry is
// present and marked as the class default.
func TestEmitTTSModel_TTSCpp(t *testing.T) {
	s := Settings{
		TtsServerExe: "C:/qwentts/tts-server.exe",
		TtlSec:       600,
		Backends: []BackendEntry{
			{ID: "qwentts", Kind: "tts", Name: "tts-server", Path: "C:/qwentts/tts-server.exe", Default: true},
			{ID: "ttscpp", Kind: "ttscpp", Name: "TTS.cpp", Path: "C:/ttscpp/tts-server.exe"},
		},
	}
	row := GgufRow{
		FullPath: "D:/models/tts/Kokoro_no_espeak_Q8_0.gguf",
		FileName: "Kokoro_no_espeak_Q8_0.gguf",
		SizeGB:   0.1,
	}
	meta := Metadata{Architecture: "kokoro"}
	var b strings.Builder
	var emitted []string
	emitTTSModel(&b, s, row, &Override{}, "kokoro-q8_0", meta, &emitted)
	out := b.String()

	for _, want := range []string{
		"C:/ttscpp/tts-server.exe",
		"--model-path D:/models/tts/Kokoro_no_espeak_Q8_0.gguf",
		"--port ${PORT}",
		"checkEndpoint: /health",
		"out: [audio]",
		// TTS.cpp 400s "Invalid Model: kokoro-q8_0" without this rewrite: its model
		// map is keyed by gguf file stem, not by our model id.
		`useModelName: "Kokoro_no_espeak_Q8_0"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted YAML missing %q:\n%s", want, out)
		}
	}
	// Kokoro's speakers are a baked-in pack and TTS.cpp has no clone route, so the
	// capability must be absent or the Speech tab offers a button that 404s.
	for _, unwanted := range []string{"--codec", "--voices-dir", "WARNING", "C:/qwentts", "voiceClone"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("TTS.cpp emit should not contain %q:\n%s", unwanted, out)
		}
	}

	// A Qwen talker in the same fleet still resolves to qwentts, even though the
	// registry also holds a TTS.cpp entry.
	talker := GgufRow{
		FullPath:  "D:/models/tts/qwen-talker-1.7b-base-Q8_0.gguf",
		FileName:  "qwen-talker-1.7b-base-Q8_0.gguf",
		CodecPath: "D:/models/tts/qwen-tokenizer-12hz-Q8_0.gguf",
	}
	got := strings.Join(ttsCmdLines(s, talker, &Override{}, Metadata{Architecture: "qwen3-tts"}), " ")
	if !strings.Contains(got, "C:/qwentts/tts-server.exe") || !strings.Contains(got, "--codec") {
		t.Errorf("talker should keep the qwentts backend and codec flag: %s", got)
	}

	// An explicit per-model backend override outranks the format preference.
	ov := &Override{Backend: "qwentts"}
	got = strings.Join(ttsCmdLines(s, row, ov, meta), " ")
	if !strings.Contains(got, "C:/qwentts/tts-server.exe") {
		t.Errorf("explicit Override.Backend should win: %s", got)
	}
}

// With no TTS.cpp entry registered, the emit must warn instead of quietly
// launching the qwentts binary against weights it cannot parse.
func TestEmitTTSModel_TTSCppNoBackendWarns(t *testing.T) {
	s := Settings{TtsServerExe: "C:/qwentts/tts-server.exe", TtlSec: 600}
	row := GgufRow{FullPath: "D:/models/tts/Kokoro_espeak_f16.gguf", FileName: "Kokoro_espeak_f16.gguf"}
	var b strings.Builder
	var emitted []string
	emitTTSModel(&b, s, row, &Override{}, "kokoro-f16", Metadata{Architecture: "kokoro"}, &emitted)
	if !strings.Contains(b.String(), "WARNING") {
		t.Errorf("unregistered TTS.cpp backend should warn:\n%s", b.String())
	}
}
