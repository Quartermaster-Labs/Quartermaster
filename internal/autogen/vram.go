package autogen

import (
	"context"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
)

// cudaGPU records whether the serving GPU is CUDA (NVIDIA). It picks which
// fixed per-process runtime constant runtimeCtxGB charges: computeCudaCtxGB for
// the CUDA runtime + cuBLAS workspace, or one of the non-CUDA constants, which
// measured LARGER, not zero. Defaults to true (assume CUDA) so NVIDIA boxes and
// tests are unchanged until DetectGpuCompute flips it.
var cudaGPU atomic.Bool

// rocmBackend records whether the llama backend BINARY is a ROCm/HIP build,
// which is a different question from cudaGPU's: the same AMD card runs either a
// Vulkan or a ROCm llama-server, and the two reserve 0.4 GB and 0.8 GB of
// per-process VRAM respectively (see computeRocmCtxGB). The GPU vendor cannot
// tell them apart, so this is set from the backend path by NoteBackendRuntime
// rather than from telemetry.
//
// Deliberately SEPARATE state rather than a three-way enum: DetectGpuCompute
// (startup telemetry) and NoteBackendRuntime (config load) run in either order
// depending on the entry point, and one flat value would let whichever ran last
// clobber the other's answer.
var rocmBackend atomic.Bool

func init() { cudaGPU.Store(true) }

// usingCudaGPU reports the detected GPU class (default true until detected).
func usingCudaGPU() bool { return cudaGPU.Load() }

// usingRocmBackend reports whether the resolved llama backend is a ROCm/HIP
// build (default false => Vulkan, the cheaper non-CUDA constant).
func usingRocmBackend() bool { return rocmBackend.Load() }

// rocmPathMarkers are the substrings a ROCm/HIP llama.cpp build carries in its
// install path. The in-app installer names its directories after the catalog
// variant id ("...-rocm/b1328-llama-windows-rocm-gfx110x"), and a hand-built
// tree almost always says so too ("build-hip"). "hip" is matched only as a
// delimited token, never bare: it is a substring of ordinary words ("ships",
// "chipset") and a false positive here charges every model 0.4 GB it does not use.
var rocmPathMarkers = []string{"rocm", "hipblas", "-hip", "_hip", "/hip", ".hip"}

// vulkanPathMarkers / cudaPathMarkers mark a path as KNOWN non-ROCm, so pointing
// the registry from a ROCm build at a Vulkan one clears the flag instead of
// leaving it latched on from the previous load.
var vulkanPathMarkers = []string{"vulkan", "kompute"}
var cudaPathMarkers = []string{"cuda", "cublas"}

// classifyBackendRuntime reads a backend executable path for the compute runtime
// it was built against. known is false when the path says nothing either way, in
// which case the caller must leave the current verdict alone: a bare
// "llama-server.exe" is not evidence of Vulkan.
func classifyBackendRuntime(exe string) (rocm, known bool) {
	p := strings.ToLower(filepath.ToSlash(strings.TrimSpace(exe)))
	if p == "" {
		return false, false
	}
	for _, m := range rocmPathMarkers {
		if strings.Contains(p, m) {
			return true, true
		}
	}
	for _, m := range append(append([]string{}, vulkanPathMarkers...), cudaPathMarkers...) {
		if strings.Contains(p, m) {
			return false, true
		}
	}
	return false, false
}

// NoteBackendRuntime records the compute runtime of the resolved llama backend
// so runtimeCtxGB can charge the right per-process constant. Called from
// LoadGenerateFile, the one choke point every sizing and estimate path goes
// through, so the emitter and the editor's preview always agree.
//
// Best-effort and idempotent: an unrecognised path leaves the previous verdict
// standing, so a box that resolves its backend through some route this cannot
// read keeps whatever DetectGpuCompute's class implies rather than flipping to a
// wrong constant. Only the DEFAULT llama exe is read; a per-model backend
// override pointing at a different flavour is not modelled (it would need the
// constant threaded per profile, and no such mixed install has been seen).
func NoteBackendRuntime(exe string) {
	if rocm, known := classifyBackendRuntime(exe); known {
		rocmBackend.Store(rocm)
	}
}

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
// VRAM (in GB) of the eligible device set — the project's single-GPU assumption
// on a box with one adapter, pooled across them otherwise. ok is false when no
// GPU telemetry is available within timeout (no monitoring backend, headless
// box, driver unavailable), in which case callers should fall back to the static
// targetVramGB.
//
// policy decides which devices count and whether an integrated GPU's shared
// system memory is part of the figure; a caller without settings should pass
// currentProbePolicy().
func SampleFreeVramGB(timeout time.Duration, policy GpuPolicy) (gb float64, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Discard logger: this is a one-shot probe, not the live monitor.
	gpuCh, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil || gpuCh == nil {
		return 0, false
	}
	select {
	case stats := <-gpuCh:
		return freeVramGBFromStats(stats, policy)
	case <-ctx.Done():
		return 0, false
	}
}

