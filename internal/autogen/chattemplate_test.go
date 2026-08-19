package autogen

import (
	"os"
	"path/filepath"
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

// A tolerant drop-in template: it reads reasoning_effort, folds the OpenAI
// ladder onto its own three rungs, and validates nothing — so there is no value
// tuple to read and the rungs are the literals it ASSIGNS. Trimmed from the
// Qwen 3.8 drop-in in circulation, including its unrelated content raise, which
// must not be mistaken for an effort guard.
const tolerantEffortTmpl = `
{%- set _effort_raw = (reasoning_effort | string | lower) if reasoning_effort is defined else 'medium' %}
{%- set _initial_effort = 'medium' %}
{%- if _effort_raw == 'minimal' or _effort_raw == 'low' %}
    {%- set _initial_effort = 'low' %}
{%- elif _effort_raw == 'high' or _effort_raw == 'xhigh' or _effort_raw == 'max' %}
    {%- set _initial_effort = 'xhigh' %}
{%- else %}
    {%- set _initial_effort = 'medium' %}
{%- endif %}
{%- if message.content is not string %}
    {{- raise_exception('Unexpected content type.') }}
{%- endif %}
`

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
		// reasoning_effort read but nothing assigned and nothing validated:
		// no rungs to name, so nothing to advertise.
		{"effort without guard", qwen38EffortLine, false, nil},
		// Tolerant template: rungs come from the assignments, deduped, with the
		// aliases it compares against (minimal/high/max) left out.
		{"tolerant assignments", tolerantEffortTmpl, false, []string{"medium", "low", "xhigh"}},
		// A guard, where present, still wins over the assignment fallback.
		{"guard beats assignments", qwen38GuardLine + tolerantEffortTmpl, false, []string{"xhigh", "medium", "low"}},
		// "none" is the enable_thinking switch, not a rung on the ladder.
		{"none is not a rung", "{%- set _eff = reasoning_effort %}{%- set _initial_effort = 'none' %}{%- set _initial_effort = 'low' %}", false, []string{"low"}},
		// An effort guard that does raise stays strict: no fallback, even though
		// the assignments are readable.
		{"effort guard raises", "{%- if resolved_reasoning_effort != 'low' %}{{- raise_exception('bad effort') }}{%- endif %}{%- set _initial_effort = 'low' %}", false, nil},
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

// A --chat-template-file override replaces the baked template, so the ladder
// advertised must come from that file — the reason a drop-in-templated model
// used to advertise nothing at all even when its template supported effort.
func TestEffortLevelsFromOverrideTemplate(t *testing.T) {
	dir := t.TempDir()
	tolerant := filepath.Join(dir, "tolerant.jinja")
	if err := os.WriteFile(tolerant, []byte(tolerantEffortTmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain.jinja")
	if err := os.WriteFile(plain, []byte("{%- for m in messages %}{{ m.content }}{%- endfor %}"), 0o644); err != nil {
		t.Fatal(err)
	}
	baked := Metadata{Architecture: "qwen35", ChatTemplatePreservesThinking: true, ChatTemplateEffortLevels: []string{"xhigh", "medium", "low"}}

	cases := []struct {
		name string
		meta Metadata
		ov   *Override
		want []string
	}{
		{"no override keeps baked ladder", baked, nil, []string{"xhigh", "medium", "low"}},
		{"override template with a ladder", baked, &Override{ChatTemplateFile: tolerant}, []string{"medium", "low", "xhigh"}},
		{"override template without one", baked, &Override{ChatTemplateFile: plain}, nil},
		{"unreadable override advertises nothing", baked, &Override{ChatTemplateFile: filepath.Join(dir, "gone.jinja")}, nil},
		// The built-in Qwen fix has no reasoning_effort logic at all.
		{"built-in fix", Metadata{Architecture: "qwen35", ChatTemplateEffortLevels: []string{"xhigh"}}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effortLevels(c.meta, c.ov); !slices.Equal(got, c.want) {
				t.Errorf("effortLevels = %v, want %v", got, c.want)
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
