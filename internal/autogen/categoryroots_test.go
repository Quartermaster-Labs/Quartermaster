package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettings_RootList_orderAndDedup(t *testing.T) {
	s := Settings{
		ModelsRoot: `E:\Models`,
		CategoryRoots: map[string]string{
			"image":      `E:\Image`,
			"tts":        ``,          // blank dropped
			"transcribe": `E:/models`, // dup of ModelsRoot (case/sep-insensitive)
		},
	}
	got := s.RootList()
	want := []string{`E:\Models`, `E:\Image`}
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
