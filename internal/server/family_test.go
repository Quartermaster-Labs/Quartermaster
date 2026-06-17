package server

import "testing"

func TestServer_modelFamily(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"short flag", `llama-server -m C:\Models\qwen.gguf -c 4096`, "C:/Models/qwen.gguf"},
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
		base + ` -c 4096`,                  // judge
	}
	want := "/models/qwen3.5-9b.gguf"
	for _, c := range cmds {
		if got := modelFamily(c); got != want {
			t.Fatalf("modelFamily(%q) = %q, want %q", c, got, want)
		}
	}
}
