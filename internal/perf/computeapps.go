package perf

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// GpuProc is one process holding GPU memory, as reported by
// nvidia-smi --query-compute-apps. MemMB is its used VRAM in MiB.
type GpuProc struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	MemMB int    `json:"mem_mb"`
}

// QueryComputeApps lists the processes currently using GPU memory via
// nvidia-smi. Returns nil (no error) when nvidia-smi is absent (non-NVIDIA /
// darwin) — the feature degrades to "no foreign processes detected" rather
// than failing. Unlike the live util/power query, --query-compute-apps reads
// NVML's process accounting, not hardware perf counters, so it does not stall
// an in-flight llama.cpp generation.
func QueryComputeApps(ctx context.Context) []GpuProc {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-compute-apps=pid,used_memory,process_name",
		"--format=csv,noheader,nounits")
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseComputeApps(string(out))
}

// parseComputeApps parses nvidia-smi CSV (noheader,nounits) rows of
// "pid, used_memory_mib, process_name". process_name may itself contain commas
// in odd cases, so the first two fields are split off and the remainder is the
// name.
func parseComputeApps(out string) []GpuProc {
	var procs []GpuProc
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		mem, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			continue // "[N/A]" on some drivers
		}
		procs = append(procs, GpuProc{PID: pid, Name: strings.TrimSpace(parts[2]), MemMB: mem})
	}
	return procs
}
