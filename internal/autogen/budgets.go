package autogen

// budgets.go derives the VRAM/RAM budgets a box should START from, by measuring
// it, instead of handing every machine the same hardcoded pair.
//
// The old constants (7 GB VRAM / 24 GB RAM) were the author's desktop written
// down. On a 12 GB card the wizard opened at 7 — 5 GB of the card simply unused,
// with every model sized down to fit a budget nothing measured — and 24 GB of
// RAM on a 16 GB box is a budget the machine cannot honour at all, so the RAM
// ceiling that is supposed to stop a plan from swapping never bound anything.
//
// The rule is the obvious one: budget what is free, i.e. total minus what the OS
// and desktop already hold. That is exactly what a free-memory reading IS, so
// both probes are one call each — no separate "system usage" model to keep
// calibrated. vramOverheadGB (0.5) is the only pad on top, charged inside the
// plan; see resolveAutoVram for why it is not deducted here as well.
//
// A probe is a SNAPSHOT, and its honest failure mode is a box that was busy when
// it was taken: run the wizard with a game open and the install seeds a budget
// that stays too small until the user raises it. Seeding beats not seeding
// anyway — a measured-low number is a number the Settings page can show against
// the card's real total, where the old constant was wrong on every box equally
// and looked deliberate. Users who want it re-measured every boot have autoVram.

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/shirou/gopsutil/v4/mem"
)

// seedMultiGpu carries the settings' pooling decision into hardwareBudgets,
// which is a process-wide OnceValues and so cannot take an argument. Defaults
// to true, matching Settings.MultiGpuEnabled's nil default.
var seedMultiGpu atomic.Bool

func init() { seedMultiGpu.Store(true) }

const (
	// recommendedVramFloorGB / recommendedRamFloorGB are the readings below which
	// a probe is treated as unusable rather than as a budget. A card reporting
	// under a gigabyte free is one that something else has taken (or one whose
	// driver is lying); seeding that would emit a catalog where nothing fits.
	recommendedVramFloorGB = 1.0
	recommendedRamFloorGB  = 2.0
)

// RecommendedVramGB is the VRAM budget a fresh install should start from: the
// POOLED free VRAM across every eligible adapter right now. ok is false with no
// GPU telemetry or a reading below the floor, and the caller then keeps its
// static default.
//
// Pooled, because the wizard's seed is what a two-card box lives with until
// someone edits it: seeding the largest card alone is how a 12 GB + 16 GB
// machine ended up with targetVramGB 11.9 and half its VRAM unused (issue #4).
func RecommendedVramGB() (float64, bool) {
	return recommendedVramGB(true)
}

// recommendedVramGB pools when multi is set, and otherwise reports only the
// device --main-gpu would pick. A user who has turned multiGpu off must not be
// handed a budget that describes memory no single card has.
func recommendedVramGB(multi bool) (float64, bool) {
	if !multi {
		set, ok := SampleGpuSet(autoVramSampleTimeout, minInferenceVramGB)
		if !ok {
			return 0, false
		}
		main := set.MainIndex()
		for _, d := range set {
			if d.Index == main {
				if d.FreeGB < recommendedVramFloorGB {
					return 0, false
				}
				return floorGB(d.FreeGB), true
			}
		}
		return 0, false
	}
	gb, ok := SampleFreeVramGB(autoVramSampleTimeout)
	if !ok || gb < recommendedVramFloorGB {
		return 0, false
	}
	return floorGB(gb), true
}

// RecommendedRamGB is the RAM budget a fresh install should start from: system
// RAM minus what is already in use. ok is false when the OS reading fails or
// falls below the floor.
//
// Available, not Free: on every modern OS "free" excludes the page/file cache,
// which is reclaimable and routinely most of RAM on a box that has been up a
// while. Budgeting against Free would hand a 32 GB machine that has read a few
// model files a 2 GB ceiling.
func RecommendedRamGB() (float64, bool) {
	vm, err := mem.VirtualMemory()
	if err != nil || vm == nil || vm.Total == 0 {
		return 0, false
	}
	avail := vm.Available
	if avail == 0 || avail > vm.Total {
		avail = vm.Total - vm.Used
	}
	gb := float64(avail) / gib
	if gb < recommendedRamFloorGB {
		return 0, false
	}
	return floorGB(gb), true
}

// floorGB truncates to one decimal. Rounding UP would claim a sliver of memory
// the probe did not actually see free.
func floorGB(gb float64) float64 { return math.Floor(gb*10) / 10 }

// hardwareBudgets caches the pair for the process. Both probes are one-shot and
// LoadGenerateFile runs on every hot reload and on some API requests, so without
// this a box with no budgets in its generate file would re-probe the GPU on each
// settings save.
var hardwareBudgets = sync.OnceValues(func() (float64, float64) {
	vram, vok := recommendedVramGB(seedMultiGpu.Load())
	if !vok {
		vram = 0
	}
	ram, rok := RecommendedRamGB()
	if !rok {
		ram = 0
	}
	return vram, ram
})

// seedHardwareBudgets replaces the placeholder constants applyDefaults wrote
// with measured ones, but ONLY for a knob that neither the generate file nor the
// UI sidecar set. An explicit value — including one the wizard measured and
// wrote at install time — is the user's and is never re-derived behind their
// back; that is what autoVram is for.
func seedHardwareBudgets(s *Settings, vramUnset, ramUnset bool) {
	if !vramUnset && !ramUnset {
		return
	}
	// The probe is cached for the process, so the pooling decision has to be
	// visible to it before it runs. A box with multiGpu off must be seeded from
	// one card: a pooled figure is memory no single adapter has, and the sizer
	// would plan every model past what fits.
	seedMultiGpu.Store(s.MultiGpuEnabled())
	vram, ram := hardwareBudgets()
	if vramUnset && vram > 0 {
		s.TargetVramGB = vram
	}
	if ramUnset && ram > 0 {
		s.MaxRamGB = ram
	}
}
