package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

// Platform-neutral half: order, blank-dropping, and exact-duplicate collapse.
// Whether a *differently cased* root is a duplicate depends on the filesystem,
// so that lives in categoryroots_{windows,posix}_test.go.
func TestSettings_RootList_orderAndDedup(t *testing.T) {
	s := Settings{
		ModelsRoot: `/srv/Models`,
		CategoryRoots: map[string]string{
			"image":      `/srv/Image`,
			"tts":        ``,            // blank dropped
			"transcribe": `/srv/Models`, // exact dup of ModelsRoot
		},
	}
	got := s.RootList()
	want := []string{`/srv/Models`, `/srv/Image`}
	if len(got) != len(want) {
		t.Fatalf("RootList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RootList[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestUpsertSidecarRoot_setAndClear(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generate.yaml")
	if err := os.WriteFile(gen, []byte("settings:\n  modelsRoot: E:/Models\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := UpsertSidecarRoot(gen, "image", `E:\Image`); err != nil {
		t.Fatal(err)
	}
	roots, err := LoadSidecarCategoryRoots(gen)
	if err != nil {
		t.Fatal(err)
	}
	if roots["image"] != `E:\Image` {
		t.Fatalf("after set: roots = %v", roots)
	}

	// It must surface through the merged generate file too.
	gf, err := LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	if gf.Settings.CategoryRoots["image"] != `E:\Image` {
		t.Fatalf("merged settings missing root: %v", gf.Settings.CategoryRoots)
	}

	// Clearing (path "") removes the key.
	if _, err := UpsertSidecarRoot(gen, "image", ""); err != nil {
		t.Fatal(err)
	}
	roots, _ = LoadSidecarCategoryRoots(gen)
	if _, ok := roots["image"]; ok {
		t.Fatalf("after clear: key still present: %v", roots)
	}
}
