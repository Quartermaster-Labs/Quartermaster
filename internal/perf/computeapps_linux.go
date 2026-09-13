//go:build linux

package perf

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// computeAppsPlatform uses Linux DRM fdinfo to attribute GPU memory to
// processes for AMD (amdgpu) GPUs, providing per-process GTT+VRAM totals
// without any external tools. This fixes the VRAM gauge on AMD APUs where
// the driver pre-allocates a large GTT pool, making the aggregate counter
// appear flat even as model allocations grow within that pool.
func computeAppsPlatform(_ context.Context) []GpuProc {
	return queryDRMFdinfo()
}

// queryDRMFdinfo walks /proc/*/fdinfo/* for amdgpu DRM clients and returns
// per-process memory (GTT + VRAM combined), deduplicated by DRM client-id so
// that duplicated fds are not counted twice.
func queryDRMFdinfo() []GpuProc {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	type pidEntry struct {
		gttMB  int
		vramMB int
		seen   map[int64]struct{} // drm-client-id dedup
	}
	byPID := make(map[int]*pidEntry)

	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fdinfo", pid))
		if err != nil {
			continue // permission denied or process gone
		}
		for _, fd := range fds {
			gtt, vram, cid, ok := parseDRMFdinfoFile(
				fmt.Sprintf("/proc/%d/fdinfo/%s", pid, fd.Name()),
			)
			if !ok {
				continue
			}
			e := byPID[pid]
			if e == nil {
				e = &pidEntry{seen: make(map[int64]struct{})}
				byPID[pid] = e
			}
			// Deduplicate by client-id when available so that dup'd fds don't
			// double-count the same allocation. When cid < 0 (kernel didn't
			// provide it) count every fd independently.
			if cid >= 0 {
				if _, dup := e.seen[cid]; dup {
					continue
				}
				e.seen[cid] = struct{}{}
			}
			e.gttMB += gtt
			e.vramMB += vram
		}
	}

	// Merge KFD per-process memory so that ROCm/HIP processes that allocate
	// fine-grained unified memory (bypassing DRM fdinfo) are still attributed.
	// /sys/class/kfd/kfd/proc/{pid}/vram_* is the KFD-side view; take the max
	// of DRM and KFD so coarse-grained allocations visible in both are not
	// double-counted.
	if kfdProcs, err := os.ReadDir("/sys/class/kfd/kfd/proc"); err == nil {
		for _, kp := range kfdProcs {
			if !kp.IsDir() {
				continue
			}
			pid, err := strconv.Atoi(kp.Name())
			if err != nil {
				continue
			}
			kMB := kfdProcMemMB(pid)
			if kMB <= 0 {
				continue
			}
			e := byPID[pid]
			if e == nil {
				e = &pidEntry{seen: make(map[int64]struct{})}
				byPID[pid] = e
			}
			if kMB > e.gttMB+e.vramMB {
				e.gttMB = kMB
				e.vramMB = 0
			}
		}
	}

	var out []GpuProc
	for pid, e := range byPID {
		memMB := e.gttMB + e.vramMB
		if memMB <= 0 {
			continue
		}
		name := ""
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
			name = strings.TrimSpace(string(b))
		}
		out = append(out, GpuProc{PID: pid, Name: name, MemMB: memMB})
	}
	return out
}

// kfdProcMemMB sums /sys/class/kfd/kfd/proc/{pid}/vram_* for one process and
// returns the total in MB.  The vram_* files are named vram_{gpu_id} where
// gpu_id comes from the KFD topology (not a DRM node index).  Returns 0 when
// the process has no KFD entry or the path is unreadable.
func kfdProcMemMB(pid int) int {
	dir := fmt.Sprintf("/sys/class/kfd/kfd/proc/%d", pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "vram_") {
			continue
		}
		b, err := os.ReadFile(dir + "/" + e.Name())
		if err != nil {
			continue
		}
		v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		total += v
	}
	return int(total / (1024 * 1024))
}

// parseDRMFdinfoFile reads one /proc/{pid}/fdinfo/{fd} file and returns
// (gttMB, vramMB, clientID, ok). ok=false when not an amdgpu DRM client or
// when the file cannot be read.
func parseDRMFdinfoFile(path string) (gttMB, vramMB int, clientID int64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	var isAMD bool
	var gttKiB, vramKiB int64
	clientID = -1

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(k) {
		case "drm-driver":
			isAMD = strings.TrimSpace(v) == "amdgpu"
		case "drm-client-id":
			clientID, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		case "drm-memory-gtt":
			gttKiB = parseDRMKiB(strings.TrimSpace(v))
		case "drm-memory-vram":
			vramKiB = parseDRMKiB(strings.TrimSpace(v))
		}
	}
	if !isAMD {
		return
	}
	return int(gttKiB / 1024), int(vramKiB / 1024), clientID, true
}

// parseDRMKiB parses a DRM fdinfo memory value like "229184 KiB" and returns
// the size in KiB. Handles KiB, MiB, GiB, and B unit suffixes.
func parseDRMKiB(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	if len(fields) < 2 {
		return v // assume KiB
	}
	switch strings.ToUpper(fields[1]) {
	case "KIB", "KB":
		return v
	case "MIB", "MB":
		return v * 1024
	case "GIB", "GB":
		return v * 1024 * 1024
	case "B":
		return v / 1024
	}
	return v // fallback: assume KiB
}
