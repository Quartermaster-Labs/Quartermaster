package autogen

import (
	"path/filepath"
	"strings"
	"testing"
)

// Real output from llama-server b10837-vulkan on a box with a discrete card and
// an APU's integrated adapter. Two things matter here and both are load-bearing:
// the integrated adapter is listed (and advertises 15.8GB, well over
// minInferenceVramGB, so an eligibility floor alone will not exclude it), and
// the discrete card sorts FIRST, which telemetry need not agree with.
const llamaListDevicesOut = `ggml_vulkan: Found 2 Vulkan devices:
Available devices:
  Vulkan0: AMD Radeon RX 7900 XTX (24560 MiB, 23748 MiB free)
  Vulkan1: AMD Radeon(TM) Graphics (16186 MiB, 15377 MiB free)
`

func TestAutogen_ParseBackendDevices_Llama(t *testing.T) {
	devs := parseBackendDevices(llamaListDevicesOut)
	if len(devs) != 2 {
		t.Fatalf("parsed %d devices, want 2: %+v", len(devs), devs)
	}
	if devs[0].ID != "Vulkan0" || devs[1].ID != "Vulkan1" {
		t.Fatalf("ids = %q,%q, want Vulkan0,Vulkan1", devs[0].ID, devs[1].ID)
	}
	if devs[0].Name != "AMD Radeon RX 7900 XTX" {
		t.Fatalf("name = %q", devs[0].Name)
	}
	// 24560 MiB / 1024, matching gpuSetFromStats' MemTotalMB conversion so the
	// two figures are comparable without a unit change at the match site.
	if got := devs[0].TotalGB; got < 23.9 || got > 24.1 {
		t.Fatalf("TotalGB = %v, want ~23.98", got)
	}
	// The banner line ("Found 2 Vulkan devices:") has no MiB group and must not
	// become a device.
	for _, d := range devs {
		if d.ID == "ggml_vulkan" {
			t.Fatalf("banner parsed as a device: %+v", devs)
		}
	}
}

func TestAutogen_ParseBackendDevices_SdServer(t *testing.T) {
	// sd-server documents "one 'name<TAB>description' per line". It reports no
	// size, so these match on name alone.
	out := "cuda0\tNVIDIA GeForce RTX 4070 Ti SUPER\ncuda1\tNVIDIA GeForce RTX 3060\ncpu\tCPU\n"
	devs := parseBackendDevices(out)
	if len(devs) != 2 {
		t.Fatalf("parsed %d devices, want 2 (cpu dropped): %+v", len(devs), devs)
	}
	if devs[0].ID != "cuda0" || devs[1].ID != "cuda1" {
		t.Fatalf("ids = %q,%q", devs[0].ID, devs[1].ID)
	}
	if devs[0].TotalGB != 0 {
		t.Fatalf("TotalGB = %v, want 0 (unreported)", devs[0].TotalGB)
	}
}

// The case the whole file exists for: telemetry and the backend disagree about
// which card is device 0. Issue #4's reporter has a 3060 beside a 4070 Ti SUPER;
// give telemetry the 3060 first and the backend the faster card first, which is
// exactly what CUDA_DEVICE_ORDER=FASTEST_FIRST produces.
func TestAutogen_BackendIDs_ReversedEnumeration(t *testing.T) {
	set := GpuSet{
		{Index: 0, Name: "NVIDIA GeForce RTX 3060", TotalGB: 12, FreeGB: 11},
		{Index: 1, Name: "NVIDIA GeForce RTX 4070 Ti SUPER", TotalGB: 16, FreeGB: 15},
	}
	devs := []BackendDevice{
		{ID: "CUDA0", Name: "NVIDIA GeForce RTX 4070 Ti SUPER", TotalGB: 16},
		{ID: "CUDA1", Name: "NVIDIA GeForce RTX 3060", TotalGB: 12},
	}
	got := set.BackendIDs(devs)
	if len(got) != 2 || got[0] != "CUDA1" || got[1] != "CUDA0" {
		t.Fatalf("BackendIDs = %v, want [CUDA1 CUDA0]: the ids must follow OUR order, not the backend's", got)
	}
}

// An adapter we filtered out (an iGPU under minInferenceVramGB, or a card the
// user excluded) is still in the backend's list. Naming devices is what keeps it
// out of the split, so the mapping must skip it rather than shift onto it.
func TestAutogen_BackendIDs_SkipsUnplannedAdapter(t *testing.T) {
	set := GpuSet{{Index: 1, Name: "AMD Radeon RX 7900 XTX", TotalGB: 23.98, FreeGB: 23}}
	devs := parseBackendDevices(llamaListDevicesOut)
	got := set.BackendIDs(devs)
	if len(got) != 1 || got[0] != "Vulkan0" {
		t.Fatalf("BackendIDs = %v, want [Vulkan0]", got)
	}
}

