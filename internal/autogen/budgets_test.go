package autogen

import "testing"

func TestAutogen_floorGB(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{11.99, 11.9}, {11.9, 11.9}, {0.05, 0.0}, {24, 24},
	} {
		if got := floorGB(tc.in); got != tc.want {
			t.Errorf("floorGB(%v) = %v want %v", tc.in, got, tc.want)
		}
	}
}

// The probe fills only what nobody pinned: an explicit budget (file or sidecar)
// must survive, or a wizard-measured value would be re-derived behind the user's
// back on every boot.
func TestAutogen_seedHardwareBudgets(t *testing.T) {
	orig := hardwareBudgets
	t.Cleanup(func() { hardwareBudgets = orig })
	hardwareBudgets = func() (float64, float64) { return 11.9, 9.3 }

	s := Settings{TargetVramGB: 7, MaxRamGB: 24}
	seedHardwareBudgets(&s, true, true)
	if s.TargetVramGB != 11.9 || s.MaxRamGB != 9.3 {
		t.Errorf("unset budgets = %v/%v want 11.9/9.3", s.TargetVramGB, s.MaxRamGB)
	}

	pinned := Settings{TargetVramGB: 7, MaxRamGB: 24}
	seedHardwareBudgets(&pinned, false, false)
	if pinned.TargetVramGB != 7 || pinned.MaxRamGB != 24 {
		t.Errorf("pinned budgets changed to %v/%v", pinned.TargetVramGB, pinned.MaxRamGB)
	}

	half := Settings{TargetVramGB: 7, MaxRamGB: 24}
	seedHardwareBudgets(&half, false, true)
	if half.TargetVramGB != 7 || half.MaxRamGB != 9.3 {
		t.Errorf("half-pinned = %v/%v want 7/9.3", half.TargetVramGB, half.MaxRamGB)
	}
}

// An unmeasurable box keeps the placeholders rather than getting a zero budget,
// which would emit a catalog where nothing fits.
func TestAutogen_seedHardwareBudgets_noReading(t *testing.T) {
	orig := hardwareBudgets
	t.Cleanup(func() { hardwareBudgets = orig })
	hardwareBudgets = func() (float64, float64) { return 0, 0 }

	s := Settings{TargetVramGB: 7, MaxRamGB: 24}
	seedHardwareBudgets(&s, true, true)
	if s.TargetVramGB != 7 || s.MaxRamGB != 24 {
		t.Errorf("no reading changed budgets to %v/%v", s.TargetVramGB, s.MaxRamGB)
	}
}

// The RAM probe has no hardware prerequisite, so it must answer on any box the
// tests run on, and never claim more than the machine has.
func TestAutogen_RecommendedRamGB(t *testing.T) {
	gb, ok := RecommendedRamGB()
	if !ok {
		t.Skip("no OS memory reading available")
	}
	if gb < recommendedRamFloorGB {
		t.Errorf("RecommendedRamGB = %v, below its own floor %v", gb, recommendedRamFloorGB)
	}
}
