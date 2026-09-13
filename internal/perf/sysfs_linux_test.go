//go:build linux

package perf

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeFile makes a fixture file, creating the directories it needs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestReadSysfsFrom_WalksTheKernelTree is the Docker half of issue #37: a
// container with no rocm-smi and no ROCm userspace still has these files, and
// this walk is what turns them into a device set. It runs against a fixture
// tree because the machine running the test is not the machine with the card.
func TestReadSysfsFrom_WalksTheKernelTree(t *testing.T) {
	root := t.TempDir()
	roots := sysfsRoots{
		DRM:    filepath.Join(root, "drm"),
		KFD:    filepath.Join(root, "kfd"),
		DevDRI: filepath.Join(root, "devdri"),
	}

	// card0: an APU with a 2GiB carve-out and a 15GiB pool, and KFD agreeing.
	apuDir := filepath.Join(roots.DRM, "card0", "device")
	writeFile(t, filepath.Join(apuDir, "uevent"), "DRIVER=amdgpu\nPCI_ID=1002:15BF\nPCI_SLOT_NAME=0000:c1:00.0\n")
	writeFile(t, filepath.Join(apuDir, "mem_info_vram_total"), "2147483648")
	writeFile(t, filepath.Join(apuDir, "mem_info_vram_used"), "1073741824")
	writeFile(t, filepath.Join(apuDir, "mem_info_gtt_total"), "16106127360")
	writeFile(t, filepath.Join(apuDir, "mem_info_gtt_used"), "4294967296")
	writeFile(t, filepath.Join(apuDir, "gpu_busy_percent"), "37")
	writeFile(t, filepath.Join(apuDir, "hwmon", "hwmon3", "temp1_input"), "45000")
	writeFile(t, filepath.Join(apuDir, "hwmon", "hwmon3", "power1_average"), "25000000")

	// KFD: node 1 is that APU (no local memory), and a process holds 8GiB of
	// fine-grained memory the DRM counter never saw.
	loc, ok := kfdLocationID("0000:c1:00.0")
	if !ok {
		t.Fatal("fixture BDF must parse")
	}
	writeFile(t, filepath.Join(roots.KFD, "topology", "nodes", "1", "properties"),
		"simd_count 12\nlocal_mem_size 0\nlocation_id "+strconv.FormatUint(loc, 10)+"\n")
	writeFile(t, filepath.Join(roots.KFD, "proc", "1234", "vram_1"), "8589934592")

	// card1: a discrete card, so the aperture must stay out of the budget.
	gpuDir := filepath.Join(roots.DRM, "card1", "device")
	writeFile(t, filepath.Join(gpuDir, "uevent"), "DRIVER=amdgpu\nPCI_ID=1002:744C\nPCI_SLOT_NAME=0000:03:00.0\n")
	writeFile(t, filepath.Join(gpuDir, "mem_info_vram_total"), "25769803776")
	writeFile(t, filepath.Join(gpuDir, "mem_info_vram_used"), "1073741824")
	writeFile(t, filepath.Join(gpuDir, "mem_info_gtt_total"), "16106127360")
	writeFile(t, filepath.Join(gpuDir, "mem_info_gtt_used"), "536870912")
	loc, _ = kfdLocationID("0000:03:00.0")
	writeFile(t, filepath.Join(roots.KFD, "topology", "nodes", "2", "properties"),
		"simd_count 96\nlocal_mem_size 25753026560\nlocation_id "+strconv.FormatUint(loc, 10)+"\n")

	// card2: an amdgpu the container was NOT given, and card3: another vendor.
	writeFile(t, filepath.Join(roots.DRM, "card2", "device", "uevent"), "DRIVER=amdgpu\nPCI_ID=1002:164E\nPCI_SLOT_NAME=0000:0b:00.0\n")
	writeFile(t, filepath.Join(roots.DRM, "card2", "device", "mem_info_vram_total"), "1073741824")
	writeFile(t, filepath.Join(roots.DRM, "card3", "device", "uevent"), "DRIVER=i915\nPCI_ID=8086:46A6\nPCI_SLOT_NAME=0000:00:02.0\n")
	writeFile(t, filepath.Join(roots.DRM, "card3", "device", "mem_info_vram_total"), "1073741824")

	// /dev/dri holds card0 and card1 only: card2 is the host's, not ours.
	writeFile(t, filepath.Join(roots.DevDRI, "card0"), "")
	writeFile(t, filepath.Join(roots.DevDRI, "card1"), "")

	stats, err := readSysfsFrom(roots)
	if err != nil {
		t.Fatalf("readSysfsFrom: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d stats, want 2 (card2 is not in /dev/dri, card3 is not amdgpu): %+v", len(stats), stats)
	}

	apu := stats[0]
	if apu.ID != 0 || !apu.Integrated {
		t.Fatalf("card0 = %+v, want the APU with the kernel's flag set", apu)
	}
	if apu.Name != "amdgpu [1002:15bf]" {
		t.Errorf("Name = %q, want the PCI id where the kernel has no product name", apu.Name)
	}
	if apu.MemTotalMB != 2048 || apu.MemUsedMB != 1024 {
		t.Errorf("dedicated = %d/%d MB, want 2048/1024", apu.MemTotalMB, apu.MemUsedMB)
	}
	if apu.SharedTotalMB != 15360 {
		t.Errorf("SharedTotalMB = %d, want 15360", apu.SharedTotalMB)
	}
	// KFD's 8GiB beats the DRM counter's 4GiB: it is the larger of the two views.
	if apu.SharedUsedMB != 8192 {
		t.Errorf("SharedUsedMB = %d, want the KFD total 8192", apu.SharedUsedMB)
	}
	if apu.GpuUtilPct != 37 || apu.TempC != 45 {
		t.Errorf("util/temp = %.0f/%d, want 37/45 from sysfs", apu.GpuUtilPct, apu.TempC)
	}
	if apu.PowerDrawW != 25 {
		t.Errorf("PowerDrawW = %.2f, want 25 from hwmon", apu.PowerDrawW)
	}
	// 1GiB + 8GiB of 2GiB + 15GiB: the gauge spans both pools.
	if want := 9.0 / 17.0 * 100; apu.MemUtilPct < want-0.01 || apu.MemUtilPct > want+0.01 {
		t.Errorf("MemUtilPct = %.2f, want %.2f", apu.MemUtilPct, want)
	}

	gpu := stats[1]
	if gpu.ID != 1 || gpu.Integrated {
		t.Fatalf("card1 = %+v, want a discrete card", gpu)
	}
	if gpu.Name != "amdgpu [1002:744c]" {
		t.Errorf("Name = %q", gpu.Name)
	}
	if gpu.MemTotalMB != 24576 {
		t.Errorf("MemTotalMB = %d, want 24576", gpu.MemTotalMB)
	}
	// The aperture is reported raw, and its usage is DRM's alone: no KFD
	// reconciliation happens on a card.
	if gpu.SharedTotalMB != 15360 || gpu.SharedUsedMB != 512 {
		t.Errorf("shared = %d/%d MB, want 15360/512", gpu.SharedTotalMB, gpu.SharedUsedMB)
	}
	if want := 100.0 / 24.0; gpu.MemUtilPct < want-0.01 || gpu.MemUtilPct > want+0.01 {
		t.Errorf("MemUtilPct = %.2f, want %.2f (dedicated only)", gpu.MemUtilPct, want)
	}
}