// Refusal is the safety property: a set we cannot fully place must produce no
// device list at all, so the caller falls back to unnamed placement instead of
// emitting one that silently drops a card the sizer budgeted for.
func TestAutogen_BackendIDs_RefusesPartialMatch(t *testing.T) {
	set := GpuSet{
		{Index: 0, Name: "AMD Radeon RX 7900 XTX", TotalGB: 23.98, FreeGB: 23},
		{Index: 1, Name: "NVIDIA GeForce RTX 3060", TotalGB: 12, FreeGB: 11},
	}
	if got := set.BackendIDs(parseBackendDevices(llamaListDevicesOut)); got != nil {
		t.Fatalf("BackendIDs = %v, want nil when a planned card is not in the backend's list", got)
	}
	if got := set.BackendIDs(nil); got != nil {
		t.Fatalf("BackendIDs with no listing = %v, want nil", got)
	}
}

// Same name, same capacity, twice. Either assignment is correct, so the mapping
// must succeed rather than refuse: two identical cards are interchangeable.
func TestAutogen_BackendIDs_IdenticalCards(t *testing.T) {
	set := GpuSet{
		{Index: 0, Name: "NVIDIA GeForce RTX 3090", TotalGB: 24, FreeGB: 23},
		{Index: 1, Name: "NVIDIA GeForce RTX 3090", TotalGB: 24, FreeGB: 23},
	}
	devs := []BackendDevice{
		{ID: "CUDA0", Name: "NVIDIA GeForce RTX 3090", TotalGB: 24},
		{ID: "CUDA1", Name: "NVIDIA GeForce RTX 3090", TotalGB: 24},
	}
	got := set.BackendIDs(devs)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("BackendIDs = %v, want two distinct ids", got)
	}
}

