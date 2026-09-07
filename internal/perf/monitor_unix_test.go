//go:build unix && !darwin

package perf

import (
	"strings"
	"testing"
)

// nvidia-smi --loop prints one line per adapter per pass and marks no pass
// boundary. scanNvidiaSmi has to hand back whole passes: a reader that sent one
// slice per line made every snapshot single-device, and the one-shot probes
// (SampleFreeVramGB, SampleGpuSet) take a single receive, so a two-card box was
// budgeted against whichever card was listed first (issue #4).
func TestScanNvidiaSmi_GroupsWholePasses(t *testing.T) {
	out := strings.Join([]string{
		"0, NVIDIA GeForce RTX 3060, GPU-0, 41, 0, 100, 12288, 30, 22.5",
		"1, NVIDIA GeForce RTX 4070 Ti SUPER, GPU-1, 44, 0, 200, 16376, 30, 25.0",
		"0, NVIDIA GeForce RTX 3060, GPU-0, 42, 0, 110, 12288, 30, 23.0",
		"1, NVIDIA GeForce RTX 4070 Ti SUPER, GPU-1, 45, 0, 210, 16376, 30, 25.5",
	}, "\n")

	var passes [][]GpuStat
	scanNvidiaSmi(strings.NewReader(out), func(s []GpuStat) { passes = append(passes, s) })

	if len(passes) != 2 {
		t.Fatalf("got %d passes, want 2 (one per --loop iteration)", len(passes))
	}
	for i, p := range passes {
		if len(p) != 2 {
			t.Fatalf("pass %d has %d devices, want both cards in one snapshot", i, len(p))
		}
		if p[0].ID != 0 || p[1].ID != 1 {
			t.Fatalf("pass %d device ids = %d,%d, want 0,1 in index order", i, p[0].ID, p[1].ID)
		}
		if p[0].MemTotalMB != 12288 || p[1].MemTotalMB != 16376 {
			t.Fatalf("pass %d totals = %d,%d, want the two cards' own memory",
				i, p[0].MemTotalMB, p[1].MemTotalMB)
		}
	}
	// The second pass must be the fresher one, not a repeat of the first.
	if passes[1][0].MemUsedMB != 110 {
		t.Fatalf("second pass used = %d, want the newer reading 110", passes[1][0].MemUsedMB)
	}
}

// A single-GPU box still reports every pass, and a bounded run (no --loop, so
// no successor line to close the last pass) must not swallow its only snapshot.
func TestScanNvidiaSmi_SingleDeviceAndTrailingPass(t *testing.T) {
	out := "0, NVIDIA GeForce RTX 3060, GPU-0, 41, 0, 100, 12288, 30, 22.5\n" +
		"0, NVIDIA GeForce RTX 3060, GPU-0, 42, 0, 120, 12288, 30, 23.0\n"

	var passes [][]GpuStat
	scanNvidiaSmi(strings.NewReader(out), func(s []GpuStat) { passes = append(passes, s) })

	if len(passes) != 2 {
		t.Fatalf("got %d passes, want 2", len(passes))
	}
	for i, p := range passes {
		if len(p) != 1 {
			t.Fatalf("pass %d has %d devices, want 1", i, len(p))
		}
	}

	passes = nil
	scanNvidiaSmi(strings.NewReader("0, NVIDIA GeForce RTX 3060, GPU-0, 41, 0, 100, 12288, 30, 22.5\n"),
		func(s []GpuStat) { passes = append(passes, s) })
	if len(passes) != 1 || len(passes[0]) != 1 {
		t.Fatalf("one-shot run produced %v, want a single one-device pass", passes)
	}
}

// Blank and unparseable lines are skipped without splitting the pass they sit in.
func TestScanNvidiaSmi_IgnoresJunkLines(t *testing.T) {
	out := strings.Join([]string{
		"",
		"0, NVIDIA GeForce RTX 3060, GPU-0, 41, 0, 100, 12288, 30, 22.5",
		"nvidia-smi: some warning on stdout",
		"1, NVIDIA GeForce RTX 4070 Ti SUPER, GPU-1, 44, 0, 200, 16376, 30, 25.0",
	}, "\n")

	var passes [][]GpuStat
	scanNvidiaSmi(strings.NewReader(out), func(s []GpuStat) { passes = append(passes, s) })

	if len(passes) != 1 || len(passes[0]) != 2 {
		t.Fatalf("got %v, want one pass holding both cards", passes)
	}
}
