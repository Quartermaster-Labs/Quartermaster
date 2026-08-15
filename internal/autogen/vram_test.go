package autogen

import (
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/perf"
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

func TestAutogen_noteFreeVramGB_keepsIdleHighWaterMark(t *testing.T) {
	// The mark stands in for "the card with none of our models resident". A later,
	// lower reading is a model holding VRAM — sizing the next plan against it
	// plans into the scraps — so only upward moves count.
	ResetIdleFreeVramGB()
	t.Cleanup(ResetIdleFreeVramGB)

	if got := noteFreeVramGB(21.9); got != 21.9 {
		t.Fatalf("first reading = %v, want 21.9", got)
	}
	if got := noteFreeVramGB(2.6); got != 21.9 {
		t.Fatalf("reading taken with a model resident = %v, want the idle 21.9", got)
	}
	// VRAM freed elsewhere legitimately raises the mark.
	if got := noteFreeVramGB(23.4); got != 23.4 {
		t.Fatalf("higher reading = %v, want 23.4", got)
	}
}

func TestAutogen_resolveAutoVram_postconditions(t *testing.T) {
	// Hardware-agnostic: with a GPU present resolveAutoVram caps the target at the
	// live free reading; without one it leaves the static value. Either way the
	// target must stay strictly positive and never exceed the free reading.
	ResetIdleFreeVramGB()
	t.Cleanup(ResetIdleFreeVramGB)
	const static = 7.0
	s := &Settings{TargetVramGB: static, VramOverheadGB: 1.0, AutoVram: true}
	resolveAutoVram(s, nil)
	if s.TargetVramGB <= 0 {
		t.Fatalf("TargetVramGB = %v, want > 0", s.TargetVramGB)
	}
	free, haveGPU := SampleFreeVramGB(autoVramSampleTimeout)
	if !haveGPU {
		if s.TargetVramGB != static {
			t.Fatalf("no GPU reading but target changed to %v (want static %v)", s.TargetVramGB, static)
		}
		return
	}
	// Tolerance, not a hard bound: resolveAutoVram budgets against the idle
	// high-water mark, which legitimately sits at or above a sample taken later.
	if s.TargetVramGB > free+0.5 {
		t.Fatalf("live target %v exceeds free reading %v", s.TargetVramGB, free)
	}
	// A static ceiling tighter than free is a deliberate limit and must survive.
	if free > static && s.TargetVramGB != static {
		t.Fatalf("tighter static ceiling %v was raised to %v (free %v)", static, s.TargetVramGB, free)
	}
	// A static ceiling ABOVE what the card has left gets clamped to the free
	// reading in full — NOT free minus vramOverheadGB. The overhead is already
	// charged inside EstVramGB, so subtracting it here spends it twice and costs
	// a layer of offload on a tight fit.
	// Tolerance, not equality: resolveAutoVram takes its own sample and live VRAM
	// drifts between the two. 0.5 still separates "the free reading" from "free
	// minus the 1.0 overhead".
	hi := &Settings{TargetVramGB: free + 4, VramOverheadGB: 1.0, AutoVram: true}
	resolveAutoVram(hi, nil)
	if hi.TargetVramGB <= free-0.5 {
		t.Fatalf("target above free clamped to %v; want ~%v, not free minus the %v overhead", hi.TargetVramGB, free, hi.VramOverheadGB)
	}
}
