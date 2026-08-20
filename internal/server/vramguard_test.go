package server

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
	"github.com/quartermaster-labs/quartermaster/internal/process"
)

// guardRouter is a stubRouter that can report per-model in-flight counts and
// child pids, which is what the guard's accounting turns on.
type guardRouter struct {
	stubRouter
	inflight map[string]int64
	pids     []int
}

func (g *guardRouter) Inflight(id string) (int64, bool) {
	n, ok := g.inflight[id]
	return n, ok
}
func (g *guardRouter) RunningPIDs() []int { return g.pids }

// newGuard builds a guard over a config of model→estVramGB with the named
// models resident, plus whichever of them are persistent / busy.
func newGuard(t *testing.T, est map[string]float64, persistent []string, inflight map[string]int64) *vramGuard {
	t.Helper()
	models := make(map[string]config.ModelConfig, len(est))
	var normal, pinned []string
	pin := make(map[string]bool)
	for _, id := range persistent {
		pin[id] = true
	}
	running := make(map[string]process.ProcessState, len(est))
	for id, gb := range est {
		models[id] = config.ModelConfig{EstVramGB: gb}
		running[id] = process.StateReady
		if pin[id] {
			pinned = append(pinned, id)
		} else {
			normal = append(normal, id)
		}
	}
	rt := &guardRouter{inflight: inflight}
	rt.running = running
	s := newTestServer(rt, nil)
	s.cfg.Store(&config.Config{
		VramBudgetGB: 24,
		Models:       models,
		Routing: config.RoutingConfig{
			Router: config.RouterConfig{
				Settings: config.RouterSettings{
					Groups: map[string]config.GroupConfig{
						"g":   {Swap: true, Members: normal},
						"pin": {Persistent: true, Members: pinned},
					},
				},
			},
		},
	})
	return newVramGuard(s, autogen.Settings{OomGuardReserveGB: 1, OomGuardGraceSec: 30})
}

// pressure publishes a trusted reading of excessMB of foreign VRAM above a zero
// idle baseline, which is what the ceiling is computed from.
func pressure(g *vramGuard, excessMB int64) {
	g.totalMB.Store(24576)
	g.foreignFloorMB.Store(0)
	g.foreignMB.Store(excessMB)
	g.trusted.Store(true)
}

// The fewest models are shed to get back under the ceiling, largest-first.
func TestVramGuard_SheddableLargestFirst(t *testing.T) {
	g := newGuard(t, map[string]float64{"big": 10, "mid": 6, "small": 2}, nil, nil)
	got, total := g.sheddable(10)
	if total != 18 {
		t.Fatalf("resident total %v, want 18", total)
	}
	// 18 > 10; dropping "big" leaves 8 <= 10, so nothing else goes.
	if want := []string{"big"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shed %v, want %v", got, want)
	}
}

// A resident set that fits sheds nothing, and the total still reports.
func TestVramGuard_SheddableNoneWhenItFits(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 4, "b": 4}, nil, nil)
	if got, total := g.sheddable(12); len(got) != 0 || total != 8 {
		t.Fatalf("shed %v total %v, want none / 8", got, total)
	}
}

// A busy model is never killed — a failed request is worse than a slow one — so
// the next-largest idle one goes instead.
func TestVramGuard_SheddableSkipsBusy(t *testing.T) {
	g := newGuard(t, map[string]float64{"big": 10, "mid": 6}, nil, map[string]int64{"big": 1})
	got, _ := g.sheddable(8)
	if want := []string{"mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shed %v, want %v (big is busy)", got, want)
	}
}

// Persistent members hold real VRAM so they still count toward the total, but
// they are never candidates.
func TestVramGuard_SheddableChargesPersistentButKeepsIt(t *testing.T) {
	g := newGuard(t, map[string]float64{"pinned": 6, "a": 5}, []string{"pinned"}, nil)
	got, total := g.sheddable(4)
	if total != 11 {
		t.Fatalf("resident total %v, want 11 (persistent charged)", total)
	}
	if want := []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shed %v, want %v", got, want)
	}
}

