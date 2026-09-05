package autogen

// gpuset.go is the multi-GPU half of the VRAM budget: the set of adapters a
// plan may actually use, instead of the single largest card every other budget
// path used to pick.
//
// The old rule (freeVramGBFromStats, largestGPU) was not a simplification, it
// was a hard assumption: on a two-card box the second card's VRAM was invisible
// to the sizer AND absent from the emitted argv, so llama.cpp got no
// --tensor-split and did whatever its own default was with memory quartermaster
// had never counted. See issue #4.
//
// The naive fix (pool the cards into one number) is worse than the bug. With
// `-sm layer` llama.cpp splits LAYERS and their KV by the tensor-split ratio,
// but the fixed costs do not split: the logits/output buffer and the runtime
// context land on --main-gpu alone. Pool 12+16 into "28 GB", plan a 26 GB
// footprint, and the 12 GB card OOMs while the sizer reports a comfortable fit.
//
// The rule that makes a scalar budget correct again:
//
//	splittable_i <= FreeGB_i - fixed_i        (per device)
//	ratio_i       = (FreeGB_i - fixed_i) / sum_j(FreeGB_j - fixed_j)
//
// With that ratio every per-device constraint binds at the same moment, so the
// pooled budget sum(FreeGB) is exactly reachable as long as the fixed costs are
// charged as overhead, which is what the existing scalar sizer already does
// with prof.Overhead. So multi-GPU sizing needs no vector solver: hand the
// sizer the summed budget, add the extra devices' fixed cost to Overhead, and
// derive the split from the same numbers. TensorSplit is that derivation.

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
)

// GpuDevice is one inference-eligible adapter.
//
// Index is the adapter's telemetry ordinal (nvidia-smi / DXGI order), which is
// what the emitted --main-gpu and --tensor-split positions mean. It is NOT
// automatically the ordinal a CUDA build addresses: the CUDA runtime defaults
// to CUDA_DEVICE_ORDER=FASTEST_FIRST while nvidia-smi enumerates by PCI bus id,
// so on a mismatched pair (a 3060 beside a 4070 Ti SUPER) the two orders
// disagree and a split derived from one applied in the other silently hands the
// small card the big share. cudaOrderEnv is emitted alongside the flags to pin
// the runtime to bus order; do not emit the flags without it.
type GpuDevice struct {
	Index   int
	Name    string
	TotalGB float64
	FreeGB  float64
}

// GpuSet is the eligible adapters, ordered by Index.
type GpuSet []GpuDevice

// FreeGB is the pooled free VRAM across the set.
func (g GpuSet) FreeGB() float64 {
	var sum float64
	for _, d := range g {
		sum += d.FreeGB
	}
	return sum
}

// TotalGB is the pooled physical VRAM across the set.
func (g GpuSet) TotalGB() float64 {
	var sum float64
	for _, d := range g {
		sum += d.TotalGB
	}
	return sum
}

// Multi reports whether this set is worth splitting across at all.
func (g GpuSet) Multi() bool { return len(g) > 1 }

// MainIndex is the device that carries the non-splittable costs: the logits /
// output buffer, the runtime context, and (in llama.cpp) the KV of any layer it
// keeps. The card with the most FREE memory, not the most total: the fixed
// costs are what a busy card has least room for, and on a desktop the card
// driving the displays is routinely the larger one.
//
// Returns -1 for an empty set.
func (g GpuSet) MainIndex() int {
	best := -1
	var bestFree float64
	for _, d := range g {
		if best < 0 || d.FreeGB > bestFree {
			best, bestFree = d.Index, d.FreeGB
		}
	}
	return best
}

// perDeviceFixedGB is the runtime context each ADDITIONAL device costs. It
// mirrors computeBufferGB's treatment of the main device: the constant is a
// CUDA-runtime figure, so it is charged only on CUDA, and a Vulkan/ROCm
// multi-GPU box gets 0 here for the same reason it gets 0 there.
//
// ponytail: one constant for every extra device. A per-backend figure belongs
// here if a non-CUDA multi-GPU build proves to cost something measurable.
func perDeviceFixedGB() float64 {
	if usingCudaGPU() {
		return computeCudaCtxGB
	}
	return 0
}

