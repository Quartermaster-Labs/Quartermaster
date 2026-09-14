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

func TestASRCmdLines_BackendPin(t *testing.T) {
	rows := []BackendEntry{
		{ID: "managed-parakeet", Kind: "asr", Path: "C:/asr/new/parakeet-server.exe"},
		{ID: "build-parakeet-0.4", Kind: "asr", Path: "C:/asr/old/parakeet-server.exe"},
	}
	withDefault := func(id string) []BackendEntry {
		out := append([]BackendEntry(nil), rows...)
		for i := range out {
			out[i].Default = out[i].ID == id
		}
		return out
	}
	row := GgufRow{
		FullPath: `C:\models\asr\parakeet-tdt-0.6b-v3-q8_0.gguf`,
		FileName: "parakeet-tdt-0.6b-v3-q8_0.gguf",
	}

	cases := []struct {
		name string
		def  string
		pin  string
		want string
	}{
		// Same rule as every other class: only an auto model follows a ★ switch.
		{"pin wins over the default", "managed-parakeet", "build-parakeet-0.4", "C:/asr/old/parakeet-server.exe"},
		{"pin wins after the default switched", "build-parakeet-0.4", "managed-parakeet", "C:/asr/new/parakeet-server.exe"},
		{"auto follows the default", "managed-parakeet", "", "C:/asr/new/parakeet-server.exe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Settings{AsrServerExe: "parakeet-server", Backends: withDefault(tc.def)}
			got := asrCmdLines(s, row, &Override{Backend: tc.pin})[0]
			if got != tc.want {
				t.Errorf("exe = %q, want %q", got, tc.want)
			}
		})
	}

	// Single-backend setup: nothing in the registry, legacy exe unchanged.
	s := Settings{AsrServerExe: "parakeet-server"}
	if got := asrCmdLines(s, row, nil)[0]; got != "parakeet-server" {
		t.Errorf("legacy exe = %q, want parakeet-server", got)
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
