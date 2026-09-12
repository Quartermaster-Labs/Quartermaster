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
//
// FreeGB_i means two different things depending on who is asking, which is why
// the derivation comes in a pair. The spawn-time retune wants the live reading,
// occupancy included, so it stops sending layers to a card another model is
// sitting on. A config being GENERATED wants stable capacity instead: it is a
// long-lived artifact planned off a single cold sample, and a card that happens
// to be busy for that one sample must not bake a plan with no --tensor-split in
// it, because spawn time can retune a ratio but can never add one. Plan* are
// the generate-time twins; see splitPlanIdleFrac.

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// Integrated marks a device that allocates out of SYSTEM memory — an APU's
	// GPU — as opposed to a card with its own VRAM. Topology, not policy: it is
	// set even when the shared pool is not counted (sharedMemory: off), because
	// the rule it feeds is about which devices may share a split.
	Integrated bool
	// SharedTotalGB is the system-memory pool the device can address as reported
	// by the platform (0 when it reports none). SharedFreeGB is how much of that
	// pool is COUNTED in FreeGB: zero both for a discrete card's host aperture
	// and for an integrated device whose pool the settings excluded. The two
	// differ exactly when shared memory was reported but not budgeted, which is
	// what makes "%.1fGB of this budget is really RAM" answerable.
	SharedTotalGB float64
	SharedFreeGB  float64
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
//
// This is the LIVE pick, for the spawn-time retune. A generate-time plan wants
// PlanMainIndex, which applies the same rule to the planning capacity.
func (g GpuSet) MainIndex() int { return g.mainIndexBy(GpuDevice.liveCapacityGB) }

// PlanMainIndex is MainIndex for a config being generated: the device with the
// most PLANNING capacity. A cold generate takes a single telemetry sample, and
// the card that happens to be busy for that one moment must not hand the fixed
// costs to the smaller card for the life of the config.
func (g GpuSet) PlanMainIndex() int { return g.mainIndexBy(GpuDevice.planCapacityGB) }

func (g GpuSet) mainIndexBy(capOf func(GpuDevice) float64) int {
	best := -1
	var bestCap float64
	for _, d := range g {
		c := capOf(d)
		if best < 0 || c > bestCap {
			best, bestCap = d.Index, c
		}
	}
	return best
}

// splitPlanIdleFrac is the share of a card's own VRAM a GENERATE-time plan
// assumes it can reach, even when the card reads busy at the moment we sample.
//
// The idle high-water FreeGB is the right number once this process has watched
// a card sit idle, but a config is generated at startup off a single sample, so
// on a cold run the high-water IS that sample. A card occupied just then would
// bake a plan that places nothing on it, and that bake is long-lived: the config
// is regenerated only when the inputs hash changes, while the occupancy that
// produced the reading is momentary.
//
// The recovery at spawn time is one-sided. dynoffload can lower -ngl and raise
// --n-cpu-moe, and retuneTensorSplit can re-derive an existing ratio from the
// live per-device reading, but nothing can ADD a --tensor-split to an argv that
// has none: an optimistic bake is recoverable and a pessimistic one is not. So
// the plan floors each device at the share of its own VRAM a card in ordinary
// use (a desktop, a driver, someone else's app) still leaves free, and lets the
// spawn-time retune deal with whatever is actually resident.
const splitPlanIdleFrac = 0.85

// planCapacityGB is the capacity a generate-time plan budgets this device at:
// its idle high-water free reading, floored at splitPlanIdleFrac of its own
// VRAM. A floor, never a cap, so a card genuinely watched idle keeps its
// measured figure and a card sampled mid-load is not written off.
func (d GpuDevice) planCapacityGB() float64 {
	floor := d.TotalGB * splitPlanIdleFrac
	if d.FreeGB > floor {
		return d.FreeGB
	}
	return floor
}

// liveCapacityGB is the capacity a spawn-time retune plans against: what the
// card reports free right now, with whatever is resident on it counted.
func (d GpuDevice) liveCapacityGB() float64 { return d.FreeGB }

