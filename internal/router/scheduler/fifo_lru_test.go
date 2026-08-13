package scheduler

import (
	"reflect"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/process"
)

// recordingPlanner captures the running set it was last handed, so a test can
// assert the ORDER the scheduler presents residents in — the input a
// budget-aware Swapper picks its eviction victims from.
type recordingPlanner struct {
	lastRunning []string
}

func (p *recordingPlanner) EvictionFor(_ string, running []string) []string {
	p.lastRunning = append([]string(nil), running...)
	return nil
}

func (p *recordingPlanner) OnSwapStart(string, []string) {}

// The running set is handed to the planner least-recently-used first, not in
// map or alphabetical order: "c" is served last, so it must sort last.
func TestFIFO_RunningSetIsLRUOrdered(t *testing.T) {
	eff := newFakeEffects()
	for _, id := range []string{"a", "b", "c"} {
		eff.states[id] = process.StateReady
	}
	planner := &recordingPlanner{}
	s := newFIFO(planner, eff)

	// Serve in an order that is the reverse of alphabetical, so a stale
	// sort.Strings would still look "right" only by accident.
	s.OnRequest(req("c"))
	s.OnRequest(req("b"))
	s.OnRequest(req("a"))

	// A request for "d" makes the planner see all three as running.
	eff.states["d"] = process.StateStopped
	s.OnRequest(req("d"))

	want := []string{"c", "b", "a"}
	if !reflect.DeepEqual(planner.lastRunning, want) {
		t.Fatalf("running set = %v, want LRU-first %v", planner.lastRunning, want)
	}
}

// A model that has never been served sorts ahead of every used one (it is the
// coldest thing resident), with ties broken alphabetically so the ordering stays
// deterministic across map iterations.
func TestFIFO_RunningSetUnusedFirstThenAlphabetical(t *testing.T) {
	eff := newFakeEffects()
	for _, id := range []string{"a", "z", "m"} {
		eff.states[id] = process.StateReady
	}
	planner := &recordingPlanner{}
	s := newFIFO(planner, eff)

	s.OnRequest(req("m")) // only m has been used

	eff.states["d"] = process.StateStopped
	s.OnRequest(req("d"))

	want := []string{"a", "z", "m"}
	if !reflect.DeepEqual(planner.lastRunning, want) {
		t.Fatalf("running set = %v, want %v", planner.lastRunning, want)
	}
}
