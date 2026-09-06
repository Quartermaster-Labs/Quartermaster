package autogen

import (
	"strings"
	"testing"
)

// The sentinel exists because "" already means inherit, so a variant had no way
// to say "drop the knob the model-wide override sets". Each case is one of the
// three states the field now carries.
func TestInheritStr(t *testing.T) {
	for _, c := range []struct {
		name    string
		variant string
		model   string
		want    string
	}{
		{"blank inherits", "", "C:/t/model.jinja", "C:/t/model.jinja"},
		{"blank inherits nothing", "", "", ""},
		{"value pins", "C:/t/var.jinja", "C:/t/model.jinja", "C:/t/var.jinja"},
		{"none drops the inherited value", "none", "C:/t/model.jinja", ""},
		{"none is case-insensitive", "None", "C:/t/model.jinja", ""},
		{"none tolerates padding", "  none  ", "C:/t/model.jinja", ""},
		{"none with nothing to drop", "none", "", ""},
		{"a path merely containing none still pins", "C:/none/x.jinja", "C:/t/m.jinja", "C:/none/x.jinja"},
	} {
		if got := inheritStr(c.variant, c.model); got != c.want {
			t.Errorf("%s: inheritStr(%q, %q) = %q, want %q", c.name, c.variant, c.model, got, c.want)
		}
	}
}

// A sentinel written at model level would otherwise reach the emitter as a
// literal path, so it is cleared once at load.
func TestNormalizeNone(t *testing.T) {
	o := Override{
		ChatTemplateFile: "none", ExtraArgs: "NONE", TensorSplit: "none",
		OverrideTensor: "3,1", VaePath: "none", T5Path: "D:/t5.gguf",
	}
	NormalizeNone(&o)
	for name, got := range map[string]string{
		"ChatTemplateFile": o.ChatTemplateFile, "ExtraArgs": o.ExtraArgs,
		"TensorSplit": o.TensorSplit, "VaePath": o.VaePath,
	} {
		if got != "" {
			t.Errorf("%s = %q, want cleared", name, got)
		}
	}
	if o.OverrideTensor != "3,1" || o.T5Path != "D:/t5.gguf" {
		t.Errorf("real values must survive: %q / %q", o.OverrideTensor, o.T5Path)
	}
}

// The whole point, end to end: a variant of a model that pins a chat template
// can run on the gguf's baked-in one.
func TestBuildCmdLines_VariantDropsInheritedChatTemplate(t *testing.T) {
	model := Override{ChatTemplateFile: "C:/my/tmpl.jinja", ExtraArgs: "--verbose"}

	for _, c := range []struct {
		name         string
		variant      VariantSpec
		wantTemplate string // "" => the flag must be absent
		wantArgs     string
	}{
		{"untouched variant inherits", VariantSpec{Name: "v"}, "C:/my/tmpl.jinja", "--verbose"},
		{"variant pins its own", VariantSpec{Name: "v", ChatTemplateFile: "C:/other.jinja"}, "C:/other.jinja", "--verbose"},
		{"none drops it", VariantSpec{Name: "v", ChatTemplateFile: "none"}, "", "--verbose"},
		{"none drops extra args independently", VariantSpec{Name: "v", ExtraArgs: "none"}, "C:/my/tmpl.jinja", ""},
	} {
		eff := model
		v := c.variant
		mergeInheritStrings(&eff, &v)

		if eff.ChatTemplateFile != c.wantTemplate {
			t.Errorf("%s: template = %q, want %q", c.name, eff.ChatTemplateFile, c.wantTemplate)
		}
		if eff.ExtraArgs != c.wantArgs {
			t.Errorf("%s: extraArgs = %q, want %q", c.name, eff.ExtraArgs, c.wantArgs)
		}

		s := Settings{}
		s.applyDefaults()
		prof := profile{Name: "t", Ctx: 8192}
		got := strings.Join(buildCmdLines(s, Metadata{Architecture: "qwen35moe"}, GgufRow{FullPath: "/m.gguf"}, prof, 8192, 99, 0, "q8_0", "q8_0", false, &eff), " ")
		if c.wantTemplate == "" {
			if strings.Contains(got, "--chat-template-file") {
				t.Errorf("%s: flag must be absent, got:\n%s", c.name, got)
			}
		} else if !strings.Contains(got, `--chat-template-file "`+c.wantTemplate+`"`) {
			t.Errorf("%s: want %s in:\n%s", c.name, c.wantTemplate, got)
		}
	}
}
