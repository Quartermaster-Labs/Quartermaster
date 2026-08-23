package quant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every weight type llama-quantize can emit (its QUANT_OPTIONS list), plus the
// formats that reach us from converters and forks. This is the regression the
// package exists for: a token missing here reads as part of the model NAME, so
// the id never gets cut at it and every ctx tier of one model becomes a model.
func TestTokenRe_KnownQuants(t *testing.T) {
	known := []string{
		// llama-quantize QUANT_OPTIONS
		"Q1_0", "Q4_0", "Q4_1", "MXFP4_MOE", "Q5_0", "Q5_1",
		"IQ2_XXS", "IQ2_XS", "IQ2_S", "IQ2_M", "IQ1_S", "IQ1_M",
		"TQ1_0", "TQ2_0", "Q2_K", "Q2_K_S", "IQ3_XXS", "IQ3_S", "IQ3_M",
		"Q3_K", "IQ3_XS", "Q3_K_S", "Q3_K_M", "Q3_K_L", "IQ4_NL", "IQ4_XS",
		"Q4_K", "Q4_K_S", "Q4_K_M", "Q5_K", "Q5_K_S", "Q5_K_M", "Q6_K",
		"Q8_0", "F16", "BF16", "F32",
		// Float formats named by their vendor's scale, not by a bit count.
		"MXFP4", "NVFP4", "FP8", "FP16",
		// ik_llama.cpp's own K/KT quants, same shape as the upstream ones.
		"IQ4_KS", "IQ4_KSS", "IQ2_KL", "IQ3_K", "IQ5_K", "IQ1_KT",
		// Unsloth's dynamic tier letters ride the same token (the "UD" marker
		// itself is a separate part - see PrefixRe).
		"Q4_K_XL", "Q4_K_XXL",
		// Legacy repacked types still sitting in old ggufs.
		"Q4_0_4_4", "Q4_0_8_8", "IQ4_NL_4_4",
	}
	for _, q := range known {
		if !TokenRe.MatchString(q) {
			t.Errorf("TokenRe does not match %q", q)
		}
		if !TokenRe.MatchString(strings.ToLower(q)) {
			t.Errorf("TokenRe does not match %q lower-cased", q)
		}
	}
}

// The other half of the contract: parts of a model NAME must not read as quants,
// or a base key gets cut short and unrelated models pile onto one row.
func TestTokenRe_NonQuants(t *testing.T) {
	for _, s := range []string{
		"qwen3.8", "27b", "a3b", "mtp", "dflash", "instruct", "it", "v2",
		"mid", "high", "vision", "32k", "e4b", "coder", "uncensored",
		"gguf", "moe", "preserved", "abliterated", "2507", "", "-",
	} {
		if TokenRe.MatchString(s) {
			t.Errorf("TokenRe matches non-quant %q", s)
		}
	}
}

// FromName reads the token off a file name, and takes the FIRST one: what
// follows a quant is a build tag, never a second weight type.
func TestFromName(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Qwen3.8-27B-Q4_K_M.gguf", "Q4_K_M"},
		{"Qwen3.8-27B-UD-Q4_K_XL.gguf", "Q4_K_XL"}, // the marker is folded in by PartIndex, not here
		{"Qwen3.8-27B-NVFP4-MTP-MID-HIGH.gguf", "NVFP4"},
		{"gpt-oss-20b-MXFP4_MOE.gguf", "MXFP4_MOE"},
		{"Bitnet-2B-TQ1_0.gguf", "TQ1_0"},
		{"Kokoro-v1_0-fp16.gguf", "FP16"},
		{"Qwen3.6-35B-A3B-IQ4_XS-00001-of-00002.gguf", "IQ4_XS"},
		{"Qwen3.6-27B-Q4_K_M-MTP-Preserved.gguf", "Q4_K_M"},
		{"sam3-large.ggml", ""},
		{"Llama-3.1-8B-Instruct.gguf", ""},
	}
	for _, c := range cases {
		if got := FromName(c.name); got != c.want {
			t.Errorf("FromName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// PartIndex is what cuts an id into <model> + <quant and everything after it>.
func TestPartIndex(t *testing.T) {
	cases := []struct {
		id   string
		want int
	}{
		{"qwen3.8-27b-q4_k_m", 2},
		{"qwen3.8-27b-nvfp4-mtp-mid-high", 2},
		{"qwen3.8-27b-ud-q4_k_xl", 2}, // folds back onto the UD marker
		{"qwen3.8-27b-i1-q4_k_m", 2},  // and onto i1
		{"q8-nickname-model", -1},     // index 0 is never a cut point
		{"some-random-model", -1},
	}
	for _, c := range cases {
		if got := PartIndex(strings.Split(c.id, "-")); got != c.want {
			t.Errorf("PartIndex(%q) = %d, want %d", c.id, got, c.want)
		}
	}
}

// The UI groups the Models table on its own copy of this pattern. They cannot
// drift harmlessly - the server stamps a model's `quant` from the Go copy while
// the table decides row membership from the TS one - so the copy is checked in
// as an exact string and asserted here.
func TestPatternMirrorsUI(t *testing.T) {
	path := filepath.Join("..", "..", "ui-svelte", "src", "lib", "quant.ts")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the UI mirror: %v", err)
	}
	const marker = "export const QUANT_PATTERN = String.raw`"
	i := strings.Index(string(b), marker)
	if i < 0 {
		t.Fatalf("%s no longer declares QUANT_PATTERN as a String.raw literal", path)
	}
	rest := string(b)[i+len(marker):]
	j := strings.Index(rest, "`")
	if j < 0 {
		t.Fatalf("%s: unterminated QUANT_PATTERN literal", path)
	}
	if got := rest[:j]; got != Pattern {
		t.Errorf("quant pattern drifted between Go and the UI:\n  go: %s\n  ts: %s", Pattern, got)
	}
}
