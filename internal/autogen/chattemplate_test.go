package autogen

import (
	"slices"
	"strings"
	"testing"
)

// Excerpts of the real upstream templates, verbatim apart from the elision:
// 3.5/3.6 opt in to preserving prior-turn <think> (so history is re-rendered
// differently once a new user turn lands), 3.8 preserves by default.
const (
	qwen36PreserveLine = `{%- if (preserve_thinking is defined and preserve_thinking is true) or (loop.index0 > ns.last_query_index) %}`
	qwen38PreserveLine = `{%- if preserve_thinking is undefined or preserve_thinking is true or loop.index0 > ns.last_query_index %}`
	qwen38EffortLine   = `{%- set resolved_reasoning_effort = reasoning_effort|default('xhigh') %}`
	qwen38GuardLine    = `{%- if resolved_reasoning_effort not in ('xhigh', 'medium', 'low') %}`
)

func TestScanChatTemplate(t *testing.T) {
	cases := []struct {
		name     string
		tmpl     string
		preserve bool
		effort   []string
	}{
		{"empty", "", false, nil},
		{"qwen36", qwen36PreserveLine, false, nil},
		{"qwen38", qwen38EffortLine + "\n" + qwen38GuardLine + "\n" + qwen38PreserveLine, true, []string{"xhigh", "medium", "low"}},
		// A converter that re-wraps/re-indents the jinja must not defeat the match.
		{"qwen38 reindented", "{%- if preserve_thinking is undefined\n     or preserve_thinking is true\n     or loop.index0 > ns.last_query_index %}", true, nil},
		// reasoning_effort read but never validated: nothing safe to advertise.
		{"effort without guard", qwen38EffortLine, false, nil},
		{"no thinking logic", "{%- for message in messages %}{{ message.content }}{%- endfor %}", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			preserve, effort := scanChatTemplate(c.tmpl)
			if preserve != c.preserve {
				t.Errorf("preservesThinking = %v, want %v", preserve, c.preserve)
			}
			if !slices.Equal(effort, c.effort) {
				t.Errorf("effortLevels = %v, want %v", effort, c.effort)
			}
		})
	}
}

// The qwen35/qwen35moe archs cover 3.5, 3.6 and 3.8 alike, so the baked
// template's own behaviour — not the arch — decides whether the drop-in fix is
// applied. An unknown template keeps the override (prior behaviour).
func TestNeedsQwenFixedChatTemplate(t *testing.T) {
	cases := []struct {
		name string
		meta Metadata
		want bool
	}{
		{"qwen36 dense", Metadata{Architecture: "qwen35"}, true},
		{"qwen36 moe", Metadata{Architecture: "qwen35moe"}, true},
		{"qwen35 arch, unknown template", Metadata{Architecture: "qwen35"}, true},
		{"qwen38 dense", Metadata{Architecture: "qwen35", ChatTemplatePreservesThinking: true}, false},
		{"qwen38 moe", Metadata{Architecture: "qwen35moe", ChatTemplatePreservesThinking: true}, false},
		{"arch case-insensitive", Metadata{Architecture: "Qwen35MoE"}, true},
		{"other arch", Metadata{Architecture: "qwen3moe"}, false},
		{"other arch, preserving template", Metadata{Architecture: "llama", ChatTemplatePreservesThinking: true}, false},
		{"no arch", Metadata{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsQwenFixedChatTemplate(c.meta); got != c.want {
				t.Errorf("needsQwenFixedChatTemplate(%+v) = %v, want %v", c.meta, got, c.want)
			}
		})
	}
}

// End to end through the argv builder: the override flag appears for 3.6 and is
// absent for 3.8, and a user-supplied template still wins over both.
func TestBuildCmdLines_QwenChatTemplateOverride(t *testing.T) {
	build := func(meta Metadata, ov *Override) string {
		s := Settings{}
		s.applyDefaults()
		prof := profile{Name: "t", Ctx: 8192}
		return strings.Join(buildCmdLines(s, meta, GgufRow{FullPath: "/m.gguf"}, prof, 8192, 99, 0, "q8_0", "q8_0", false, ov), " ")
	}

	qwen36 := Metadata{Architecture: "qwen35moe"}
	qwen38 := Metadata{Architecture: "qwen35moe", ChatTemplatePreservesThinking: true, ChatTemplateEffortLevels: []string{"xhigh", "medium", "low"}}

	if got := build(qwen36, nil); !strings.Contains(got, "--chat-template-file "+qwenFixedChatTemplateFile) {
		t.Errorf("qwen3.6 should get the fixed template, got:\n%s", got)
	}
	if got := build(qwen38, nil); strings.Contains(got, "--chat-template-file") {
		t.Errorf("qwen3.8 should keep its baked template, got:\n%s", got)
	}
	if got := build(qwen38, &Override{ChatTemplateFile: "C:/my/tmpl.jinja"}); !strings.Contains(got, `--chat-template-file "C:/my/tmpl.jinja"`) {
		t.Errorf("user template must win, got:\n%s", got)
	}
}