// A CPU-resident model (no estimate) holds no VRAM, so unloading it frees
// nothing and it must not be picked as a victim.
func TestVramGuard_SheddableIgnoresCPUModels(t *testing.T) {
	g := newGuard(t, map[string]float64{"asr": 0, "a": 10}, nil, nil)
	got, total := g.sheddable(4)
	if total != 10 {
		t.Fatalf("resident total %v, want 10 (CPU model uncounted)", total)
	}
	if want := []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shed %v, want %v", got, want)
	}
}

// Nothing shed when everything left is busy: the guard logs and accepts the
// degradation rather than failing an in-flight request.
func TestVramGuard_SheddableNothingLeftToShed(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10}, nil, map[string]int64{"a": 3})
	if got, _ := g.sheddable(2); len(got) != 0 {
		t.Fatalf("shed %v, want nothing (all busy)", got)
	}
}

// With nothing of ours on the card, all used VRAM is foreign — a reading that
// needs no per-process source and is always trustworthy.
func TestVramGuard_ForeignAllWhenNothingResident(t *testing.T) {
	g := newGuard(t, nil, nil, nil)
	stat := perf.GpuStat{ID: 0, MemTotalMB: 24576, MemUsedMB: 8192}
	got, ok := g.foreignMB4(context.Background(), stat)
	if !ok || got != 8192 {
		t.Fatalf("foreign %v ok=%v, want 8192 true", got, ok)
	}
}

// A child the per-process source cannot see would be billed as foreign, which
// is exactly the mistake that evicts everything — the whole reading is refused.
func TestVramGuard_UnattributableReadingRefused(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10}, nil, nil)
	g.s.local.(*guardRouter).pids = []int{999999} // no such compute app
	stat := perf.GpuStat{ID: 0, MemTotalMB: 24576, MemUsedMB: 20000}
	if _, ok := g.foreignMB4(context.Background(), stat); ok {
		t.Fatal("expected the reading to be refused when our pid isn't attributable")
	}
}

// THE regression test for this guard. The desktop's idle VRAM cost is already
// priced into vramBudgetGB, so subtracting it again would report pressure on a
// perfectly healthy box and shed a model for nothing. With foreign usage sitting
// at its baseline the ceiling must come back as exactly the configured budget.
func TestVramGuard_IdleDesktopDoesNotTightenBudget(t *testing.T) {
	g := newGuard(t, nil, nil, nil)
	// 1.5GB held by the compositor, and it never moves.
	for i := 0; i < 3; i++ {
		g.sample(context.Background(), []perf.GpuStat{{ID: 0, MemTotalMB: 24576, MemUsedMB: 1536}})
	}
	got, ok := g.ceilingGB()
	if !ok {
		t.Fatal("no ceiling published")
	}
	if want := 24.0; got != want {
		t.Fatalf("idle box: ceiling %v, want the configured budget %v", got, want)
	}
}

// Foreign usage ABOVE the idle baseline is what shrinks the budget, less the
// reserve kept back for that app's own further growth.
func TestVramGuard_ExcessForeignTightensBudget(t *testing.T) {
	g := newGuard(t, nil, nil, nil)
	g.sample(context.Background(), []perf.GpuStat{{ID: 0, MemTotalMB: 24576, MemUsedMB: 1536}})
	// A game starts and takes 8GB on top of the 1.5GB baseline.
	g.sample(context.Background(), []perf.GpuStat{{ID: 0, MemTotalMB: 24576, MemUsedMB: 1536 + 8192}})
	got, ok := g.ceilingGB()
	if !ok {
		t.Fatal("no ceiling published")
	}
	// 24 budget - 8 excess - 1 reserve.
	if want := 15.0; got != want {
		t.Fatalf("ceiling %v, want %v", got, want)
	}
}