// Vendor prefixes, trademark marks and brand words differ between telemetry and
// ggml for the same silicon. The model designation is what has to survive.
func TestAutogen_DeviceNamesMatch(t *testing.T) {
	match := [][2]string{
		{"AMD Radeon(TM) Graphics", "Radeon Graphics"},
		{"NVIDIA GeForce RTX 4070 Ti SUPER", "GeForce RTX 4070 Ti SUPER"},
		{"AMD Radeon RX 7900 XTX", "Radeon RX 7900 XTX"},
	}
	for _, p := range match {
		if !deviceNamesMatch(p[0], p[1]) {
			t.Errorf("deviceNamesMatch(%q, %q) = false, want true", p[0], p[1])
		}
	}
	// A 4070 beside a 4070 Ti is the case containment must NOT collapse, since
	// picking the wrong one hands the split to the wrong card.
	differ := [][2]string{
		{"NVIDIA GeForce RTX 4070 Ti SUPER", "NVIDIA GeForce RTX 3060"},
		{"AMD Radeon RX 7900 XTX", "AMD Radeon(TM) Graphics"},
	}
	for _, p := range differ {
		if deviceNamesMatch(p[0], p[1]) {
			t.Errorf("deviceNamesMatch(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

// DeviceFlagFor's refusal paths, which are the ones that decide whether a
// launch keeps the argv that shipped. The probing path needs a real backend
// binary and is covered by parseBackendDevices + BackendIDs above.
func TestAutogen_DeviceFlagFor_Refusals(t *testing.T) {
	two := GpuSet{
		{Index: 0, Name: "NVIDIA GeForce RTX 3060", TotalGB: 12, FreeGB: 11},
		{Index: 3, Name: "NVIDIA GeForce RTX 4070 Ti SUPER", TotalGB: 16, FreeGB: 15},
	}
	// No probe can help a set that isn't split.
	if devs, order := DeviceFlagFor("llama-server", GpuSet{two[0]}, 0); devs != "" || order != nil {
		t.Fatalf("single-device DeviceFlagFor = %q,%v, want \"\",nil", devs, order)
	}
	// A main index that names no device in the set: nothing to pin it to.
	if devs, order := DeviceFlagFor("llama-server", two, 1); devs != "" || order != nil {
		t.Fatalf("unknown main index = %q,%v, want \"\",nil", devs, order)
	}
	// A binary that cannot be run at all degrades to unnamed placement rather
	// than to an error.
	if devs, order := DeviceFlagFor(filepath.Join(t.TempDir(), "not-a-backend"), two, 3); devs != "" || order != nil {
		t.Fatalf("missing exe = %q,%v, want \"\",nil", devs, order)
	}
}

// The placement rule the whole ordering rests on: measured on a Vulkan build,
// -sm layer puts the non-splittable output weight on the device listed LAST and
// ignores --main-gpu. So the main device has to be last, and everything else
// keeps its relative order (the split vector is only meaningful positionally).
func TestAutogen_MainLastOrder(t *testing.T) {
	three := GpuSet{
		{Index: 0, Name: "a", TotalGB: 12, FreeGB: 11},
		{Index: 3, Name: "b", TotalGB: 16, FreeGB: 15},
		{Index: 7, Name: "c", TotalGB: 24, FreeGB: 23},
	}
	// Main is telemetry index 3, which is POSITION 1. Only the named form can
	// say so, and it must end up last.
	got := three.MainLastOrder(3)
	if len(got) != 3 || got[0] != 0 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("MainLastOrder(3) = %v, want [0 2 1]", got)
	}
	// Already last: an identity permutation, not a rotation.
	if got := three.MainLastOrder(7); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("MainLastOrder(7) = %v, want [0 1 2]", got)
	}
	if got := three.MainLastOrder(9); got != nil {
		t.Fatalf("MainLastOrder of an absent index = %v, want nil", got)
	}
	// The split must travel with the list or each card gets the other's ratio.
	if got := PermuteSplit([]float64{0.2, 0.3, 0.5}, []int{0, 2, 1}); got[0] != 0.2 || got[1] != 0.5 || got[2] != 0.3 {
		t.Fatalf("PermuteSplit = %v, want [0.2 0.5 0.3]", got)
	}
	// A length that cannot be mapped is left alone rather than rearranged.
	in := []float64{0.5, 0.5}
	if got := PermuteSplit(in, []int{0, 2, 1}); len(got) != 2 || got[0] != 0.5 {
		t.Fatalf("PermuteSplit with a mismatched order = %v, want the input back", got)
	}
}

// The probe has to enumerate the way the LAUNCH will. Every multi-GPU config we
// emit carries CUDA_DEVICE_ORDER=PCI_BUS_ID, while the CUDA runtime defaults to
// FASTEST_FIRST: a listing read under the inherited environment numbers a
// mismatched pair the other way round, so "CUDA0" handed back to a bus-ordered
// process names the OTHER card. That reversal is the exact failure --device
// exists to prevent, and it is silent.
func TestAutogen_ProbeEnv_PinsBusOrder(t *testing.T) {
	var found bool
	for _, kv := range probeEnv() {
		if kv == cudaOrderEnv {
			found = true
		}
	}
	if !found {
		t.Fatalf("probe environment does not pin %s", cudaOrderEnv)
	}
}

// A single-GPU box is the population that never asked for any of this. It must
// get no probe, no --device and no env block: byte-identical to what shipped.
func TestAutogen_SingleGpu_EmitsNothingNew(t *testing.T) {
	one := GpuSet{{Index: 0, Name: "NVIDIA GeForce RTX 4090", TotalGB: 24, FreeGB: 23}}
	if devs, order := DeviceFlagFor(filepath.Join(t.TempDir(), "nope"), one, 0); devs != "" || order != nil {
		t.Fatalf("single-GPU DeviceFlagFor = %q,%v, want no flags", devs, order)
	}
	var b strings.Builder
	writeSingleDeviceEnv(&b, Settings{Gpus: one}, filepath.Join(t.TempDir(), "nope"))
	if b.String() != "" {
		t.Fatalf("single-GPU writeSingleDeviceEnv emitted %q, want nothing", b.String())
	}
}

// A generate reaches up to five distinct backend binaries. The per-probe window
// is deliberately generous, so without a shared budget a box where every backend
// is wedged multiplies it into a startup that visibly hangs.
func TestAutogen_ProbeBudget_BoundsTheWholeGenerate(t *testing.T) {
	probeBudgetMu.Lock()
	saved := probeBudgetLeft
	probeBudgetMu.Unlock()
	t.Cleanup(func() {
		probeBudgetMu.Lock()
		probeBudgetLeft = saved
		probeBudgetMu.Unlock()
	})

	if d := takeProbeBudget(); d != backendProbeTimeout {
		t.Fatalf("first probe window = %v, want the full %v", d, backendProbeTimeout)
	}
	spendProbeBudget(backendProbeTimeout)
	if d := takeProbeBudget(); d <= 0 || d > backendProbeBudget-backendProbeTimeout {
		t.Fatalf("second probe window = %v, want what is left of %v", d, backendProbeBudget)
	}
	spendProbeBudget(backendProbeBudget)
	if d := takeProbeBudget(); d != 0 {
		t.Fatalf("exhausted probe window = %v, want 0", d)
	}
	if _, err := probeBackendDevices(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("probe with no budget returned no error")
	}
}

// A hand-written --tensor-split is positional against the list the backend would
// have enumerated on its own. --device replaces that list in main-last order, so
// emitting both would silently point the user's ratio at a different pair.
func TestAutogen_PinsOwnSplit(t *testing.T) {
	if pinsOwnSplit(nil) {
		t.Fatal("no override pins nothing")
	}
	if pinsOwnSplit(&Override{}) {
		t.Fatal("blank tensorSplit pins nothing")
	}
	if pinsOwnSplit(&Override{TensorSplit: "  "}) {
		t.Fatal("whitespace tensorSplit pins nothing")
	}
	if !pinsOwnSplit(&Override{TensorSplit: "3,1"}) {
		t.Fatal("a hand-written ratio must suppress --device")
	}
}
