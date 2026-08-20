package router

import (
	"reflect"
	"testing"
)

// liveSwapper is budgetSwapper with a live VRAM ceiling probe attached.
// ok=false models "no trustworthy reading".
func liveSwapper(budgetGB float64, est map[string]float64, ceilingGB float64, ok bool) *groupSwapper {
	sw := budgetSwapper(budgetConf(budgetGB, est))
	h := &liveVramHolder{}
	h.set(func() (float64, bool) { return ceilingGB, ok })
	sw.liveVram = h
	return sw
}

// A foreign GPU app shrinks the budget: the set that fit the static 24 GB no
// longer fits the 10 GB actually left, so residents are shed. Without this the
// router evicts for room that isn't there and the spawn guard refuses anyway.
func TestGroupSwapper_LiveCeilingTightensBudget(t *testing.T) {
	sw := liveSwapper(24, map[string]float64{"a": 6, "b": 5, "c": 4}, 10, true)
	got := sw.EvictionFor("c", []string{"a", "b"})
	// 6+5+4=15 > 10; evicting "a" (LRU-first) leaves 5+4=9 <= 10.
	if want := []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("live ceiling 10GB: evict %v, want %v", got, want)
	}
}

// A live ceiling ABOVE the configured budget never loosens it — the budget is a
// user ceiling, not a floor to be raised by an idle card.
func TestGroupSwapper_LiveCeilingNeverLoosens(t *testing.T) {
	sw := liveSwapper(12, map[string]float64{"a": 5, "b": 5, "c": 5}, 48, true)
	got := sw.EvictionFor("c", []string{"a", "b"})
	if len(got) != 1 {
		t.Fatalf("a 48GB reading must not raise a 12GB budget; evicted %v", got)
	}
}

// No trustworthy reading leaves the static budget in force — the pre-guard
// behaviour. Guessing low here would evict every resident model for nothing.
func TestGroupSwapper_LiveCeilingUnavailableKeepsStaticBudget(t *testing.T) {
	sw := liveSwapper(24, map[string]float64{"a": 6, "b": 5, "c": 4}, 2, false)
	if got := sw.EvictionFor("c", []string{"a", "b"}); len(got) != 0 {
		t.Fatalf("unusable reading must not tighten the budget; evicted %v", got)
	}
}

// A ceiling under even the target alone evicts everything evictable; the spawn
// guard then sizes or refuses the load against what the GPU really has free.
func TestGroupSwapper_LiveCeilingBelowTargetEvictsAll(t *testing.T) {
	sw := liveSwapper(24, map[string]float64{"a": 6, "b": 5, "c": 4}, 1, true)
	got := sw.EvictionFor("c", []string{"a", "b"})
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ceiling below the target: evict %v, want %v", got, want)
	}
}

// A nil holder (Matrix router, or a build with no perf monitor) must read as
// "no reading" rather than panic.
func TestLiveVramHolder_NilIsNoReading(t *testing.T) {
	var h *liveVramHolder
	if _, ok := h.ceiling(); ok {
		t.Fatal("nil holder reported a usable ceiling")
	}
	h.set(func() (float64, bool) { return 1, true }) // must not panic
	if _, ok := (&liveVramHolder{}).ceiling(); ok {
		t.Fatal("unset holder reported a usable ceiling")
	}
}
