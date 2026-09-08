package autogen

import "testing"

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
