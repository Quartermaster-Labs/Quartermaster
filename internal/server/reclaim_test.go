package server

import (
	"errors"
	"testing"
)

// offloadWithReclaim: the post-eviction VRAM-reclaim retry around the spawn guard.
// Modeled here with an injectable offloader that "fits" once free VRAM ≥ need and
// fails open when there's no telemetry (ok=false), matching LiveOffloadArgs.
func TestServer_OffloadWithReclaim(t *testing.T) {
	offload := func(free float64, ok bool) ([]string, error) {
		if !ok {
			return []string{"passthrough"}, nil // no reading -> trust baked plan
		}
		if free < 4 {
			return nil, errors.New("insufficient VRAM")
		}
		return []string{"ok"}, nil
	}
	noSleep := func() {}

	// 1. Fits on the first (cached) reading -> no probing at all.
	probes := 0
	sample := func() (float64, bool) { probes++; return 99, true }
	out, err := offloadWithReclaim(20, true, offload, sample, noSleep, 6)
	if err != nil || len(out) != 1 || probes != 0 {
		t.Fatalf("ample path: out=%v err=%v probes=%d (want fit, 0 probes)", out, err, probes)
	}

	// 2. Refused on the stale reading, VRAM reclaimed by the 3rd fresh probe.
	probes = 0
	rising := []float64{1.8, 1.9, 5.0}
	sample = func() (float64, bool) {
		i := probes
		if i >= len(rising) {
			i = len(rising) - 1
		}
		probes++
		return rising[i], true
	}
	out, err = offloadWithReclaim(1.8, true, offload, sample, noSleep, 6)
	if err != nil || len(out) != 1 {
		t.Fatalf("reclaim path: out=%v err=%v (want eventual fit)", out, err)
	}
	if probes != 3 {
		t.Fatalf("reclaim path: probes=%d (want 3 — stops as soon as it fits)", probes)
	}

	// 3. VRAM never comes back -> refused after exhausting all tries.
	probes = 0
	sample = func() (float64, bool) { probes++; return 1.8, true }
	_, err = offloadWithReclaim(1.8, true, offload, sample, noSleep, 6)
	if err == nil {
		t.Fatal("stuck-low path: want refusal after all retries, got nil")
	}
	if probes != 6 {
		t.Fatalf("stuck-low path: probes=%d (want 6 tries)", probes)
	}

	// 4. No GPU telemetry (!ok) -> fail open on the first call, no probing.
	probes = 0
	sample = func() (float64, bool) { probes++; return 0, false }
	out, err = offloadWithReclaim(0, false, offload, sample, noSleep, 6)
	if err != nil || len(out) != 1 || probes != 0 {
		t.Fatalf("no-telemetry path: out=%v err=%v probes=%d (want passthrough, 0 probes)", out, err, probes)
	}
}