// The baseline tracks DOWN: closing the game restores the full budget.
func TestVramGuard_FloorFollowsForeignDown(t *testing.T) {
	g := newGuard(t, nil, nil, nil)
	g.sample(context.Background(), []perf.GpuStat{{ID: 0, MemTotalMB: 24576, MemUsedMB: 9728}})
	g.sample(context.Background(), []perf.GpuStat{{ID: 0, MemTotalMB: 24576, MemUsedMB: 1536}})
	if got, _ := g.ceilingGB(); got != 24.0 {
		t.Fatalf("ceiling %v after the foreign app closed, want the full budget", got)
	}
}

// With no budget configured (multiResident off) the card total stands in, so the
// watchdog still has a real ceiling to judge a game's arrival against.
func TestVramGuard_NoBudgetUsesCardTotal(t *testing.T) {
	g := newGuard(t, nil, nil, nil)
	cfg := g.s.config()
	cfg.VramBudgetGB = 0
	g.s.cfg.Store(&cfg)
	g.sample(context.Background(), []perf.GpuStat{{ID: 0, MemTotalMB: 24576, MemUsedMB: 1536}})
	if got, _ := g.ceilingGB(); got != 24.0 {
		t.Fatalf("ceiling %v, want the 24GB card total", got)
	}
}

// No usable GPU sample publishes "unknown", which the router reads as "keep the
// static budget".
func TestVramGuard_NoGpuPublishesUnknown(t *testing.T) {
	g := newGuard(t, nil, nil, nil)
	g.trusted.Store(true)
	g.totalMB.Store(24576)
	g.sample(context.Background(), []perf.GpuStat{{ID: 0, MemTotalMB: 0}})
	if _, ok := g.ceilingGB(); ok {
		t.Fatal("expected an unknown ceiling with no usable GPU sample")
	}
}

// The watchdog needs the pressure to persist: the first over-ceiling sample only
// starts the clock, so a transient spike costs nothing.
func TestVramGuard_WatchdogWaitsOutTheGrace(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10}, nil, nil)
	pressure(g, 20480) // 20GB taken by a game: ceiling 24-20-1 = 3GB < 10GB resident
	g.watchdog()
	if g.overSince.IsZero() {
		t.Fatal("first over-ceiling sample should have started the grace clock")
	}
	if n := g.s.local.(*guardRouter).unloadCalls.Load(); n != 0 {
		t.Fatalf("unloaded %d models during the grace period, want 0", n)
	}
	// Pressure relieved before the grace elapsed: the clock resets, no unload.
	pressure(g, 0)
	g.watchdog()
	if !g.overSince.IsZero() {
		t.Fatal("grace clock should reset once the set fits again")
	}
}

// Once the grace has elapsed the victims are unloaded.
func TestVramGuard_WatchdogUnloadsAfterGrace(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10}, nil, nil)
	g.settings.OomGuardGraceSec = 0
	pressure(g, 20480)
	g.watchdog() // starts the clock
	g.watchdog() // grace of 0 has elapsed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.s.local.(*guardRouter).unloadCalls.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("watchdog never unloaded after the grace elapsed")
}

// The watchdog is opt-out.
func TestVramGuard_WatchdogDisabled(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10}, nil, nil)
	off := false
	g.settings.OomGuardEvict = &off
	g.settings.OomGuardGraceSec = 0
	pressure(g, 20480)
	g.watchdog()
	g.watchdog()
	if n := g.s.local.(*guardRouter).unloadCalls.Load(); n != 0 {
		t.Fatalf("unloaded %d models with the watchdog off, want 0", n)
	}
}

// A memoised refusal stands on an unchanged reading, so a retry storm doesn't
// re-run the reclaim probe loop for each request.
func TestVramRefusals_HitOnUnchangedFree(t *testing.T) {
	var r vramRefusals
	want := errors.New("insufficient VRAM")
	r.put("m", want, 2.0)
	got, hit := r.get("m", 2.0, true)
	if !hit || !errors.Is(got, want) {
		t.Fatalf("get = %v %v, want the memoised error", got, hit)
	}
}

