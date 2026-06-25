package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGen(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gen.yaml")
	if err := os.WriteFile(p, []byte("settings:\n  modelsRoot: Z:/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAutogen_Sidecar_pruneDeadPaths(t *testing.T) {
	gen := writeGen(t)
	dir := filepath.Dir(gen)
	live := filepath.Join(dir, "live.gguf")
	if err := os.WriteFile(live, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "gone.gguf")

	for _, ov := range []Override{
		{Match: live, Ctx: 1},        // explicit path, exists -> keep
		{Match: dead, Ctx: 2},        // explicit path, missing -> prune
		{Match: "*Qwen*", Ctx: 3},    // glob -> keep (never pruned)
		{Match: "bare-name", Ctx: 4}, // non-path fragment -> keep
	} {
		if _, err := UpsertSidecarOverride(gen, ov); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := PruneSidecar(gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != dead {
		t.Fatalf("expected only %q pruned, got %v", dead, removed)
	}
	rows, _ := LoadSidecarOverrides(gen)
	if len(rows) != 3 {
		t.Fatalf("expected 3 kept, got %d: %+v", len(rows), rows)
	}

	// Idempotent: nothing left to prune.
	again, err := PruneSidecar(gen)
	if err != nil || again != nil {
		t.Fatalf("second prune should be a no-op, got %v err=%v", again, err)
	}
}

func TestAutogen_Sidecar_upsertReplaceDelete(t *testing.T) {
	gen := writeGen(t)

	// Absent sidecar => empty.
	rows, err := LoadSidecarOverrides(gen)
	if err != nil || len(rows) != 0 {
		t.Fatalf("expected empty, got %v err=%v", rows, err)
	}

	// Insert.
	if _, err := UpsertSidecarOverride(gen, Override{Match: "Z:/m/a.gguf", Ctx: 4096}); err != nil {
		t.Fatal(err)
	}
	rows, _ = LoadSidecarOverrides(gen)
	if len(rows) != 1 || rows[0].Ctx != 4096 {
		t.Fatalf("insert failed: %+v", rows)
	}

	// Replace by Match (separator/case-insensitive) — not a second row.
	if _, err := UpsertSidecarOverride(gen, Override{Match: `z:\m\a.gguf`, Ctx: 8192}); err != nil {
		t.Fatal(err)
	}
	rows, _ = LoadSidecarOverrides(gen)
	if len(rows) != 1 || rows[0].Ctx != 8192 {
		t.Fatalf("replace failed: %+v", rows)
	}

	// Delete (reset to default).
	removed, err := DeleteSidecarOverride(gen, "Z:/m/a.gguf")
	if err != nil || !removed {
		t.Fatalf("delete failed removed=%v err=%v", removed, err)
	}
	rows, _ = LoadSidecarOverrides(gen)
	if len(rows) != 0 {
		t.Fatalf("expected empty after delete, got %+v", rows)
	}
}

// Sidecar overrides must merge ahead of the generate file's (UI wins).
func TestAutogen_Sidecar_mergedFirst(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "gen.yaml")
	genBody := "settings:\n  modelsRoot: Z:/x\noverrides:\n  - match: \"*\"\n    ctx: 1\n"
	if err := os.WriteFile(gen, []byte(genBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertSidecarOverride(gen, Override{Match: "Z:/m/a.gguf", Ctx: 9}); err != nil {
		t.Fatal(err)
	}
	gf, err := LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gf.Overrides) != 2 || gf.Overrides[0].Match != "Z:/m/a.gguf" {
		t.Fatalf("sidecar row should be first, got: %+v", gf.Overrides)
	}
}
