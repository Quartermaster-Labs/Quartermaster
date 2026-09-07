package main

import (
	"strings"
	"testing"
)

// The components flag is the one piece of headlessChoices with real parsing:
// empty means "the recommended set", which is hardware-derived and therefore
// not asserted here, while an explicit list and "none" must be exact.
func TestHeadlessChoices_Components(t *testing.T) {
	t.Run("an explicit list is taken verbatim", func(t *testing.T) {
		c := headlessChoices("/opt/qm", "/models", "vulkan", "llama-server, sd-server")
		if got := strings.Join(c.Components, ","); got != "llama-server,sd-server" {
			t.Errorf("components = %q", got)
		}
		if c.Dir != "/opt/qm" || c.ModelsRoot != "/models" || c.Variant != "vulkan" {
			t.Errorf("choices = %+v", c)
		}
	})

	t.Run("none installs nothing", func(t *testing.T) {
		if c := headlessChoices("/opt/qm", "", "cpu", "none"); len(c.Components) != 0 {
			t.Errorf("components = %v, want none", c.Components)
		}
	})

	// An empty models folder is a legitimate answer ("I'll choose in the
	// dashboard"), so it must not be turned into a path.
	t.Run("an empty models root stays empty", func(t *testing.T) {
		if c := headlessChoices("/opt/qm", "", "cpu", "none"); c.ModelsRoot != "" {
			t.Errorf("modelsRoot = %q, want empty", c.ModelsRoot)
		}
	})
}
