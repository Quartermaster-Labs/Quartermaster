package perf

import "testing"

func TestParseKFDProps_ApuHasNoLocalMemory(t *testing.T) {
	// A 780M as KFD describes it: a GPU node with no local memory of its own.
	const props = `cpu_cores_count 0
simd_count 12
mem_banks_count 1
local_mem_size 0
location_id 1024
name gfx1103
`
	p := parseKFDProps(props)
	if !p.HasGPU() {
		t.Fatalf("simd_count 12 must read as a GPU node: %+v", p)
	}
	if !p.HasLocalMem {
		t.Fatalf("local_mem_size was present: %+v", p)
	}
	if !p.IsAPU() {
		t.Errorf("local_mem_size 0 must read as an APU: %+v", p)
	}
	if p.LocationID != 1024 {
		t.Errorf("LocationID = %d, want 1024", p.LocationID)
	}
}

func TestParseKFDProps_DiscreteCardHasLocalMemory(t *testing.T) {
	const props = "simd_count 96\nlocal_mem_size 25753026560\nlocation_id 25856\n"
	p := parseKFDProps(props)
	if !p.HasGPU() || !p.HasLocalMem {
		t.Fatalf("expected a GPU node with local memory: %+v", p)
	}
	if p.IsAPU() {
		t.Errorf("local_mem_size > 0 must NOT read as an APU: %+v", p)
	}
}

func TestParseKFDProps_CPUNodeIsNotAnApu(t *testing.T) {
	// KFD lists CPU nodes in the same topology, and they have no local memory
	// either. simd_count is what separates them from a GPU.
	const props = "simd_count 0\nlocal_mem_size 0\nlocation_id 0\n"
	p := parseKFDProps(props)
	if p.HasGPU() {
		t.Fatalf("simd_count 0 is not a GPU node: %+v", p)
	}
	if p.IsAPU() {
		t.Errorf("a CPU node must not read as an APU: %+v", p)
	}
}

func TestParseKFDProps_MissingKeyIsUnknown(t *testing.T) {
	// An older kernel that does not report local_mem_size must answer
	// "unknown", not "zero": zero is the APU answer.
	p := parseKFDProps("simd_count 12\nlocation_id 1024\n")
	if p.HasLocalMem {
		t.Fatalf("HasLocalMem must be false when the key is absent: %+v", p)
	}
	if p.IsAPU() {
		t.Errorf("an unanswerable node must not read as an APU: %+v", p)
	}
}

