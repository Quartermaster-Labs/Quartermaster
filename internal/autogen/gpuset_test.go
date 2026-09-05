package autogen

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/perf"
)

// gpuSetFromStats is the eligibility rule the whole multi-GPU path is built on:
// newest sample per device id, anything under the inference floor dropped,
// Index order preserved.
func TestAutogen_gpuSetFromStats(t *testing.T) {
	now := time.Now()
	set := gpuSetFromStats([]perf.GpuStat{
		{ID: 1, Name: "4070 Ti SUPER", MemTotalMB: 16384, MemUsedMB: 8192, Timestamp: now.Add(-time.Minute)},
		{ID: 1, Name: "4070 Ti SUPER", MemTotalMB: 16384, MemUsedMB: 4096, Timestamp: now},
		{ID: 0, Name: "3060", MemTotalMB: 12288, MemUsedMB: 1024, Timestamp: now},
		{ID: 2, Name: "iGPU", MemTotalMB: 2048, MemUsedMB: 0, Timestamp: now},
		{ID: 3, Name: "no telemetry", MemTotalMB: 0, MemUsedMB: 0, Timestamp: now},
	}, minInferenceVramGB)

	if len(set) != 2 {
		t.Fatalf("got %d devices %+v, want the two real cards", len(set), set)
	}
	if set[0].Index != 0 || set[1].Index != 1 {
		t.Fatalf("devices out of Index order: %+v", set)
	}
	// The newest ID 1 sample wins: 16 GB total, 4 GB used -> 12 GB free.
	if got := set[1].FreeGB; got != 12 {
		t.Fatalf("ID 1 free = %.2f, want 12 (from the newest sample)", got)
	}
	if got := set.TotalGB(); got != 28 {
		t.Fatalf("pooled total = %.2f, want 28", got)
	}
	if got := set.FreeGB(); got != 23 {
		t.Fatalf("pooled free = %.2f, want 23", got)
	}
	if !set.Multi() {
		t.Fatal("two cards did not report Multi")
	}
	// MainIndex follows FREE memory, not total: the split's fixed costs land on
	// whichever card can actually absorb them.
	if got := set.MainIndex(); got != 1 {
		t.Fatalf("MainIndex = %d, want 1 (12 GB free vs 11)", got)
	}
	// A used reading above total (a driver hiccup) clamps to 0 free rather than
	// going negative and poisoning the pooled sum.
	one := gpuSetFromStats([]perf.GpuStat{
		{ID: 0, MemTotalMB: 8192, MemUsedMB: 9000, Timestamp: now},
	}, minInferenceVramGB)
	if len(one) != 1 || one[0].FreeGB != 0 {
		t.Fatalf("over-used card = %+v, want free clamped to 0", one)
	}
	if one.Multi() || one.MainIndex() != 0 {
		t.Fatalf("single card reported Multi=%v main=%d", one.Multi(), one.MainIndex())
	}
	if set := gpuSetFromStats(nil, minInferenceVramGB); len(set) != 0 {
		t.Fatalf("empty stats produced %+v", set)
	}
}

