package autogen

import "testing"

// SettingsPatch.apply must overlay TtlSec (dashboard idle-eviction knob), and 0
// must be reachable so a user can disable auto-unload — defaults run BEFORE the
// patch, so patch=0 wins over the applyDefaults() 600 fallback.
func TestSettingsPatch_ApplyTtlSec(t *testing.T) {
	i := func(v int) *int { return &v }

	s := Settings{TtlSec: 600}
	(&SettingsPatch{TtlSec: i(120)}).apply(&s)
	if s.TtlSec != 120 {
		t.Fatalf("TtlSec = %d, want 120", s.TtlSec)
	}

	// 0 => never auto-unload; must not be ignored as a zero value.
	s = Settings{TtlSec: 600}
	(&SettingsPatch{TtlSec: i(0)}).apply(&s)
	if s.TtlSec != 0 {
		t.Fatalf("TtlSec = %d, want 0 (never unload)", s.TtlSec)
	}

	// nil => inherit (untouched).
	s = Settings{TtlSec: 600}
	(&SettingsPatch{}).apply(&s)
	if s.TtlSec != 600 {
		t.Fatalf("TtlSec = %d, want 600 (inherited)", s.TtlSec)
	}
}
