package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// newBundleFlags mirrors the subset of main's flag set that applyBundleFlags
// touches. Declared here rather than reaching into main() so the test does not
// depend on the order flags are declared in.
func newBundleFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("config", "", "")
	fs.String("generate", "", "")
	fs.String("listen", "", "")
	fs.String("playground-port", "", "")
	fs.Bool("watch-config", false, "")
	fs.Bool("app", false, "")
	fs.Bool("tray", false, "")
	return fs
}

func valueOf(t *testing.T, fs *flag.FlagSet, name string) string {
	t.Helper()
	f := fs.Lookup(name)
	if f == nil {
		t.Fatalf("no flag %q", name)
	}
	return f.Value.String()
}

func TestApplyBundleFlags_DoubleClick(t *testing.T) {
	fs := newBundleFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	applyBundleFlags(fs, filepath.FromSlash("/opt/qm"))

	want := map[string]string{
		"config":          filepath.Join("/opt/qm", "config", "config.yaml"),
		"generate":        filepath.Join("/opt/qm", "config", "quartermaster-generate.yaml"),
		"listen":          bundleListen,
		"playground-port": bundlePlayground,
		"watch-config":    "true",
		"app":             "true",
		"tray":            "false", // -app implies it downstream; not set here
	}
	for name, w := range want {
		if got := valueOf(t, fs, name); got != w {
			t.Errorf("%s = %q, want %q", name, got, w)
		}
	}
}

// A flag typed on the command line always wins; the rest are still filled in.
func TestApplyBundleFlags_ExplicitFlagWins(t *testing.T) {
	fs := newBundleFlags()
	if err := fs.Parse([]string{"-listen", ":9000", "-app=false"}); err != nil {
		t.Fatal(err)
	}
	applyBundleFlags(fs, filepath.FromSlash("/opt/qm"))

	if got := valueOf(t, fs, "listen"); got != ":9000" {
		t.Errorf("listen = %q, want :9000", got)
	}
	if got := valueOf(t, fs, "app"); got != "false" {
		t.Errorf("app = %q, want false (an explicit -app=false must not be re-enabled)", got)
	}
	if got := valueOf(t, fs, "playground-port"); got != bundlePlayground {
		t.Errorf("playground-port = %q, want the packaged default", got)
	}
}

// -tray means the login launch: no window, and no -app added behind its back.
func TestApplyBundleFlags_TrayStaysWindowless(t *testing.T) {
	for _, args := range [][]string{{"-tray"}, {"background"}} {
		fs := newBundleFlags()
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		applyBundleFlags(fs, filepath.FromSlash("/opt/qm"))

		if got := valueOf(t, fs, "app"); got != "false" {
			t.Errorf("%v: app = %q, want false", args, got)
		}
		if got := valueOf(t, fs, "tray"); got != "true" {
			t.Errorf("%v: tray = %q, want true", args, got)
		}
	}
}

// Outside an install nothing is defaulted, so a dev build (or a binary on
// someone's PATH) still refuses to run without -config rather than silently
// binding the packaged ports.
func TestBundleRootOf(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "Quartermaster.exe")

	if got, ok := bundleRootOf(exe); ok {
		t.Fatalf("bare directory reported as an install: %q", got)
	}

	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", bundleMarker), []byte("settings: {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := bundleRootOf(exe)
	if !ok {
		t.Fatal("install layout not recognised once the generate file exists")
	}
	// EvalSymlinks resolves the temp dir (macOS /var -> /private/var), so
	// compare resolved forms rather than the raw t.TempDir() string.
	want, _ := filepath.EvalSymlinks(root)
	if got != want {
		t.Errorf("root = %q, want %q", got, want)
	}
}
