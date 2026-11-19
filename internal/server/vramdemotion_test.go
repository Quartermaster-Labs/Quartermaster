package server

import "testing"

// The two loads measured on 2026-11-19 (27B, est 19.52 GB): the demoted one and
// the healthy one after a reboot, plus the over-estimate case that must not
// look like a demotion.
func TestDemotion_DemotedMB(t *testing.T) {
	cases := []struct {
		name     string
		est      float64
		ded, sh  int
		want     int
		reported bool
	}{
		{"demoted at load", 19.52, 18063, 2765, 1925, true},
		{"healthy: estimate a little high, under the floor", 19.52, 19808, 922, 180, false},
		{"estimate too high, nothing in shared", 12.0, 1200, 300, 300, false},
		{"loaded MORE than estimated", 10.0, 11000, 900, 0, false},
	}
	for _, c := range cases {
		got := demotedMB(c.est, c.ded, c.sh)
		if got != c.want {
			t.Errorf("%s: demotedMB = %d, want %d", c.name, got, c.want)
		}
		if (got >= demotionFloorMB) != c.reported {
			t.Errorf("%s: reported = %v, want %v", c.name, got >= demotionFloorMB, c.reported)
		}
	}
}

func TestDemotion_PenaltyAccumulatesAndRetires(t *testing.T) {
	d := newDemotionTracker()
	const m = "qwen"

	d.planned(m, 19.5, 20.6)
	if d.measured(m, 0) {
		t.Error("a clean first load changed nothing visible, want changed=false")
	}
	if p := d.penaltyGB(m, 20.6); p != 0 {
		t.Fatalf("clean load left penalty %.2f, want 0", p)
	}

	d.planned(m, 19.5, 20.6)
	if !d.measured(m, 1024) {
		t.Error("a demotion must report a visible change")
	}
	if got := d.liveMB(m); got != 1024 {
		t.Errorf("liveMB = %d, want 1024", got)
	}
	if p := d.penaltyGB(m, 20.6); p != 1 {
		t.Fatalf("penalty = %.2f, want 1.0", p)
	}

	// The held-back reload demoted again: hold back more, not the same.
	d.planned(m, 18.5, 20.6)
	d.measured(m, 512)
	if p := d.penaltyGB(m, 20.6); p != 1.5 {
		t.Fatalf("penalty after a second demotion = %.2f, want 1.5", p)
	}

	if !d.stopped(m) || d.liveMB(m) != 0 {
		t.Error("stop must clear the running figure")
	}
	if p := d.penaltyGB(m, 21.0); p != 1.5 {
		t.Errorf("penalty must survive a stop and a small rise in free; got %.2f", p)
	}
	// Free rose by at least the held-back amount: whatever pinned it is gone.
	if p := d.penaltyGB(m, 22.1); p != 0 {
		t.Errorf("penalty must retire once free rises by its size; got %.2f", p)
	}
	if p := d.penaltyGB(m, 20.6); p != 0 {
		t.Errorf("a retired penalty must stay retired; got %.2f", p)
	}
}

// Every reader used outside WireDynamicOffload must cope with no tracker.
func TestDemotion_NilSafe(t *testing.T) {
	var d *demotionTracker
	d.planned("m", 1, 1)
	if d.penaltyGB("m", 1) != 0 || d.liveMB("m") != 0 || d.stopped("m") {
		t.Error("nil tracker must read as empty")
	}
}
