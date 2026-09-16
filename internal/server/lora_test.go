package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListLoraFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zeta.gguf", "Alpha.GGUF", "notes.txt", "beta.safetensors", "mid.gguf"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A nested collection is NOT walked: a mis-set folder is routinely a whole
	// models tree, and opening a modal must not read one.
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "deep.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listLoraFiles(dir)
	want := []string{"Alpha.GGUF", "mid.gguf", "zeta.gguf"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("entry %d = %q, want %q (case-insensitive name sort, .gguf only)", i, got[i].Name, want[i])
		}
	}
}

// Every failure mode answers with an empty list rather than an error: the
// picker renders the same either way, and a folder pointing outside the app is
// allowed to be missing.
func TestListLoraFiles_emptyAndMissing(t *testing.T) {
	if got := listLoraFiles(""); got == nil || len(got) != 0 {
		t.Errorf("no folder configured: got %v, want an empty non-nil slice (it is marshalled as [], not null)", got)
	}
	if got := listLoraFiles(filepath.Join(t.TempDir(), "gone")); got == nil || len(got) != 0 {
		t.Errorf("missing folder: got %v, want an empty non-nil slice", got)
	}
}

// The DTO mappers drop blank rows in both directions, so a half-typed row in
// the editor never reaches the generate file as a --lora with no filename.
func TestLoraRefDTOs_dropBlankPaths(t *testing.T) {
	half := 0.5
	in := []loraRefDTO{{Path: "a.gguf"}, {Path: "   "}, {Path: " b.gguf ", Scale: &half}}
	refs := toLoraRefs(in)
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2", len(refs))
	}
	if refs[1].Path != "b.gguf" {
		t.Errorf("path not trimmed: %q", refs[1].Path)
	}
	if refs[1].Scale == nil || *refs[1].Scale != 0.5 {
		t.Errorf("scale lost: %v", refs[1].Scale)
	}
	// A nil scale must stay nil, not become 0: 0 is "loaded but inert".
	if refs[0].Scale != nil {
		t.Errorf("an unset scale became %v, which would emit --lora-scaled ... 0", *refs[0].Scale)
	}
	if got := toLoraRefDTOs(nil); got != nil {
		t.Errorf("no adapters should map to nil (omitted from the DTO), got %v", got)
	}
}
