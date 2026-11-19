//go:build windows

package perf

import "fmt"

// ProcessGpuMemory reads where one process's GPU allocations live, from PDH
// `\GPU Process Memory(pid_<pid>_*)`: Dedicated Usage is what sits in VRAM,
// Shared Usage what sits in system memory the GPU reads across PCIe. Both are
// summed over the adapter's mirror LUIDs, in MB.
//
// The pair is how a WDDM demotion becomes visible. When a load overruns what the
// OS will keep resident, Windows does not fail the allocation: it places the
// overflow in system memory and leaves it there, so the process loads "fine" and
// decodes several times slower at long context. Only the process's own counters
// show it; the adapter-wide free figure does not.
//
// ok is false when PDH or the counter is unavailable, or when it reports
// nothing for the pid (the process is gone or holds no GPU memory).
func ProcessGpuMemory(pid int) (dedicatedMB, sharedMB int, ok bool) {
	sum := func(counter string) (int, bool) {
		q, err := initPdhLuidCounter(fmt.Sprintf(`\GPU Process Memory(pid_%d_*)\%s`, pid, counter), "GPU Process Memory", false)
		if err != nil {
			return 0, false
		}
		defer q.close()
		items := q.collectRaw()
		if len(items) == 0 {
			return 0, false
		}
		var b float64
		for _, it := range items {
			if p, ok := parsePdhPid(it.Name); ok && p == pid {
				b += it.Val
			}
		}
		return int(b / (1024 * 1024)), true
	}
	ded, ok1 := sum("Dedicated Usage")
	sh, ok2 := sum("Shared Usage")
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return ded, sh, true
}
