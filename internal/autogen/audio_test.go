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
		{"qwen3", "Qwen3.6-35B-A3B-Q4_K_S.gguf", false}, // ordinary chat LM
		{"llama", "some-llm.gguf", false},
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
	var b strings.Builder
	var emitted []string
	emitTTSModel(&b, s, row, &Override{}, "qwen-talker-1.7b-base-q8_0", "qwen3-tts", &emitted)
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

	// Missing codec surfaces a WARNING and no --codec flag.
	row.CodecPath, row.CodecSizeGB = "", 0
	var b2 strings.Builder
	var em2 []string
	emitTTSModel(&b2, s, row, &Override{}, "talker-nocodec", "qwen3-tts", &em2)
	if !strings.Contains(b2.String(), "WARNING") || strings.Contains(b2.String(), "--codec") {
		t.Errorf("missing-codec emit should warn and omit --codec:\n%s", b2.String())
	}
}