// VRAM coming back invalidates the memo immediately — the user closed the game,
// and the next request must load rather than inherit a stale no.
func TestVramRefusals_FreedVramInvalidates(t *testing.T) {
	var r vramRefusals
	r.put("m", errors.New("insufficient VRAM"), 2.0)
	if _, hit := r.get("m", 12.0, true); hit {
		t.Fatal("memo survived a large rise in free VRAM")
	}
	if _, hit := r.get("m", 12.0, true); hit {
		t.Fatal("invalidated memo was not dropped")
	}
}

// Jitter below the reclaim epsilon is not "VRAM came back".
func TestVramRefusals_JitterKeepsMemo(t *testing.T) {
	var r vramRefusals
	r.put("m", errors.New("insufficient VRAM"), 2.0)
	if _, hit := r.get("m", 2.0+vramReclaimEpsilonGB/2, true); !hit {
		t.Fatal("memo dropped on telemetry jitter")
	}
}

// An expired memo is gone.
func TestVramRefusals_Expires(t *testing.T) {
	var r vramRefusals
	r.put("m", errors.New("insufficient VRAM"), 2.0)
	r.mu.Lock()
	e := r.m["m"]
	e.at = time.Now().Add(-2 * vramRefusalTTL)
	r.m["m"] = e
	r.mu.Unlock()
	if _, hit := r.get("m", 2.0, true); hit {
		t.Fatal("memo outlived its TTL")
	}
}

// A spawn that gets through clears any older no.
func TestVramRefusals_ClearOnSuccess(t *testing.T) {
	var r vramRefusals
	r.put("m", errors.New("insufficient VRAM"), 2.0)
	r.clear("m")
	if _, hit := r.get("m", 2.0, true); hit {
		t.Fatal("cleared memo still hit")
	}
}

// largestGPU picks the biggest card and keeps only each ID's newest sample, so
// every VRAM decision in the process talks about the same adapter.
func TestLargestGPU_PicksBiggestNewest(t *testing.T) {
	now := time.Now()
	got, ok := largestGPU([]perf.GpuStat{
		{ID: 0, MemTotalMB: 8192, MemUsedMB: 100, Timestamp: now},
		{ID: 1, MemTotalMB: 24576, MemUsedMB: 999, Timestamp: now.Add(-time.Minute)},
		{ID: 1, MemTotalMB: 24576, MemUsedMB: 4096, Timestamp: now},
	})
	if !ok || got.ID != 1 || got.MemUsedMB != 4096 {
		t.Fatalf("largestGPU = %+v ok=%v, want the newest ID 1 sample", got, ok)
	}
	if _, ok := largestGPU(nil); ok {
		t.Fatal("empty history reported a GPU")
	}
}

// sort.Slice is stable enough for the victim order the tests assert; this guards
// the assumption that equal-size candidates don't reorder the assertions above.
func TestVramGuard_SheddableDeterministicOnTies(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 5, "b": 5, "c": 5}, nil, nil)
	got, _ := g.sheddable(4)
	sort.Strings(got)
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shed %v, want all three", got)
	}
}

// A model still loading reports no in-flight requests (its caller is parked on
// the swap), so shedding it would kill the load the user just asked for. It is
// charged toward the total but never a victim.
func TestVramGuard_SheddableSkipsStarting(t *testing.T) {
	g := newGuard(t, map[string]float64{"loading": 10, "resident": 6}, nil, nil)
	g.s.local.(*guardRouter).running["loading"] = process.StateStarting
	got, total := g.sheddable(8)
	if total != 16 {
		t.Fatalf("resident total %v, want 16 (a starting model is charged)", total)
	}
	if want := []string{"resident"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shed %v, want %v (loading must not be interrupted)", got, want)
	}
}
