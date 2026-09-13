package perf

// Pure parsers for the Linux-only readers: the kernel's DRM counters, KFD
// topology, and /proc fdinfo. They take text, never paths, so their tests run on
// every platform; the file access itself lives in monitor_unix.go and
// computeapps_linux.go.

import (
	"strconv"
	"strings"
	"time"
)

const bytesPerMB = 1024 * 1024

// kfdNodeProps is the part of a KFD topology node's properties file that
// decides "integrated". An APU is a GPU node whose local_mem_size is zero:
// the kernel's own statement that the device has no memory of its own, which is
// worth more than any product-name or size-ratio heuristic.
type kfdNodeProps struct {
	LocationID uint64
	SimdCount  uint64
	// LocalMemBytes is local_mem_size, and HasLocalMem records that the file
	// carried the key at all: a kernel too old to report it, or a file that was
	// read in a truncated state, must answer "unknown" rather than "zero".
	LocalMemBytes uint64
	HasLocalMem   bool
}

// HasGPU reports whether this is a GPU node. KFD lists CPU nodes in the same
// topology, and they have no local memory either; they also have no SIMDs.
func (p kfdNodeProps) HasGPU() bool { return p.SimdCount > 0 }

// IsAPU reports whether the node is a GPU with no dedicated memory.
func (p kfdNodeProps) IsAPU() bool {
	return p.HasGPU() && p.HasLocalMem && p.LocalMemBytes == 0
}

// parseKFDProps reads a KFD node's properties ("key value" per line).
func parseKFDProps(text string) kfdNodeProps {
	var p kfdNodeProps
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "location_id":
			p.LocationID = v
		case "simd_count":
			p.SimdCount = v
		case "local_mem_size":
			p.LocalMemBytes = v
			p.HasLocalMem = true
		}
	}
	return p
}

// kfdLocationID packs a PCI address ("0000:65:00.0") the way KFD's location_id
// does: bus << 8 | dev << 3 | fn.
func kfdLocationID(bdf string) (uint64, bool) {
	parts := strings.Split(bdf, ":")
	if len(parts) < 2 {
		return 0, false
	}
	df := strings.SplitN(parts[len(parts)-1], ".", 2)
	if len(df) != 2 {
		return 0, false
	}
	bus, err1 := strconv.ParseUint(parts[len(parts)-2], 16, 16)
	dev, err2 := strconv.ParseUint(df[0], 16, 8)
	fn, err3 := strconv.ParseUint(df[1], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	return bus<<8 | dev<<3 | fn, true
}

// drmUevent is the identifying half of a DRM device's uevent file.
type drmUevent struct {
	Driver  string
	PCISlot string
	PCIID   string
}

// parseDrmUevent reads the "KEY=value" lines every DRM device exposes.
func parseDrmUevent(text string) drmUevent {
	var u drmUevent
	for _, line := range strings.Split(text, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch key {
		case "DRIVER":
			u.Driver = value
		case "PCI_SLOT_NAME":
			u.PCISlot = value
		case "PCI_ID":
			u.PCIID = strings.ToLower(value)
		}
	}
	return u
}

// sysfsMem is one amdgpu card's DRM counters, in bytes: the dedicated pool
// (the BIOS carve-out, on an APU) and the GTT pool of system RAM the driver can
// map for it.
type sysfsMem struct {
	VramTotalB int64
	VramUsedB  int64
	GttTotalB  int64
	GttUsedB   int64
}

// sysfsGpuStat assembles a GpuStat from a card's sysfs counters. It keeps the
// two pools apart, exactly like the rocm-smi and DXGI readers, because whether
// the shared pool counts toward inference memory is the sizer's decision (see
// GpuStat).
//
// kfdProcBytes is KFD's per-process total for the box, 0 when KFD is not
// available. On an APU the ROCm runtime can hand out fine-grained unified
// memory that never appears in mem_info_gtt_used, so the real figure is the
// LARGER of the two views, never their sum: both are describing the same
// allocations.
func sysfsGpuStat(id int, name string, mem sysfsMem, integrated bool, kfdProcBytes int64) GpuStat {
	gttUsedB := mem.GttUsedB
	if integrated && kfdProcBytes > gttUsedB {
		gttUsedB = kfdProcBytes
	}

	// The utilization percentage is a gauge, not a budget input, and on an APU
	// the dedicated pool alone reads over 100% while the card spills into GTT.
	totalB, usedB := mem.VramTotalB, mem.VramUsedB
	if integrated {
		totalB += mem.GttTotalB
		usedB += gttUsedB
	}

	stat := GpuStat{
		Timestamp:     time.Now(),
		ID:            id,
		Name:          name,
		Integrated:    integrated,
		MemTotalMB:    int(mem.VramTotalB / bytesPerMB),
		MemUsedMB:     int(mem.VramUsedB / bytesPerMB),
		SharedTotalMB: int(mem.GttTotalB / bytesPerMB),
		SharedUsedMB:  int(gttUsedB / bytesPerMB),
	}
	if totalB > 0 {
		stat.MemUtilPct = float64(usedB) / float64(totalB) * 100
	}
	return stat
}

// drmFdinfo is one file's DRM client memory, from /proc/<pid>/fdinfo/<fd>.
type drmFdinfo struct {
	Driver      string
	ClientID    int64
	HasClientID bool
	GttBytes    int64
	VramBytes   int64
}

// parseDRMFdinfo reads a DRM client's fdinfo. ok is false for the common case
// of a file that belongs to no amdgpu client: only GPU clients carry drm-* keys.
func parseDRMFdinfo(text string) (drmFdinfo, bool) {
	var info drmFdinfo
	for _, line := range strings.Split(text, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "drm-driver":
			info.Driver = strings.TrimSpace(value)
		case "drm-client-id":
			if id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				info.ClientID, info.HasClientID = id, true
			}
		case "drm-memory-gtt":
			info.GttBytes = parseDRMMemValue(strings.TrimSpace(value))
		case "drm-memory-vram":
			info.VramBytes = parseDRMMemValue(strings.TrimSpace(value))
		}
	}
	return info, info.Driver == "amdgpu"
}

// parseDRMMemValue parses a value like "229184 KiB" into bytes. The kernel
// prints the largest unit that keeps the number readable, so every suffix
// occurs in the wild.
func parseDRMMemValue(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	if len(fields) < 2 {
		// Unreachable with the kernel's own format (it always prints a unit);
		// if it ever happens, bytes is the reading that under-claims least.
		return v
	}
	switch strings.ToUpper(fields[1]) {
	case "KIB", "KB":
		return v << 10
	case "MIB", "MB":
		return v << 20
	case "GIB", "GB":
		return v << 30
	}
	return v
}
