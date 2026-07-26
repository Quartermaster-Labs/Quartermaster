package server

import (
	"net/http"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

func TestRequestedCtx(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		header  string
		wantCtx int
		wantOK  bool
	}{
		{"none", "qwen", "", 0, false},
		{"suffix", "qwen?ctx=32768", "", 32768, true},
		{"header", "qwen", "16384", 16384, true},
		{"suffix wins over header", "qwen?ctx=32768", "16384", 32768, true},
		{"bad header ignored", "qwen", "huge", 0, false},
		{"out-of-range header ignored", "qwen", "8", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
			if tc.header != "" {
				r.Header.Set("X-QM-Ctx", tc.header)
			}
			ctx, ok := requestedCtx(r, tc.model)
			if ctx != tc.wantCtx || ok != tc.wantOK {
				t.Errorf("requestedCtx = %d,%v want %d,%v", ctx, ok, tc.wantCtx, tc.wantOK)
			}
		})
	}
}

func TestCountCtxVariants(t *testing.T) {
	models := map[string]config.ModelConfig{
		"qwen":             {},
		"qwen@ctx4096":     {},
		"qwen@ctx32768":    {},
		"qwen-coder":       {},
		"qwen-coder@ctx16": {},
		"other@ctx4096":    {},
	}
	if n := countCtxVariants(models, "qwen"); n != 2 {
		t.Errorf("countCtxVariants(qwen) = %d, want 2 (must not count qwen-coder's)", n)
	}
	if n := countCtxVariants(models, "other"); n != 1 {
		t.Errorf("countCtxVariants(other) = %d, want 1", n)
	}
}

// The variant must land in every group its base belongs to — that shared group
// is what keeps base and variant under one exclusive VRAM budget, so they can
// never be resident at the same time.
func TestAddVariantToConfig_JoinsBaseGroups(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{"qwen": {Cmd: "llama-server -m /m/a.gguf"}},
	}
	cfg.Routing.Router.Settings.Groups = map[string]config.GroupConfig{
		"gpu":   {Members: []string{"qwen", "llama"}},
		"other": {Members: []string{"sd"}},
	}
	origModels := cfg.Models
	origMembers := cfg.Routing.Router.Settings.Groups["gpu"].Members

	addVariantToConfig(&cfg, "qwen", "qwen@ctx4096", config.ModelConfig{Cmd: "x"})

	if _, ok := cfg.Models["qwen@ctx4096"]; !ok {
		t.Fatal("variant not registered")
	}
	if !containsStr(cfg.Routing.Router.Settings.Groups["gpu"].Members, "qwen@ctx4096") {
		t.Error("variant did not join the base model's group")
	}
	if containsStr(cfg.Routing.Router.Settings.Groups["other"].Members, "qwen@ctx4096") {
		t.Error("variant joined a group its base is not in")
	}
	// COW: the maps and slices the live config still points at must be untouched.
	if _, ok := origModels["qwen@ctx4096"]; ok {
		t.Error("original Models map was mutated in place")
	}
	if len(origMembers) != 2 {
		t.Errorf("original group members mutated in place: %q", origMembers)
	}
}