// TensorSplit charges the whole plan's fixed cost to the main device and each
// other device its own runtime context, then normalises the remainder. Under
// that ratio every per-device constraint binds at once, which is what makes the
// POOLED budget safe to size against.
func TestAutogen_TensorSplit(t *testing.T) {
	setCudaGPU(t, false) // perDeviceFixedGB == 0, so the arithmetic is exact

	set := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 11},
		{Index: 1, TotalGB: 16, FreeGB: 15},
	}
	// 2 GB of fixed cost on the main device (ID 1, most free): 11 and 13 of 24.
	split := set.TensorSplit(2)
	if len(split) != 2 || split[0] != 0.46 || split[1] != 0.54 {
		t.Fatalf("TensorSplit = %v, want [0.46 0.54]", split)
	}
	if got := FormatSplit(split); got != "0.46,0.54" {
		t.Fatalf("FormatSplit = %q", got)
	}
	// A device with no room left after its fixed cost gets nothing, and the
	// whole model goes to the card that can hold it.
	tight := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 0.5},
		{Index: 1, TotalGB: 16, FreeGB: 10},
	}
	if split := tight.TensorSplit(10); len(split) != 2 || split[0] != 1 || split[1] != 0 {
		t.Fatalf("TensorSplit = %v, want everything on the card with room", split)
	}
	// Nothing to split: no flags, and the single-GPU path stands.
	full := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 0},
		{Index: 1, TotalGB: 16, FreeGB: 2},
	}
	if split := full.TensorSplit(4); split != nil {
		t.Fatalf("TensorSplit with no room anywhere = %v, want nil", split)
	}
	if split := (GpuSet{{Index: 0, FreeGB: 12}}).TensorSplit(1); split != nil {
		t.Fatalf("single-device TensorSplit = %v, want nil", split)
	}
	if got := set.ExtraDeviceOverheadGB(); got != 0 {
		t.Fatalf("non-CUDA extra-device overhead = %.2f, want 0", got)
	}

	setCudaGPU(t, true)
	if got := set.ExtraDeviceOverheadGB(); got != computeCudaCtxGB {
		t.Fatalf("CUDA extra-device overhead = %.2f, want one context for the second card", got)
	}
	if got := (GpuSet{{Index: 0}}).ExtraDeviceOverheadGB(); got != 0 {
		t.Fatalf("single-device overhead = %.2f, want 0", got)
	}
}

// EligibleGpuStats is what the server's guard and VRAM tracker call. It has to
// apply the same floor as the sizer, and collapse to the sizer's main device
// when multiGpu is off.
func TestAutogen_EligibleGpuStats(t *testing.T) {
	now := time.Now()
	hist := []perf.GpuStat{
		{ID: 0, MemTotalMB: 12288, MemUsedMB: 1024, Timestamp: now},
		{ID: 1, MemTotalMB: 16384, MemUsedMB: 8192, Timestamp: now.Add(-time.Minute)},
		{ID: 1, MemTotalMB: 16384, MemUsedMB: 4096, Timestamp: now},
		{ID: 2, MemTotalMB: 2048, MemUsedMB: 0, Timestamp: now},
	}
	multi := EligibleGpuStats(hist, true)
	if len(multi) != 2 || multi[0].ID != 0 || multi[1].ID != 1 || multi[1].MemUsedMB != 4096 {
		t.Fatalf("EligibleGpuStats(multi) = %+v, want both cards' newest samples", multi)
	}
	single := EligibleGpuStats(hist, false)
	if len(single) != 1 || single[0].ID != 1 {
		t.Fatalf("EligibleGpuStats(single) = %+v, want only the main device", single)
	}
	if got := EligibleGpuStats(nil, true); got != nil {
		t.Fatalf("empty stats produced %+v", got)
	}
	// LiveGpuSet is the un-smoothed set the spawn guard retunes the split from.
	if live := LiveGpuSet(hist, true); len(live) != 2 || live[1].FreeGB != 12 {
		t.Fatalf("LiveGpuSet = %+v, want raw per-device free", live)
	}
}

// MultiGpuEnabled defaults ON, and turning it off has to hide the resolved set
// from every caller rather than needing each one to re-check the flag.
func TestAutogen_MultiGpuEnabled(t *testing.T) {
	set := GpuSet{{Index: 0, FreeGB: 11}, {Index: 1, FreeGB: 15}}
	s := Settings{Gpus: set}
	if !s.MultiGpuEnabled() || len(s.GpuSetOrEmpty()) != 2 {
		t.Fatal("nil multiGpu did not default to on")
	}
	off := false
	s.MultiGpu = &off
	if s.MultiGpuEnabled() || s.GpuSetOrEmpty() != nil {
		t.Fatal("multiGpu:false still exposed the device set")
	}
}

