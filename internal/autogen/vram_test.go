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
			// An iGPU reports a slice of system RAM as dedicated VRAM. Pooling
			// it would invent budget no card has, so it is dropped below the
			// inference floor and only the dGPU counts.
			"drops the iGPU below the floor",
			[]perf.GpuStat{
				{ID: 0, MemTotalMB: 2048, MemUsedMB: 0},    // iGPU, 2GB free
				{ID: 1, MemTotalMB: 8192, MemUsedMB: 2048}, // dGPU, 6GB free
			},
			6.0, true,
		},
		{
			// Two real cards POOL. This is the rule that changed for issue #4:
			// the old sizer reported the largest adapter alone (6.0 here) and
			// left the second card's VRAM unused.
			"pools two eligible cards",
			[]perf.GpuStat{
				{ID: 0, MemTotalMB: 12288, MemUsedMB: 1024},
				{ID: 1, MemTotalMB: 16384, MemUsedMB: 4096},
			},
			23.0, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gb, ok := freeVramGBFromStats(tc.stats, GpuPolicy{})
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
	free, haveGPU := SampleFreeVramGB(autoVramSampleTimeout, GpuPolicy{})
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

// TestClassifyBackendRuntime pins the path classifier that decides between the
// 0.4 GB Vulkan and 0.8 GB ROCm per-process constants. Getting this wrong is
// silent: it just mis-sizes every model on the box by 0.4 GB, in whichever
// direction, so the "says nothing" case must stay UNKNOWN rather than defaulting.
func TestClassifyBackendRuntime(t *testing.T) {
	cases := []struct {
		name       string
		exe        string
		rocm, know bool
	}{
		{"installer rocm layout", `E:\bin\custom-lemonade-sdk-llamacpp-rocm\b1328-llama-windows-rocm-gfx110x\llama-server.exe`, true, true},
		{"hand-built hip", "/opt/llama.cpp/build-hip/bin/llama-server", true, true},
		{"hipblas in path", "C:/llama/hipblas-b1300/llama-server.exe", true, true},
		{"vulkan build", `E:\bin\llamacpp\b10405-llama-windows-vulkan\llama-server.exe`, false, true},
		{"cuda build", `E:\bin\llamacpp\b10405-llama-windows-cuda-12.4\llama-server.exe`, false, true},
		{"bare exe says nothing", "llama-server.exe", false, false},
		{"empty", "  ", false, false},
		// "hip" must not match inside an ordinary word, or every model on the box
		// is charged 0.4 GB it never allocates.
		{"hip inside a word", "D:/shipsets/llama/llama-server.exe", false, false},
		{"chipset dir", `D:\chipset-tools\llama-server.exe`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rocm, known := classifyBackendRuntime(tc.exe)
			if rocm != tc.rocm || known != tc.know {
				t.Fatalf("classifyBackendRuntime(%q) = (rocm=%v, known=%v), want (%v, %v)", tc.exe, rocm, known, tc.rocm, tc.know)
			}
		})
	}
}

// TestNoteBackendRuntimeLatch covers the idempotence rule: an unreadable path
// must leave the previous verdict standing (a bare exe is not evidence of
// Vulkan), but a path that DOES name a runtime must be able to clear the flag
// again when the UI repoints the registry at a different build.
func TestNoteBackendRuntimeLatch(t *testing.T) {
	t.Cleanup(func() { rocmBackend.Store(false) })

	NoteBackendRuntime(`E:\bin\llamacpp-rocm\b1328-llama-windows-rocm-gfx110x\llama-server.exe`)
	if !usingRocmBackend() {
		t.Fatal("a rocm path should set the rocm backend flag")
	}
	NoteBackendRuntime("llama-server.exe")
	if !usingRocmBackend() {
		t.Fatal("an unrecognised path must leave the previous verdict alone, not clear it")
	}
	NoteBackendRuntime(`E:\bin\llamacpp\b10405-llama-windows-vulkan\llama-server.exe`)
	if usingRocmBackend() {
		t.Fatal("repointing at a vulkan build should clear the rocm flag")
	}
}
