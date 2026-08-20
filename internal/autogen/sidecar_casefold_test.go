//go:build windows || darwin

package autogen

// On a case-folding filesystem /m/A.gguf and /m/a.gguf are one file, so a re-save
// under the other spelling must REPLACE the row rather than leave two conflicting
// overrides for one model (override resolution is first-match).

import "testing"

func TestAutogen_Sidecar_matchKeyFoldsCase(t *testing.T) {
	gen := writeGen(t)
	if _, err := UpsertSidecarOverride(gen, Override{Match: "/m/A.gguf", Ctx: 4096}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertSidecarOverride(gen, Override{Match: "/m/a.gguf", Ctx: 8192}); err != nil {
		t.Fatal(err)
	}
	rows, _ := LoadSidecarOverrides(gen)
	if len(rows) != 1 || rows[0].Ctx != 8192 {
		t.Fatalf("differently-cased Match should replace, got %+v", rows)
	}
}
