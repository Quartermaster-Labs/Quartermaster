//go:build !windows && !darwin

package autogen

import "testing"

// On a case-sensitive filesystem /m/A.gguf and /m/a.gguf are two different files,
// so they must get two independent override rows. Folding case here would let an
// override for one model silently overwrite another's.
func TestAutogen_Sidecar_matchKeyKeepsCase(t *testing.T) {
	gen := writeGen(t)
	if _, err := UpsertSidecarOverride(gen, Override{Match: "/m/A.gguf", Ctx: 4096}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertSidecarOverride(gen, Override{Match: "/m/a.gguf", Ctx: 8192}); err != nil {
		t.Fatal(err)
	}
	rows, _ := LoadSidecarOverrides(gen)
	if len(rows) != 2 {
		t.Fatalf("differently-cased Match should be a second row, got %+v", rows)
	}
}
