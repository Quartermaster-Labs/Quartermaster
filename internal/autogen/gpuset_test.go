package autogen

import (
	"path/filepath"
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
	}, GpuPolicy{})

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
	}, GpuPolicy{})
	if len(one) != 1 || one[0].FreeGB != 0 {
		t.Fatalf("over-used card = %+v, want free clamped to 0", one)
	}
	if one.Multi() || one.MainIndex() != 0 {
		t.Fatalf("single card reported Multi=%v main=%d", one.Multi(), one.MainIndex())
	}
	if set := gpuSetFromStats(nil, GpuPolicy{}); len(set) != 0 {
		t.Fatalf("empty stats produced %+v", set)
	}
}

// TensorSplit charges the whole plan's fixed cost to the main device and each
// other device its own runtime context, then normalises the remainder. Under
// that ratio every per-device constraint binds at once, which is what makes the
// POOLED budget safe to size against.
func TestAutogen_TensorSplit(t *testing.T) {
	// Non-CUDA on purpose: perDeviceFixedGB is computeHipCtxGB (0.4), the case
	// that used to be charged 0 and hand a Vulkan/ROCm box a free extra device.
	setCudaGPU(t, false)

	set := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 11},
		{Index: 1, TotalGB: 16, FreeGB: 15},
	}
	// 2 GB of fixed cost on the main device (ID 1, most free) and 0.4 on the
	// other: 10.6 and 13 of 23.6.
	split := set.TensorSplit(2)
	if len(split) != 2 || split[0] != 0.45 || split[1] != 0.55 {
		t.Fatalf("TensorSplit = %v, want [0.45 0.55]", split)
	}
	if got := FormatSplit(split); got != "0.45,0.55" {
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
	// One extra device, so one runtime context, at the backend's own constant.
	if got := set.ExtraDeviceOverheadGB(); got != computeHipCtxGB {
		t.Fatalf("non-CUDA extra-device overhead = %.2f, want %.2f", got, computeHipCtxGB)
	}

	setCudaGPU(t, true)
	if got := set.ExtraDeviceOverheadGB(); got != computeCudaCtxGB {
		t.Fatalf("CUDA extra-device overhead = %.2f, want one context for the second card", got)
	}
	if got := (GpuSet{{Index: 0}}).ExtraDeviceOverheadGB(); got != 0 {
		t.Fatalf("single-device overhead = %.2f, want 0", got)
	}
}

