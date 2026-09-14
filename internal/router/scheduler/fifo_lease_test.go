package scheduler

import (
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/process"
)

// A job lease is the scheduler's answer to work that outlives its HTTP request:
// sd-server's async video API answers the POST in milliseconds and renders for
// minutes afterwards. These tests pin the three properties the video proxy
// depends on: a leased model is not evicted, nothing spawns alongside it, and
// the idle-grace hold arms on RELEASE rather than on the 202.

func videoFIFO(eff Effects, planner Swapper) *FIFO {
	models := map[string]config.ModelConfig{
		"vid": {Capabilities: config.ModelCapConfig{In: []string{"text"}, Out: []string{"video"}}},
		"llm": {},
	}
	return NewFIFO("test", nil, planner, config.FifoConfig{HoldMs: intp(0)}, models, eff)
}

// The whole point: after the POST's own request is done, the lease alone must
// keep another model from evicting the renderer.
func TestFIFO_LeaseBlocksEviction(t *testing.T) {
	eff := newFakeEffects()
	eff.states["vid"] = process.StateReady
	eff.states["llm"] = process.StateStopped
	s := videoFIFO(eff, &stubPlanner{evict: map[string][]string{"llm": {"vid"}}})

	s.OnRequest(req("vid")) // the vid_gen POST
	s.OnLease(LeaseEvent{ModelID: "vid", Acquire: true})
	s.OnServeDone(ServeDoneEvent{ModelID: "vid"}) // the 202 lands, request over

	s.OnRequest(reqCh("llm"))
	if got := eff.startsFor("llm"); got != 0 {
		t.Fatalf("StartSwap(llm)=%d, want 0: the lease must defer a swap that evicts a rendering model", got)
	}
	if got := eff.errored("llm"); got != 0 {
		t.Fatalf("errored(llm)=%d, want 0: the request should be QUEUED behind the render, not rejected", got)
	}

	// Render finishes: the deferred swap is retried from the release path.
	s.OnLease(LeaseEvent{ModelID: "vid"})
	if got := eff.startsFor("llm"); got != 1 {
		t.Fatalf("StartSwap(llm)=%d after release, want 1", got)
	}
}

// A render's peak VRAM runs above its steady-state --max-vram cap, so no second
// process may load underneath it even when eviction is not involved.
func TestFIFO_LeaseBlocksCoResidentSpawn(t *testing.T) {
	eff := newFakeEffects()
	eff.states["vid"] = process.StateReady
	eff.states["llm"] = process.StateStopped
	s := videoFIFO(eff, &stubPlanner{}) // no eviction needed for llm

	s.OnRequest(req("vid"))
	s.OnLease(LeaseEvent{ModelID: "vid", Acquire: true})
	s.OnServeDone(ServeDoneEvent{ModelID: "vid"})

	s.OnRequest(reqCh("llm"))
	if got := eff.startsFor("llm"); got != 0 {
		t.Fatalf("StartSwap(llm)=%d, want 0: nothing spawns alongside an in-flight render", got)
	}
	if !s.renderInFlight() {
		t.Error("renderInFlight()=false while a video lease is held")
	}

	s.OnLease(LeaseEvent{ModelID: "vid"})
	if s.renderInFlight() {
		t.Error("renderInFlight()=true after the lease was released")
	}
}

// Releasing a lease must arm the idle-grace hold exactly as a finished request
// does, so the model that just rendered is not handed away instantly.
func TestFIFO_LeaseReleaseArmsHold(t *testing.T) {
	eff := newFakeEffects()
	eff.states["vid"] = process.StateReady
	eff.states["llm"] = process.StateStopped
	models := map[string]config.ModelConfig{
		"vid": {Capabilities: config.ModelCapConfig{In: []string{"text"}, Out: []string{"video"}}},
		"llm": {},
	}
	s := NewFIFO("test", nil, &stubPlanner{evict: map[string][]string{"llm": {"vid"}}},
		config.FifoConfig{HoldMs: intp(2000)}, models, eff)

	s.OnRequest(req("vid"))
	s.OnLease(LeaseEvent{ModelID: "vid", Acquire: true})
	s.OnServeDone(ServeDoneEvent{ModelID: "vid"})
	s.OnLease(LeaseEvent{ModelID: "vid"})

	if _, held := s.hold["vid"]; !held {
		t.Fatal("releasing the last lease should arm the idle-grace hold")
	}
	s.OnRequest(reqCh("llm"))
	if got := eff.startsFor("llm"); got != 0 {
		t.Errorf("StartSwap(llm)=%d during the hold, want 0", got)
	}
}

// The proxy both defers the release and calls it on the terminal poll. The
// router's release is idempotent, but a double delivery here must still not
// drive the counter negative and unpin a model that is genuinely busy.
func TestFIFO_LeaseDoubleReleaseIsHarmless(t *testing.T) {
	eff := newFakeEffects()
	eff.states["vid"] = process.StateReady
	s := videoFIFO(eff, &stubPlanner{})

	s.OnLease(LeaseEvent{ModelID: "vid", Acquire: true})
	s.OnLease(LeaseEvent{ModelID: "vid", Acquire: true})
	s.OnLease(LeaseEvent{ModelID: "vid"})
	s.OnLease(LeaseEvent{ModelID: "vid"})
	s.OnLease(LeaseEvent{ModelID: "vid"})

	if n, ok := s.inFlight["vid"]; ok {
		t.Fatalf("inFlight[vid]=%d after every lease was released, want the entry gone", n)
	}
	if s.renderInFlight() {
		t.Error("renderInFlight()=true with no leases held")
	}
}

// A lease taken on a model that never served a request still gets a hold window,
// so its release does not hand the GPU away in the same instant.
func TestFIFO_LeaseWithoutPriorRequestStillHolds(t *testing.T) {
	eff := newFakeEffects()
	eff.states["vid"] = process.StateReady
	models := map[string]config.ModelConfig{
		"vid": {Capabilities: config.ModelCapConfig{Out: []string{"video"}}},
	}
	s := NewFIFO("test", nil, &stubPlanner{}, config.FifoConfig{HoldMs: intp(1500)}, models, eff)

	s.OnLease(LeaseEvent{ModelID: "vid", Acquire: true})
	s.OnLease(LeaseEvent{ModelID: "vid"})

	if len(eff.wakes) == 0 || eff.wakes[0] != 1500*time.Millisecond {
		t.Errorf("wakes=%v, want a 1.5s hold armed on release", eff.wakes)
	}
}
