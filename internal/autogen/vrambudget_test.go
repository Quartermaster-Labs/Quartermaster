package autogen

import (
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

func TestEmitVramBudget_DefaultsOn(t *testing.T) {
	var b strings.Builder
	emitVramBudget(&b, Settings{TargetVramGB: 23.5})
	if !strings.Contains(b.String(), "vramBudgetGB: 23.5") {
		t.Fatalf("expected the budget emitted by default, got:\n%s", b.String())
	}
}

func TestEmitVramBudget_OffWithheldsBudget(t *testing.T) {
	off := false
	var b strings.Builder
	emitVramBudget(&b, Settings{TargetVramGB: 23.5, MultiResident: &off})
	if b.Len() != 0 {
		t.Fatalf("expected no budget with multiResident off, got:\n%s", b.String())
	}
}

func TestWriteEstVram_OmitsUnknown(t *testing.T) {
	var b strings.Builder
	writeEstVram(&b, 0)
	if b.Len() != 0 {
		t.Fatalf("expected nothing emitted for an unknown estimate, got %q", b.String())
	}
	writeEstVram(&b, 6.123)
	if got := b.String(); got != "    estVramGB: 6.12\n" {
		t.Fatalf("estVramGB line = %q", got)
	}
}

// The emitted budget + per-model estimate must survive a real config load, since
// that pair is what the router admits against.
func TestVramBudget_RoundTripsThroughConfigLoad(t *testing.T) {
	yamlText := `
vramBudgetGB: 23.5
models:
  "a":
    cmd: fake-server --port ${PORT}
    estVramGB: 6.12
  "b":
    cmd: fake-server --port ${PORT}
`
	cfg, err := config.LoadConfigFromReader(strings.NewReader(yamlText))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VramBudgetGB != 23.5 {
		t.Errorf("VramBudgetGB=%v want 23.5", cfg.VramBudgetGB)
	}
	if cfg.Models["a"].EstVramGB != 6.12 {
		t.Errorf("models.a.EstVramGB=%v want 6.12", cfg.Models["a"].EstVramGB)
	}
	if cfg.Models["b"].EstVramGB != 0 {
		t.Errorf("an omitted estimate must stay 0 (unknown), got %v", cfg.Models["b"].EstVramGB)
	}
}

// The admission floor reads a placement as a GPU-resident fraction. Dense models
// scale by -ngl; MoE by --n-cpu-moe against the expert-weight share, since -ngl
// stays 99 there and the experts are what actually move.
func TestGpuResidentFraction(t *testing.T) {
	dense := Metadata{BlockCount: 40}
	moe := Metadata{BlockCount: 48, IsMoE: true, ExpertWeightShare: 0.9}

	cases := []struct {
		name string
		meta Metadata
		ngl  int
		ncpu int
		want float64
	}{
		{"dense fully offloaded", dense, 99, 0, 1},
		{"dense half offloaded", dense, 20, 0, 0.5},
		{"dense cpu only", dense, 0, 0, 0},
		{"moe no expert offload", moe, 99, 0, 1},
		{"moe all experts on cpu", moe, 99, 48, 0.1},
		{"moe half experts on cpu", moe, 99, 24, 0.55},
		{"unknown blocks has no opinion", Metadata{}, 0, 0, 1},
	}
	for _, tc := range cases {
		if got := gpuResidentFraction(tc.meta, tc.ngl, tc.ncpu); round2(got) != tc.want {
			t.Errorf("%s: gpuResidentFraction=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestMinGpuFraction_Defaults(t *testing.T) {
	s := Settings{}
	s.applyDefaults()
	if s.MinGpuFraction != 0.5 {
		t.Errorf("default floor = %v want 0.5", s.MinGpuFraction)
	}
	off := Settings{MinGpuFraction: -1}
	off.applyDefaults()
	if off.MinGpuFraction != 0 {
		t.Errorf("negative floor should disable it, got %v", off.MinGpuFraction)
	}
}