// perDeviceFixedGB is the runtime context each ADDITIONAL device costs. It is
// the same per-device constant computeBufferGB charges the main device, so it
// tracks whichever backend is in use (runtimeCtxGB).
//
// It used to return 0 on anything but CUDA, on the reasoning that the constant
// was a CUDA-runtime figure that would not transfer. Measurement said otherwise:
// the Vulkan/ROCm runtime reserves MORE than CUDA does, not nothing, so a
// non-CUDA two-card box was silently handed a whole extra device's runtime as
// free budget. The main-device charge was corrected in #24; this is the same
// constant seen from the extra devices' side.
func perDeviceFixedGB() float64 { return runtimeCtxGB() }

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
// mainFixedGB is the non-splittable footprint the MAIN device carries: the
// compute buffer, the runtime context, the spec/draft overhead and the
// projector. Each other device is charged perDeviceFixedGB here, so mainFixedGB
// must NOT include ExtraDeviceOverheadGB: that term is the other devices'
// runtime seen from the pooled budget's side, and passing it in bills the same
// bytes to both cards, shifting layers off the main GPU onto one with no room.
//
// The remainder is what layers and KV may occupy, and the ratio is that
// remainder normalised, which is precisely the ratio under which the pooled
// budget is reachable without any single card going over.
//
// A device with no room left after its fixed cost gets 0, and llama.cpp will
// place nothing on it. Returns nil for a set that isn't worth splitting or when
// no device has room, so the caller emits no flags and the single-GPU path
// stands.
func (g GpuSet) TensorSplit(mainFixedGB float64) []float64 {
	if len(g) < 2 {
		return nil
	}
	return g.splitBy(GpuDevice.liveCapacityGB, g.MainIndex(), mainFixedGB)
}

// PlanTensorSplit is the ratio a GENERATED config carries: the same derivation
// over the planning capacity instead of the live reading, and, for a set worth
// splitting at all, never nil.
//
// The nil is the point. The ratio itself barely matters, because the spawn-time
// retune re-derives it from live telemetry on every load; what cannot be
// recovered is the split's EXISTENCE, since retuneTensorSplit rewrites a baked
// --tensor-split and has no way to add one. A generate pass that caught a card
// mid-load used to bake a single-device plan that the runtime then outgrew
// silently: dynoffload would raise the offload to the pooled budget while the
// argv still carried no placement instruction at all, leaving llama.cpp to
// split by its own default and put more on a card than the sizer ever counted.
// See issue #4.
func (g GpuSet) PlanTensorSplit(mainFixedGB float64) []float64 {
	if len(g) < 2 {
		return nil
	}
	if split := g.splitBy(GpuDevice.planCapacityGB, g.PlanMainIndex(), mainFixedGB); split != nil {
		return split
	}
	// Every device swallowed whole by its own fixed cost, which means the plan
	// does not fit on this box in any arrangement. Fall back to the physical
	// ratio (llama.cpp's own default placement) rather than to no split: the
	// flags have to exist for the retune to have something to rewrite.
	total := g.TotalGB()
	if total <= 0 {
		return nil
	}
	out := make([]float64, len(g))
	for i, d := range g {
		out[i] = math.Round(d.TotalGB/total*100) / 100
	}
	return out
}