// PlanTensorSplit is the generate-time twin: same derivation, but over each
// card's stable capacity, and never nil for a set worth splitting. A config is
// a long-lived artifact planned off one cold sample, and spawn time can retune
// a baked ratio but cannot add one to an argv that has none, so a card busy for
// that single sample must not bake a plan without a split in it. See issue #4.
func TestAutogen_PlanTensorSplit(t *testing.T) {
	// Non-CUDA, matching TestAutogen_TensorSplit: perDeviceFixedGB is 0.4.
	setCudaGPU(t, false)

	// Both cards idle: the plan and the live derivation agree, because the
	// floor never raises a reading that is already above it.
	idle := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 11},
		{Index: 1, TotalGB: 16, FreeGB: 15},
	}
	if got := idle.PlanTensorSplit(2); len(got) != 2 || got[0] != 0.45 || got[1] != 0.55 {
		t.Fatalf("idle PlanTensorSplit = %v, want the same [0.45 0.55] as the live split", got)
	}
	if got := idle.PlanMainIndex(); got != 1 {
		t.Fatalf("idle PlanMainIndex = %d, want 1", got)
	}

	// The case the bake used to get wrong: the big card is mid-load at the one
	// moment the generate pass samples. The live derivation writes it off (0.06
	// of the layers) and hands the fixed costs to the small card; the plan
	// floors it at 85% of its own VRAM, keeps it main, and gives it the larger
	// share. Spawn time can walk that back, and could not have walked a
	// missing split forward.
	busy := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 11},
		{Index: 1, TotalGB: 16, FreeGB: 1},
	}
	if got := busy.MainIndex(); got != 0 {
		t.Fatalf("live MainIndex = %d, want the idle small card", got)
	}
	if got := busy.PlanMainIndex(); got != 1 {
		t.Fatalf("PlanMainIndex = %d, want the momentarily busy big card", got)
	}
	if got := busy.PlanTensorSplit(2); len(got) != 2 || got[0] != 0.48 || got[1] != 0.52 {
		t.Fatalf("busy PlanTensorSplit = %v, want [0.48 0.52]", got)
	}

	// No arrangement fits: the live split gives up and returns nil, the plan
	// falls back to the physical ratio so the flags still exist for the retune
	// to rewrite.
	tiny := GpuSet{
		{Index: 0, TotalGB: 0.3, FreeGB: 0},
		{Index: 1, TotalGB: 0.3, FreeGB: 0},
	}
	if got := tiny.TensorSplit(5); got != nil {
		t.Fatalf("live TensorSplit with no room = %v, want nil", got)
	}
	if got := tiny.PlanTensorSplit(5); len(got) != 2 || got[0] != 0.5 || got[1] != 0.5 {
		t.Fatalf("PlanTensorSplit fallback = %v, want the physical ratio [0.5 0.5]", got)
	}

	// A single card is still no split at all.
	if got := (GpuSet{{Index: 0, TotalGB: 12, FreeGB: 12}}).PlanTensorSplit(1); got != nil {
		t.Fatalf("single-device PlanTensorSplit = %v, want nil", got)
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
	multi := EligibleGpuStats(hist, true, GpuPolicy{})
	if len(multi) != 2 || multi[0].ID != 0 || multi[1].ID != 1 || multi[1].MemUsedMB != 4096 {
		t.Fatalf("EligibleGpuStats(multi) = %+v, want both cards' newest samples", multi)
	}
	single := EligibleGpuStats(hist, false, GpuPolicy{})
	if len(single) != 1 || single[0].ID != 1 {
		t.Fatalf("EligibleGpuStats(single) = %+v, want only the main device", single)
	}
	if got := EligibleGpuStats(nil, true, GpuPolicy{}); got != nil {
		t.Fatalf("empty stats produced %+v", got)
	}
	// LiveGpuSet is the un-smoothed set the spawn guard retunes the split from.
	if live := LiveGpuSet(hist, true, GpuPolicy{}); len(live) != 2 || live[1].FreeGB != 12 {
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
	// 1-0.4=0.6 on card 0 against 15-5=10 on the main card: 0.06 and 0.94.
	if got[len(got)-1] != "0.06,0.94" {
		t.Fatalf("--tensor-split = %q, want the live ratio 0.06,0.94", got[len(got)-1])
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

// The extra devices' runtime context must never reach splitBy as part of the
// MAIN device's fixed cost: splitBy already charges perDeviceFixedGB to each
// non-main device, so folding ExtraDeviceOverheadGB in bills the secondary
// card's runtime twice, and both charges land on the main card's side of the
// ratio. Issue #4's box is the shape it hurts: a 16 GB main card beside a 12 GB
// one, where every point of ratio shifted off the main GPU lands on the card
// that ran out of memory.
func TestAutogen_TensorSplit_ExcludesExtraDeviceOverhead(t *testing.T) {
	setCudaGPU(t, false)

	set := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 11}, // RTX 3060
		{Index: 1, TotalGB: 16, FreeGB: 15}, // RTX 4070 Ti SUPER, the main device
	}
	const overhead = 2.0

	// Correct: 15-2 = 13 on main, 11-0.4 = 10.6 on the other, 23.6 total.
	want := set.TensorSplit(overhead)
	if len(want) != 2 || want[0] != 0.45 || want[1] != 0.55 {
		t.Fatalf("TensorSplit(%.1f) = %v, want [0.45 0.55]", overhead, want)
	}

	// What the generate path used to pass. The second card's 0.4 GB is deducted
	// from the main card as well as from itself, so the ratio tips toward the
	// smaller card by exactly the amount that was double-counted.
	doubled := set.TensorSplit(overhead + set.ExtraDeviceOverheadGB())
	if doubled[0] <= want[0] {
		t.Fatalf("double-charged split %v is not biased toward the secondary card vs %v; "+
			"the regression this guards is no longer observable and the test needs new numbers",
			doubled, want)
	}
}

// Once generate pins the device list with --device, --main-gpu is a position in
// THAT list. The spawn-time retune rewrites --main-gpu, so it has to follow the
// same rule or it puts back the telemetry ordinal the naming was meant to
// replace: here main is telemetry index 3, which is position 1 of the pinned
// pair.
func TestAutogen_retuneTensorSplit_PinnedDeviceList(t *testing.T) {
	setCudaGPU(t, false)
	s := Settings{Gpus: GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 1},
		{Index: 3, TotalGB: 16, FreeGB: 15},
	}}
	// A pinned list whose order cannot be re-derived (no probeable backend exe
	// here) is left completely alone. Rewriting --tensor-split in SET order
	// against a list written in main-last order would hand each card the other's
	// ratio, which is worse than the stale ratio it replaced.
	named := []string{"llama-server", "-m", "/m.gguf", "--device", "Vulkan1,Vulkan0",
		"-sm", "layer", "--main-gpu", "1", "--tensor-split", "0.5,0.5"}
	got := retuneTensorSplit(s, named, 5, nil)
	if got[10] != "0.5,0.5" || got[8] != "1" || got[4] != "Vulkan1,Vulkan0" {
		t.Fatalf("retune rewrote an unmappable pinned launch: %v", got[4:])
	}

	// Same set, no --device: the telemetry index is still the best stand-in for
	// the backend's own ordinal, and the split is already in set order.
	unnamed := []string{"llama-server", "-m", "/m.gguf", "-sm", "layer", "--main-gpu", "0", "--tensor-split", "0.5,0.5"}
	reg := retuneTensorSplit(s, unnamed, 5, nil)
	if reg[6] != "3" {
		t.Fatalf("unnamed --main-gpu = %q, want the telemetry index 3", reg[6])
	}
	if reg[8] == "0.5,0.5" {
		t.Fatalf("unnamed --tensor-split was not retuned: %q", reg[8])
	}
}

