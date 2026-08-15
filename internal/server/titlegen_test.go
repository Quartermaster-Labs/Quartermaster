package server

import (
	"os"
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

// A still-open span must never be titled: its text is not final.
func TestTitlegen_SplitClosedThinkSpans(t *testing.T) {
	content := "<think>Weighing the two options</think>Answer one.<thinking>Now checking prices</thinking>Answer two.<think>unclosed tail"
	got := splitClosedThinkSpans(content, 0)
	want := []string{"Weighing the two options", "Now checking prices"}
	if len(got) != len(want) {
		t.Fatalf("closed spans = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("span %d = %q, want %q", i, got[i], want[i])
		}
	}
	if n := len(splitClosedThinkSpans("<think>still going", 0)); n != 0 {
		t.Errorf("open-only content produced %d spans", n)
	}
}

func TestTitlegen_EndedThinkSpan(t *testing.T) {
	cases := []struct {
		name          string
		content, next string
		want          bool
	}{
		{"close in delta", "<think>a</think>", "</think>", true},
		{"tag split across deltas", "<think>a</thi" + "nk>", "nk>", true},
		{"uppercase tag", "<THINK>a</THINK>", "</THINK>", true},
		{"alternate tag", "<reasoning>a</reasoning>", "</reasoning>", true},
		{"plain prose", "just answering now", " now", false},
		{"open tag only", "<think>thinking", "thinking", false},
	}
	for _, c := range cases {
		if got := endedThinkSpan(c.content, c.next); got != c.want {
			t.Errorf("%s: endedThinkSpan = %v, want %v", c.name, got, c.want)
		}
	}
	// Delta longer than the accumulated content must not slice out of range.
	if endedThinkSpan("hi", "a much longer delta than the content") {
		t.Error("short content reported a closed span")
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

// writeFakeTitlegenCache lays down a cache entry of exactly the pinned size, so
// resolution finds it and never reaches the network.
func writeFakeTitlegenCache(t *testing.T, generatePath string) string {
	t.Helper()
	path := titlegenCachePath(generatePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(titlegenAssetSize); err != nil {
		t.Fatal(err)
	}
	return path
}

// The model is no longer embedded: it is fetched once and cached beside the
// generate control file. Resolution must find that cache without a download.
func TestTitlegen_ResolvesCachedModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QM_TITLEGEN_MODEL", "")
	gen := filepath.Join(dir, "quartermaster-generate.yaml")

	want := writeFakeTitlegenCache(t, gen)
	if got := titlegenModelPath(gen); got != want {
		t.Errorf("model path = %q, want the cached copy %q", got, want)
	}
	// FetchTitlegenAsset must short-circuit on a complete cache rather than
	// re-downloading 79 MiB on every start.
	if got, err := FetchTitlegenAsset(gen); err != nil || got != want {
		t.Errorf("FetchTitlegenAsset = %q, %v; want %q, nil", got, err, want)
	}
}

// A truncated cache entry (killed download) must not be served as if complete —
// llama-completion would fail on the partial gguf much later, and confusingly.
func TestTitlegen_RejectsPartialCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QM_TITLEGEN_MODEL", "")
	gen := filepath.Join(dir, "quartermaster-generate.yaml")

	path := writeFakeTitlegenCache(t, gen)
	if err := os.Truncate(path, titlegenAssetSize/2); err != nil {
		t.Fatal(err)
	}
	if titlegenCached(path) {
		t.Error("half-written cache reported as complete")
	}
}

// QM_TITLEGEN_MODEL wins over the cache (bring your own title model), and a bad
// value resolves to no model rather than silently falling back.
func TestTitlegen_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "quartermaster-generate.yaml")
	writeFakeTitlegenCache(t, gen)

	mine := filepath.Join(dir, "mine.gguf")
	if err := os.WriteFile(mine, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QM_TITLEGEN_MODEL", mine)
	if got := titlegenModelPath(gen); got != mine {
		t.Errorf("model path = %q, want the override %q", got, mine)
	}

	t.Setenv("QM_TITLEGEN_MODEL", filepath.Join(dir, "nope.gguf"))
	if got := titlegenModelPath(gen); got != "" {
		t.Errorf("missing override resolved to %q, want none", got)
	}
}

// Before the generate control file is known there is nowhere to cache, so there
// is no title model — and asking must not mark the fetch as failed for the run.
func TestTitlegen_NoGeneratePath(t *testing.T) {
	t.Setenv("QM_TITLEGEN_MODEL", "")
	if got := titlegenModelPath(""); got != "" {
		t.Errorf("no generate path resolved to %q, want none", got)
	}
	if titlegenFetchFailed {
		t.Error("resolving without a generate path poisoned the fetch for the run")
	}
	if _, err := FetchTitlegenAsset(""); err == nil {
		t.Error("FetchTitlegenAsset with no generate path should error")
	}
}
