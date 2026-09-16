package autogen

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// Every UI tab must be walkable. The Models tab renders its folder picker on
// all of them, so a category absent from CategoryOrder is a root the user can
// set and see and that is then never scanned.
func TestSettings_RootList_coversEveryUICategory(t *testing.T) {
	roots := map[string]string{}
	for _, c := range CategoryOrder {
		roots[c] = `/srv/` + c
	}
	s := Settings{ModelsRoot: `/srv/Models`, CategoryRoots: roots}
	got := s.RootList()
	if len(got) != len(CategoryOrder)+1 {
		t.Fatalf("RootList = %v, want %d entries", got, len(CategoryOrder)+1)
	}
	for i, c := range CategoryOrder {
		if want := `/srv/` + c; got[i+1] != want {
			t.Errorf("RootList[%d] = %q, want %q", i+1, got[i+1], want)
		}
	}
}

// CategoryOrder is the server-side mirror of MODEL_CATEGORIES in the dashboard;
// the pick endpoint rejects anything not in it, so a new UI tab that forgets to
// add itself here would 400 instead of working. Read the real file so the two
// lists cannot drift silently.
func TestCategoryOrder_matchesUITabs(t *testing.T) {
	const uiFile = "../../ui-svelte/src/lib/modelUtils.ts"
	src, err := os.ReadFile(uiFile)
	if err != nil {
		t.Skipf("ui sources unavailable: %v", err)
	}
	block := regexp.MustCompile(`(?s)MODEL_CATEGORIES[^=]*=\s*\[(.*?)\]`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("could not find MODEL_CATEGORIES in %s", uiFile)
	}
	var ui []string
	for _, m := range regexp.MustCompile(`id:\s*"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		ui = append(ui, string(m[1]))
	}
	if len(ui) == 0 {
		t.Fatalf("parsed no category ids from %s", uiFile)
	}
	if !slices.Equal(ui, CategoryOrder) {
		t.Fatalf("CategoryOrder = %v, UI MODEL_CATEGORIES = %v (keep them in sync)", CategoryOrder, ui)
	}
}

func TestIsCategory(t *testing.T) {
	for _, c := range CategoryOrder {
		if !IsCategory(c) {
			t.Errorf("IsCategory(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"", "LLM", "audio", "../etc"} {
		if IsCategory(c) {
			t.Errorf("IsCategory(%q) = true, want false", c)
		}
	}
}