// A backend device id carries both halves of the pin: which enumeration to
// filter, and which ordinal in it. Getting the pairing wrong does not fail
// loudly, it silently runs the model on a card the sizer did not budget.
func TestAutogen_visibleDevicesEnvFor(t *testing.T) {
	want := map[string][2]string{
		"CUDA0":   {"CUDA_VISIBLE_DEVICES", "0"},
		"Vulkan1": {"GGML_VK_VISIBLE_DEVICES", "1"},
		"ROCm2":   {"HIP_VISIBLE_DEVICES", "2"},
	}
	for id, w := range want {
		name, ord, ok := visibleDevicesEnvFor(id)
		if !ok || name != w[0] || ord != w[1] {
			t.Errorf("visibleDevicesEnvFor(%q) = %q,%q,%v; want %q,%q,true", id, name, ord, ok, w[0], w[1])
		}
	}
	// An unrecognised backend, and an id with no ordinal at all, get nothing
	// rather than a guess.
	for _, id := range []string{"SYCL0", "Metal0", "Vulkan", "0"} {
		if _, _, ok := visibleDevicesEnvFor(id); ok {
			t.Errorf("visibleDevicesEnvFor(%q) claimed a pin", id)
		}
	}
}

// With no probe to go on, CUDA still pins from the telemetry index (PCI bus
// order makes the two agree) and everything else emits nothing, which is what
// shipped.
func TestAutogen_singleDeviceEnvFor_NoProbe(t *testing.T) {
	set := GpuSet{
		{Index: 0, TotalGB: 12, FreeGB: 11},
		{Index: 1, TotalGB: 16, FreeGB: 15},
	}
	missing := filepath.Join(t.TempDir(), "not-a-backend")

	setCudaGPU(t, true)
	pin, isCuda := singleDeviceEnvFor(missing, set)
	if pin != "CUDA_VISIBLE_DEVICES=1" || !isCuda {
		t.Fatalf("CUDA fallback = %q,%v; want CUDA_VISIBLE_DEVICES=1,true", pin, isCuda)
	}

	setCudaGPU(t, false)
	if pin, _ := singleDeviceEnvFor(missing, set); pin != "" {
		t.Fatalf("non-CUDA with no probe = %q, want no pin", pin)
	}
}

