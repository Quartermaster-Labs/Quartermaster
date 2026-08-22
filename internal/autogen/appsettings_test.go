package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

// The sidecar block layers over the generate file field by field, and an unset
// field inherits rather than blanking. This is the whole reason mergeAppSettings
// is written out by hand instead of reflected.
func TestLoadAppSettings_SidecarLayersOverFile(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "quartermaster-generate.yaml")
	write(t, gen, `
settings:
  modelsRoot: ""
  app:
    listen: "0.0.0.0:1250"
    adminAllow: "10.0.0.0/8"
    watchModelsIntervalSec: 9
    updateCheck: false
`)

	got, err := LoadAppSettings(gen)
	if err != nil {
		t.Fatalf("LoadAppSettings: %v", err)
	}
	if got.Listen != "0.0.0.0:1250" || got.AdminAllow != "10.0.0.0/8" || got.WatchModelsIntervalSec != 9 {
		t.Fatalf("file block not read: %+v", got)
	}
	if got.UpdateCheck == nil || *got.UpdateCheck {
		t.Fatalf("updateCheck: want explicit false, got %v", got.UpdateCheck)
	}

	// The dashboard changes the port and clears the allow-list, and says nothing
	// about the interval.
	if err := UpsertSidecarApp(gen, AppSettings{Listen: "127.0.0.1:9000"}); err != nil {
		t.Fatalf("UpsertSidecarApp: %v", err)
	}
	got, err = LoadAppSettings(gen)
	if err != nil {
		t.Fatalf("LoadAppSettings after upsert: %v", err)
	}
	if got.Listen != "127.0.0.1:9000" {
		t.Errorf("listen = %q, want the sidecar value", got.Listen)
	}
	if got.WatchModelsIntervalSec != 9 {
		t.Errorf("interval = %d, want 9 inherited from the file", got.WatchModelsIntervalSec)
	}
	// An empty string in the sidecar means "not set here", NOT "clear it" — the
	// dashboard clears by writing a block that keeps every field it still wants.
	if got.AdminAllow != "10.0.0.0/8" {
		t.Errorf("adminAllow = %q, want the file value inherited", got.AdminAllow)
	}
	if got.UpdateCheck == nil || *got.UpdateCheck {
		t.Errorf("updateCheck should still be the file's explicit false, got %v", got.UpdateCheck)
	}
}

// UpsertSidecarApp replaces its block wholesale, which is how the dashboard
// clears a field: PUT the whole form with that one emptied.
func TestUpsertSidecarApp_ReplacesTheWholeBlock(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "quartermaster-generate.yaml")
	write(t, gen, "settings:\n  modelsRoot: \"\"\n")

	no := false
	if err := UpsertSidecarApp(gen, AppSettings{
		Listen:     ":1250",
		AdminAllow: "10.0.0.0/8",
		HfToken:    "hf_secret",
		AdminOpen:  &no,
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := UpsertSidecarApp(gen, AppSettings{Listen: ":1250", HfToken: "hf_secret"}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	stored, err := LoadSidecarApp(gen)
	if err != nil {
		t.Fatalf("LoadSidecarApp: %v", err)
	}
	if stored == nil {
		t.Fatal("no stored block")
	}
	if stored.AdminAllow != "" {
		t.Errorf("adminAllow = %q, want cleared by the replace", stored.AdminAllow)
	}
	if stored.AdminOpen != nil {
		t.Errorf("adminOpen = %v, want cleared by the replace", *stored.AdminOpen)
	}
	if stored.HfToken != "hf_secret" {
		t.Errorf("hfToken = %q, want carried through", stored.HfToken)
	}
}

// The app block is top-level in the sidecar, so a settings reset (which is what
// the VRAM card's "restore defaults" does) must not take the ports with it.
func TestSidecarApp_SurvivesSettingsClear(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "quartermaster-generate.yaml")
	write(t, gen, "settings:\n  modelsRoot: \"\"\n")

	vram := 20.0
	if err := UpsertSidecarSettings(gen, SettingsPatch{TargetVramGB: &vram}); err != nil {
		t.Fatalf("UpsertSidecarSettings: %v", err)
	}
	if err := UpsertSidecarApp(gen, AppSettings{Listen: ":1250"}); err != nil {
		t.Fatalf("UpsertSidecarApp: %v", err)
	}
	if _, err := ClearSidecarSettings(gen); err != nil {
		t.Fatalf("ClearSidecarSettings: %v", err)
	}

	stored, err := LoadSidecarApp(gen)
	if err != nil {
		t.Fatalf("LoadSidecarApp: %v", err)
	}
	if stored == nil || stored.Listen != ":1250" {
		t.Fatalf("app block lost by a settings reset: %+v", stored)
	}
}

// Nothing configured anywhere is the common case (a dev checkout, a fresh
// install) and must not be an error — every caller falls through to its default.
func TestLoadAppSettings_MissingFilesAreNotErrors(t *testing.T) {
	got, err := LoadAppSettings(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("LoadAppSettings: %v", err)
	}
	if (got != AppSettings{}) {
		t.Fatalf("want the zero block, got %+v", got)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
