package server

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// revealTarget is the security boundary for both the reveal button and the
// in-app listing, so the escape attempts are the cases worth pinning.
func TestProxyManager_RevealTargetContainment(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inside := filepath.Join(root, "repo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(inside, "model.gguf")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("empty means the root", func(t *testing.T) {
		got, err := revealTarget(root, "")
		if err != nil || got != root {
			t.Fatalf("got %q, %v; want %q", got, err, root)
		}
	})
	t.Run("a file resolves to its folder", func(t *testing.T) {
		got, err := revealTarget(root, file)
		if err != nil || got != inside {
			t.Fatalf("got %q, %v; want %q", got, err, inside)
		}
	})
	t.Run("outside is refused", func(t *testing.T) {
		if _, err := revealTarget(root, outside); err == nil {
			t.Fatal("expected a refusal for a path outside the models root")
		}
	})
	t.Run("traversal is refused", func(t *testing.T) {
		if _, err := revealTarget(root, filepath.Join(root, "..", "elsewhere")); err == nil {
			t.Fatal("expected a refusal for a traversing path")
		}
	})
	t.Run("a symlink out of the root is refused", func(t *testing.T) {
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outside, link); err != nil {
			// Unprivileged Windows cannot make one; nothing to assert there.
			t.Skipf("cannot create a symlink here: %v", err)
		}
		if _, err := revealTarget(root, link); err == nil {
			t.Fatal("a symlink pointing outside the models root must not resolve")
		}
	})
	t.Run("a relative path resolves under the root", func(t *testing.T) {
		got, err := revealTarget(root, "repo")
		if err != nil || got != inside {
			t.Fatalf("got %q, %v; want %q", got, err, inside)
		}
		// The breadcrumb form, slashes and all.
		if got, err := revealTarget(root, "repo/model.gguf"); err != nil || got != inside {
			t.Fatalf("slashed rel: got %q, %v; want %q", got, err, inside)
		}
	})
	t.Run("a relative path cannot traverse out", func(t *testing.T) {
		if _, err := revealTarget(root, "../.."); err == nil {
			t.Fatal("expected a refusal for a traversing relative path")
		}
	})
	t.Run("a missing folder is refused", func(t *testing.T) {
		if _, err := revealTarget(root, filepath.Join(root, "gone")); err == nil {
			t.Fatal("expected a refusal for a path that is not on disk")
		}
	})
}

// The listing itself: folders first, then name, with sizes only on files.
func TestProxyManager_HubFilesListing(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"zeta-repo", "Alpha-repo"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"b.gguf", "A.gguf"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("1234"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries, truncated, err := listFolderEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("four entries should not truncate")
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name)
	}
	want := []string{"Alpha-repo", "zeta-repo", "A.gguf", "b.gguf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order: got %v, want %v", got, want)
	}
	for _, e := range entries {
		if e.Dir && e.Size != 0 {
			t.Fatalf("%s: a directory must report no size", e.Name)
		}
		if !e.Dir && e.Size != 4 {
			t.Fatalf("%s: got size %d, want 4", e.Name, e.Size)
		}
		if e.Modified == "" {
			t.Fatalf("%s: missing mtime", e.Name)
		}
	}
}

// A directory bigger than the cap is reported as truncated rather than handed
// to the browser whole.
func TestProxyManager_HubFilesTruncates(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < hubFilesMaxEntries+5; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, truncated, err := listFolderEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(entries) != hubFilesMaxEntries {
		t.Fatalf("got %d entries, truncated=%v; want %d and true", len(entries), truncated, hubFilesMaxEntries)
	}
}

func TestProxyManager_FileManagerAvailable(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if !fileManagerAvailable() {
			t.Fatal("windows and darwin always ship an opener")
		}
		return
	}
	// On Linux the answer is whatever the box has; it must not panic and must
	// agree with the PATH lookup, which is all this can assert portably.
	_ = fileManagerAvailable()
}

func TestProxyManager_RevealTargetKeepsSeparators(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A request can carry forward slashes even on Windows; the containment
	// check has to accept that rather than read it as a different tree.
	got, err := revealTarget(root, strings.ReplaceAll(sub, string(filepath.Separator), "/"))
	if err != nil {
		t.Fatalf("forward-slashed path refused: %v", err)
	}
	if filepath.Clean(got) != sub {
		t.Fatalf("got %q, want %q", got, sub)
	}
}
