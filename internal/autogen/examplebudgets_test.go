package autogen

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// The shipped example is what package-windows seeds a runtime generate file
// from and what the setup wizard copies, so a literal budget in it is not
// documentation: it is the value every install starts with. It also DISABLES
// the measurement, because seedHardwareBudgets only fills a knob that is still
// zero (overrides.go) - a written 7 is indistinguishable from a user who chose
// 7. That shipped pair is how a 16 GB card ended up budgeted at 7 GB, and a
// 16 GB box at a 24 GB RAM ceiling it could never honour. Both knobs must stay
// commented out; pin one only by editing this test too.
func TestExampleGenerate_LeavesBudgetsUnset(t *testing.T) {
	raw, err := os.ReadFile("../../quartermaster-generate.example.yaml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	var gf struct {
		Settings struct {
			TargetVramGB float64 `yaml:"targetVramGB"`
			MaxRamGB     float64 `yaml:"maxRamGB"`
		} `yaml:"settings"`
	}
	if err := yaml.Unmarshal(raw, &gf); err != nil {
		t.Fatalf("parse example: %v", err)
	}
	if gf.Settings.TargetVramGB != 0 {
		t.Errorf("example pins targetVramGB=%v; leave it commented out so the box is measured", gf.Settings.TargetVramGB)
	}
	if gf.Settings.MaxRamGB != 0 {
		t.Errorf("example pins maxRamGB=%v; leave it commented out so the box is measured", gf.Settings.MaxRamGB)
	}
}
