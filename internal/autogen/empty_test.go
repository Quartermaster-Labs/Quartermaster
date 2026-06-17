package autogen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A blank modelsRoot must not be a load error: the server boots with an empty
// catalog so a setup UI can point it at a folder later.
func TestAutogen_LoadGenerateFile_blankModelsRootOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gen.yaml")
	if err := os.WriteFile(path, []byte("settings:\n  serverExe: llama-server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gf, err := LoadGenerateFile(path, "")
	if err != nil {
		t.Fatalf("blank modelsRoot should load, got error: %v", err)
	}
	if gf.Settings.ModelsRoot != "" {
		t.Fatalf("expected empty ModelsRoot, got %q", gf.Settings.ModelsRoot)
	}
}

// Discovery short-circuits on an empty root (no cwd scan) and yields no rows.
func TestAutogen_DiscoverGgufModels_emptyRoot(t *testing.T) {
	rows, err := DiscoverGgufModels("")
	if err != nil {
		t.Fatalf("empty root should not error, got: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("empty root should yield 0 rows, got %d", len(rows))
	}
}

// Generate over an empty/blank root emits a valid config with no models and an
// empty exclusive group.
func TestAutogen_Generate_emptyCatalog(t *testing.T) {
	out, err := Generate(GenerateFile{Settings: Settings{ServerExe: "llama-server"}}, "T")
	if err != nil {
		t.Fatalf("generate empty catalog: %v", err)
	}
	if !strings.Contains(out, "models:\n") || !strings.Contains(out, "  exclusive:\n") {
		t.Fatalf("expected models + exclusive group, got:\n%s", out)
	}
}
