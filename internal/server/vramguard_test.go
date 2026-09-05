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
	s.offloadSettings.Store(&autogen.Settings{OomGuardReserveGB: 1, OomGuardGraceSec: 30})
	return newVramGuard(s)
}

// tune edits the settings the guard reads (they live on the Server now, so a
// test tweaks them through the same pointer swap a live reload uses).
func tune(g *vramGuard, f func(*autogen.Settings)) {
	next := g.settings()
	f(&next)
	g.s.offloadSettings.Store(&next)
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
	tune(g, func(s *autogen.Settings) { s.OomGuardGraceSec = 0 })
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
	tune(g, func(s *autogen.Settings) { s.OomGuardEvict, s.OomGuardGraceSec = &off, 0 })
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

// pooledGPUStat sums every eligible card and keeps only each ID's newest sample,
// so the guard's ceiling describes the same pooled budget the sizer planned the
// resident models against. With multi off it collapses to the main device.
func TestPooledGPUStat_SumsEligibleNewest(t *testing.T) {
	now := time.Now()
	hist := []perf.GpuStat{
		{ID: 0, MemTotalMB: 8192, MemUsedMB: 100, Timestamp: now},
		{ID: 1, MemTotalMB: 24576, MemUsedMB: 999, Timestamp: now.Add(-time.Minute)},
		{ID: 1, MemTotalMB: 24576, MemUsedMB: 4096, Timestamp: now},
	}
	got, ok := pooledGPUStat(hist, true)
	if !ok || got.MemTotalMB != 8192+24576 || got.MemUsedMB != 100+4096 {
		t.Fatalf("pooledGPUStat(multi) = %+v ok=%v, want both cards' newest samples summed", got, ok)
	}
	// Single-device mode pins to the card with the most FREE memory: ID 1 has
	// 20480 MiB free against ID 0's 8092.
	got, ok = pooledGPUStat(hist, false)
	if !ok || got.MemTotalMB != 24576 || got.MemUsedMB != 4096 {
		t.Fatalf("pooledGPUStat(single) = %+v ok=%v, want only the newest ID 1 sample", got, ok)
	}
	if _, ok := pooledGPUStat(nil, true); ok {
		t.Fatal("empty history reported a GPU")
	}
	// An adapter under the inference floor (an iGPU slicing system RAM) is not
	// budget: pooling it would invent VRAM no card has.
	got, ok = pooledGPUStat([]perf.GpuStat{
		{ID: 0, MemTotalMB: 2048, MemUsedMB: 128, Timestamp: now},
		{ID: 1, MemTotalMB: 12288, MemUsedMB: 1024, Timestamp: now},
	}, true)
	if !ok || got.MemTotalMB != 12288 {
		t.Fatalf("pooledGPUStat = %+v ok=%v, want the iGPU dropped", got, ok)
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

// The thrash regression. A resident model sized just under the budget, with a
// browser tab holding a fraction of a GB above the idle floor: the shed ceiling
// must still clear it. Charging OomGuardReserveGB here (as the admission ceiling
// does) put 21.8GB over a 21.6GB ceiling, the watchdog unloaded the model, and
// the spawn guard - which sizes against live free VRAM - reloaded it unchanged
// on the next request, every couple of minutes, forever.
func TestVramGuard_NoShedWhenReserveIsTheOnlyPressure(t *testing.T) {
	g := newGuard(t, map[string]float64{"llm": 21.8}, nil, nil)
	cfg := g.s.config()
	cfg.VramBudgetGB = 22.8
	g.s.cfg.Store(&cfg)
	pressure(g, 205) // 0.2GB above the idle floor

	if got, _ := g.ceilingGB(); got > 21.8 {
		t.Fatalf("admission ceiling %v, want the reserve still charged there", got)
	}
	if got, _ := g.shedCeilingGB(); got < 22.5 {
		t.Fatalf("shed ceiling %v, want ~22.6 (excess only, no reserve)", got)
	}
	g.watchdog()
	if !g.overSince.IsZero() {
		t.Fatal("watchdog armed on 0.2GB of foreign VRAM; the model fits")
	}
}

// Having shed once, the guard sits out the cooldown even while the reading still
// says it is over - the model it killed is reloaded by the next request and
// would otherwise be killed again on the very next sample.
func TestVramGuard_CooldownAfterShed(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10}, nil, nil)
	tune(g, func(s *autogen.Settings) { s.OomGuardGraceSec = 0 })
	pressure(g, 20480)
	g.watchdog()
	g.watchdog() // sheds
	if g.lastShed.IsZero() {
		t.Fatal("shed did not stamp lastShed")
	}
	// The shed is asynchronous (vramguard.go: `go g.s.local.Unload`), so its
	// increment can land after watchdog() returns. Waiting for it before the
	// baseline is what makes this test about the cooldown rather than about the
	// scheduler: reading `before` too early counts the FIRST shed as a second one.
	before := int32(0)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if before = g.s.local.(*guardRouter).unloadCalls.Load(); before > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if before == 0 {
		t.Fatal("the first shed never unloaded anything")
	}
	g.watchdog()
	g.watchdog()
	if n := g.s.local.(*guardRouter).unloadCalls.Load(); n != before {
		t.Fatalf("shed again during the cooldown (%d unloads, want %d)", n, before)
	}
}

// The slack absorbs estimate noise, but a real overshoot still sheds.
func TestVramGuard_ShedSlackAbsorbsNoiseOnly(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10.3}, nil, nil)
	pressure(g, 14336) // 14GB taken: shed ceiling 24-14 = 10GB, resident 10.3
	g.watchdog()
	if !g.overSince.IsZero() {
		t.Fatal("armed on a 0.3GB overshoot, inside the slack")
	}
	g2 := newGuard(t, map[string]float64{"a": 11.5}, nil, nil)
	pressure(g2, 14336) // same ceiling, 1.5GB over: past the slack
	g2.watchdog()
	if g2.overSince.IsZero() {
		t.Fatal("did not arm on a 1.5GB overshoot")
	}
}

// A settings edit reaches the already-running guard. The spawn-time offload
// guard used to capture autogen.Settings by value at boot, so raising
// targetVramGB in the dashboard regenerated every baked plan but left the live
// re-plan sizing against the old budget — a model that fit was cut to a
// fraction of its layers and refused on the minGpuFraction floor until restart.
func TestVramGuard_SettingsRefreshedLive(t *testing.T) {
	g := newGuard(t, map[string]float64{"a": 10}, nil, nil)
	if got := g.settings().OomGuardReserveGB; got != 1 {
		t.Fatalf("reserve = %v want 1", got)
	}
	g.s.UpdateOffloadSettings(autogen.Settings{TargetVramGB: 22.8, OomGuardReserveGB: 2})
	if got := g.settings().OomGuardReserveGB; got != 2 {
		t.Errorf("reserve after update = %v want 2", got)
	}
	if got := g.s.offloadSettingsVal().TargetVramGB; got != 22.8 {
		t.Errorf("target after update = %v want 22.8", got)
	}
}

// Before WireDynamicOffload nothing reads these settings, and publishing them
// from a reload must not make an unwired server start acting on them.
func TestVramGuard_UpdateOffloadSettingsUnwired(t *testing.T) {
	s := &Server{}
	s.UpdateOffloadSettings(autogen.Settings{TargetVramGB: 22.8})
	if s.offloadSettings.Load() != nil {
		t.Error("unwired server stored offload settings")
	}
}