// ExtraDeviceOverheadGB is what the SIZER must add to a profile's overhead
// before budgeting against FreeGB(): every device past the main one pays its
// own runtime context, and none of it is splittable.
func (g GpuSet) ExtraDeviceOverheadGB() float64 {
	if len(g) < 2 {
		return 0
	}
	return float64(len(g)-1) * perDeviceFixedGB()
}

// TensorSplit is the --tensor-split ratio, one entry per device in Index order.
//
// mainFixedGB is the whole non-splittable footprint the main device carries
// (prof.Overhead, which by this point already folds in the compute buffer, the
// spec/draft overhead and the projector). Each other device is charged
// perDeviceFixedGB. The remainder is what layers and KV may occupy, and the
// ratio is that remainder normalised, which is precisely the ratio under which
// the pooled budget is reachable without any single card going over.
//
// A device with no room left after its fixed cost gets 0, and llama.cpp will
// place nothing on it. Returns nil for a set that isn't worth splitting or when
// no device has room, so the caller emits no flags and the single-GPU path
// stands.
func (g GpuSet) TensorSplit(mainFixedGB float64) []float64 {
	if len(g) < 2 {
		return nil
	}
	main := g.MainIndex()
	rem := make([]float64, len(g))
	var sum float64
	for i, d := range g {
		fixed := perDeviceFixedGB()
		if d.Index == main {
			fixed = mainFixedGB
		}
		r := d.FreeGB - fixed
		if r < 0 {
			r = 0
		}
		rem[i] = r
		sum += r
	}
	if sum <= 0 {
		return nil
	}
	out := make([]float64, len(g))
	for i, r := range rem {
		// Two decimals: llama.cpp normalises the vector itself, and a long
		// mantissa in the config buys nothing but an unreadable command line.
		out[i] = math.Round(r/sum*100) / 100
	}
	return out
}

// FormatSplit renders a ratio vector as llama.cpp's comma-separated argument.
func FormatSplit(split []float64) string {
	parts := make([]string, len(split))
	for i, v := range split {
		parts[i] = strconv.FormatFloat(v, 'g', -1, 64)
	}
	return strings.Join(parts, ",")
}

// cudaOrderEnv pins the CUDA runtime to PCI-bus enumeration so a device ordinal
// means the same thing to the sizer, to nvidia-smi and to llama.cpp. Without it
// the runtime's FASTEST_FIRST default can reverse the pair and apply the split
// backwards. Harmless on a Vulkan/ROCm build, which ignores it.
const cudaOrderEnv = "CUDA_DEVICE_ORDER=PCI_BUS_ID"

// minInferenceVramGB is the default eligibility floor. An iGPU reports a small
// slice of system memory as dedicated VRAM, and pooling that into the budget
// invents memory the sizer will then plan a model into, while a split that
// hands real layers to an iGPU is slower than not splitting at all. Settings
// override it via MinGpuVramGB.
const minInferenceVramGB = 3.0

// idleFreeByDevice is the per-device twin of idleFreeVramGB: the highest free
// reading ever seen for each device index. Same reason, per card: a sample
// taken while one of our own models is resident describes that model's
// leftovers, and using it would size the next plan into the scraps AND skew the
// split toward whichever card happened to be empty.
var (
	idleFreeMu      sync.Mutex
	idleFreeByDev   = map[int]float64{}
	lastResolvedSet GpuSet
	lastResolvedAt  time.Time
)

// gpuSetCacheTTL is how long a resolved set is reused instead of re-probed.
// ResolveGpuSet is now on the estimate-preview path, which the config editor
// calls on every edit, and a cold nvidia-smi probe is seconds. Caching costs
// nothing in accuracy: the per-device figures are idle HIGH-WATER marks, which
// only ever rise, and the spawn guard re-reads live VRAM anyway.
const gpuSetCacheTTL = 60 * time.Second

