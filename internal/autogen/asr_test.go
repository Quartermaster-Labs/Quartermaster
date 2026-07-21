package autogen

import (
	"strings"
	"testing"
)

func TestIsASRModel(t *testing.T) {
	cases := []struct {
		arch string
		file string
		want bool
	}{
		{"parakeet", "parakeet-tdt-0.6b-v3-q8_0.gguf", true},
		{"", "parakeet-tdt-0.6b-v3-q8_0.gguf", true},
		{"fastconformer", "whatever.gguf", true},
		{"", "nemotron-3.5-asr-streaming-0.6b-q8_0.gguf", true},
		{"", "nemotron-speech-streaming-en-0.6b-q8_0.gguf", true},
		// Nemotron TEXT models must keep routing to llama-server.
		{"nemotron", "Nemotron-4-340B-Instruct-Q4_K_M.gguf", false},
		{"llama", "Meta-Llama-3-8B-Q8_0.gguf", false},
		{"qwen3", "qwen-talker-1.7b-base-Q8_0.gguf", false},
	}
	for _, c := range cases {
		if got := IsASRModel(Metadata{Architecture: c.arch}, c.file); got != c.want {
			t.Errorf("IsASRModel(%q, %q) = %v, want %v", c.arch, c.file, got, c.want)
		}
	}
}

func TestEmitASRModel(t *testing.T) {
	s := Settings{AsrServerExe: "parakeet-server", TtlSec: 600}
	row := GgufRow{
		FullPath: `C:\models\asr\parakeet-tdt-0.6b-v3-q8_0.gguf`,
		FileName: "parakeet-tdt-0.6b-v3-q8_0.gguf",
		SizeGB:   0.7,
	}
	var b strings.Builder
	var emitted []string
	emitASRModel(&b, s, row, nil, "parakeet-tdt-0.6b-v3", "parakeet", &emitted)

	out := b.String()
	for _, want := range []string{
		"parakeet-server",
		"--model C:/models/asr/parakeet-tdt-0.6b-v3-q8_0.gguf",
		"--port ${PORT}",
		"ttl: 600",
		"checkEndpoint: none",
		"in: [audio]",
		"out: [text]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted config missing %q:\n%s", want, out)
		}
	}
	if len(emitted) != 1 || emitted[0] != "parakeet-tdt-0.6b-v3" {
		t.Errorf("emitted = %v, want one entry", emitted)
	}
}
