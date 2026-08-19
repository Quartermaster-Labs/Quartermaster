package scheduler

import (
	"io"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/process"
)

// Tests for the idle-grace hold: after a model's last request drains it stays
// un-evictable for a window, so an agent loop's next round (which arrives after
// a tool call, looking exactly like a new conversation) finds it still warm.
//
// These drive the scheduler synchronously like the rest of the suite, and never
// sleep: fakeEffects records Wake requests instead of arming timers, and a hold
// is "expired" by rewriting its deadline into the past and calling OnWake —
// which is exactly what the run loop does when the real timer fires.

// holdFIFO builds a scheduler with a long hold window and a long patience, so
// neither expires by accident during a test.
func holdFIFO(planner Swapper, eff Effects) *FIFO {
	return NewFIFO("test", logmon.NewWriter(io.Discard), planner,
		config.FifoConfig{HoldMs: intp(10_000), PatienceMs: intp(300_000)}, nil, eff)
}

// expire rewinds a model's hold deadline, standing in for the passage of time.
func expire(s *FIFO, model string) {
	s.hold[model] = time.Now().Add(-time.Second)
}

func TestFIFO_HoldDefersEviction(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	eff.states["b"] = process.StateStopped
	s := holdFIFO(&stubPlanner{evict: map[string][]string{"b": {"a"}}}, eff)

	s.OnRequest(req("a"))
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})

	// a is idle but held: b must wait rather than evict it.
	s.OnRequest(req("b"))
	if got := eff.startsFor("b"); got != 0 {
		t.Fatalf("StartSwap(b)=%d want 0 while a is held", got)
	}
	if len(s.queued) != 1 {
		t.Fatalf("queued=%d want 1", len(s.queued))
	}
	// The hold is a wall-clock deadline no other event announces, so the
	// scheduler must have asked to be woken for it.
	if len(eff.wakes) == 0 {
		t.Fatal("no Wake requested; the queued request would wait forever")
	}

	expire(s, "a")
	s.OnWake()
	if got := eff.startsFor("b"); got != 1 {
		t.Fatalf("StartSwap(b)=%d want 1 once the hold lapsed", got)
	}
}

func TestFIFO_HoldYieldsToImpatientCaller(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	eff.states["b"] = process.StateStopped
	s := holdFIFO(&stubPlanner{evict: map[string][]string{"b": {"a"}}}, eff)

	s.OnRequest(req("a"))
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})

	// Patience < 0 means "I do not wait behind holds" — the escape hatch that
	// keeps an interactive turn from sitting behind a background agent loop.
	r := req("b")
	r.Patience = -1
	s.OnRequest(r)

	if got := eff.startsFor("b"); got != 1 {
		t.Fatalf("StartSwap(b)=%d want 1 for a caller with no patience", got)
	}
}

func TestFIFO_PatienceExpiryPreemptsRenewedHold(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	eff.states["b"] = process.StateStopped
	s := holdFIFO(&stubPlanner{evict: map[string][]string{"b": {"a"}}}, eff)

	s.OnRequest(req("a"))
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})

	// b arrived a long time ago and its patience is short: it has waited out
	// the incumbent and now preempts, even though the hold is still live. This
	// is what stops a hold renewed every round from starving the other loop.
	r := req("b")
	r.Arrived = time.Now().Add(-time.Minute)
	r.Patience = time.Second
	s.OnRequest(r)

	if got := eff.startsFor("b"); got != 1 {
		t.Fatalf("StartSwap(b)=%d want 1 once patience ran out", got)
	}
	if _, held := s.hold["a"]; !held {
		t.Fatal("hold on a was cleared; patience should preempt it, not delete it")
	}
}

func TestFIFO_HoldWaitNeverExceedsPatience(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	eff.states["b"] = process.StateStopped
	s := holdFIFO(&stubPlanner{evict: map[string][]string{"b": {"a"}}}, eff)

	s.OnRequest(req("a"))
	s.OnServeDone(ServeDoneEvent{ModelID: "a"}) // 10s hold
	eff.wakes = nil

	r := req("b")
	r.Patience = 500 * time.Millisecond
	s.OnRequest(r)

	if len(eff.wakes) != 1 {
		t.Fatalf("wakes=%d want 1", len(eff.wakes))
	}
	// Waking at the 10s hold expiry would leave this caller queued 9.5s past
	// the point it was entitled to preempt.
	if eff.wakes[0] > 500*time.Millisecond {
		t.Fatalf("Wake(%s) want <= 500ms (the caller's remaining patience)", eff.wakes[0])
	}
}

func TestFIFO_HoldWindowFromCaller(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	s := holdFIFO(&stubPlanner{}, eff)

	// X-QM-Hold-Ms: an explicit 0 arrives as -1 and means "no hold" — a client
	// that knows its loop just ended must be able to release the GPU at once,
	// and that has to be distinguishable from sending no header at all.
	r := req("a")
	r.Hold = -1
	s.OnRequest(r)
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})
	if _, held := s.hold["a"]; held {
		t.Fatal("hold granted despite an explicit X-QM-Hold-Ms: 0")
	}

	r = req("a")
	r.Hold = 250 * time.Millisecond
	s.OnRequest(r)
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})
	until, held := s.hold["a"]
	if !held {
		t.Fatal("no hold granted for a caller that asked for one")
	}
	if d := time.Until(until); d > 250*time.Millisecond {
		t.Fatalf("hold window %s want <= 250ms (the caller's own)", d)
	}
}

func TestFIFO_HoldReleasedWhenModelStops(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	s := holdFIFO(&stubPlanner{}, eff)

	s.OnRequest(req("a"))
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})
	if _, held := s.hold["a"]; !held {
		t.Fatal("expected a hold on a")
	}

	// Unloading stops the process. Protecting a model that no longer exists
	// would keep queued requests waiting for nothing.
	s.OnUnload([]string{"a"}, time.Second)
	if _, held := s.hold["a"]; held {
		t.Fatal("hold survived the model being unloaded")
	}
}

func TestFIFO_HoldDisabledByConfig(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	eff.states["b"] = process.StateStopped
	s := newFIFO(&stubPlanner{evict: map[string][]string{"b": {"a"}}}, eff) // HoldMs: 0

	s.OnRequest(req("a"))
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})
	s.OnRequest(req("b"))

	if got := eff.startsFor("b"); got != 1 {
		t.Fatalf("StartSwap(b)=%d want 1 with holds disabled", got)
	}
}

// A queued request is told its position, and told 0 when it leaves the queue.
// The zero is the whole point: it is the only signal that the wait stopped
// being a wait for a turn and became a model load, which is what the playground
// renders as "Waiting its turn" versus "Loading model".
func TestFIFO_PositionZeroOnPromotion(t *testing.T) {
	eff := newFakeEffects()
	eff.states["a"] = process.StateReady
	eff.states["b"] = process.StateStopped
	s := holdFIFO(&stubPlanner{evict: map[string][]string{"b": {"a"}}}, eff)

	s.OnRequest(req("a"))
	s.OnServeDone(ServeDoneEvent{ModelID: "a"})

	r := req("b")
	r.PositionCh = make(chan int, 1)
	s.OnRequest(r) // queued behind a's hold
	if got := <-r.PositionCh; got != 1 {
		t.Fatalf("position=%d want 1 while queued", got)
	}

	expire(s, "a")
	s.OnWake()
	if got := <-r.PositionCh; got != 0 {
		t.Fatalf("position=%d want 0 once promoted into the swap", got)
	}
}
