package server

import (
	"encoding/json"
	"testing"
)

// The Qwen 3.8 ladder, as its template declares it (richest first).
var qwenLevels = []string{"xhigh", "medium", "low"}

func TestNormalizeReasoningEffort(t *testing.T) {
	cases := []struct {
		req      string
		levels   []string
		level    string
		disabled bool
		ok       bool
	}{
		// Exact matches pass through untouched.
		{"low", qwenLevels, "low", false, true},
		{"medium", qwenLevels, "medium", false, true},
		{"xhigh", qwenLevels, "xhigh", false, true},
		{"XHIGH", qwenLevels, "xhigh", false, true},
		// The OpenAI ladder snaps onto the template's.
		{"high", qwenLevels, "xhigh", false, true},
		{"minimal", qwenLevels, "low", false, true},
		// "none" is OpenAI's don't-think, which is enable_thinking, not a level.
		{"none", qwenLevels, "", true, true},
		{"off", qwenLevels, "", true, true},
		// Nothing to snap onto, or nothing recognisable to snap.
		{"low", nil, "", false, false},
		{"", qwenLevels, "", false, false},
		{"turbo", qwenLevels, "", false, false},
		// A template with an unrankable vocabulary still honours exact matches.
		{"balanced", []string{"balanced", "thorough"}, "balanced", false, true},
		{"high", []string{"balanced", "thorough"}, "", false, false},
	}
	for _, c := range cases {
		level, disabled, ok := normalizeReasoningEffort(c.req, c.levels)
		if level != c.level || disabled != c.disabled || ok != c.ok {
			t.Errorf("normalize(%q, %v) = (%q, %v, %v), want (%q, %v, %v)",
				c.req, c.levels, level, disabled, ok, c.level, c.disabled, c.ok)
		}
	}
}

func TestApplyReasoningEffort(t *testing.T) {
	kwargs := func(t *testing.T, body []byte) map[string]any {
		t.Helper()
		var parsed struct {
			ReasoningEffort any            `json:"reasoning_effort"`
			Kwargs          map[string]any `json:"chat_template_kwargs"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		if parsed.ReasoningEffort != nil {
			t.Errorf("top-level reasoning_effort survived: %s", body)
		}
		return parsed.Kwargs
	}

	t.Run("translates into the kwarg", func(t *testing.T) {
		body, err := applyReasoningEffort([]byte(`{"model":"q","reasoning_effort":"high"}`), qwenLevels)
		if err != nil {
			t.Fatal(err)
		}
		if got := kwargs(t, body)["reasoning_effort"]; got != "xhigh" {
			t.Errorf("reasoning_effort kwarg = %v, want xhigh", got)
		}
	})

	t.Run("none disables thinking instead", func(t *testing.T) {
		body, err := applyReasoningEffort([]byte(`{"reasoning_effort":"none"}`), qwenLevels)
		if err != nil {
			t.Fatal(err)
		}
		k := kwargs(t, body)
		if k["enable_thinking"] != false {
			t.Errorf("enable_thinking = %v, want false", k["enable_thinking"])
		}
		if _, set := k["reasoning_effort"]; set {
			t.Errorf("effort level set alongside thinking-off: %s", body)
		}
	})

	t.Run("merges beside existing kwargs", func(t *testing.T) {
		body, err := applyReasoningEffort([]byte(`{"chat_template_kwargs":{"enable_thinking":true},"reasoning_effort":"low"}`), qwenLevels)
		if err != nil {
			t.Fatal(err)
		}
		k := kwargs(t, body)
		if k["enable_thinking"] != true || k["reasoning_effort"] != "low" {
			t.Errorf("kwargs = %v, want both preserved", k)
		}
	})

	t.Run("an explicit kwarg wins", func(t *testing.T) {
		body, err := applyReasoningEffort([]byte(`{"chat_template_kwargs":{"reasoning_effort":"medium"},"reasoning_effort":"high"}`), qwenLevels)
		if err != nil {
			t.Fatal(err)
		}
		if got := kwargs(t, body)["reasoning_effort"]; got != "medium" {
			t.Errorf("reasoning_effort kwarg = %v, want medium (caller's own)", got)
		}
	})

	// Untouched cases: the body must come back byte-identical, since anything
	// else would rewrite a request we have no business interpreting.
	for _, c := range []struct {
		name   string
		body   string
		levels []string
	}{
		{"no ladder advertised", `{"reasoning_effort":"high"}`, nil},
		{"unrecognised value", `{"reasoning_effort":"turbo"}`, qwenLevels},
		{"no effort field", `{"model":"q","messages":[]}`, qwenLevels},
		{"non-string effort", `{"reasoning_effort":3}`, qwenLevels},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, err := applyReasoningEffort([]byte(c.body), c.levels)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != c.body {
				t.Errorf("body = %s, want unchanged %s", body, c.body)
			}
		})
	}
}
