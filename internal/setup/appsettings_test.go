package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// The whole point of setAppKey: what the setup binary writes must be what the
// server reads back, through autogen's own loader rather than through a string
// comparison that would pass on a file the server cannot parse.
func TestApplyAppSettings_RoundTripsThroughAutogen(t *testing.T) {
	gen := filepath.Join(t.TempDir(), "quartermaster-generate.yaml")
	if err := os.WriteFile(gen, []byte(autogen.MinimalGenerateFile), 0o644); err != nil {
		t.Fatal(err)
	}
	open := true
	w := New(Options{App: autogen.AppSettings{
		AdminAllow:  "192.168.1.0/24",
		AdminOpen:   &open,
		TlsCertFile: "C:/certs/qm.pem",
		TlsKeyFile:  "/etc/ssl/qm.key",
	}})
	if err := w.applyAppSettings(gen); err != nil {
		t.Fatalf("applyAppSettings: %v", err)
	}

	got, err := autogen.LoadAppSettings(gen)
	if err != nil {
		t.Fatalf("LoadAppSettings: %v", err)
	}
	if got.AdminAllow != "192.168.1.0/24" {
		t.Errorf("AdminAllow = %q", got.AdminAllow)
	}
	if got.AdminOpen == nil || !*got.AdminOpen {
		t.Errorf("AdminOpen = %v, want true", got.AdminOpen)
	}
	// Backslashes are normalised to forward slashes on the way in, which is
	// what keeps a Windows path from reading as escape sequences in YAML.
	if got.TlsCertFile != "C:/certs/qm.pem" || got.TlsKeyFile != "/etc/ssl/qm.key" {
		t.Errorf("TLS = %q, %q", got.TlsCertFile, got.TlsKeyFile)
	}
}

// A run that passed no flags must leave the file byte-identical: the common
// case is a desktop install through the window, and rewriting the operator's
// commented file for nothing is how comments get lost.
func TestApplyAppSettings_NoFlagsLeavesTheFileAlone(t *testing.T) {
	gen := filepath.Join(t.TempDir(), "quartermaster-generate.yaml")
	if err := os.WriteFile(gen, []byte(autogen.MinimalGenerateFile), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(Options{}).applyAppSettings(gen); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("file changed:\n%s", after)
	}
}

// setAppKey edits the nested block, so the cases that matter are the ones
// setSettingsKey's flat scan cannot express.
func TestSetAppKey(t *testing.T) {
	t.Run("creates the app block inside an existing settings block", func(t *testing.T) {
		gen := writeGen(t, "settings:\n  modelsRoot: \"/models\"\n\noverrides: []\n")
		if err := setAppKey(gen, "adminAllow", "10.0.0.0/8"); err != nil {
			t.Fatal(err)
		}
		got := read(t, gen)
		if !strings.Contains(got, "  app:\n    adminAllow: 10.0.0.0/8\n") {
			t.Errorf("block not created:\n%s", got)
		}
		if !strings.Contains(got, "overrides: []") {
			t.Errorf("trailing top-level key lost:\n%s", got)
		}
	})

	t.Run("replaces a value already in the block", func(t *testing.T) {
		gen := writeGen(t, "settings:\n  app:\n    listen: \"0.0.0.0:1250\"\n    adminAllow: 10.0.0.0/8\n")
		if err := setAppKey(gen, "adminAllow", "192.168.0.0/16"); err != nil {
			t.Fatal(err)
		}
		got := read(t, gen)
		if strings.Contains(got, "10.0.0.0/8") {
			t.Errorf("old value survived:\n%s", got)
		}
		if !strings.Contains(got, "    adminAllow: 192.168.0.0/16") {
			t.Errorf("new value missing:\n%s", got)
		}
		if !strings.Contains(got, `    listen: "0.0.0.0:1250"`) {
			t.Errorf("sibling key disturbed:\n%s", got)
		}
	})

	t.Run("adds to an app block that is missing the key", func(t *testing.T) {
		gen := writeGen(t, "settings:\n  app:\n    listen: \"0.0.0.0:1250\"\n  modelsRoot: \"/models\"\n")
		if err := setAppKey(gen, "adminOpen", "true"); err != nil {
			t.Fatal(err)
		}
		got := read(t, gen)
		if !strings.Contains(got, "    listen: \"0.0.0.0:1250\"\n    adminOpen: true\n  modelsRoot:") {
			t.Errorf("key not appended inside the block:\n%s", got)
		}
	})

	t.Run("builds settings when the file has none", func(t *testing.T) {
		gen := writeGen(t, "overrides: []\n")
		if err := setAppKey(gen, "adminAllow", "10.0.0.0/8"); err != nil {
			t.Fatal(err)
		}
		if got := read(t, gen); !strings.Contains(got, "settings:\n  app:\n    adminAllow: 10.0.0.0/8") {
			t.Errorf("settings block not created:\n%s", got)
		}
	})

	// Comments are the reason this edits lines instead of round-tripping
	// through yaml.v3, and a CRLF file is what a Windows operator edits.
	t.Run("keeps comments and the file's line ending", func(t *testing.T) {
		gen := writeGen(t, "settings:\r\n  # where the models live\r\n  modelsRoot: \"/models\"\r\n")
		if err := setAppKey(gen, "adminAllow", "10.0.0.0/8"); err != nil {
			t.Fatal(err)
		}
		got := read(t, gen)
		if !strings.Contains(got, "# where the models live") {
			t.Errorf("comment lost:\n%q", got)
		}
		if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
			t.Errorf("mixed line endings:\n%q", got)
		}
	})
}

func writeGen(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "quartermaster-generate.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
