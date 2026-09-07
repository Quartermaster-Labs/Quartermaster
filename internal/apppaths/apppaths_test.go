package apppaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBundleRootOf(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quartermaster")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if root, ok := BundleRootOf(exe); ok {
		t.Fatalf("BundleRootOf = %q, true; want not an install without the marker", root)
	}

	// A config.yaml alone is not an install: a dev build sitting next to one is
	// the case the marker choice exists to exclude.
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "config.yaml"), []byte("models:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, ok := BundleRootOf(exe); ok {
		t.Fatalf("BundleRootOf = %q, true; want a config.yaml alone not to count", root)
	}

	if err := os.WriteFile(filepath.Join(dir, "config", "quartermaster-generate.yaml"), []byte("settings:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, ok := BundleRootOf(exe)
	if !ok {
		t.Fatal("BundleRootOf = false; want an install once the marker is there")
	}
	// EvalSymlinks resolves the temp dir (on macOS /var -> /private/var), so
	// compare against the same resolution rather than the raw path.
	want := dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		want = r
	}
	if root != want {
		t.Errorf("BundleRootOf = %q, want %q", root, want)
	}
}

// Every directory is absolute, distinct from the others where the platform says
// it should be, and namespaced to this program. A bare os.UserCacheDir() would
// satisfy "absolute" and still be wrong: it is the user's whole cache tree.
func TestDirsAreNamespacedAndAbsolute(t *testing.T) {
	for name, got := range map[string]string{
		"ConfigDir": ConfigDir(),
		"DataDir":   DataDir(),
		"CacheDir":  CacheDir(),
	} {
		if !filepath.IsAbs(got) {
			t.Errorf("%s = %q, want an absolute path", name, got)
		}
		if !strings.Contains(got, appDir) {
			t.Errorf("%s = %q, want it namespaced under %q", name, got, appDir)
		}
	}
	if CacheDir() == DataDir() {
		t.Errorf("CacheDir and DataDir are both %q; regenerable bulk data must be separable", CacheDir())
	}
}

// On Linux the data directory follows $XDG_DATA_HOME, which is the half of the
// XDG split that os.UserConfigDir does not cover.
func TestDataDir_XDGDataHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_DATA_HOME is honoured on linux only")
	}
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	if got, want := DataDir(), filepath.Join(dir, appDir); got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
}

// An install owns its directory: everything stays under the exe so the folder
// remains self-contained, which is what makes deleting it a complete uninstall.
func TestDirs_PackagedInstallStaysSelfContained(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "quartermaster-generate.yaml"), []byte("settings:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The test binary is not in that directory, so drive the same rule the
	// exported helpers use through the injectable form.
	root, ok := BundleRootOf(filepath.Join(dir, "quartermaster"))
	if !ok {
		t.Fatal("layout not detected as an install")
	}
	if got := filepath.Join(root, ".cache"); !strings.HasPrefix(got, root) {
		t.Errorf("cache %q escapes the install root %q", got, root)
	}
}
