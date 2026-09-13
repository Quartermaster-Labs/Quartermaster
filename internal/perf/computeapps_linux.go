//go:build linux

package perf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// computeAppsPlatform attributes GPU memory to processes through the kernel's
// own interfaces: DRM fdinfo for the clients the driver knows about, plus KFD's
// per-process counter for the ROCm allocations that never reach DRM. No tool is
// involved, so foreign-VRAM detection works on an AMD box in a container that
// has neither nvidia-smi nor ROCm userspace (issue #37).
func computeAppsPlatform(_ context.Context) []GpuProc {
	byPID := drmFdinfoByPID()
	mergeKFDProcs(byPID)

	out := make([]GpuProc, 0, len(byPID))
	for pid, usage := range byPID {
		if usage.bytes <= 0 {
			continue
		}
		out = append(out, GpuProc{PID: pid, Name: procName(pid), MemMB: int(usage.bytes / bytesPerMB)})
	}
	return out
}

// drmPIDUsage is one process's total across every amdgpu client it holds.
type drmPIDUsage struct {
	bytes int64
	// clients dedups by DRM client id: dup'ed file descriptors point at the same
	// client and the same allocation, and counting them twice would invent VRAM
	// (and, in the guard, evict a model over a phantom).
	clients map[int64]struct{}
}

// drmFdinfoByPID walks /proc/<pid>/fdinfo/<fd>. Reading another user's fdinfo
// is denied without privileges, which is why every read failure is skipped: the
// result is then the processes this instance can see, and the guard treats a
// missing managed child as "refuse the reading" rather than "all foreign".
func drmFdinfoByPID() map[int]*drmPIDUsage {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	byPID := make(map[int]*drmPIDUsage)
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", p.Name(), "fdinfo"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			info, ok := drmFdinfoAt(filepath.Join("/proc", p.Name(), "fdinfo", fd.Name()))
			if !ok {
				continue
			}
			usage := byPID[pid]
			if usage == nil {
				usage = &drmPIDUsage{clients: make(map[int64]struct{})}
				byPID[pid] = usage
			}
			if info.HasClientID {
				if _, dup := usage.clients[info.ClientID]; dup {
					continue
				}
				usage.clients[info.ClientID] = struct{}{}
			}
			usage.bytes += info.GttBytes + info.VramBytes
		}
	}
	return byPID
}

// mergeKFDProcs folds KFD's view in for the processes that hold ROCm
// allocations. The two views overlap, so the larger wins rather than the sum:
// on an APU a HIP process can allocate fine-grained unified memory that the DRM
// counters never see, and adding both would double-count everything they share.
func mergeKFDProcs(byPID map[int]*drmPIDUsage) {
	procs, err := os.ReadDir("/sys/class/kfd/kfd/proc")
	if err != nil {
		return
	}
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		kfdBytes := kfdProcBytesFor(pid)
		if kfdBytes <= 0 {
			continue
		}
		usage := byPID[pid]
		if usage == nil {
			usage = &drmPIDUsage{clients: make(map[int64]struct{})}
			byPID[pid] = usage
		}
		if kfdBytes > usage.bytes {
			usage.bytes = kfdBytes
		}
	}
}

// kfdProcBytesFor sums /sys/class/kfd/kfd/proc/<pid>/vram_* for one process.
// The files are named for the KFD gpu id, not a DRM card index, so they are
// globbed rather than indexed.
func kfdProcBytesFor(pid int) int64 {
	paths, _ := filepath.Glob(filepath.Join("/sys/class/kfd/kfd/proc", strconv.Itoa(pid), "vram_*"))
	var total int64
	for _, path := range paths {
		if v, ok := sysfsReadInt64(path); ok {
			total += v
		}
	}
	return total
}

func drmFdinfoAt(path string) (drmFdinfo, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return drmFdinfo{}, false
	}
	return parseDRMFdinfo(string(b))
}

// procName is the short process name the foreign-VRAM listing shows; the guard
// itself matches on it, so it has to be the kernel's own string.
func procName(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
