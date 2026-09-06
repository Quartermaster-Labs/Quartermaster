package autogen

import (
	"path/filepath"
	"testing"
)

// A split set charges the sum of every shard. Stat'ing the first shard alone
// told the sizer an 80B MoE was a quarter of its size, so it offloaded the whole
// model to a 24 GB card and spent the phantom slack on context.
func TestGgufSetSizeBytes_SumsSiblings(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Model-Q4_K_XL-00001-of-00004.gguf", 1000)
	writeStub(t, dir, "Model-Q4_K_XL-00002-of-00004.gguf", 2000)
	writeStub(t, dir, "Model-Q4_K_XL-00003-of-00004.gguf", 3000)
	writeStub(t, dir, "Model-Q4_K_XL-00004-of-00004.gguf", 4000)

	first := filepath.Join(dir, "Model-Q4_K_XL-00001-of-00004.gguf")
	if got := ggufSetSizeBytes(first, 1000); got != 10000 {
		t.Errorf("first shard: got %d bytes, want 10000 (the whole set)", got)
	}
	// Asked about a later shard, the answer is still the set: the caller's own
	// size is trusted for that shard and the rest are stat'd.
	third := filepath.Join(dir, "Model-Q4_K_XL-00003-of-00004.gguf")
	if got := ggufSetSizeBytes(third, 3000); got != 10000 {
		t.Errorf("third shard: got %d bytes, want 10000", got)
	}
}

// A missing sibling is skipped, not fatal: a short total still beats a quarter
// one, and a truncated set is llama.cpp's error to report.
func TestGgufSetSizeBytes_MissingSiblingSkipped(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Model-00001-of-00003.gguf", 1000)
	writeStub(t, dir, "Model-00003-of-00003.gguf", 3000)

	first := filepath.Join(dir, "Model-00001-of-00003.gguf")
	if got := ggufSetSizeBytes(first, 1000); got != 4000 {
		t.Errorf("got %d bytes, want 4000 (shard 2 absent)", got)
	}
}

// An unsharded gguf keeps its own size, and a name that only looks shard-ish is
// not treated as a set.
func TestGgufSetSizeBytes_Unsharded(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Model-Q4_K_M.gguf", 1000)

	cases := []string{
		filepath.Join(dir, "Model-Q4_K_M.gguf"),
		filepath.Join(dir, "Model-001-of-004.gguf"), // not the 5-digit split form
		filepath.Join(dir, "Model-00001-of-00001.gguf"),
	}
	for _, p := range cases {
		if got := ggufSetSizeBytes(p, 1000); got != 1000 {
			t.Errorf("%s: got %d bytes, want the file's own 1000", filepath.Base(p), got)
		}
	}
}

// End to end: discovery collapses a split set to shard 1 but must report the
// whole set's size on the row, since every sizing path charges it as weights.
func TestDiscoverGgufModels_SplitSetChargesWholeSet(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Big-MoE-Q4_K_XL-00001-of-00004.gguf", 10_000_000)
	writeStub(t, dir, "Big-MoE-Q4_K_XL-00002-of-00004.gguf", 20_000_000)
	writeStub(t, dir, "Big-MoE-Q4_K_XL-00003-of-00004.gguf", 20_000_000)
	writeStub(t, dir, "Big-MoE-Q4_K_XL-00004-of-00004.gguf", 20_000_000)

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 served row (a split set is one model), got %d", len(rows))
	}
	if want := round(float64(70_000_000)/gib, 2); rows[0].SizeGB != want {
		t.Errorf("SizeGB=%v, want %v (all four shards)", rows[0].SizeGB, want)
	}
	if filepath.Base(rows[0].FullPath) != "Big-MoE-Q4_K_XL-00001-of-00004.gguf" {
		t.Errorf("FullPath=%q, want the real first shard", rows[0].FullPath)
	}
}
