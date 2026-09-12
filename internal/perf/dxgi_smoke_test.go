//go:build windows

package perf

import "testing"

func TestDxgiSmoke(t *testing.T) {
	adapters, err := openDxgiAdapters()
	if err != nil {
		t.Fatalf("openDxgiAdapters: %v", err)
	}
	defer func() {
		for i := range adapters {
			adapters[i].release()
		}
	}()
	if len(adapters) == 0 {
		t.Skip("no adapter with dedicated VRAM found")
	}
	var mem, shared map[LUID]float64
	if pm, err := initPdhGpuMem(); err == nil {
		defer pm.close()
		pm.collect() // prime; rate/format counters need a second collect
		mem = pm.collect()
	} else {
		t.Logf("PDH GPU mem unavailable: %v", err)
	}
	if ps, err := initPdhGpuSharedMem(); err == nil {
		defer ps.close()
		ps.collect()
		shared = ps.collect()
	} else {
		t.Logf("PDH GPU shared mem unavailable: %v", err)
	}
	for i := range adapters {
		a := &adapters[i]
		t.Logf("gpu[%d] name=%q totalMB=%d usedMB=%d sharedMB=%d sharedUsedMB=%d mirrors=%d",
			i, a.name, a.totalMB, a.usedMB(mem), a.sharedMB, a.sharedUsedMB(shared, true), len(a.ptrs))
	}
}

// The shared pool is the one reading whose failure mode is asymmetric: reported
// as FREE when its usage is unknown, the fold would budget an aperture the GPU
// may not be able to fill, and the sizer would plan layers into memory that is
// not there. Reporting it FULL costs those layers their place on the GPU and
// never over-commits. So "no data" must resolve to fully-used, and only a
// counter that answers for this adapter may report a smaller number.
func TestDxgiSharedUsedMB_FailsClosed(t *testing.T) {
	l0 := LUID{LowPart: 0x1C5CA, HighPart: 0}
	l1 := LUID{LowPart: 0x1EF99, HighPart: 0}
	other := LUID{LowPart: 0x99, HighPart: 0}
	a := dxgiAdapter{name: "AMD Radeon(TM) Graphics", totalMB: 485, sharedMB: 15930, luids: []LUID{l0, l1}}

	const mib = 1024 * 1024
	if got := a.sharedUsedMB(nil, false); got != 15930 {
		t.Errorf("no counter: got %d MB, want the aperture reported full (%d)", got, a.sharedMB)
	}
	if got := a.sharedUsedMB(map[LUID]float64{}, true); got != 15930 {
		t.Errorf("counter with no data: got %d MB, want the aperture reported full", got)
	}
	if got := a.sharedUsedMB(map[LUID]float64{other: 512 * mib}, true); got != 0 {
		t.Errorf("counter without this adapter's LUID: got %d MB, want 0 (idle aperture)", got)
	}
	if got := a.sharedUsedMB(map[LUID]float64{l0: 256 * mib}, true); got != 256 {
		t.Errorf("got %d MB, want the counter's 256", got)
	}
	// Mirrors: usage can land on any one of them, so the busiest wins.
	if got := a.sharedUsedMB(map[LUID]float64{l0: 128 * mib, l1: 4096 * mib}, true); got != 4096 {
		t.Errorf("got %d MB, want the max across mirror LUIDs (4096)", got)
	}
}