// SampleTotalVramGB takes a single GPU telemetry snapshot and returns the TOTAL
// VRAM (in GB) of the eligible device set. Used by SAM placement to compare the
// physical card against the primary-model budget. ok is false when no GPU
// telemetry is available (headless/test), so callers fail open.
func SampleTotalVramGB(timeout time.Duration, policy GpuPolicy) (gb float64, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	gpuCh, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil || gpuCh == nil {
		return 0, false
	}
	select {
	case stats := <-gpuCh:
		return totalVramGBFromStats(stats, policy)
	case <-ctx.Done():
		return 0, false
	}
}

// totalVramGBFromStats returns the POOLED total capacity of every eligible
// adapter. Split out for unit testing without a real GPU.
//
// Was "the largest adapter's total". Pooling is what makes the figure mean the
// same thing as the budget it is compared against (see freeVramGBFromStats);
// adapters below the eligibility floor are excluded from both, and the figure
// folds in an integrated device's shared pool under the same policy the budget
// uses — otherwise a total describing the carveout alone would read as smaller
// than a budget derived from the carveout plus GTT.
func totalVramGBFromStats(stats []perf.GpuStat, policy GpuPolicy) (float64, bool) {
	set := gpuSetFromStats(stats, policy)
	if len(set) == 0 {
		return 0, false
	}
	return set.TotalGB(), true
}

// autoVramSampleTimeout bounds the one-shot probe; some backends (nvidia-smi)
// need a tick before the first sample lands.
const autoVramSampleTimeout = 8 * time.Second

// idleFreeVramGB is the highest free-VRAM reading ever sampled, as float64 bits;
// 0 means nothing sampled yet. It stands in for "the card with none of OUR
// models resident", which is the only budget a load plan may be sized against.
//
// A raw live reading is not that budget. autoVram resolves on every EnsureConfig
// AND on every estimate preview, and both run happily while a model is loaded —
// at which point free VRAM is what the resident model left over (2.6GB of 24 on
// a 27B), so the sizer plans the next load into the scraps and shoves the weights
// to CPU. The preview showed "2.5 / 2.6 GB, GPU 2/65" for a model that was
// running fine at 21.4GB, and a settings save in that state would have baked the
// same budget into config.yaml.
//
// Taking the max is what makes it self-correcting: free VRAM peaks when nothing
// of ours is loaded, so the startup sample (EnsureConfig runs before the router
// loads anything) sets the mark and every mid-session sample is lower and
// ignored. Freeing VRAM elsewhere raises it again. The mark can go stale-high if
// the desktop grows its own usage mid-session, but that is the safe direction:
// LiveOffloadArgs re-probes live free at spawn and only ever offloads MORE than
// the baked plan, so an optimistic plan gets corrected at launch while a
// pessimistic one is permanent.
var idleFreeVramGB atomic.Uint64

// noteFreeVramGB records a fresh reading and returns the idle high-water mark.
func noteFreeVramGB(gb float64) float64 {
	bits := math.Float64bits(gb)
	for {
		cur := idleFreeVramGB.Load()
		if cur != 0 && math.Float64frombits(cur) >= gb {
			return math.Float64frombits(cur)
		}
		if idleFreeVramGB.CompareAndSwap(cur, bits) {
			return gb
		}
	}
}

// ResetIdleFreeVramGB clears the high-water mark. Tests only.
func ResetIdleFreeVramGB() { idleFreeVramGB.Store(0) }

// autoVramFloorGB is the smallest usable target we'll accept from a live
// reading; below it we keep the static configured value instead.
const autoVramFloorGB = 1.0

