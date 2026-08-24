package setup

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// ensureGenerate reports creation, which is what gates first-run-only seeding:
// a repair/upgrade run must not overwrite budgets the user has since tuned.
func TestSetup_ensureGenerateReportsCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "quartermaster-generate.yaml")
	created, err := ensureGenerate(path)
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	created, err = ensureGenerate(path)
	if err != nil || created {
		t.Fatalf("second call: created=%v err=%v want false", created, err)
	}
}

// The seeded budget has to survive a round trip through the YAML loader as a
// number: setSettingsKey writes the value verbatim, so a quoted or
// exponent-formatted scalar would fail to unmarshal into the float64 field.
func TestSetup_seededBudgetParsesAsFloat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quartermaster-generate.yaml")
	if err := os.WriteFile(path, []byte(minimalGenerate), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setSettingsKey(path, "targetVramGB", formatGB(11.9)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Settings struct {
			TargetVramGB float64 `yaml:"targetVramGB"`
		} `yaml:"settings"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("seeded file does not parse: %v", err)
	}
	if got.Settings.TargetVramGB != 11.9 {
		t.Errorf("targetVramGB = %v want 11.9", got.Settings.TargetVramGB)
	}
}
