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
		t.Skip("no dedicated-VRAM adapter found")
	}
	var mem map[LUID]float64
	if pm, err := initPdhGpuMem(); err == nil {
		defer pm.close()
		pm.collect() // prime; rate/format counters need a second collect
		mem = pm.collect()
	} else {
		t.Logf("PDH GPU mem unavailable: %v", err)
	}
	for i := range adapters {
		a := &adapters[i]
		t.Logf("gpu[%d] name=%q totalMB=%d usedMB=%d mirrors=%d",
			i, a.name, a.totalMB, a.usedMB(mem), len(a.ptrs))
	}
}
