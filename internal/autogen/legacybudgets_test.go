package autogen

import "testing"

// The pre-1.0.4 example shipped targetVramGB: 7 / maxRamGB: 24 as literals, so
// every install made before that carries them whether or not anyone chose them.
// LoadGenerateFile reads exactly that pair as "unset" and measures the box
// instead; anything else is a number somebody typed and is left alone.
func TestIsShippedLegacyBudgets(t *testing.T) {
	cases := []struct {
		name string
		vram float64
		ram  float64
		want bool
	}{
		{"the shipped pair", 7, 24, true},
		{"unset (a post-fix file)", 0, 0, false},
		{"vram tuned, ram still shipped", 12, 24, false},
		{"ram tuned, vram still shipped", 7, 64, false},
		{"both tuned", 15.9, 110, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isShippedLegacyBudgets(Settings{TargetVramGB: c.vram, MaxRamGB: c.ram})
			if got != c.want {
				t.Errorf("isShippedLegacyBudgets(vram=%v, ram=%v) = %v, want %v", c.vram, c.ram, got, c.want)
			}
		})
	}
}