// An APU's "VRAM" is the BIOS carve-out; the pool the GPU actually allocates
// from is the shared one (AMD's GTT). Before issue #37 the carve-out was the
// whole reading, it sat under the 3GB floor, and the only GPU in the machine was
// dropped from every budget in the program.
func TestAutogen_IntegratedGpuCountsSharedPool(t *testing.T) {
	now := time.Now()
	set := gpuSetFromStats([]perf.GpuStat{{
		ID: 0, Name: "AMD Radeon 780M card0 (gfx1103)",
		MemTotalMB: 2048, MemUsedMB: 256,
		SharedTotalMB: 16384, SharedUsedMB: 2048,
		Timestamp: now,
	}}, GpuPolicy{})

	if len(set) != 1 {
		t.Fatalf("got %+v, want the APU kept: a 2GB carve-out is under the floor, 2+16GB is not", set)
	}
	d := set[0]
	if !d.Integrated {
		t.Error("Integrated = false, want true")
	}
	if d.TotalGB != 18 || d.FreeGB != 15.75 {
		t.Errorf("total/free = %.2f/%.2f, want 18/15.75 (carve-out + shared)", d.TotalGB, d.FreeGB)
	}
	if d.SharedFreeGB != 14 {
		t.Errorf("SharedFreeGB = %.2f, want 14", d.SharedFreeGB)
	}
}

// The same test from the other side, and the one that matters most: a discrete
// card reports a host aperture (GTT) too, and counting it would budget layers
// into PCIe memory. Its own 24GB is far above the shape test's bound, so only
// the dedicated pool counts.
func TestAutogen_DiscreteGpuIgnoresSharedPool(t *testing.T) {
	now := time.Now()
	set := gpuSetFromStats([]perf.GpuStat{{
		ID: 0, Name: "AMD Radeon RX 7900 XTX",
		MemTotalMB: 24576, MemUsedMB: 1024,
		SharedTotalMB: 16384, SharedUsedMB: 512,
		Timestamp: now,
	}}, GpuPolicy{})

	if len(set) != 1 {
		t.Fatalf("got %+v, want one card", set)
	}
	d := set[0]
	if d.Integrated {
		t.Error("Integrated = true for a discrete card")
	}
	if d.TotalGB != 24 || d.SharedFreeGB != 0 {
		t.Errorf("total/sharedFree = %.2f/%.2f, want 24/0 (aperture must not count)", d.TotalGB, d.SharedFreeGB)
	}
	if d.SharedTotalGB != 16 {
		t.Errorf("SharedTotalGB = %.2f, want 16 reported (but not budgeted)", d.SharedTotalGB)
	}
}

// A small discrete card in a big-RAM box reports the shape an APU does, minus
// the ratio: an 8GB card with a 16GB aperture is only 2x. It must stay a
// dedicated device, or the budget invents host memory for it.
func TestAutogen_SmallDiscreteCardIsNotCalledIntegrated(t *testing.T) {
	set := gpuSetFromStats([]perf.GpuStat{{
		ID: 0, Name: "AMD Radeon RX 6600",
		MemTotalMB: 8192, MemUsedMB: 0,
		SharedTotalMB: 16384, SharedUsedMB: 0,
	}}, GpuPolicy{})

	if len(set) != 1 || set[0].Integrated || set[0].TotalGB != 8 {
		t.Fatalf("got %+v, want one 8GB dedicated device", set)
	}
}

// Name markers catch an APU whose carve-out is too large for the shape test
// (an 8GB UMA setting) without ever matching a card's name.
func TestAutogen_ApuNameMarker(t *testing.T) {
	set := gpuSetFromStats([]perf.GpuStat{{
		ID: 0, Name: "Ryzen 7 7840U w/ Radeon 780M card0 (gfx1103)",
		MemTotalMB: 8192, MemUsedMB: 0,
		SharedTotalMB: 16384, SharedUsedMB: 0,
	}}, GpuPolicy{})

	if len(set) != 1 || !set[0].Integrated || set[0].TotalGB != 24 {
		t.Fatalf("got %+v, want the APU counted at 8+16GB", set)
	}
}