func TestKFDLocationID(t *testing.T) {
	cases := []struct {
		bdf  string
		want uint64
		ok   bool
	}{
		{"0000:65:00.0", 0x6500, true}, // bus 0x65, dev 0, fn 0
		{"0000:0c:00.0", 0x0c00, true}, // a dGPU on a low bus
		{"0000:c1:00.0", 0xc100, true}, // APU-class bus
		{"0000:03:00.1", 0x0301, true}, // function 1
		{"65:00.0", 0x6500, true},      // no domain
		{"nonsense", 0, false},         // no devfn
		{"0000:zz:00.0", 0, false},     // not hex
		{"0000:65:00", 0, false},       // no function
	}
	for _, tc := range cases {
		got, ok := kfdLocationID(tc.bdf)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("kfdLocationID(%q) = (%d, %v), want (%d, %v)", tc.bdf, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseDrmUevent(t *testing.T) {
	const uevent = `DRIVER=amdgpu
PCI_CLASS=30000
PCI_ID=1002:15BF
PCI_SLOT_NAME=0000:c1:00.0
`
	u := parseDrmUevent(uevent)
	if u.Driver != "amdgpu" {
		t.Errorf("Driver = %q, want amdgpu", u.Driver)
	}
	if u.PCISlot != "0000:c1:00.0" {
		t.Errorf("PCISlot = %q", u.PCISlot)
	}
	// Lowercased: it is pasted into a device name, and the kernel prints upper.
	if u.PCIID != "1002:15bf" {
		t.Errorf("PCIID = %q, want 1002:15bf", u.PCIID)
	}
}

func TestParseDRMFdinfo_AmdgpuClient(t *testing.T) {
	const fdinfo = `pos:	0
flags:	0100002
mnt_id:	30
drm-driver:	amdgpu
drm-pdev:	0000:03:00.0
drm-client-id:	27
drm-memory-vram:	1 GiB
drm-memory-gtt:	229184 KiB
drm-memory-cpu:	0 B
`
	info, ok := parseDRMFdinfo(fdinfo)
	if !ok {
		t.Fatal("an amdgpu client must parse")
	}
	if info.Driver != "amdgpu" || !info.HasClientID || info.ClientID != 27 {
		t.Fatalf("driver/client id wrong: %+v", info)
	}
	if info.VramBytes != 1<<30 {
		t.Errorf("VramBytes = %d, want %d", info.VramBytes, 1<<30)
	}
	if want := int64(229184) << 10; info.GttBytes != want {
		t.Errorf("GttBytes = %d, want %d", info.GttBytes, want)
	}
}

func TestParseDRMFdinfo_OtherVendorIsSkipped(t *testing.T) {
	const fdinfo = "drm-driver:\ti915\ndrm-client-id:\t3\ndrm-memory-vram:\t2 MiB\n"
	if _, ok := parseDRMFdinfo(fdinfo); ok {
		t.Error("an i915 client must not be attributed to the AMD counters")
	}
}

func TestParseDRMFdinfo_NonDrmFileIsSkipped(t *testing.T) {
	const fdinfo = "pos:\t0\nflags:\t0100000\nmnt_id:\t30\n"
	if _, ok := parseDRMFdinfo(fdinfo); ok {
		t.Error("a file with no drm-driver key must not parse as a client")
	}
}

func TestParseDRMMemValue(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"229184 KiB", 229184 << 10},
		{"1 GiB", 1 << 30},
		{"512 MiB", 512 << 20},
		{"1024 B", 1024},
		{"4096", 4096}, // no unit: bytes
		{"", 0},
		{"nonsense KiB", 0},
	}
	for _, tc := range cases {
		if got := parseDRMMemValue(tc.in); got != tc.want {
			t.Errorf("parseDRMMemValue(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSysfsGpuStat_ApuKeepsThePoolsApart(t *testing.T) {
	mem := sysfsMem{
		VramTotalB: 2 << 30, // the BIOS carve-out
		VramUsedB:  1 << 30,
		GttTotalB:  15 << 30, // the pool the GPU actually allocates from
		GttUsedB:   4 << 30,
	}
	got := sysfsGpuStat(1, "amdgpu [1002:15bf]", mem, true, 0)

	if got.MemTotalMB != 2048 {
		t.Errorf("MemTotalMB = %d, want the dedicated 2048", got.MemTotalMB)
	}
	if got.SharedTotalMB != 15360 {
		t.Errorf("SharedTotalMB = %d, want 15360", got.SharedTotalMB)
	}
	if !got.Integrated {
		t.Error("Integrated must survive into the stat")
	}
	// The gauge is over the two pools together: dedicated alone would read 50%.
	if want := 5.0 / 17.0 * 100; got.MemUtilPct < want-0.01 || got.MemUtilPct > want+0.01 {
		t.Errorf("MemUtilPct = %.2f, want %.2f", got.MemUtilPct, want)
	}
}

func TestSysfsGpuStat_DiscreteCardIgnoresGtt(t *testing.T) {
	mem := sysfsMem{
		VramTotalB: 24 << 30,
		VramUsedB:  6 << 30,
		GttTotalB:  15 << 30,
		GttUsedB:   1 << 30,
	}
	got := sysfsGpuStat(0, "Navi 31", mem, false, 0)

	if got.MemTotalMB != 24576 || got.MemUsedMB != 6144 {
		t.Errorf("dedicated pools wrong: %+v", got)
	}
	if got.SharedTotalMB != 15360 || got.SharedUsedMB != 1024 {
		t.Errorf("the aperture must still be reported raw: %+v", got)
	}
	if want := 25.0; got.MemUtilPct != want {
		t.Errorf("MemUtilPct = %.2f, want %.2f (dedicated only)", got.MemUtilPct, want)
	}
}

func TestSysfsGpuStat_KFDTotalCanRaiseGttUsed(t *testing.T) {
	mem := sysfsMem{VramTotalB: 2 << 30, GttTotalB: 15 << 30, GttUsedB: 1 << 30}

	// ROCm handed out 8 GiB of fine-grained unified memory that the DRM counter
	// never saw. The larger view wins.
	got := sysfsGpuStat(1, "amdgpu", mem, true, 8<<30)
	if got.SharedUsedMB != 8192 {
		t.Errorf("SharedUsedMB = %d, want the KFD total 8192", got.SharedUsedMB)
	}

	// The DRM counter already saw more than KFD knows about: keep the larger,
	// never the sum (both describe the same allocations).
	got = sysfsGpuStat(1, "amdgpu", mem, true, 512<<20)
	if got.SharedUsedMB != 1024 {
		t.Errorf("SharedUsedMB = %d, want the DRM 1024", got.SharedUsedMB)
	}

	// A discrete card has no KFD reconciliation: VRAM is TTM's to report.
	got = sysfsGpuStat(0, "Navi 31", mem, false, 8<<30)
	if got.SharedUsedMB != 1024 {
		t.Errorf("SharedUsedMB = %d, want the DRM 1024 on a card", got.SharedUsedMB)
	}
}