// setCudaGPU forces the CUDA flag for one test and restores it after, so the
// fixed-cost arithmetic doesn't depend on the machine running the suite.
func setCudaGPU(t *testing.T, on bool) {
	t.Helper()
	prev := cudaGPU.Load()
	cudaGPU.Store(on)
	t.Cleanup(func() { cudaGPU.Store(prev) })
}

// The split flags are emitted ONLY for a real multi-device plan, and never on a
// CPU-only load: -ngl 0 means there are no layers on any GPU to divide, and
// --tensor-split would then be a lie the runtime guard would later act on.
func TestAutogen_emitsTensorSplitOnlyWhenSplitting(t *testing.T) {
	s := Settings{TargetVramGB: 24}
	row := GgufRow{FullPath: "/m.gguf"}
	prof := profile{Name: "solo", TensorSplit: []float64{0.46, 0.54}, MainGpu: 1}

	got := strings.Join(buildCmdLines(s, Metadata{}, row, prof, 8192, 99, 0, "q8_0", "q8_0", false, nil), " ")
	if !strings.Contains(got, "-sm layer --main-gpu 1") || !strings.Contains(got, "--tensor-split 0.46,0.54") {
		t.Fatalf("multi-device plan emitted no split flags:\n%s", got)
	}

	// -ngl 0: everything is on the CPU, so nothing is split.
	got = strings.Join(buildCmdLines(s, Metadata{}, row, prof, 8192, 0, 0, "q8_0", "q8_0", false, nil), " ")
	if strings.Contains(got, "--tensor-split") || strings.Contains(got, "--main-gpu") {
		t.Fatalf("CPU-only load still emitted split flags:\n%s", got)
	}

	// Single GPU: no TensorSplit on the profile, no flags.
	solo := profile{Name: "solo", MainGpu: -1}
	got = strings.Join(buildCmdLines(s, Metadata{}, row, solo, 8192, 99, 0, "q8_0", "q8_0", false, nil), " ")
	if strings.Contains(got, "--tensor-split") || strings.Contains(got, "-sm ") {
		t.Fatalf("single-GPU plan emitted split flags:\n%s", got)
	}
}

// retuneTensorSplit is the spawn-time half: a baked ratio built from idle VRAM
// is re-derived against the cards as they look now, so a model landing while
// another one holds one card does not send layers to a device with no room.
func TestAutogen_retuneTensorSplit(t *testing.T) {
	setCudaGPU(t, false)
	args := []string{"llama-server", "-m", "/m.gguf", "-sm", "layer", "--main-gpu", "0", "--tensor-split", "0.5,0.5"}

	// No device set (no telemetry): the baked ratio is the best guess we have.
	if got := retuneTensorSplit(Settings{}, args, 1, nil); !reflect.DeepEqual(got, args) {
		t.Fatalf("no-telemetry retune changed the argv: %v", got)
	}

	// Card 0 is now nearly full, card 1 is empty: the ratio and the main device
	// both move to card 1.
	s := Settings{Gpus: GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 1},
		{Index: 1, TotalGB: 16, FreeGB: 15},
	}}
	got := retuneTensorSplit(s, args, 5, nil)
	if got[len(got)-1] != "0.09,0.91" {
		t.Fatalf("--tensor-split = %q, want the live ratio 0.09,0.91", got[len(got)-1])
	}
	if got[6] != "1" {
		t.Fatalf("--main-gpu = %q, want 1", got[5])
	}
	if args[len(args)-1] != "0.5,0.5" {
		t.Fatal("retune mutated the caller's argv")
	}

	// An argv with no --tensor-split is left exactly as it is.
	plain := []string{"llama-server", "-m", "/m.gguf"}
	if got := retuneTensorSplit(s, plain, 1, nil); !reflect.DeepEqual(got, plain) {
		t.Fatalf("single-GPU argv changed: %v", got)
	}
}
