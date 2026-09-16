package autogen

import (
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

func TestResolveLlmLoraDir_ladder(t *testing.T) {
	const model = "D:/LLM/Models/qwen/qwen3-27b-Q5_K_M.gguf"
	cases := []struct {
		name string
		s    Settings
		want string
	}{
		{"llm key wins", Settings{
			LoraDirs: map[string]string{"llm": "D:/loras/llm", "image": "D:/loras/img"},
			LoraDir:  "D:/loras/all",
		}, "D:/loras/llm"},
		{"falls back to fleet-wide", Settings{
			LoraDirs: map[string]string{"image": "D:/loras/img"},
			LoraDir:  "D:/loras/all",
		}, "D:/loras/all"},
		{"falls back to the model's own dir", Settings{}, "D:/LLM/Models/qwen"},
		{"blank llm key is not a setting", Settings{
			LoraDirs: map[string]string{"llm": "   "},
			LoraDir:  "D:/loras/all",
		}, "D:/loras/all"},
		// The image key must never leak into the llama-server path: an SDXL LoRA
		// is not loadable by a text model, and this is the whole reason the map
		// is keyed by category rather than shared.
		{"image key is not consulted", Settings{
			LoraDirs: map[string]string{"image": "D:/loras/img"},
		}, "D:/LLM/Models/qwen"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveLlmLoraDir(c.s, model); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveLlmLoraDir_backslashModelPath(t *testing.T) {
	// A Windows-written generate file carries backslashes; the fallback has to
	// find the last separator anyway.
	got := resolveLlmLoraDir(Settings{}, `D:\LLM\Models\qwen\q.gguf`)
	if got != "D:/LLM/Models/qwen" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveLoraPath(t *testing.T) {
	cases := []struct{ dir, in, want string }{
		{"D:/loras", "adapter.gguf", "D:/loras/adapter.gguf"},
		{"D:/loras/", "adapter.gguf", "D:/loras/adapter.gguf"},
		{"D:/loras", "sub/adapter.gguf", "D:/loras/sub/adapter.gguf"},
		{"D:/loras", `sub\adapter.gguf`, "D:/loras/sub/adapter.gguf"},
		// Absolute in every spelling a shared config might carry: an absolute
		// entry must survive being read on a platform whose filepath.IsAbs
		// disagrees, which is why isAbsLoraPath is hand-rolled.
		{"D:/loras", "E:/other/adapter.gguf", "E:/other/adapter.gguf"},
		{"D:/loras", `E:\other\adapter.gguf`, "E:/other/adapter.gguf"},
		{"D:/loras", "/mnt/loras/adapter.gguf", "/mnt/loras/adapter.gguf"},
		{"D:/loras", `\\nas\share\adapter.gguf`, "//nas/share/adapter.gguf"},
		// No folder configured and no model dir: the name stands alone rather
		// than becoming "/adapter.gguf".
		{"", "adapter.gguf", "adapter.gguf"},
		{"D:/loras", "  ", ""},
	}
	for _, c := range cases {
		if got := resolveLoraPath(c.dir, c.in); got != c.want {
			t.Errorf("resolveLoraPath(%q, %q) = %q, want %q", c.dir, c.in, got, c.want)
		}
	}
}

func TestLlmLoraLines(t *testing.T) {
	s := Settings{LoraDirs: map[string]string{"llm": "D:/loras/llm"}}
	const model = "D:/LLM/Models/qwen/q.gguf"

	if got := llmLoraLines(s, nil, model); got != nil {
		t.Fatalf("nil override should emit nothing, got %v", got)
	}
	if got := llmLoraLines(s, &Override{}, model); got != nil {
		t.Fatalf("a model with no adapters must emit nothing (existing configs stay byte-identical), got %v", got)
	}

	ov := &Override{Loras: []LoraRef{
		{Path: "a.gguf"},                  // no scale => plain --lora
		{Path: "b.gguf", Scale: f64(0.7)}, // scaled
		{Path: "c.gguf", Scale: f64(0)},   // 0 is REAL: loaded-but-inert, enabled per request
		{Path: "  "},                      // half-typed editor row, skipped
		{Path: "E:/elsewhere/d.gguf", Scale: f64(1)},
	}}
	got := llmLoraLines(s, ov, model)
	want := []string{
		`--lora "D:/loras/llm/a.gguf"`,
		`--lora-scaled "D:/loras/llm/b.gguf" 0.7`,
		`--lora-scaled "D:/loras/llm/c.gguf" 0`,
		`--lora-scaled "E:/elsewhere/d.gguf" 1`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// --lora is Additive: a user's own --lora in custom args must ADD to the
// model's adapters, not silently replace them the way every ReplaceAll flag
// does. Guards both halves: the generated lines survive, and ClearOwnedFields
// does not wipe Override.Loras.
func TestOwnedKnobs_loraIsNotOwned(t *testing.T) {
	owned := OwnedKnobs([]string{"--lora", "D:/x/extra.gguf"})
	if owned["loras"] {
		t.Fatal("--lora must not mark the loras knob owned (it is Additive)")
	}
	// A ReplaceAll neighbour still behaves, so the change is scoped.
	if !OwnedKnobs([]string{"-c", "4096"})["ctx"] {
		t.Fatal("-c should still own ctx")
	}
}

// --lora-scaled takes TWO value tokens. An under-counted flag leaves its scale
// stranded as a bare token, which the walk then treats as a positional.
func TestOwnedKnobs_loraScaledConsumesBothValues(t *testing.T) {
	// Without ExtraValues the walk would stop on "0.5" and then read "-c" as a
	// fresh flag anyway, so assert via a token the walk must NOT misread: put a
	// value that looks like a flag in the scale slot's wake.
	owned := OwnedKnobs([]string{"--lora-scaled", "D:/x/a.gguf", "-c", "--top-k", "20"})
	if owned["ctx"] {
		t.Fatal("-c sat in --lora-scaled's second value slot and must have been consumed, not read as a flag")
	}
	if !owned["topK"] {
		t.Fatal("--top-k after the consumed pair should still be seen")
	}
}

func TestFlagTable_loraSpellings(t *testing.T) {
	for _, name := range []string{"--lora", "--lora-scaled"} {
		d, ok := LookupFlag(name)
		if !ok {
			t.Fatalf("%s missing from the flag table", name)
		}
		if d.Repeat != Additive {
			t.Errorf("%s must be Additive", name)
		}
		if d.Knob != "loras" {
			t.Errorf("%s knob = %q, want loras", name, d.Knob)
		}
	}
	if d, _ := LookupFlag("--lora-scaled"); d.ExtraValues != 1 {
		t.Errorf("--lora-scaled takes FNAME SCALE, so ExtraValues must be 1, got %d", d.ExtraValues)
	}
	if d, _ := LookupFlag("--lora"); d.ExtraValues != 0 {
		t.Errorf("--lora takes one value, got ExtraValues %d", d.ExtraValues)
	}
}

// The emitted flags must be reachable from a real command render, not just from
// the helper: the wiring into buildCmdLines is the part that silently goes
// missing.
func TestBuildCmdLines_emitsLoras(t *testing.T) {
	s := Settings{LoraDirs: map[string]string{"llm": "D:/loras/llm"}}
	row := GgufRow{FullPath: "D:/LLM/Models/qwen/q.gguf"}
	ov := &Override{Loras: []LoraRef{{Path: "a.gguf", Scale: f64(0.5)}}}
	lines := buildCmdLines(s, Metadata{}, row, profile{}, 4096, 99, 0, "q8_0", "q8_0", false, ov)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `--lora-scaled "D:/loras/llm/a.gguf" 0.5`) {
		t.Fatalf("adapter flag missing from the rendered command:\n%s", joined)
	}
	// And a model without adapters must be unchanged.
	if strings.Contains(strings.Join(buildCmdLines(s, Metadata{}, row, profile{}, 4096, 99, 0, "q8_0", "q8_0", false, &Override{}), "\n"), "--lora") {
		t.Fatal("a model with no adapters emitted a --lora flag")
	}
}
