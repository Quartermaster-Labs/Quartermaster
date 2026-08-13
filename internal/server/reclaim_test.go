package server

import (
	"errors"
	"testing"
)

// offloadWithReclaim: the post-eviction VRAM-reclaim retry around the spawn guard.
// Modeled here with an injectable offloader that mirrors LiveOffloadArgs: it fails
// open with no telemetry, refuses below the fully-offloaded floor, returns the
// baked args unchanged when there's ample VRAM, and otherwise rewrites them to
// signal "offloaded more than baked".
func TestServer_OffloadWithReclaim(t *testing.T) {
	baked := []string{"-ngl", "99", "--n-cpu-moe", "3"}
	offloaded := []string{"-ngl", "99", "--n-cpu-moe", "41"}
	offload := func(free float64, ok bool) ([]string, error) {
		switch {
		case !ok:
			return baked, nil // no reading -> trust baked plan
		case free < 4:
			return nil, errors.New("insufficient VRAM")
		case free < 20:
			return offloaded, nil // tight -> offload more than baked
		default:
			return baked, nil // ample -> leave the baked plan alone
		}
	}
	noSleep := func() {}

	// 1. Ample on the first (cached) reading, guard leaves baked args -> no probing.
	probes := 0
	sample := func() (float64, bool) { probes++; return 99, true }
	out, err := offloadWithReclaim(baked, 20, true, offload, sample, noSleep, 6)
	if err != nil || !sameArgs(out, baked) || probes != 0 {
		t.Fatalf("ample path: out=%v err=%v probes=%d (want baked, 0 probes)", out, err, probes)
	}

	// 2. Stale-low sample over-offloads (fits, but heavier than baked). A fresh
	//    post-eviction probe shows ample -> reconfirm back to the baked plan.
	probes = 0
	sample = func() (float64, bool) { probes++; return 99, true }
	out, err = offloadWithReclaim(baked, 5, true, offload, sample, noSleep, 6)
	if err != nil || !sameArgs(out, baked) {
		t.Fatalf("over-offload path: out=%v err=%v (want reconfirm to baked)", out, err)
	}
	if probes != 1 {
		t.Fatalf("over-offload path: probes=%d (want 1 — one fresh probe confirms fit)", probes)
	}

	// 3. Genuinely tight even after reclaim -> keep the heavier offload. Takes two
	//    probes: the first still offloads more than baked, so it can't be trusted
	//    until a second probe shows free VRAM has stopped climbing (reclaim done).
	probes = 0
	sample = func() (float64, bool) { probes++; return 10, true }
	out, err = offloadWithReclaim(baked, 5, true, offload, sample, noSleep, 6)
	if err != nil || !sameArgs(out, offloaded) || probes != 2 {
		t.Fatalf("tight path: out=%v err=%v probes=%d (want offloaded, 2 probes)", out, err, probes)
	}

	// 3b. The layer-shaving regression: a mid-reclaim probe FITS but offloads more
	//     than baked, and free VRAM is still climbing. Accepting it would load the
	//     model a layer short; keep probing until the baked plan fits.
	probes = 0
	climbing := []float64{6.0, 12.0, 30.0}
	sample = func() (float64, bool) {
		i := min(probes, len(climbing)-1)
		probes++
		return climbing[i], true
	}
	out, err = offloadWithReclaim(baked, 5, true, offload, sample, noSleep, 6)
	if err != nil || !sameArgs(out, baked) || probes != 3 {
		t.Fatalf("climbing path: out=%v err=%v probes=%d (want baked, 3 probes)", out, err, probes)
	}

	// 4. Refused on the stale reading, VRAM reclaimed by the 3rd fresh probe.
	probes = 0
	rising := []float64{1.8, 1.9, 25.0}
	sample = func() (float64, bool) {
		i := probes
		if i >= len(rising) {
			i = len(rising) - 1
		}
		probes++
		return rising[i], true
	}
	out, err = offloadWithReclaim(baked, 1.8, true, offload, sample, noSleep, 6)
	if err != nil || !sameArgs(out, baked) {
		t.Fatalf("reclaim path: out=%v err=%v (want eventual fit)", out, err)
	}
	if probes != 3 {
		t.Fatalf("reclaim path: probes=%d (want 3 — stops as soon as it fits)", probes)
	}

	// 5. VRAM never comes back -> refused after exhausting all tries.
	probes = 0
	sample = func() (float64, bool) { probes++; return 1.8, true }
	_, err = offloadWithReclaim(baked, 1.8, true, offload, sample, noSleep, 6)
	if err == nil {
		t.Fatal("stuck-low path: want refusal after all retries, got nil")
	}
	if probes != 6 {
		t.Fatalf("stuck-low path: probes=%d (want 6 tries)", probes)
	}

	// 6. No GPU telemetry (!ok) -> fail open on the first call, no probing.
	probes = 0
	sample = func() (float64, bool) { probes++; return 0, false }
	out, err = offloadWithReclaim(baked, 0, false, offload, sample, noSleep, 6)
	if err != nil || !sameArgs(out, baked) || probes != 0 {
		t.Fatalf("no-telemetry path: out=%v err=%v probes=%d (want passthrough, 0 probes)", out, err, probes)
	}
}
