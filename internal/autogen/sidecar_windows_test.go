//go:build windows

package autogen

// Windows-only: the UI may re-save the same gguf spelled with backslashes, and
// only there does filepath.ToSlash fold them into the same key.

import "testing"

func TestAutogen_Sidecar_matchKeyFoldsSeparators(t *testing.T) {
	gen := writeGen(t)
	if _, err := UpsertSidecarOverride(gen, Override{Match: "Z:/m/a.gguf", Ctx: 4096}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertSidecarOverride(gen, Override{Match: `z:\m\a.gguf`, Ctx: 8192}); err != nil {
		t.Fatal(err)
	}
	rows, _ := LoadSidecarOverrides(gen)
	if len(rows) != 1 || rows[0].Ctx != 8192 {
		t.Fatalf("backslash-spelled Match should replace, got %+v", rows)
	}
}