// splitBy is the shared derivation: each device's capacity less its fixed cost,
// normalised. main is the device index carrying mainFixedGB (-1 for none).
// Returns nil when no device has room left, which the two callers read
// differently.
func (g GpuSet) splitBy(capOf func(GpuDevice) float64, main int, mainFixedGB float64) []float64 {
	rem := make([]float64, len(g))
	var sum float64
	for i, d := range g {
		fixed := perDeviceFixedGB()
		if d.Index == main {
			fixed = mainFixedGB
		}
		r := capOf(d) - fixed
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

// MainLastOrder returns the positions of g in the order the devices must be
// handed to llama.cpp: every other device first, in their existing relative
// order, and the main device LAST. Returns nil when mainIndex names no device
// in g.
//
// Last is not arbitrary. Measured on a Vulkan build (llama-server -lv 10,
// -sm layer, -ts 0.5,0.5, two devices): the device listed LAST carries the
// non-splittable output weight on top of its layer share, and the surplus moves
// with the list rather than with --main-gpu. Reversing --device swapped a
// ~146MiB surplus from one card's model buffer to the other's, while
// --main-gpu 0 and --main-gpu 1 at a fixed list produced byte-identical model,
// KV and compute buffers.
//
// So the ordering IS the placement instruction, and mainFixedGB is only charged
// to the right card if that card is last. See splitBy, which prices the main
// device differently from the rest.
func (g GpuSet) MainLastOrder(mainIndex int) []int {
	pos := -1
	for i, d := range g {
		if d.Index == mainIndex {
			pos = i
			break
		}
	}
	if pos < 0 {
		return nil
	}
	out := make([]int, 0, len(g))
	for i := range g {
		if i != pos {
			out = append(out, i)
		}
	}
	return append(out, pos)
}

// PermuteSplit reorders a ratio vector to match an order from MainLastOrder, so
// --tensor-split keeps addressing the same devices as the --device list beside
// it. A mismatched length is returned unchanged: a split that cannot be mapped
// must not be silently rearranged onto the wrong cards.
func PermuteSplit(split []float64, order []int) []float64 {
	if len(order) != len(split) {
		return split
	}
	out := make([]float64, len(split))
	for i, p := range order {
		out[i] = split[p]
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

// SharedMemory modes. The system-memory pool a GPU can address is the whole
// story on an APU and a host-side aperture on a discrete card, and the telemetry
// carries no flag that tells the two apart — a 780M and a 6600 both report
// "VRAM" plus a GTT several times larger. So the user gets the last word.
const (
	// SharedMemoryAuto counts the shared pool only for a device that looks like
	// an APU: a name marker, or a dedicated pool small enough and a shared pool
	// large enough that no discrete card has that shape. Default.
	SharedMemoryAuto = "auto"
	// SharedMemoryOff never counts it: the pre-#37 behaviour, and the escape
	// hatch if auto guesses wrong on a discrete card.
	SharedMemoryOff = "off"
	// SharedMemoryOn counts it for every device that reports one.
	SharedMemoryOn = "on"
)

// GpuPolicy is the user's device-selection policy, passed to the set builders
// explicitly rather than read from a package global, so the sizer, the OOM
// guard and the spawn-time retune cannot disagree about which devices count.
// Build it from Settings with DevicePolicy.
type GpuPolicy struct {
	// MinTotalGB is the eligibility floor, applied to the device's TOTAL after
	// the shared pool (when counted) is folded in. 0 => minInferenceVramGB.
	MinTotalGB float64
	// SharedMemory is SharedMemoryAuto / Off / On.
	SharedMemory string
	// PoolIntegrated allows an integrated GPU to be a split target beside a
	// dedicated card. See Settings.PoolIntegratedGpu.
	PoolIntegrated bool
}

// minTotalGB resolves the floor, with the same default the wizard writes out.
func (p GpuPolicy) minTotalGB() float64 {
	if p.MinTotalGB <= 0 {
		return minInferenceVramGB
	}
	return p.MinTotalGB
}

// probePolicy is the device-selection policy for the one-shot probes that cannot
// take a Settings argument: the hardware-budget seed (a process-wide OnceValues)
// and vllm's per-process card probe. Stored from the settings before any of them
// run — seedHardwareBudgets and ResolveGpuSet both do it — and the zero value
// means auto with the default floor, i.e. exactly the behaviour these probes had
// before the policy existed.
var probePolicy atomic.Value // holds GpuPolicy

// setProbePolicy records the policy the one-shot probes should use.
func setProbePolicy(p GpuPolicy) { probePolicy.Store(p) }

// currentProbePolicy returns the last recorded policy.
func currentProbePolicy() GpuPolicy {
	if v := probePolicy.Load(); v != nil {
		if p, ok := v.(GpuPolicy); ok {
			return p
		}
	}
	return GpuPolicy{}
}

// latestGpuStats is the newest reading per device id, ordered by id. The server
// hands us a sample HISTORY, the one-shot probe a single sample; keeping the
// newest per id is correct for both.
func latestGpuStats(stats []perf.GpuStat) []perf.GpuStat {
	latest := make(map[int]perf.GpuStat, len(stats))
	for _, g := range stats {
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

// gpuSetFromStats builds the eligible set from a telemetry sample: newest
// reading per device id, adapters below the floor dropped, ordered by index.
// Split out for unit testing without a real GPU.
func gpuSetFromStats(stats []perf.GpuStat, policy GpuPolicy) GpuSet {
	minTotalGB := policy.minTotalGB()
	var out GpuSet
	for _, g := range latestGpuStats(stats) {
		integrated := detectedIntegrated(g)
		counted := sharedPoolCounts(g, policy.SharedMemory)
		eff := effectiveGpuStat(g, counted)
		if eff.MemTotalMB <= 0 {
			continue
		}
		totalGB := float64(eff.MemTotalMB) / 1024.0
		if totalGB < minTotalGB {
			continue
		}
		free := eff.MemTotalMB - eff.MemUsedMB
		if free < 0 {
			free = 0
		}
		dev := GpuDevice{
			Index:      g.ID,
			Name:       g.Name,
			TotalGB:    totalGB,
			FreeGB:     float64(free) / 1024.0,
			Integrated: integrated,
		}
		if g.SharedTotalMB > 0 {
			dev.SharedTotalGB = float64(g.SharedTotalMB) / 1024.0
		}
		if counted {
			sharedFree := g.SharedTotalMB - g.SharedUsedMB
			if sharedFree > 0 {
				dev.SharedFreeGB = float64(sharedFree) / 1024.0
			}
		}
		out = append(out, dev)
	}
	return dropUnpooledIntegrated(out, policy.PoolIntegrated)
}

// dropUnpooledIntegrated keeps an integrated GPU out of a set that also holds a
// dedicated card, unless the user has explicitly allowed the pairing.
//
// The eligibility floor used to carry this rule, by making the device invisible
// outright — which on an APU box also hid the only GPU in the machine, from
// every budget in the program (issue #37). The rule was never about the device,
// though: it is about PAIRING one with a card that does the job better, so it is
// applied to the set. A set of nothing but integrated devices is returned
// unchanged, which is the APU-only box.
func dropUnpooledIntegrated(set GpuSet, allow bool) GpuSet {
	if allow || len(set) < 2 {
		return set
	}
	hasDedicated := false
	for _, d := range set {
		if !d.Integrated {
			hasDedicated = true
			break
		}
	}
	if !hasDedicated {
		return set
	}
	out := make(GpuSet, 0, len(set))
	for _, d := range set {
		if !d.Integrated {
			out = append(out, d)
		}
	}
	return out
}

// maxIntegratedDedicatedGB and integratedSharedRatio bound AUTO's shape test:
// a device is treated as integrated when its own pool is small AND the shared
// pool dwarfs it. A discrete card in a large-RAM machine reports a big GTT too,
// so the ratio carries the weight — an 8GB card with a 16GB aperture is 2x.
// Exceeding either bound only means the pool is not counted, which is the
// conservative answer.
const (
	maxIntegratedDedicatedGB = 8.0
	integratedSharedRatio    = 3.0
)

// apuNameMarkers name an APU where the shape test cannot. "Ryzen" appears in
// rocm-smi's Card Series on an APU and never on a discrete card; "Radeon
// Graphics" is the generic name an APU's iGPU reports (a card reports "Radeon
// RX ...").
var apuNameMarkers = []string{"ryzen", "athlon", "radeon graphics"}

// detectedIntegrated reports the device's TOPOLOGY: does it allocate out of
// system memory? Independent of whether the pool is counted, so the pooling
// rule and the budget rule can differ (a user may budget a discrete card's host
// aperture without wanting it treated as an iGPU).
func detectedIntegrated(g perf.GpuStat) bool {
	if g.MemTotalMB <= 0 || g.SharedTotalMB <= 0 {
		return false
	}
	name := strings.ToLower(g.Name)
	for _, marker := range apuNameMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	dedicated := float64(g.MemTotalMB)
	return dedicated <= maxIntegratedDedicatedGB*1024 &&
		float64(g.SharedTotalMB) >= integratedSharedRatio*dedicated
}

// sharedPoolCounts reports whether this device's shared pool is budget under
// the given mode. Only AUTO guesses, and it guesses conservatively: see
// detectedIntegrated, which refuses to call a large card integrated even when it
// reports a large aperture.
func sharedPoolCounts(g perf.GpuStat, mode string) bool {
	if g.SharedTotalMB <= 0 {
		return false
	}
	switch mode {
	case SharedMemoryOff:
		return false
	case SharedMemoryOn:
		return true
	default:
		return detectedIntegrated(g)
	}
}

// effectiveGpuStat is the device with its counted pool folded into the memory
// figures every budget computation reads. The RAW stat keeps the two pools
// apart for display and for the platform readers; only the folded one is
// allowed to answer "how much can this device hold".
func effectiveGpuStat(g perf.GpuStat, counted bool) perf.GpuStat {
	if !counted {
		return g
	}
	// Both pools are summed, including USED. On an APU a driver may report a
	// shared allocation under both headings, which overstates used and so
	// understates free: the sizer then offloads a layer or two more than it had
	// to, and the model still loads. Understating free is the safe direction to
	// be wrong in, and the alternative (taking the larger pool instead of the
	// sum) would hide real memory on Linux, where GTT genuinely grows past the
	// carve-out.
	g.MemTotalMB += g.SharedTotalMB
	g.MemUsedMB += g.SharedUsedMB
	return g
}

// DescribeGpuStats renders one telemetry reading for a human: what each adapter
// reported, what the policy made of it, and what the eligible set adds up to.
// cmd/monitor-test prints it, so a report from a machine we do not have arrives
// with the classification already attached -- which is what issue #37 (an APU
// whose only GPU was invisible) needed and could not get.
func DescribeGpuStats(stats []perf.GpuStat, multi bool, policy GpuPolicy) []string {
	set := gpuSetFromStats(stats, policy)
	kept := make(map[int]bool, len(set))
	for _, d := range set {
		kept[d.Index] = true
	}
	var lines []string
	for _, g := range latestGpuStats(stats) {
		integrated := detectedIntegrated(g)
		counted := sharedPoolCounts(g, policy.SharedMemory)
		eff := effectiveGpuStat(g, counted)
		topology := "dedicated"
		if integrated {
			topology = "integrated"
		}
		line := fmt.Sprintf("  [%d] %-28s %s %6d MB used / %6d MB total",
			g.ID, truncName(g.Name, 28), topology, g.MemUsedMB, g.MemTotalMB)
		if g.SharedTotalMB > 0 {
			line += fmt.Sprintf("  + shared %6d MB used / %6d MB total (%s)",
				g.SharedUsedMB, g.SharedTotalMB, onOff(counted))
		}
		usableGB := float64(eff.MemTotalMB) / 1024.0
		if counted {
			line += fmt.Sprintf("  => %5.1f GB usable", usableGB)
		}
		switch {
		case usableGB < policy.minTotalGB():
			line += fmt.Sprintf("  DROPPED (under the %.1f GB floor)", policy.minTotalGB())
		case !kept[g.ID]:
			line += "  EXCLUDED (an integrated GPU is not a split target unless poolIntegratedGpu is on)"
		}
		lines = append(lines, line)
	}
	if len(set) == 0 {
		return append(lines, "  eligible budget: none (no GPU counts; the sizer keeps its static budget)")
	}
	mode := "single"
	if multi {
		mode = "multi"
	}
	lines = append(lines, fmt.Sprintf("  eligible budget (%s): %.1f GB total, %.1f GB free over %d device(s)",
		mode, set.TotalGB(), set.FreeGB(), len(set)))
	return lines
}

// truncName keeps the diagnostic table aligned on a long adapter name.
func truncName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func onOff(b bool) string {
	if b {
		return "counted"
	}
	return "not counted"
}

// SampleGpuSet takes one telemetry snapshot and returns the eligible devices.
// ok is false when no GPU telemetry is available within timeout, and the caller
// keeps whatever static budget it had.
func SampleGpuSet(timeout time.Duration, policy GpuPolicy) (GpuSet, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	gpuCh, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil || gpuCh == nil {
		return nil, false
	}
	select {
	case stats := <-gpuCh:
		set := gpuSetFromStats(stats, policy)
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
	// The one-shot probes a generate pass makes on its own (vllm's card total)
	// cannot see the Settings, so seed them with the same policy this resolve is
	// using. Idempotent: same source, same value.
	setProbePolicy(s.DevicePolicy())

	idleFreeMu.Lock()
	cached := lastResolvedSet
	fresh := len(cached) > 0 && time.Since(lastResolvedAt) < gpuSetCacheTTL
	idleFreeMu.Unlock()

	set, ok := cached, fresh
	if !ok {
		set, ok = SampleGpuSet(autoVramSampleTimeout, s.DevicePolicy())
	}
	if !ok {
		return
	}
	if !s.MultiGpuEnabled() && len(set) > 1 {
		main := set.PlanMainIndex()
		for _, d := range set {
			if d.Index == main {
				set = GpuSet{d}
				break
			}
		}
	}
	s.Gpus = set
	if logf == nil {
		return
	}
	// An APU box has ONE device, which is exactly the box this fork could not
	// see at all before #37 — and the box a bug report is about. Say what the
	// budget is and how much of it is host memory, or "the GPU is still
	// invisible" arrives with nothing to check.
	if len(set) == 1 {
		d := set[0]
		if d.Integrated && d.SharedFreeGB > 0 {
			logf(fmt.Sprintf("gpu: %s is an integrated GPU; free %.2fGB (%.2fGB dedicated + %.2fGB shared system memory)",
				d.Name, d.FreeGB, d.FreeGB-d.SharedFreeGB, d.SharedFreeGB))
		}
		return
	}
	names := make([]string, len(set))
	for i, d := range set {
		names[i] = fmt.Sprintf("%d:%s %.1f/%.1fGB free", d.Index, d.Name, d.FreeGB, d.TotalGB)
		if d.Integrated && d.SharedFreeGB > 0 {
			names[i] += fmt.Sprintf(" (integrated, %.1fGB shared)", d.SharedFreeGB)
		}
		// A card busy at this instant is planned against its stable capacity,
		// so say so: otherwise the emitted --tensor-split reads as wrong
		// against the free figure on the same line, and this log is what a bug
		// report pastes.
		if c := d.planCapacityGB(); c > d.FreeGB {
			names[i] += fmt.Sprintf(" (plan %.1fGB)", c)
		}
	}
	// The main device the PLAN picks, which is the one the config will name.
	logf(fmt.Sprintf("multi-gpu: %d devices [%s] -> pooled budget %.2fGB, main gpu %d",
		len(set), strings.Join(names, ", "), set.FreeGB(), set.PlanMainIndex()))
}

// EligibleGpuStats is the exported eligibility rule, for callers outside this
// package that hold a raw perf sample history and must describe the SAME cards
// the sizer planned against: newest sample per device id, adapters under the
// inference floor dropped, integrated devices kept out of a mixed set unless the
// policy allows it, and (when multi is false) everything but the device
// --main-gpu would pick removed.
//
// The returned stats are the FOLDED ones — an integrated device's shared pool is
// already inside MemTotalMB/MemUsedMB — because the callers pool those figures
// and compare the result against a budget the sizer derived from the same fold.
// Returning the raw stats here is how the guard and the gauge would end up
// describing a card the plan never used.
//
// The server's OOM guard and idle-VRAM tracker call it. Before issue #4 they
// each open-coded "largest adapter", which is how a two-card box got a ceiling
// describing one card while the models were sized for both.
func EligibleGpuStats(stats []perf.GpuStat, multi bool, policy GpuPolicy) []perf.GpuStat {
	set := gpuSetFromStats(stats, policy)
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
		out = append(out, effectiveGpuStat(g, sharedPoolCounts(g, policy.SharedMemory)))
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
func LiveGpuSet(stats []perf.GpuStat, multi bool, policy GpuPolicy) GpuSet {
	return gpuSetFromStats(EligibleGpuStats(stats, multi, policy), policy)
}

// writeSingleDeviceEnv emits the env block a SINGLE-DEVICE backend needs on a
// multi-GPU box. sd-server, tts-server and whisper have no split of their own:
// left alone the CUDA runtime hands them whatever it calls device 0, which under
// its FASTEST_FIRST default is not necessarily the card the sizer budgeted, and
// not necessarily the same card twice. Pin them to the same main device the LLM
// split pins to, enumerated by PCI bus so the ordinal means here what it means
// in nvidia-smi.
//
// Vulkan and ROCm have the same problem and their own variables for it, but the
// ordinal those take is the BACKEND's, which telemetry does not supply: it used
// to be a no-op there, leaving a two-card Vulkan box to whichever adapter ggml
// enumerated first. singleDeviceEnvFor asks the binary instead, so the pin is
// derived rather than assumed.
//
// A no-op on a single-GPU box (nothing to disambiguate), when the device set was
// never resolved, and when neither the probe nor the CUDA fallback can name the
// device.
func writeSingleDeviceEnv(b *strings.Builder, s Settings, exe string) {
	set := s.GpuSetOrEmpty()
	if !set.Multi() {
		return
	}
	pin, isCuda := singleDeviceEnvFor(exe, set)
	if pin == "" {
		return
	}
	b.WriteString("    env:\n")
	// CUDA_DEVICE_ORDER only means anything alongside CUDA_VISIBLE_DEVICES, and
	// it is what makes the ordinal in it mean what nvidia-smi means.
	if isCuda {
		fmt.Fprintf(b, "      - %q\n", cudaOrderEnv)
	}
	fmt.Fprintf(b, "      - %q\n", pin)
}

// singleDeviceEnvFor returns the NAME=VALUE pin for the plan's main device as
// this backend enumerates it, and whether it is the CUDA one.
//
// Preferred path: ask the binary what it calls its devices and read the ordinal
// off the id it gives the main card ("Vulkan1" -> 1). That is the only way to
// get a Vulkan or ROCm ordinal right, because the telemetry index is a different
// enumeration and using it directly would pin the wrong adapter, silently.
//
// Fallback: CUDA alone, where CUDA_DEVICE_ORDER=PCI_BUS_ID makes the runtime's
// ordinal agree with nvidia-smi's, so the telemetry index IS the right value.
// Everything else refuses, which is the behaviour that shipped.
func singleDeviceEnvFor(exe string, set GpuSet) (string, bool) {
	main := set.PlanMainIndex()
	if devs, err := ListBackendDevices(exe); err == nil {
		if ids := set.BackendIDs(devs); len(ids) == len(set) {
			for i, d := range set {
				if d.Index != main {
					continue
				}
				if name, ord, ok := visibleDevicesEnvFor(ids[i]); ok {
					return name + "=" + ord, name == "CUDA_VISIBLE_DEVICES"
				}
				break
			}
		}
	}
	if usingCudaGPU() {
		return fmt.Sprintf("CUDA_VISIBLE_DEVICES=%d", main), true
	}
	return "", false
}

// visibleDevicesEnvFor splits a backend device id into the variable that filters
// that backend's device list and the ordinal to give it. The id carries both:
// ggml names devices "<backend><ordinal>", and each backend's filter variable
// indexes the same enumeration the id was numbered from.
//
// An unrecognised backend gets nothing rather than a guess. A filter variable
// aimed at the wrong enumeration does not fail loudly, it just runs the model on
// a card the sizer did not budget.
func visibleDevicesEnvFor(id string) (string, string, bool) {
	cut := strings.IndexFunc(id, func(r rune) bool { return r >= '0' && r <= '9' })
	if cut <= 0 {
		return "", "", false
	}
	ord := id[cut:]
	switch strings.ToLower(id[:cut]) {
	case "cuda":
		return "CUDA_VISIBLE_DEVICES", ord, true
	case "vulkan", "vk":
		return "GGML_VK_VISIBLE_DEVICES", ord, true
	case "rocm", "hip":
		return "HIP_VISIBLE_DEVICES", ord, true
	}
	return "", "", false
}
