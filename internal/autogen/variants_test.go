package autogen

import (
	"os"
	"strings"
	"testing"
)

// A named custom variant on an override emits an extra "<model>-<slug>" entry
// alongside the solo model, with its own forced ctx. Gated on the real models
// tree (needs gguf metadata to size the profile).
func TestAutogen_Generate_namedVariant(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	gf := GenerateFile{
		Settings: Settings{ModelsRoot: realModelsRoot},
		Overrides: []Override{{
			Match:    "*",
			Variants: []VariantSpec{{Name: "My Tiny", Ctx: 8192, KvK: "q4_0", KvV: "q4_0"}},
		}},
	}
	gf.Settings.applyDefaults()
	out, err := Generate(gf, "T")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// slugified suffix
	if !strings.Contains(out, "-my-tiny\":") {
		t.Fatalf("expected a -my-tiny variant entry, got none")
	}
	// the variant forces ctx 8192
	if !strings.Contains(out, "-c 8192") {
		t.Fatalf("expected variant ctx -c 8192 in output")
	}
}
