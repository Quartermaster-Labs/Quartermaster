package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/apppaths"
	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// The bare-binary case the feature exists for: no flags at all, so both paths
// land in the per-user config directory and a control file is seeded there.
func TestApplyUserConfigDefaults_BareLaunch(t *testing.T) {
	fs := newBundleFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "quartermaster")

	got, err := applyUserConfigDefaults(fs, map[string]bool{}, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}
	if want := filepath.Join(root, userConfigName); valueOf(t, fs, "config") != want {
		t.Errorf("config = %q, want %q", valueOf(t, fs, "config"), want)
	}
	genPath := filepath.Join(root, userGenerateName)
	if valueOf(t, fs, "generate") != genPath {
		t.Errorf("generate = %q, want %q", valueOf(t, fs, "generate"), genPath)
	}

	// Seeded and loadable: autogen.EnsureConfig exits the process on a control
	// file it cannot read, so "the file exists" is not enough of an assertion.
	if _, err := autogen.LoadGenerateFile(genPath, ""); err != nil {
		t.Fatalf("seeded generate file does not load: %v", err)
	}
}

// The safety rule of the whole change: a config the user named is theirs, so
// -generate must NOT be defaulted next to it -- autogen would overwrite the
// config on the next boot. Naming either half opts out of both.
func TestApplyUserConfigDefaults_ExplicitPathOptsOut(t *testing.T) {
	for _, given := range []string{"config", "generate"} {
		t.Run(given, func(t *testing.T) {
			fs := newBundleFlags()
			if err := fs.Parse([]string{"-" + given, "mine.yaml"}); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "quartermaster")

			got, err := applyUserConfigDefaults(fs, map[string]bool{given: true}, root, true)
			if err != nil {
				t.Fatal(err)
			}
			if got != "" {
				t.Fatalf("root = %q, want none applied", got)
			}
			other := map[string]string{"config": "generate", "generate": "config"}[given]
			if v := valueOf(t, fs, other); v != "" {
				t.Errorf("-%s = %q, want it left unset", other, v)
			}
			// Not even the directory: a run that opts out must leave no trace.
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Errorf("stat %s = %v, want not-exist", root, err)
			}
		})
	}
}

// Every later launch reuses the file the first one wrote. Re-seeding would
// throw away the modelsRoot, budgets and overrides the dashboard saves there.
func TestSeedGenerateFile_KeepsExisting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, userGenerateName)
	const body = "settings:\n  modelsRoot: \"/models\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedGenerateFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("generate file was rewritten:\n%s", got)
	}
}

func TestUserConfigRoot(t *testing.T) {
	root := apppaths.ConfigDir()
	if !strings.HasSuffix(root, string(os.PathSeparator)+"quartermaster") {
		t.Errorf("root = %q, want it to end in a quartermaster directory", root)
	}
	if !filepath.IsAbs(root) {
		t.Errorf("root = %q, want an absolute path", root)
	}
}

// -quit resolves the paths but creates nothing: a stop command that leaves a
// config directory behind is a stop command that just configured the machine.
// With the directory already there it must still resolve, because that file is
// where the listen address of the instance being stopped is stored.
func TestApplyUserConfigDefaults_QuitDoesNotCreate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "quartermaster")

	fs := newBundleFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	got, err := applyUserConfigDefaults(fs, map[string]bool{}, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("root = %q, want none applied for a missing directory", got)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want not-exist", root, err)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fs = newBundleFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if got, err := applyUserConfigDefaults(fs, map[string]bool{}, root, false); err != nil || got != root {
		t.Fatalf("applyUserConfigDefaults = %q, %v; want %q, nil", got, err, root)
	}
	if want := filepath.Join(root, userConfigName); valueOf(t, fs, "config") != want {
		t.Errorf("config = %q, want %q", valueOf(t, fs, "config"), want)
	}
	// Resolved, not seeded.
	if _, err := os.Stat(filepath.Join(root, userGenerateName)); !os.IsNotExist(err) {
		t.Errorf("generate file was seeded under -quit: %v", err)
	}
}
