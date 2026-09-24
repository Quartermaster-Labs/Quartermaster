package server

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSized(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Every file counts once, whatever it is: shards, a projector, a stray .part.
// That is the whole point versus summing catalog rows.
func TestDiskUsage_SumsEveryFileOnce(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "a", "m-00001-of-00002.gguf"), 1000)
	writeSized(t, filepath.Join(root, "a", "m-00002-of-00002.gguf"), 500)
	writeSized(t, filepath.Join(root, "a", "mmproj.gguf"), 200)
	writeSized(t, filepath.Join(root, "b", "c", "x.gguf.part"), 30)

	res, err := walkDiskUsage(root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 1730 || res.Files != 4 || res.Skipped != 0 {
		t.Errorf("got bytes=%d files=%d skipped=%d, want 1730/4/0", res.Bytes, res.Files, res.Skipped)
	}
}

// A link is not followed: a link back up the tree must not loop, and a linked
// folder is not counted twice. Skipped where the OS will not let a test make one
// (Windows without developer mode).
func TestDiskUsage_DoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "real", "w.gguf"), 100)
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create symlink here: %v", err)
	}
	if err := os.Symlink(root, filepath.Join(root, "real", "loop")); err != nil {
		t.Skipf("cannot create symlink here: %v", err)
	}
	res, err := walkDiskUsage(root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 100 || res.Files != 1 {
		t.Errorf("got bytes=%d files=%d, want 100/1", res.Bytes, res.Files)
	}
}

// A models root that is itself a link is resolved, not walked as a leaf.
func TestDiskUsage_RootSymlinkIsResolved(t *testing.T) {
	dir := t.TempDir()
	writeSized(t, filepath.Join(dir, "real", "w.gguf"), 64)
	link := filepath.Join(dir, "models")
	if err := os.Symlink(filepath.Join(dir, "real"), link); err != nil {
		t.Skipf("cannot create symlink here: %v", err)
	}
	res, err := walkDiskUsage(link)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 64 || res.Root != link {
		t.Errorf("got bytes=%d root=%q, want 64 and the unresolved root", res.Bytes, res.Root)
	}
}

func TestDiskUsage_MissingRootErrors(t *testing.T) {
	if _, err := walkDiskUsage(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a missing models root")
	}
}

// The cache is keyed by root: a reload that moves the models folder must not be
// answered with the old tree's total.
func TestDiskUsage_CacheKeyedByRoot(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeSized(t, filepath.Join(a, "x"), 10)
	writeSized(t, filepath.Join(b, "y"), 20)
	ra, err := modelsDiskUsage(a, false)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := modelsDiskUsage(b, false)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Bytes != 10 || rb.Bytes != 20 {
		t.Errorf("got %d/%d, want 10/20", ra.Bytes, rb.Bytes)
	}
	// Within the TTL a new file is not seen; refresh forces a rewalk.
	writeSized(t, filepath.Join(b, "z"), 5)
	if r, _ := modelsDiskUsage(b, false); r.Bytes != 20 {
		t.Errorf("cached read = %d, want 20", r.Bytes)
	}
	if r, _ := modelsDiskUsage(b, true); r.Bytes != 25 {
		t.Errorf("forced read = %d, want 25", r.Bytes)
	}
}
