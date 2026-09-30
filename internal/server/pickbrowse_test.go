package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// pickBrowseTarget is the web picker's security boundary: the dashboard has no
// login, so anything it resolves outside the roots is a disk listing handed to
// whoever can reach the admin listener.
func TestProxyManager_PickBrowseContainment(t *testing.T) {
	models := t.TempDir()
	bins := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(models, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []pickRoot{{ID: "models", Path: models}, {ID: "backends", Path: bins}}

	t.Run("no root and no path lists the first root", func(t *testing.T) {
		rt, got, err := pickBrowseTarget(roots, "", "")
		if err != nil || rt.ID != "models" || got != models {
			t.Fatalf("got %q %q, %v", rt.ID, got, err)
		}
	})
	t.Run("an absolute path selects the root holding it", func(t *testing.T) {
		rt, got, err := pickBrowseTarget(roots, "", bins)
		if err != nil || rt.ID != "backends" || got != bins {
			t.Fatalf("got %q %q, %v", rt.ID, got, err)
		}
	})
	t.Run("an absolute path under no root is refused", func(t *testing.T) {
		if _, _, err := pickBrowseTarget(roots, "", outside); err == nil {
			t.Fatal("expected a refusal for a path outside every root")
		}
	})
	t.Run("an unknown root id is refused", func(t *testing.T) {
		if _, _, err := pickBrowseTarget(roots, "etc", ""); err == nil {
			t.Fatal("expected a refusal for an unknown root id")
		}
	})
	t.Run("traversal out of a named root is refused", func(t *testing.T) {
		if _, _, err := pickBrowseTarget(roots, "models", "../.."); err == nil {
			t.Fatal("expected a refusal for a traversing relative path")
		}
		if _, _, err := pickBrowseTarget(roots, "models", outside); err == nil {
			t.Fatal("expected a refusal for an absolute path outside the named root")
		}
	})
	t.Run("a symlink out of a root is refused", func(t *testing.T) {
		link := filepath.Join(models, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		if _, _, err := pickBrowseTarget(roots, "models", "escape"); err == nil {
			t.Fatal("a symlink pointing outside the root must not resolve")
		}
		if _, _, err := pickBrowseTarget(roots, "", link); err == nil {
			t.Fatal("an absolute symlink path pointing outside every root must not resolve")
		}
	})
}

func TestProxyManager_PickBrowseFilter(t *testing.T) {
	in := []hubFileEntry{
		{Name: "sub", Dir: true},
		{Name: "model.GGUF"},
		{Name: "readme.md"},
	}
	names := func(es []hubFileEntry) []string {
		out := []string{}
		for _, e := range es {
			out = append(out, e.Name)
		}
		return out
	}

	got, filtered := filterPickEntries(in, true, nil)
	if n := names(got); len(n) != 1 || n[0] != "sub" || filtered {
		t.Fatalf("folders only: got %v filtered=%v", n, filtered)
	}
	got, filtered = filterPickEntries(in, false, []string{".gguf"})
	if n := names(got); len(n) != 2 || n[1] != "model.GGUF" || !filtered {
		t.Fatalf("gguf filter: got %v filtered=%v", n, filtered)
	}
	got, filtered = filterPickEntries(in, false, nil)
	if len(got) != 3 || filtered {
		t.Fatalf("no filter: got %v filtered=%v", names(got), filtered)
	}
}

// A browser on another machine must never get a native dialog popped on the
// server's screen: the pick routes answer 501 so the UI opens the web picker.
func TestProxyManager_NativePickRefusesRemote(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/pick-folder", nil)
	req.RemoteAddr = "192.168.1.20:5555"
	rec := httptest.NewRecorder()
	called := false
	if _, ok := runNativePick(rec, req, func() (string, error) { called = true; return "x", nil }); ok {
		t.Fatal("expected no pick for a remote caller")
	}
	if called {
		t.Fatal("the native dialog must not be launched for a remote caller")
	}
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("got %d, want 501", rec.Code)
	}
}

func TestProxyManager_PickedFolder(t *testing.T) {
	dir := t.TempDir()
	if _, err := pickedFolder(dir); err != nil {
		t.Fatalf("existing folder refused: %v", err)
	}
	if _, err := pickedFolder("relative/dir"); err == nil {
		t.Fatal("a relative path must be refused")
	}
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pickedFolder(file); err == nil {
		t.Fatal("a file must be refused as a folder")
	}
}
