package server

import (
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

func TestServer_modelFamily(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"short flag", `llama-server -m /models/qwen.gguf -c 4096`, "/models/qwen.gguf"},
		{"long flag", `llama-server --model /models/qwen.gguf --ctx 8192`, "/models/qwen.gguf"},
		{"equals long", `llama-server --model=/models/qwen.gguf`, "/models/qwen.gguf"},
		{"equals short", `llama-server -m=/models/qwen.gguf`, "/models/qwen.gguf"},
		{"multiline cmd", "llama-server \\\n  -m /models/q.gguf \\\n  -c 4096", "/models/q.gguf"},
		{"variants share family", `srv --model /m/base.gguf -c 65536 --port 1`, "/m/base.gguf"},
		{"no model flag", `some-proxy --listen :8080`, ""},
		{"flag without value", `llama-server -m`, ""},
		{"empty", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelFamily(tc.cmd); got != tc.want {
				t.Fatalf("modelFamily(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// Two ctx tiers + game + judge of one gguf must collapse to one family key.
func TestServer_modelFamily_variantsCollapse(t *testing.T) {
	base := `llama-server -m /models/qwen3.5-9b.gguf`
	cmds := []string{
		base + ` -c 32768`,
		base + ` -c 65536`,
		base + ` -c 16384 --port ${PORT}`, // game
		base + ` -c 4096`,                 // judge
	}
	want := "/models/qwen3.5-9b.gguf"
	for _, c := range cmds {
		if got := modelFamily(c); got != want {
			t.Fatalf("modelFamily(%q) = %q, want %q", c, got, want)
		}
	}
}

// audiocpp_server has no --model flag, so the ONLY place its weights are named
// is the typed audiocpp: block. Before the fallback this answered "", and the
// config editor turned that into "model has no gguf path to override" - which
// is what kept audio.cpp off the backend picker for its own models.
func TestServer_modelGguf_audioCppBlock(t *testing.T) {
	mc := config.ModelConfig{
		Cmd:      "audiocpp_server --host 127.0.0.1 --port 1 --backend vulkan",
		AudioCpp: config.AudioCppConfig{Family: "qwen3_tts", Path: "D:/models/qwen3-tts.gguf", Task: "tts"},
	}
	if got := modelGguf(mc); got != "D:/models/qwen3-tts.gguf" {
		t.Fatalf("modelGguf(audio.cpp) = %q, want the audiocpp block path", got)
	}
	// The command still wins where it names one: every other backend keeps the
	// key it has always had, block or no block.
	mc.Cmd = "llama-server -m /models/qwen.gguf"
	if got := modelGguf(mc); got != "/models/qwen.gguf" {
		t.Fatalf("modelGguf(llama) = %q, want the -m path", got)
	}
	if got := modelGguf(config.ModelConfig{Cmd: "some-proxy --listen :8080"}); got != "" {
		t.Fatalf("modelGguf(no model) = %q, want empty", got)
	}
}
