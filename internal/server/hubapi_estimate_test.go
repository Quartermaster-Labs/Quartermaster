package server

import "testing"

func TestHubMetaFamily(t *testing.T) {
	cases := []struct{ name, want string }{
		// Quants of one model share a header.
		{"Qwen3-8B-Q4_K_M.gguf", "QWEN3-8B|GGUF"},
		{"Qwen3-8B-Q2_K.gguf", "QWEN3-8B|GGUF"},
		{"Qwen3-8B-UD-Q4_K_XL.gguf", "QWEN3-8B|GGUF"},
		// What follows the quant is part of the identity: an MTP build carries
		// extra layers, so it must not be sized off the plain build's header.
		{"Qwen3-8B-Q4_K_M-MTP.gguf", "QWEN3-8B|MTP.GGUF"},
		// Two models in one repo stay apart.
		{"Qwen3-4B-Q4_K_M.gguf", "QWEN3-4B|GGUF"},
		// No quant tag: it speaks only for itself.
		{"model.gguf", ""},
	}
	for _, c := range cases {
		if got := hubMetaFamily(c.name); got != c.want {
			t.Errorf("hubMetaFamily(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
