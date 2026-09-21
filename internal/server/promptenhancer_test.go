package server

import (
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// The resolver has three sources for one answer, and the ranking between them is
// the whole contract: the image model's own prompt beats the shared row's, and
// an id with no row at all still resolves.
func TestServer_promptEnhancerFor(t *testing.T) {
	cfg := config.Config{PromptEnhancers: map[string]config.PromptEnhancerConfig{
		"PE-I2I": {Name: "Qwen edits", SystemPrompt: "shared", Vision: true},
	}}

	// Row hit, case-insensitively: the ids are filename-derived and retyped.
	got := promptEnhancerFor(cfg, "pe-i2i", "")
	if got == nil || got.Model != "PE-I2I" || got.Name != "Qwen edits" {
		t.Fatalf("row lookup: %#v", got)
	}
	if got.SystemPrompt != "shared" || !got.Vision {
		t.Errorf("row fields lost: %#v", got)
	}

	// The image model's own prompt wins: same rewriter, different target.
	if got = promptEnhancerFor(cfg, "pe-i2i", "mine"); got.SystemPrompt != "mine" {
		t.Errorf("per-model prompt should win, got %q", got.SystemPrompt)
	}

	// No row: the id resolves to itself rather than to nil, which used to hide
	// the button with nothing to explain why.
	got = promptEnhancerFor(cfg, "typed-by-hand", "mine")
	if got == nil || got.Model != "typed-by-hand" || got.Name != "typed-by-hand" {
		t.Fatalf("row-less id: %#v", got)
	}
	if got.SystemPrompt != "mine" {
		t.Errorf("row-less prompt: %q", got.SystemPrompt)
	}
	if got.Vision {
		t.Error("a name that says nothing about direction must not claim vision")
	}

	// ...but a row-less id whose NAME says i2i gets the reference image, since a
	// rewriter asked about a picture it cannot see is the worse default.
	if got = promptEnhancerFor(cfg, "qwen-image-2.1-pe-i2i-q8_0", ""); !got.Vision {
		t.Error("an i2i name should imply vision when nothing declares it")
	}

	// Nothing chosen, and the explicit refusal, both mean no enhancer.
	if promptEnhancerFor(cfg, "", "x") != nil || promptEnhancerFor(cfg, "None", "x") != nil {
		t.Error("blank / none must resolve to no enhancer")
	}
}
