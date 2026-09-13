package server

import (
	"errors"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// The boot check names what will make a spawn die: a flag the emitter writes
// that this backend build dropped, and a flag neither source knows. Commands
// that are not llama-server (sd-server, tts-server, ...) are not walked, and a
// backend whose --help could not be read produces no claim at all.
func TestLaunchFlagWarnings(t *testing.T) {
	models := map[string]config.ModelConfig{
		"good":  {Cmd: "llama-server.exe -m a.gguf -c 4096 -fa on"},
		"stale": {Cmd: `C:/llama/llama-server.exe -m b.gguf -c 4096 -cms 256 --made-up`},
		"image": {Cmd: "sd-server.exe -m c.safetensors --diffusion-model d.safetensors"},
	}
	probe := func(exe string) ([]string, error) {
		if !strings.Contains(exe, "llama-server") {
			t.Fatalf("probed %q; only llama-server commands should be checked", exe)
		}
		return []string{"-m", "-c", "-fa", "--flash-attn"}, nil
	}

	got := launchFlagWarnings(models, probe)
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want exactly one (the stale model)", got)
	}
	for _, want := range []string{`"stale"`, "-cms", "this backend build does not list it", "--made-up", "not a llama-server flag"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("warning %q is missing %q", got[0], want)
		}
	}
}

func TestLaunchFlagWarnings_probeFailureIsSilent(t *testing.T) {
	models := map[string]config.ModelConfig{"m": {Cmd: "llama-server -m a.gguf --made-up"}}
	probe := func(string) ([]string, error) { return nil, errors.New("no --help") }
	if got := launchFlagWarnings(models, probe); len(got) != 0 {
		t.Fatalf("warnings = %v, want none when the probe cannot judge", got)
	}
}

func TestLaunchFlagWarnings_ignoresNonLlamaAndEmptyCmds(t *testing.T) {
	models := map[string]config.ModelConfig{
		"empty":   {},
		"vllm":    {Cmd: "python -m vllm.entrypoints.openai.api_server --model x"},
		"sam":     {Cmd: "sam3_server --checkpoint x.pt --port 8080"},
		"trellis": {Cmd: "trellis2-server --package y --port 8080"},
	}
	probe := func(exe string) ([]string, error) {
		t.Fatalf("probed %q, want no probe for non-llama commands", exe)
		return nil, nil
	}
	if got := launchFlagWarnings(models, probe); len(got) != 0 {
		t.Fatalf("warnings = %v, want none", got)
	}
}
