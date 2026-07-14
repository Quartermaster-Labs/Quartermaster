//go:build windows

package perf

import (
	"context"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// computeAppsPlatform lists per-process dedicated VRAM via the Windows PDH
// "GPU Process Memory" counter — a vendor-neutral source (AMD/Intel/NVIDIA)
// used when nvidia-smi is absent, so foreign-VRAM detection works on non-NVIDIA
// GPUs. Best-effort: returns nil if PDH or the counter is unavailable.
func computeAppsPlatform(ctx context.Context) []GpuProc {
	q, err := initPdhProcMem()
	if err != nil {
		return nil
	}
	defer q.close()

	byPid := map[int]int{} // pid -> dedicated VRAM MB (summed across LUIDs/engines)
	for _, it := range q.collectRaw() {
		pid, ok := parsePdhPid(it.Name)
		if !ok || it.Val <= 0 {
			continue
		}
		byPid[pid] += int(it.Val / (1024 * 1024))
	}
	if len(byPid) == 0 {
		return nil
	}

	out := make([]GpuProc, 0, len(byPid))
	for pid, mb := range byPid {
		if mb <= 0 {
			continue
		}
		out = append(out, GpuProc{PID: pid, Name: procName(ctx, pid), MemMB: mb})
	}
	return out
}

// parsePdhPid extracts the pid from a "GPU Process Memory" instance name, e.g.
// "pid_1234_luid_0x00000000_0x0000C69E_phys_0".
func parsePdhPid(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "pid_")
	if !ok {
		return 0, false
	}
	i := strings.IndexByte(rest, '_')
	if i < 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(rest[:i])
	if err != nil {
		return 0, false
	}
	return pid, true
}

// procName resolves a pid to its executable name; "" if the process is gone or
// inaccessible (foreignGPU filters by name, so an unknown name is simply skipped).
func procName(ctx context.Context, pid int) string {
	p, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return ""
	}
	name, err := p.NameWithContext(ctx)
	if err != nil {
		return ""
	}
	return name
}
