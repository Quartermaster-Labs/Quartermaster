package autogen

import (
	"reflect"
	"strings"
	"testing"
)

// An empty, blank or comment-only custom text must leave the generated command
// exactly as it was. This is the acceptance test the whole feature is built
// around: every sidecar written before custom args existed regenerates
// byte-identically.
func TestComposeCmd_BlankCustomIsByteIdentical(t *testing.T) {
	lines := []string{"llama-server", "-m /models/foo.gguf", "-c 8192 -ub 512"}
	want := strings.Join(lines, " ")
	for _, custom := range []string{"", "   ", "\n", "# a comment\n"} {
		cc, err := ComposeCmd(lines, custom)
		if err != nil {
			t.Fatalf("ComposeCmd(%q): %v", custom, err)
		}
		if !reflect.DeepEqual(cc.Lines, lines) {
			t.Errorf("ComposeCmd(%q) changed the lines: %v", custom, cc.Lines)
		}
		if cc.Effective != want {
			t.Errorf("ComposeCmd(%q).Effective = %q, want %q", custom, cc.Effective, want)
		}
		for _, tok := range cc.Tokens {
			if tok.Suppressed {
				t.Errorf("ComposeCmd(%q) suppressed %q with no custom flags", custom, tok.Text)
			}
		}
	}
}

// A custom flag drops the generated occurrences of the same knob, by any
// spelling, and the rest of the generated line survives.
func TestComposeCmd_SuppressesTheOwnedKnob(t *testing.T) {
	lines := []string{
		"llama-server",
		"-m /models/foo.gguf",
		"-c 8192 -ub 512",
		"-cram 4096",
		"--load-mode none",
		"--temp 0.8",
	}
	cases := []struct {
		name    string
		custom  string
		gone    string
		present string
	}{
		{"exact flag", "-cram 2048", "-cram 4096", "-cram 2048"},
		{"alias suppresses canonical", "--ctx-size 32768", "-c 8192", "--ctx-size 32768"},
		{"inline value", "--temp=0.5", "--temp 0.8", "--temp=0.5"},
		{"legacy spelling owns load mode", "--no-mmap", "--load-mode none", "--no-mmap"},
		{"long alias of a short flag", "--cache-ram 1024", "-cram 4096", "--cache-ram 1024"},
	}
	for _, c := range cases {
		cc, err := ComposeCmd(lines, c.custom)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if strings.Contains(cc.Effective, c.gone) {
			t.Errorf("%s: %q survived in %q", c.name, c.gone, cc.Effective)
		}
		if !strings.Contains(cc.Effective, c.present) {
			t.Errorf("%s: %q missing from %q", c.name, c.present, cc.Effective)
		}
		// The rest of a shared line must survive the rebuild.
		if c.name == "alias suppresses canonical" && !strings.Contains(cc.Effective, "-ub 512") {
			t.Errorf("%s: -ub 512 lost in the rebuild: %q", c.name, cc.Effective)
		}
		suppressed := false
		for _, tok := range cc.Tokens {
			if tok.Suppressed {
				suppressed = true
			}
		}
		if !suppressed {
			t.Errorf("%s: the replaced generated flag was not reported as suppressed (tokens=%v)", c.name, cc.Tokens)
		}
	}
}

// A flag the table does not know is passed through untouched and suppresses
// nothing: quartermaster cannot know which generated flag it replaces.
func TestComposeCmd_UnknownFlagIsPassthrough(t *testing.T) {
	cc, err := ComposeCmd([]string{"llama-server", "-c 8192", "--load-mode none"}, "--rope-freq-scale 0.5 --override-kv x=int:1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cc.Effective, "-c 8192") || !strings.Contains(cc.Effective, "--load-mode none") {
		t.Errorf("unknown custom flags suppressed something: %q", cc.Effective)
	}
	if !strings.HasSuffix(cc.Effective, "--rope-freq-scale 0.5 --override-kv x=int:1") {
		t.Errorf("custom text must be appended verbatim: %q", cc.Effective)
	}
}

// Unparseable text (llama-server will reject it too) still reaches the emitted
// command, and the error is returned so the preview can show it.
func TestComposeCmd_UnparseableTextStillEmitted(t *testing.T) {
	cc, _ := ComposeCmd([]string{"llama-server", "-c 8192"}, `--chat-template-file "unterminated`)
	if !strings.Contains(cc.Effective, "unterminated") {
		t.Errorf("unparseable custom text was dropped: %q", cc.Effective)
	}
}

// The editor's toggle keeps the text but stops applying it.
func TestCustomArgsText_OffAndLegacyFallback(t *testing.T) {
	ov := &Override{CustomArgs: "-cram 2048", ExtraArgs: "-cram 1024"}
	if got := ov.CustomArgsText(); got != "-cram 2048" {
		t.Errorf("CustomArgsText() = %q, want the new field", got)
	}
	ov.CustomArgsOff = true
	if got := ov.CustomArgsText(); got != "" {
		t.Errorf("disabled CustomArgsText() = %q, want empty", got)
	}
	ov.CustomArgsOff = false
	ov.CustomArgs = ""
	if got := ov.CustomArgsText(); got != "-cram 1024" {
		t.Errorf("CustomArgsText() = %q, want the legacy fallback", got)
	}
}

// The emitter must not emit a flag twice after a custom occurrence: the -cram
// bug from issue #38, end to end through emitProfile.
func TestEmitProfile_CustomArgsReplaceNotDuplicate(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "llama", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	ov := &Override{CacheRamMB: 4096, CustomArgs: "-cram 2048"}
	var b strings.Builder
	emitProfile(&b, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, ov)
	out := b.String()
	if n := strings.Count(out, "-cram"); n != 1 {
		t.Errorf("want exactly one -cram, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "-cram 2048") {
		t.Errorf("custom -cram 2048 missing:\n%s", out)
	}
}

// A sidecar from the old two-way box carries its text in extraArgs. -cms is the
// flag that used to be hoisted to survive; suppression now does the same job
// without a special case.
func TestEmitProfile_LegacyExtraArgsReplaceNotDuplicate(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "llama", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	ov := &Override{ExtraArgs: "-cms 2048"}
	var b strings.Builder
	emitProfile(&b, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, ov)
	out := b.String()
	if n := strings.Count(out, "-cms"); n != 1 {
		t.Errorf("want exactly one -cms, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "-cms 2048") {
		t.Errorf("legacy -cms 2048 missing:\n%s", out)
	}
}

func TestMergeInheritStrings_CustomArgs(t *testing.T) {
	eff := Override{CustomArgs: "model-wide"}
	mergeInheritStrings(&eff, &VariantSpec{CustomArgs: "variant"})
	if eff.CustomArgs != "variant" {
		t.Errorf("variant text should win, got %q", eff.CustomArgs)
	}
	eff = Override{CustomArgs: "model-wide"}
	mergeInheritStrings(&eff, &VariantSpec{})
	if eff.CustomArgs != "model-wide" {
		t.Errorf("variant blank should inherit, got %q", eff.CustomArgs)
	}
	eff = Override{CustomArgs: "model-wide"}
	mergeInheritStrings(&eff, &VariantSpec{CustomArgs: "none"})
	if eff.CustomArgs != "" {
		t.Errorf("variant 'none' should clear, got %q", eff.CustomArgs)
	}
}