// noteDeviceFreeGB records a reading and returns the device's idle high-water mark.
func noteDeviceFreeGB(index int, gb float64) float64 {
	idleFreeMu.Lock()
	defer idleFreeMu.Unlock()
	if cur, ok := idleFreeByDev[index]; ok && cur >= gb {
		return cur
	}
	idleFreeByDev[index] = gb
	return gb
}

// ResetIdleFreeByDevice clears the per-device high-water marks. Tests only.
func ResetIdleFreeByDevice() {
	idleFreeMu.Lock()
	defer idleFreeMu.Unlock()
	idleFreeByDev = map[int]float64{}
	lastResolvedSet = nil
	lastResolvedAt = time.Time{}
}

// gpuSetFromStats builds the eligible set from a telemetry sample: newest
// reading per device id, adapters below minTotalGB dropped, ordered by index.
// Split out for unit testing without a real GPU.
func gpuSetFromStats(stats []perf.GpuStat, minTotalGB float64) GpuSet {
	if minTotalGB <= 0 {
		minTotalGB = minInferenceVramGB
	}
	// The server hands us a sample HISTORY, the one-shot probe hands us a single
	// sample; keeping the newest per id is correct for both.
	latest := make(map[int]perf.GpuStat, len(stats))
	for _, g := range stats {
		if prev, seen := latest[g.ID]; !seen || g.Timestamp.After(prev.Timestamp) {
			latest[g.ID] = g
		}
	}
	var out GpuSet
	for _, g := range latest {
		if g.MemTotalMB <= 0 {
			continue
		}
		totalGB := float64(g.MemTotalMB) / 1024.0
		if totalGB < minTotalGB {
			continue
		}
		free := g.MemTotalMB - g.MemUsedMB
		if free < 0 {
			free = 0
		}
		out = append(out, GpuDevice{
			Index:   g.ID,
			Name:    g.Name,
			TotalGB: totalGB,
			FreeGB:  float64(free) / 1024.0,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// SampleGpuSet takes one telemetry snapshot and returns the eligible devices.
// ok is false when no GPU telemetry is available within timeout, and the caller
// keeps whatever static budget it had.
func SampleGpuSet(timeout time.Duration, minTotalGB float64) (GpuSet, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	gpuCh, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil || gpuCh == nil {
		return nil, false
	}
	select {
	case stats := <-gpuCh:
		set := gpuSetFromStats(stats, minTotalGB)
		if len(set) == 0 {
			return nil, false
		}
		// Budget each card against ITS idle high-water mark, never the raw
		// sample: a probe taken while one of our own models is resident
		// describes that model's leftovers, and on a split plan it would also
		// skew the ratio toward whichever card happened to be empty.
		for i := range set {
			set[i].FreeGB = noteDeviceFreeGB(set[i].Index, set[i].FreeGB)
		}
		idleFreeMu.Lock()
		lastResolvedSet = set
		lastResolvedAt = time.Now()
		idleFreeMu.Unlock()
		return set, true
	case <-ctx.Done():
		return nil, false
	}
}

// LastGpuSet is the most recently resolved set, for callers on a path that must
// not re-probe (the spawn guard runs per model load). nil when nothing has been
// sampled yet, which every consumer treats as single-GPU.
func LastGpuSet() GpuSet {
	idleFreeMu.Lock()
	defer idleFreeMu.Unlock()
	return lastResolvedSet
}

// ResolveGpuSet samples the host's adapters and stores the eligible set on the
// settings, so every downstream sizing decision in this generate pass talks
// about the same cards with the same numbers. No-op when the user has turned
// multi-GPU off, which pins the set to the single main device and reproduces
// the pre-multi-GPU behaviour exactly.
func ResolveGpuSet(s *Settings, logf func(string)) {
	idleFreeMu.Lock()
	cached := lastResolvedSet
	fresh := len(cached) > 0 && time.Since(lastResolvedAt) < gpuSetCacheTTL
	idleFreeMu.Unlock()

	set, ok := cached, fresh
	if !ok {
		set, ok = SampleGpuSet(autoVramSampleTimeout, s.MinGpuVramGB)
	}
	if !ok {
		return
	}
	if !s.MultiGpuEnabled() && len(set) > 1 {
		main := set.MainIndex()
		for _, d := range set {
			if d.Index == main {
				set = GpuSet{d}
				break
			}
		}
	}
	s.Gpus = set
	if logf == nil || len(set) < 2 {
		return
	}
	names := make([]string, len(set))
	for i, d := range set {
		names[i] = fmt.Sprintf("%d:%s %.1f/%.1fGB free", d.Index, d.Name, d.FreeGB, d.TotalGB)
	}
	logf(fmt.Sprintf("multi-gpu: %d devices [%s] -> pooled budget %.2fGB, main gpu %d",
		len(set), strings.Join(names, ", "), set.FreeGB(), set.MainIndex()))
}

// EligibleGpuStats is the exported eligibility rule, for callers outside this
// package that hold a raw perf sample history and must describe the SAME cards
// the sizer planned against: newest sample per device id, adapters under the
// inference floor dropped, and (when multi is false) everything but the device
// --main-gpu would pick removed.
//
// The server's OOM guard and idle-VRAM tracker call it. Before issue #4 they
// each open-coded "largest adapter", which is how a two-card box got a ceiling
// describing one card while the models were sized for both.
func EligibleGpuStats(stats []perf.GpuStat, multi bool) []perf.GpuStat {
	set := gpuSetFromStats(stats, minInferenceVramGB)
	if len(set) == 0 {
		return nil
	}
	keep := make(map[int]bool, len(set))
	if multi {
		for _, d := range set {
			keep[d.Index] = true
		}
	} else {
		// Same pick as the split's main device, so a multiGpu:false install
		// budgets, guards and loads on one and the same card. It is chosen by
		// free memory, so a foreign app filling the big card can move it; that
		// is deliberate, the alternative is guarding a card we would not load on.
		keep[set.MainIndex()] = true
	}
	latest := make(map[int]perf.GpuStat, len(stats))
	for _, g := range stats {
		if !keep[g.ID] {
			continue
		}
		if prev, seen := latest[g.ID]; !seen || g.Timestamp.After(prev.Timestamp) {
			latest[g.ID] = g
		}
	}
	out := make([]perf.GpuStat, 0, len(latest))
	for _, g := range latest {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LiveGpuSet builds a device set from a raw perf sample, WITHOUT the idle
// high-water smoothing SampleGpuSet applies. The two answer different questions:
// the sizer plans against an IDLE budget (what the box could give a model), the
// spawn guard needs the truth (what each card has left right now, with whatever
// is already resident on it counted).
//
// That distinction is what makes a stale --tensor-split dangerous: a ratio built
// from idle capacity keeps sending a third of the layers to a card another model
// is already sitting on. See retuneTensorSplit.
func LiveGpuSet(stats []perf.GpuStat, multi bool) GpuSet {
	return gpuSetFromStats(EligibleGpuStats(stats, multi), minInferenceVramGB)
}

// writeSingleDeviceEnv emits the env block a SINGLE-DEVICE backend needs on a
// multi-GPU box. sd-server, tts-server and whisper have no split of their own:
// left alone the CUDA runtime hands them whatever it calls device 0, which under
// its FASTEST_FIRST default is not necessarily the card the sizer budgeted, and
// not necessarily the same card twice. Pin them to the same main device the LLM
// split pins to, enumerated by PCI bus so the ordinal means here what it means
// in nvidia-smi.
//
// A no-op on a single-GPU box (nothing to disambiguate), on a non-CUDA GPU
// (these variables mean nothing to Vulkan or ROCm), and when the device set was
// never resolved.
func writeSingleDeviceEnv(b *strings.Builder, s Settings) {
	if !usingCudaGPU() {
		return
	}
	set := s.GpuSetOrEmpty()
	if !set.Multi() {
		return
	}
	fmt.Fprintf(b, "    env:\n      - %q\n      - %q\n",
		cudaOrderEnv, fmt.Sprintf("CUDA_VISIBLE_DEVICES=%d", set.MainIndex()))
}
