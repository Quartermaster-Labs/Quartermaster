package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadGgufMetadataCached_HitSkipsParse verifies a fingerprint-matching entry
// is returned without touching the (bogus) file, and that changing the file
// invalidates the entry so the real parser runs again. Uses a non-GGUF file: a
// cache hit must succeed (never parses it), a miss must fail (tries to parse it).
func TestReadGgufMetadataCached_HitSkipsParse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.gguf")
	if err := os.WriteFile(p, []byte("not a real gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.ToSlash(p)

	// Seed a cache entry with the current fingerprint.
	want := Metadata{Architecture: "test-arch", BlockCount: 42}
	metaCacheMu.Lock()
	metaCache[key] = cachedMeta{size: fi.Size(), mtime: fi.ModTime().UnixNano(), meta: want}
	metaCacheMu.Unlock()

	// Cache hit: returns the seeded metadata, never parses the bogus file.
	got, err := ReadGgufMetadataCached(p)
	if err != nil {
		t.Fatalf("cache hit should not error: %v", err)
	}
	if got.Architecture != "test-arch" || got.BlockCount != 42 {
		t.Fatalf("cache hit returned wrong metadata: %+v", got)
	}

	// Change the file: size differs from the cached fingerprint, so the next read
	// is a miss and falls through to ReadGgufMetadata, which fails on non-GGUF.
	if err := os.WriteFile(p, []byte("changed content, different length"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGgufMetadataCached(p); err == nil {
		t.Error("expected parse error after fingerprint invalidation, got nil")
	}
}
