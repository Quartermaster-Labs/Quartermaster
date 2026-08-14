package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

func TestTitlegen_SplitThinkSpans(t *testing.T) {
	content := "<think>Weighing the two options</think>Answer one.<thinking>Now checking prices</thinking>Answer two.<think>unclosed tail"
	got := splitThinkSpans(content, 0)
	want := []string{"Weighing the two options", "Now checking prices", "unclosed tail"}
	if len(got) != len(want) {
		t.Fatalf("spans = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("span %d = %q, want %q", i, got[i], want[i])
		}
	}
	if n := len(splitThinkSpans(content, 2)); n != 2 {
		t.Errorf("capped spans = %d, want 2", n)
	}
	if n := len(splitThinkSpans("no reasoning here", 0)); n != 0 {
		t.Errorf("plain content produced %d spans", n)
	}
}

func TestTitlegen_TrimTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{`  "comparing three laptops."  `, "Comparing three laptops"},
		{"a", ""},
		{"", ""},
		{"weighing the trade-offs between a fast local model and a much more accurate remote one", "Weighing the trade-offs between a fast local model and a…"},
	}
	for _, c := range cases {
		if got := trimTitle(c.in); got != c.want {
			t.Errorf("trimTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := trimTitle(cases[3].in); len(got) > titlegenMaxOutput+3 {
		t.Errorf("trimTitle overran the cap: %d chars", len(got))
	}
}

func TestTitlegen_CleanOutput(t *testing.T) {
	prompt := "summarize: the user asks about VRAM"
	raw := "build: 1234 (abc)\nmain: loading model\n\x1b[32mVRAM headroom on a 24GB card\x1b[0m [end of text]\nllama_perf: 12ms\n"
	if got := cleanTitlegenOutput(raw, prompt); got != "VRAM headroom on a 24GB card" {
		t.Errorf("cleanTitlegenOutput = %q", got)
	}
	// An echoed prompt must not become the title.
	if got := cleanTitlegenOutput(prompt+"\n", prompt); got != "" {
		t.Errorf("echoed prompt leaked through as %q", got)
	}
}

func TestTitlegen_PromptCapsInput(t *testing.T) {
	long := strings.Repeat("reasoning words ", 500)
	p := titlegenPrompt(long)
	if !strings.HasPrefix(p, titlegenShots) {
		t.Fatalf("missing few-shot preamble: %q", p[:20])
	}
	if !strings.HasSuffix(p, "\nTitle:") {
		t.Errorf("missing trailing cue: %q", p[len(p)-20:])
	}
	// Only the input text is capped; the fixed few-shot preamble rides along.
	if len(p) > titlegenMaxInput+len(titlegenShots)+len("\nTitle:") {
		t.Errorf("prompt not capped: %d chars", len(p))
	}
	if titlegenPrompt("   ") != "" {
		t.Error("blank input should produce no prompt")
	}
}

func TestTitlegen_PickLlamaExe(t *testing.T) {
	entries := []autogen.BackendEntry{
		{Kind: "sd", Path: "C:/sd/sd-server.exe", Default: true},
		{Kind: "llama", Path: "C:/a/llama-server.exe"},
		{Kind: "llama.cpp", Path: "C:/b/llama-server.exe", Default: true},
	}
	if got := pickLlamaExe(entries); got != "C:/b/llama-server.exe" {
		t.Errorf("pickLlamaExe = %q, want the ★default llama entry", got)
	}
	if got := pickLlamaExe(entries[:2]); got != "C:/a/llama-server.exe" {
		t.Errorf("pickLlamaExe without a default = %q, want first llama entry", got)
	}
	if got := pickLlamaExe(entries[:1]); got != "" {
		t.Errorf("pickLlamaExe with no llama entry = %q", got)
	}
}

// The model must extract itself: a fresh install has no titlegen folder, and the
// feature is meant to work with zero setup.
func TestTitlegen_ExtractsEmbeddedModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QM_TITLEGEN_MODEL", "")
	gen := filepath.Join(dir, "quartermaster-generate.yaml")

	path := titlegenModelPath(gen)
	if path == "" {
		t.Fatal("no model path produced")
	}
	if want := filepath.Join(dir, "titlegen", titlegenAssetName); path != want {
		t.Errorf("model path = %q, want %q", path, want)
	}
	// Second call must reuse the extracted file, not rewrite it.
	if again := titlegenModelPath(gen); again != path {
		t.Errorf("second resolve = %q, want %q", again, path)
	}
	if titlegenModelPath("") != "" {
		t.Error("no generate path should mean no title model")
	}
}
