package autogen

import (
	"testing"

	"github.com/mostlygeek/llama-swap/internal/perf"
)

func TestAutogen_freeVramGBFromStats(t *testing.T) {
	cases := []struct {
		name   string
		stats  []perf.GpuStat
		wantGB float64
		wantOK bool
	}{
		{"empty", nil, 0, false},
		{"no total", []perf.GpuStat{{MemTotalMB: 0, MemUsedMB: 0}}, 0, false},
		{"single", []perf.GpuStat{{MemTotalMB: 8192, MemUsedMB: 1024}}, 7.0, true},
		{"used exceeds total clamps to 0", []perf.GpuStat{{MemTotalMB: 8192, MemUsedMB: 9000}}, 0, true},
		{
			"picks largest total",
			[]perf.GpuStat{
				{MemTotalMB: 2048, MemUsedMB: 0},    // iGPU, 2GB free
				{MemTotalMB: 8192, MemUsedMB: 2048}, // dGPU, 6GB free
			},
			6.0, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gb, ok := freeVramGBFromStats(tc.stats)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && gb != tc.wantGB {
				t.Fatalf("gb = %v, want %v", gb, tc.wantGB)
			}
		})
	}
}

func TestAutogen_resolveAutoVram_postconditions(t *testing.T) {
	// Hardware-agnostic: with a GPU present resolveAutoVram swaps in the live
	// usable VRAM; without one it leaves the static value. Either way the target
	// must stay strictly positive, and any live override must not exceed the raw
	// free reading (overhead is subtracted, never added).
	const static = 7.0
	s := &Settings{TargetVramGB: static, VramOverheadGB: 1.0, AutoVram: true}
	resolveAutoVram(s, nil)
	if s.TargetVramGB <= 0 {
		t.Fatalf("TargetVramGB = %v, want > 0", s.TargetVramGB)
	}
	if free, ok := SampleFreeVramGB(autoVramSampleTimeout); ok {
		if s.TargetVramGB != static && s.TargetVramGB > free {
			t.Fatalf("live target %v exceeds free reading %v", s.TargetVramGB, free)
		}
	} else if s.TargetVramGB != static {
		t.Fatalf("no GPU reading but target changed to %v (want static %v)", s.TargetVramGB, static)
	}
}
