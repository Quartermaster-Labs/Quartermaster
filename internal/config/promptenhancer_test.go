package config

import (
	"strings"
	"testing"
)

// The generated document shape autogen emits (internal/autogen/generate_emit.go,
// emitPromptEnhancers + writePromptEnhancer). This test is the contract between
// the two packages: rename a YAML key on either side and it fails here rather
// than silently resolving to no enhancer at runtime.
func TestConfig_PromptEnhancerLoads(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
promptEnhancers:
  "pe-i2i":
    name: "Qwen PE (edit)"
    vision: true
    systemPrompt: |
      Rewrite the instruction.
      Do not answer it.
models:
  "qwen_image_2.1":
    cmd: sd-server --port ${PORT}
    promptEnhancer: "pe-i2i"
  "plain":
    cmd: llama-server --port ${PORT}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	e, ok := cfg.PromptEnhancers["pe-i2i"]
	if !ok {
		t.Fatalf("pe-i2i missing: %#v", cfg.PromptEnhancers)
	}
	if !e.Vision || e.Name != "Qwen PE (edit)" {
		t.Errorf("enhancer metadata = %#v", e)
	}
	if want := "Rewrite the instruction.\nDo not answer it.\n"; e.SystemPrompt != want {
		t.Errorf("system prompt = %q, want %q", e.SystemPrompt, want)
	}
	if got := cfg.Models["qwen_image_2.1"].PromptEnhancer; got != "pe-i2i" {
		t.Errorf("model link = %q, want pe-i2i", got)
	}
	// Opting out is the default: a model that says nothing gets no enhancer.
	if got := cfg.Models["plain"].PromptEnhancer; got != "" {
		t.Errorf("unrelated model picked up %q", got)
	}
}

// A config with no enhancers at all must still load: the block is new, and every
// config written before this feature existed omits it.
func TestConfig_PromptEnhancerAbsent(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader("models:\n  \"m\":\n    cmd: llama-server --port ${PORT}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.PromptEnhancers) != 0 {
		t.Errorf("want no enhancers, got %#v", cfg.PromptEnhancers)
	}
}

// The two directions are distinct keys on the model, so a model can send a
// txt2img prompt to PE-T2I and an edit instruction to PE-I2I.
func TestConfig_PromptEnhancerPairLoads(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
promptEnhancers:
  "pe-t2i":
    systemPrompt: "compose"
  "pe-i2i":
    vision: true
    systemPrompt: "edit"
models:
  "both":
    cmd: sd-server --port ${PORT}
    promptEnhancer: "pe-t2i"
    promptEnhancerEdit: "pe-i2i"
  "text-only":
    cmd: sd-server --port ${PORT}
    promptEnhancer: "pe-t2i"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Models["both"]; got.PromptEnhancer != "pe-t2i" || got.PromptEnhancerEdit != "pe-i2i" {
		t.Errorf("pair = %q / %q", got.PromptEnhancer, got.PromptEnhancerEdit)
	}
	// Naming only one leaves the other empty; the client reads that as "this one
	// covers both directions" rather than "no enhancer for edits".
	if got := cfg.Models["text-only"]; got.PromptEnhancerEdit != "" {
		t.Errorf("unset edit enhancer = %q, want empty", got.PromptEnhancerEdit)
	}
}
