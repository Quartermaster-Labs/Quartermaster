package server

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestServer_ApplyImageSeed(t *testing.T) {
	cases := []struct {
		name, in, wantPrompt string
		wantSeedKept         bool
	}{
		{"top-level seed moves into block",
			`{"prompt":"a fox","seed":1234}`,
			`a fox <sd_cpp_extra_args>{"seed":1234}</sd_cpp_extra_args>`, false},
		{"no seed means random",
			`{"prompt":"a fox"}`,
			`a fox <sd_cpp_extra_args>{"seed":-1}</sd_cpp_extra_args>`, false},
		{"non-numeric seed means random",
			`{"prompt":"a fox","seed":"abc"}`,
			`a fox <sd_cpp_extra_args>{"seed":-1}</sd_cpp_extra_args>`, false},
		{"merges into existing block",
			`{"prompt":"a fox <sd_cpp_extra_args>{\"steps\":8}</sd_cpp_extra_args>","seed":7}`,
			`a fox <sd_cpp_extra_args>{"steps":8,"seed":7}</sd_cpp_extra_args>`, false},
		{"block seed wins",
			`{"prompt":"a fox <sd_cpp_extra_args>{\"seed\":5}</sd_cpp_extra_args>","seed":7}`,
			`a fox <sd_cpp_extra_args>{"seed":5}</sd_cpp_extra_args>`, true},
		{"unparseable block left alone",
			`{"prompt":"a fox <sd_cpp_extra_args>{nope</sd_cpp_extra_args>"}`,
			`a fox <sd_cpp_extra_args>{nope</sd_cpp_extra_args>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := applyImageSeed([]byte(tc.in))
			if err != nil {
				t.Fatalf("applyImageSeed: %v", err)
			}
			if got := gjson.GetBytes(out, "prompt").String(); got != tc.wantPrompt {
				t.Errorf("prompt = %q, want %q", got, tc.wantPrompt)
			}
			hadSeed := gjson.Get(tc.in, "seed").Exists()
			if kept := gjson.GetBytes(out, "seed").Exists(); kept != (hadSeed && tc.wantSeedKept) {
				t.Errorf("top-level seed kept = %v", kept)
			}
		})
	}

	t.Run("non-string prompt untouched", func(t *testing.T) {
		in := `{"prompt":["a"],"seed":3}`
		out, _ := applyImageSeed([]byte(in))
		if string(out) != in {
			t.Errorf("body changed: %s", out)
		}
	})
}

func TestServer_IsSDServerCmd(t *testing.T) {
	for cmd, want := range map[string]bool{
		`E:/Apps/LLM/sd-server/sd-server.exe --diffusion-model x.gguf --port 9000`: true,
		`/usr/local/bin/sd-server -m sdxl.safetensors`:                             true,
		`llama-server -m x.gguf`:                                                   false,
		``:                                                                         false,
	} {
		if got := isSDServerCmd(cmd); got != want {
			t.Errorf("isSDServerCmd(%q) = %v, want %v", cmd, got, want)
		}
	}
}
