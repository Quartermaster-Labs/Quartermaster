package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStub(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A DFlash drafter (filename carries "dflash" as an infix, not a prefix) is
// paired to its target model by dir and not served as its own row.
func TestDiscoverGgufModels_DFlashPairing(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Qwen3.6-35B-A3B-Q4_K_M.gguf", 1024)
	writeStub(t, dir, "Qwen3.6-35B-A3B-DFlash-Q8_0.gguf", 60_000_000) // ~0.06 GB, rounds nonzero at 2dp

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 served row (draft not served standalone), got %d", len(rows))
	}
	row := rows[0]
	if row.DraftKind != "dflash" {
		t.Fatalf("DraftKind = %q, want dflash", row.DraftKind)
	}
	if row.DraftSizeGB <= 0 {
		t.Fatalf("DraftSizeGB not populated: %v", row.DraftSizeGB)
	}
	if filepath.Base(row.DraftPath) != "Qwen3.6-35B-A3B-DFlash-Q8_0.gguf" {
		t.Fatalf("DraftPath = %q", row.DraftPath)
	}
}

// The pre-existing MTP prefix convention ("mtp-*.gguf") still pairs correctly
// and is distinguished from a DFlash drafter by DraftKind.
func TestDiscoverGgufModels_MtpPairing(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Gemma-4-12B-it.gguf", 1024)
	writeStub(t, dir, "mtp-gemma-4-12B-it.gguf", 256)

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 served row, got %d", len(rows))
	}
	if rows[0].DraftKind != "mtp" {
		t.Fatalf("DraftKind = %q, want mtp", rows[0].DraftKind)
	}
}
