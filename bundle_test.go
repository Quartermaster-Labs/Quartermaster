package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// newBundleFlags mirrors the subset of main's flag set that the bundle stages
// touch. Declared here rather than reaching into main() so the test does not
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
	fs.String("admin-allow", "", "")
	fs.Bool("admin-open", false, "")
	fs.Bool("watch-models", true, "")
	fs.Duration("watch-models-interval", 5*time.Second, "")
	fs.Bool("no-update-check", false, "")
	return fs
}

// applyBundle runs the two bundle stages the way main() does, with nothing in
// between. The stages are only meaningfully separate when stored app settings
// sit between them - see TestAppSettings_BeatBundleDefaults.
func applyBundle(fs *flag.FlagSet, root string) {
	applyBundlePaths(fs, root)
	applyBundleNetDefaults(fs, root)
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
	applyBundle(fs, filepath.FromSlash("/opt/qm"))

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
	applyBundle(fs, filepath.FromSlash("/opt/qm"))

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
	fs := newBundleFlags()
	if err := fs.Parse([]string{"-tray"}); err != nil {
		t.Fatal(err)
	}
	applyBundle(fs, filepath.FromSlash("/opt/qm"))

	if got := valueOf(t, fs, "app"); got != "false" {
		t.Errorf("app = %q, want false", got)
	}
	if got := valueOf(t, fs, "tray"); got != "true" {
		t.Errorf("tray = %q, want true", got)
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

// The precedence the three stages exist to produce:
// argv > stored app settings > packaged default.
func TestAppSettings_BeatBundleDefaults(t *testing.T) {
	no := false
	app := autogen.AppSettings{
		Listen:                 "127.0.0.1:9999",
		PlaygroundListen:       "127.0.0.1:9998",
		AdminAllow:             "100.64.0.0/10",
		WatchModels:            &no,
		WatchModelsIntervalSec: 30,
		UpdateCheck:            &no,
	}

	// Nothing on argv: the stored settings supply the addresses, and the
	// packaged defaults must not overwrite them afterwards.
	fs := newBundleFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	applyBundlePaths(fs, filepath.FromSlash("/opt/qm"))
	applyAppSettings(fs, map[string]bool{}, app)
	applyBundleNetDefaults(fs, filepath.FromSlash("/opt/qm"))

	want := map[string]string{
		"listen":                "127.0.0.1:9999",
		"playground-port":       "127.0.0.1:9998",
		"admin-allow":           "100.64.0.0/10",
		"watch-models":          "false",
		"watch-models-interval": "30s",
		"no-update-check":       "true",
	}
	for name, w := range want {
		if got := valueOf(t, fs, name); got != w {
			t.Errorf("%s = %q, want %q (stored app settings must beat the packaged default)", name, got, w)
		}
	}

	// argv wins over both. argvGiven is the set captured right after Parse -
	// passing it is the whole mechanism, since by this point Visit can no longer
	// tell an argv flag from one a previous stage set.
	fs2 := newBundleFlags()
	if err := fs2.Parse([]string{"-listen", ":7000"}); err != nil {
		t.Fatal(err)
	}
	argv := map[string]bool{}
	fs2.Visit(func(f *flag.Flag) { argv[f.Name] = true })
	applyBundlePaths(fs2, filepath.FromSlash("/opt/qm"))
	applyAppSettings(fs2, argv, app)
	applyBundleNetDefaults(fs2, filepath.FromSlash("/opt/qm"))

	if got := valueOf(t, fs2, "listen"); got != ":7000" {
		t.Errorf("listen = %q, want :7000 (argv must beat the stored setting)", got)
	}
	if got := valueOf(t, fs2, "playground-port"); got != "127.0.0.1:9998" {
		t.Errorf("playground-port = %q, want the stored setting", got)
	}
}
