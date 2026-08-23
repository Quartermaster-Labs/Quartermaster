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

// Diffusion text encoders / VAEs are components of an image model, not models.
// They used to be emitted as llama-server rows and show up in the UI as LLMs.
func TestDiscoverGgufModels_SkipsImageEncoders(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Qwen3.6-35B-A3B-Q4_K_M.gguf", 1024)
	for _, n := range []string{
		"T5-v1_1-xxl-encoder-Q8_0.gguf",
		"t5xxl_fp16.gguf",
		"umt5-xxl-encoder-Q5_K_M.gguf",
		"clip_l.gguf",
		"clip-g-Q8_0.gguf",
		"flux-vae-f16.gguf",
		"ae.gguf",
	} {
		writeStub(t, dir, n, 1024)
	}

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].BaseID != "qwen3.6-35b-a3b" {
		var got []string
		for _, r := range rows {
			got = append(got, r.ID)
		}
		t.Fatalf("want only the served model, got %v", got)
	}
}

// The encoder rule must not eat a real seq2seq LLM that merely starts with t5.
func TestDiscoverGgufModels_KeepsFlanT5(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "flan-t5-large-Q4_K_M.gguf", 1024)

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want flan-t5 served, got %d rows", len(rows))
	}
}

// A "FastMTP" head is skipped outright: not served as its own model, and not
// paired as a -md draft either. Regression on both halves: it used to surface in
// the catalog as a phantom second model (growing nonsensical 32k/64k ctx
// variants of its own), and pairing it as a draft instead hard-failed the
// llama-server launch, since its reduced 32768-row output.weight + d2t remap is
// a vocab layout no build we ship can load.
func TestDiscoverGgufModels_FastMtpSkipped(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Qwen3.8-27B-Uncensored-HauhauCS-Aggressive-Q4_K_P.gguf", 1024)
	writeStub(t, dir, "Qwen3.8-27B-Uncensored-HauhauCS-Aggressive-FastMTP-32K.gguf", 256)

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 served row, got %d: %+v", len(rows), rows)
	}
	if rows[0].DraftPath != "" || rows[0].DraftKind != "" {
		t.Fatalf("FastMTP head paired as a draft: kind=%q path=%q", rows[0].DraftKind, rows[0].DraftPath)
	}
	// The config editor's "is a draft available for this dir" probe must agree.
	if p, k, _ := DraftSidecarForDir(dir); p != "" || k != "" {
		t.Fatalf("DraftSidecarForDir offered the FastMTP head: kind=%q path=%q", k, p)
	}
}

// A model that merely advertises a baked-in MTP head in its file name is a real
// model and must keep being served — the FastMTP rule must not swallow it.
func TestDiscoverGgufModels_NativeMtpIsNotASidecar(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Qwen3.6-27B-uncensored-heretic-v2-Native-MTP-Preserved-Q4_K_M.gguf", 1024)

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 served row, got %d: %+v", len(rows), rows)
	}
	if rows[0].DraftPath != "" {
		t.Fatalf("DraftPath = %q, want empty", rows[0].DraftPath)
	}
}

// An NVFP4 gguf whose quant sits MID-name ("…-NVFP4-MTP-MID-HIGH") is a quant of
// its model, not a model of its own: the row carries the token in Quant, and the
// id is left exactly as the file spells it rather than gaining a second "-nvfp4"
// tail. Both matter downstream - the Models table cuts a row's key at the quant,
// so an unrecognised one gave every ctx tier and the vision twin a row apiece.
func TestDiscoverGgufModels_NVFP4MidNameQuant(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Qwen3.8-27B-NVFP4-MTP-MID-HIGH.gguf", 1024)

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 served row, got %d: %+v", len(rows), rows)
	}
	if rows[0].Quant != "NVFP4" {
		t.Fatalf("Quant = %q, want NVFP4", rows[0].Quant)
	}
	const want = "qwen3.8-27b-nvfp4-mtp-mid-high"
	if rows[0].ID != want {
		t.Fatalf("ID = %q, want %q (no duplicated quant tail)", rows[0].ID, want)
	}
	if got := ModelBaseKey(rows[0].BaseID); got != "qwen3.8-27b" {
		t.Fatalf("ModelBaseKey(%q) = %q, want qwen3.8-27b", rows[0].BaseID, got)
	}
}

// A trailing quant is still stripped from the base and re-appended to the id -
// the mid-name rule must not stop the common layout from producing an id.
func TestDiscoverGgufModels_TrailingQuantStillAppended(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Qwen3.8-27B-NVFP4.gguf", 1024)

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 served row, got %d", len(rows))
	}
	if rows[0].BaseID != "qwen3.8-27b" || rows[0].ID != "qwen3.8-27b-nvfp4" {
		t.Fatalf("BaseID/ID = %q/%q", rows[0].BaseID, rows[0].ID)
	}
}