// ResolveAutoVram replaces s.TargetVramGB with the live free-VRAM budget when
// s.AutoVram is set, so a preview (EstimatePlan) sizes against the same budget
// EnsureConfig bakes into the config. No-op when AutoVram is off.
func ResolveAutoVram(s *Settings, logf func(string)) {
	if !s.AutoVram {
		// The device set is a hardware fact the sizer needs either way: it
		// decides the extra-device overhead and the tensor-split ratio even when
		// the BUDGET stays the static targetVramGB.
		ResolveGpuSet(s, logf)
		return
	}
	resolveAutoVram(s, logf)
}

// resolveAutoVram replaces s.TargetVramGB with the live free VRAM when a GPU
// reading is available and sane. On any failure it leaves the configured
// TargetVramGB untouched.
//
// The reading is used AS THE BUDGET, not minus vramOverheadGB. TargetVramGB is
// the ceiling a plan's total footprint must fit under, and that footprint
// (EstVramGB) already carries vramOverheadGB inside it — sizeProfile folds the
// setting into prof.Overhead. Subtracting it here too spent the headroom twice
// and made autoVram plan a further 0.5GB short of what the card had, which on a
// tight fit is a whole extra layer pushed to CPU. Cap only, never raise: the
// static TargetVramGB stays a user ceiling, so autoVram can tighten a budget the
// desktop has eaten into but can't talk a plan past a deliberate limit.
func resolveAutoVram(s *Settings, logf func(string)) {
	// Resolve the eligible device set first: it decides how many cards the
	// budget below pools, and every later sizing decision in this pass reads it
	// off the settings rather than taking a second, differently-timed sample.
	ResolveGpuSet(s, logf)
	sampledGB, ok := 0.0, false
	if len(s.Gpus) > 0 {
		sampledGB, ok = s.Gpus.FreeGB(), true
	} else {
		sampledGB, ok = SampleFreeVramGB(autoVramSampleTimeout, s.DevicePolicy())
	}
	// Budget against the idle high-water mark, never the raw sample: a sample
	// taken while one of our own models is resident describes the leftovers, not
	// what the next load may use. See idleFreeVramGB.
	freeGB := sampledGB
	if ok {
		freeGB = noteFreeVramGB(sampledGB)
		if logf != nil && freeGB > sampledGB {
			logf(fmt.Sprintf("autoVram: sampled free=%.2fGB with models resident; using idle free=%.2fGB",
				sampledGB, freeGB))
		}
	}
	if !ok {
		if logf != nil {
			logf(fmt.Sprintf("autoVram: no GPU reading; using static targetVramGB=%g", s.TargetVramGB))
		}
		return
	}
	if freeGB < autoVramFloorGB {
		if logf != nil {
			logf(fmt.Sprintf("autoVram: free=%.2fGB below floor %.1fGB; keeping static targetVramGB=%g",
				freeGB, autoVramFloorGB, s.TargetVramGB))
		}
		return
	}
	if s.TargetVramGB > 0 && s.TargetVramGB <= freeGB {
		if logf != nil {
			logf(fmt.Sprintf("autoVram: free=%.2fGB; keeping tighter static targetVramGB=%g", freeGB, s.TargetVramGB))
		}
		return
	}
	if logf != nil {
		logf(fmt.Sprintf("autoVram: free=%.2fGB -> targetVramGB=%.2f (was %g)", freeGB, freeGB, s.TargetVramGB))
	}
	s.TargetVramGB = freeGB
}

// freeVramGBFromStats returns the POOLED free VRAM of every eligible adapter.
// Split out for unit testing without a real GPU.
//
// Was "the largest adapter's free VRAM", which on a two-card box left the
// second card's memory invisible to every budget in the program (issue #4).
// Pooling is only sound because the fixed per-device costs are charged as
// overhead and the emitted --tensor-split matches the same per-device
// remainders: see the header comment in gpuset.go. A caller that has turned
// multiGpu off never reaches here with more than one device: ResolveGpuSet pins
// the set to the main card first. On a box whose only GPU is an APU the figure
// includes the shared system-memory pool the device allocates from, folded in by
// gpuSetFromStats under the same policy the budget uses (issue #37).
func freeVramGBFromStats(stats []perf.GpuStat, policy GpuPolicy) (float64, bool) {
	set := gpuSetFromStats(stats, policy)
	if len(set) == 0 {
		return 0, false
	}
	return set.FreeGB(), true
}
