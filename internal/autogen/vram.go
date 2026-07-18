package autogen

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
)

// cudaGPU records whether the serving GPU is CUDA (NVIDIA). It gates the fixed
// CUDA-context term in computeBufferGB — that cost is a CUDA-runtime figure and
// shouldn't be charged on Vulkan/ROCm (AMD/Intel). Defaults to true (assume
// CUDA) so NVIDIA boxes and tests are unchanged until DetectGpuCompute flips it.
var cudaGPU atomic.Bool

func init() { cudaGPU.Store(true) }

// usingCudaGPU reports the detected GPU class (default true until detected).
func usingCudaGPU() bool { return cudaGPU.Load() }

// DetectGpuCompute samples the GPU once and records whether it is CUDA (NVIDIA),
// so the sizer only charges the CUDA-context overhead on a CUDA GPU. Best-effort:
// on no reading it leaves the default (assume CUDA). Call once at startup, before
// EnsureConfig, mirroring ResolveAutoVram's live-probe timing.
func DetectGpuCompute(logf func(string)) {
	ctx, cancel := context.WithTimeout(context.Background(), autoVramSampleTimeout)
	defer cancel()
	gpuCh, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil || gpuCh == nil {
		return
	}
	select {
	case stats := <-gpuCh:
		best := -1
		for i := range stats {
			if stats[i].MemTotalMB <= 0 {
				continue
			}
			if best < 0 || stats[i].MemTotalMB > stats[best].MemTotalMB {
				best = i
			}
		}
		if best < 0 {
			return
		}
		isCuda := strings.Contains(strings.ToLower(stats[best].Name), "nvidia")
		cudaGPU.Store(isCuda)
		if logf != nil {
			logf(fmt.Sprintf("gpu compute: %q -> cuda=%v (CUDA-context overhead %s)",
				stats[best].Name, isCuda, map[bool]string{true: "charged", false: "skipped"}[isCuda]))
		}
	case <-ctx.Done():
	}
}

// SampleFreeVramGB takes a single GPU telemetry snapshot and returns the free
// VRAM (in GB) of the adapter with the most total memory — the project's
// single-GPU assumption. ok is false when no GPU telemetry is available within
// timeout (no monitoring backend, headless box, driver unavailable), in which
// case callers should fall back to the static targetVramGB.
func SampleFreeVramGB(timeout time.Duration) (gb float64, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Discard logger: this is a one-shot probe, not the live monitor.
	gpuCh, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil || gpuCh == nil {
		return 0, false
	}
	select {
	case stats := <-gpuCh:
		return freeVramGBFromStats(stats)
	case <-ctx.Done():
		return 0, false
	}
}

// SampleTotalVramGB takes a single GPU telemetry snapshot and returns the TOTAL
// VRAM (in GB) of the adapter with the most total memory. Used by SAM placement
// to compare the physical card against the primary-model budget. ok is false when
// no GPU telemetry is available (headless/test), so callers fail open.
func SampleTotalVramGB(timeout time.Duration) (gb float64, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	gpuCh, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil || gpuCh == nil {
		return 0, false
	}
	select {
	case stats := <-gpuCh:
		return totalVramGBFromStats(stats)
	case <-ctx.Done():
		return 0, false
	}
}

// totalVramGBFromStats picks the adapter with the largest total memory and
// returns its total VRAM in GB. Split out for unit testing without a real GPU.
func totalVramGBFromStats(stats []perf.GpuStat) (float64, bool) {
	best := -1
	for i := range stats {
		if stats[i].MemTotalMB <= 0 {
			continue
		}
		if best < 0 || stats[i].MemTotalMB > stats[best].MemTotalMB {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return float64(stats[best].MemTotalMB) / 1024.0, true
}

// autoVramSampleTimeout bounds the one-shot probe; some backends (nvidia-smi)
// need a tick before the first sample lands.
const autoVramSampleTimeout = 8 * time.Second

// autoVramFloorGB is the smallest usable target we'll accept from a live
// reading; below it we keep the static configured value instead.
const autoVramFloorGB = 1.0

// ResolveAutoVram replaces s.TargetVramGB with the live free-VRAM budget when
// s.AutoVram is set, so a preview (EstimatePlan) sizes against the same budget
// EnsureConfig bakes into the config. No-op when AutoVram is off.
func ResolveAutoVram(s *Settings, logf func(string)) {
	if !s.AutoVram {
		return
	}
	resolveAutoVram(s, logf)
}

// resolveAutoVram replaces s.TargetVramGB with the live free VRAM (minus
// s.VramOverheadGB) when a GPU reading is available and sane. On any failure it
// leaves the configured TargetVramGB untouched.
func resolveAutoVram(s *Settings, logf func(string)) {
	freeGB, ok := SampleFreeVramGB(autoVramSampleTimeout)
	if !ok {
		if logf != nil {
			logf(fmt.Sprintf("autoVram: no GPU reading; using static targetVramGB=%g", s.TargetVramGB))
		}
		return
	}
	usable := freeGB - s.VramOverheadGB
	if usable < autoVramFloorGB {
		if logf != nil {
			logf(fmt.Sprintf("autoVram: free=%.2fGB - overhead=%.2fGB = %.2fGB below floor %.1fGB; keeping static targetVramGB=%g",
				freeGB, s.VramOverheadGB, usable, autoVramFloorGB, s.TargetVramGB))
		}
		return
	}
	if logf != nil {
		logf(fmt.Sprintf("autoVram: free=%.2fGB - overhead=%.2fGB -> targetVramGB=%.2f (was %g)",
			freeGB, s.VramOverheadGB, usable, s.TargetVramGB))
	}
	s.TargetVramGB = usable
}

// freeVramGBFromStats picks the adapter with the largest total memory and
// returns its free VRAM in GB. Split out for unit testing without a real GPU.
func freeVramGBFromStats(stats []perf.GpuStat) (float64, bool) {
	best := -1
	for i := range stats {
		if stats[i].MemTotalMB <= 0 {
			continue
		}
		if best < 0 || stats[i].MemTotalMB > stats[best].MemTotalMB {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	free := stats[best].MemTotalMB - stats[best].MemUsedMB
	if free < 0 {
		free = 0
	}
	return float64(free) / 1024.0, true
}