// The two explicit modes exist because auto has to guess. off is the escape
// hatch that restores the pre-#37 reading exactly; on budgets the pool for a
// device auto refused, without also calling it integrated (topology is what the
// pooling rule reads).
func TestAutogen_SharedMemoryModes(t *testing.T) {
	apu := perf.GpuStat{
		ID: 0, Name: "AMD Radeon 780M card0 (gfx1103)",
		MemTotalMB: 2048, MemUsedMB: 256,
		SharedTotalMB: 16384, SharedUsedMB: 2048,
	}
	gpu := perf.GpuStat{
		ID: 0, Name: "AMD Radeon RX 7900 XTX",
		MemTotalMB: 24576, MemUsedMB: 1024,
		SharedTotalMB: 16384, SharedUsedMB: 512,
	}

	if set := gpuSetFromStats([]perf.GpuStat{apu}, GpuPolicy{SharedMemory: SharedMemoryOff}); len(set) != 0 {
		t.Fatalf("off = %+v, want the APU back under the floor (pre-#37 behaviour)", set)
	}
	if set := gpuSetFromStats([]perf.GpuStat{apu}, GpuPolicy{SharedMemory: SharedMemoryOn}); len(set) != 1 || set[0].TotalGB != 18 {
		t.Fatalf("on = %+v, want the APU counted", set)
	}
	set := gpuSetFromStats([]perf.GpuStat{gpu}, GpuPolicy{SharedMemory: SharedMemoryOn})
	if len(set) != 1 || set[0].TotalGB != 40 || set[0].Integrated {
		t.Fatalf("on (discrete) = %+v, want 24+16GB budgeted but still a dedicated device", set)
	}
}

// An APU beside a card is not a split target by default: real layers on an iGPU
// are slower than not splitting at all. It is a PAIRING rule, so the APU still
// gets the whole budget when it is the only GPU (see the tests above).
func TestAutogen_IntegratedPairingNeedsTheToggle(t *testing.T) {
	now := time.Now()
	hist := []perf.GpuStat{
		{
			ID: 0, Name: "AMD Radeon 780M card0 (gfx1103)",
			MemTotalMB: 2048, MemUsedMB: 256, SharedTotalMB: 16384, SharedUsedMB: 2048, Timestamp: now,
		},
		{ID: 1, Name: "NVIDIA GeForce RTX 4070 Ti SUPER", MemTotalMB: 16384, MemUsedMB: 4096, Timestamp: now},
	}

	got := gpuSetFromStats(hist, GpuPolicy{})
	if len(got) != 1 || got[0].Index != 1 {
		t.Fatalf("set = %+v, want only the card: an iGPU is not a split target by default", got)
	}

	got = gpuSetFromStats(hist, GpuPolicy{PoolIntegrated: true})
	if len(got) != 2 || got[0].Index != 0 || !got[0].Integrated {
		t.Fatalf("set = %+v, want both devices with the APU first", got)
	}
	// The pooled budget is the sum of both, and the APU's share of it is the
	// shared pool, which is what the sizer's own log line reports.
	if free := got.FreeGB(); free != 15.75+12 {
		t.Fatalf("pooled free = %.2f, want 27.75", free)
	}
}

// DescribeGpuStats is what cmd/monitor-test prints, and the reason it exists is
// that the reporter of a device-specific bug has to be able to say which side of
// the policy their machine landed on.
func TestAutogen_DescribeGpuStats(t *testing.T) {
	stats := []perf.GpuStat{
		{
			ID: 0, Name: "AMD Radeon 780M card0 (gfx1103)",
			MemTotalMB: 2048, MemUsedMB: 256, SharedTotalMB: 16384, SharedUsedMB: 2048,
		},
		{ID: 1, Name: "AMD Radeon RX 6600", MemTotalMB: 8192, MemUsedMB: 0, SharedTotalMB: 16384},
		{ID: 2, Name: "tiny", MemTotalMB: 1024, MemUsedMB: 0},
	}

	lines := DescribeGpuStats(stats, true, GpuPolicy{})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"integrated",     // the APU's topology is named
		"counted",        // ...and that its pool is part of the budget
		"not counted",    // ...while the card's aperture is not
		"18.0 GB usable", // the fold is shown, so 2GB -> 18GB is visible
		"8.0 GB usable",  // the card keeps its own 8GB
		"DROPPED (under the 3.0 GB floor)",
		"EXCLUDED",     // the APU is unpairable beside a card by default
		"8.0 GB total", // so the summary holds the card alone
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("output %q missing %q", joined, want)
		}
	}

	// The escape hatch has to be visible in the diagnostic too, or a user
	// comparing a pre-#37 report against a post-#37 one sees nothing change.
	off := strings.Join(DescribeGpuStats(stats[:1], true, GpuPolicy{SharedMemory: SharedMemoryOff}), "\n")
	if !strings.Contains(off, "not counted") || !strings.Contains(off, "no GPU counts") {
		t.Fatalf("sharedMemory=off output = %q, want the pool uncounted and no budget", off)
	}
}