func TestReadSysfsFrom_NoCardsIsNoTool(t *testing.T) {
	root := t.TempDir()
	_, err := readSysfsFrom(sysfsRoots{
		DRM:    filepath.Join(root, "drm"),
		KFD:    filepath.Join(root, "kfd"),
		DevDRI: filepath.Join(root, "devdri"),
	})
	if err != ErrNoGpuTool {
		t.Fatalf("err = %v, want ErrNoGpuTool so the chain can report no GPU backend", err)
	}
}

// KFD answering "not an APU" must not be confused with KFD being absent: the
// rocm-smi path asks by node id, and a wrong "unknown" would silently drop the
// kernel's only classification signal.
func TestKFDIsAPUIn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "topology", "nodes", "1", "properties"), "simd_count 12\nlocal_mem_size 0\n")
	writeFile(t, filepath.Join(root, "topology", "nodes", "2", "properties"), "simd_count 96\nlocal_mem_size 25753026560\n")
	writeFile(t, filepath.Join(root, "topology", "nodes", "3", "properties"), "simd_count 0\nlocal_mem_size 0\n")

	if apu, known := kfdIsAPUIn(root, 1); !known || !apu {
		t.Errorf("node 1 = (%v, %v), want (true, true)", apu, known)
	}
	if apu, known := kfdIsAPUIn(root, 2); !known || apu {
		t.Errorf("node 2 = (%v, %v), want (false, true): a card is a known non-APU", apu, known)
	}
	if _, known := kfdIsAPUIn(root, 3); known {
		t.Error("node 3 is a CPU node: KFD cannot answer, and that must be (false, false)")
	}
	if _, known := kfdIsAPUIn(root, 99); known {
		t.Error("a missing node must be (false, false)")
	}
	if _, known := kfdIsAPUIn(root, -1); known {
		t.Error("no Node ID column must be (false, false), never node 0")
	}
}
