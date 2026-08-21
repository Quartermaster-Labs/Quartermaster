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
		// No override => the model runs its own template, ladder and all, even
		// for the archs whose template re-renders history.
		{"history-mutating template keeps its ladder", Metadata{Architecture: "qwen35", ChatTemplateEffortLevels: []string{"xhigh"}}, nil, []string{"xhigh"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effortLevels(c.meta, c.ov); !slices.Equal(got, c.want) {
				t.Errorf("effortLevels = %v, want %v", got, c.want)
			}
		})
	}
}

// End to end through the argv builder: --chat-template-file appears ONLY when
// the user set one. Chat templates are user-managed; no model family gets a
// substitute picked for it, however badly its baked template behaves.
func TestBuildCmdLines_ChatTemplateOnlyWhenUserSet(t *testing.T) {
	build := func(meta Metadata, ov *Override) string {
		s := Settings{}
		s.applyDefaults()
		prof := profile{Name: "t", Ctx: 8192}
		return strings.Join(buildCmdLines(s, meta, GgufRow{FullPath: "/m.gguf"}, prof, 8192, 99, 0, "q8_0", "q8_0", false, ov), " ")
	}

	mutating := Metadata{Architecture: "qwen35moe"}
	preserving := Metadata{Architecture: "qwen35moe", ChatTemplatePreservesThinking: true, ChatTemplateEffortLevels: []string{"xhigh", "medium", "low"}}

	for _, meta := range []Metadata{mutating, preserving} {
		if got := build(meta, nil); strings.Contains(got, "--chat-template-file") {
			t.Errorf("no override set, so no template flag; got:%s%s", "\n", got)
		}
	}
	if got := build(mutating, &Override{ChatTemplateFile: "C:/my/tmpl.jinja"}); !strings.Contains(got, `--chat-template-file "C:/my/tmpl.jinja"`) {
		t.Errorf("user template must be emitted, got:%s%s", "\n", got)
	}
}
