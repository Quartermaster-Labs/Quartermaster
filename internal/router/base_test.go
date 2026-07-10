package router

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/router/scheduler"
)

// These tests cover baseRouter's own machinery — the run loop, process
// lifecycle (doSwap), grant/ServeHTTP plumbing, Unload, and Shutdown. The
// scheduling decision logic (queueing, collation, eviction collisions) lives in
// the scheduler package and is tested directly there; see fifo_test.go.

// stubPlanner evicts nothing. baseRouter tests drive the run loop through the
// default FIFO scheduler without exercising any particular eviction policy.
type stubPlanner struct{}

func (s *stubPlanner) EvictionFor(string, []string) []string { return nil }
func (s *stubPlanner) OnSwapStart(string, []string)          {}

func newTestBase(t *testing.T, processes map[string]process.Process, planner scheduler.Swapper) *baseRouter {
	t.Helper()
	conf := config.Config{HealthCheckTimeout: 5}
	b, err := newBaseRouter("test", conf, processes, logmon.NewWriter(io.Discard), planner)
	if err != nil {
		t.Fatalf("newBaseRouter: %v", err)
	}
	b.testProcessed = make(chan struct{}, 64)
	go b.run()
	t.Cleanup(func() {
		if !b.shuttingDown.Load() {
			_ = b.Shutdown(time.Second)
		}
	})
	return b
}

// SetPreEvict must install a hook on every process bound to its own model ID,
// and that hook must fire when the process is stopped (e.g. Unload/eviction/TTL).
func TestBaseRouter_SetPreEvict(t *testing.T) {
	a := newFakeProcess("a")
	a.markReady()
	b := newTestBase(t, map[string]process.Process{"a": a}, &stubPlanner{})

	var got atomic.Pointer[string]
	b.SetPreEvict(func(id string) { got.Store(&id) })

	b.Unload(time.Second, "a")

	if p := got.Load(); p == nil || *p != "a" {
		t.Fatalf("preEvict hook not fired with model id; got %v", p)
	}
}

// SetPostLoad must install a hook on every process bound to its own model ID,
// fired when the process reaches Ready (cold load), before requests are served.
func TestBaseRouter_SetPostLoad(t *testing.T) {
	a := newFakeProcess("a")
	b := newTestBase(t, map[string]process.Process{"a": a}, &stubPlanner{})

	var got atomic.Pointer[string]
	b.SetPostLoad(func(id string) { got.Store(&id) })

	a.markReady() // process becomes Ready

	if p := got.Load(); p == nil || *p != "a" {
		t.Fatalf("postLoad hook not fired with model id; got %v", p)
	}
}

func TestBaseRouter_RunningModels(t *testing.T) {
	ready := newFakeProcess("ready")
	ready.markReady()
	starting := newFakeProcess("starting")
	starting.setState(process.StateStarting)
	stopped := newFakeProcess("stopped")

	b := newTestBase(t, map[string]process.Process{
		"ready": ready, "starting": starting, "stopped": stopped,
	}, &stubPlanner{})

	running := b.RunningModels()
	if len(running) != 2 {
		t.Fatalf("running=%v want 2 entries", running)
	}
	if running["ready"] != process.StateReady {
		t.Errorf("ready state=%q want ready", running["ready"])
	}
	if running["starting"] != process.StateStarting {
		t.Errorf("starting state=%q want starting", running["starting"])
	}
	if _, ok := running["stopped"]; ok {
		t.Errorf("stopped process should be excluded from RunningModels")
	}
}

func TestBaseRouter_UnloadAll(t *testing.T) {
	a := newFakeProcess("a")
	a.markReady()
	c := newFakeProcess("c")
	c.markReady()

	b := newTestBase(t, map[string]process.Process{"a": a, "c": c}, &stubPlanner{})
	b.Unload(time.Second)

	if a.State() != process.StateStopped || c.State() != process.StateStopped {
		t.Fatalf("Unload() should stop every process: a=%q c=%q", a.State(), c.State())
	}
}

// ApplyConfig must live-patch the process set without evicting kept models:
// a running model stays running and is retuned for its next spawn, an added
// model is created, a removed model is dropped from the map and stopped, and the
// config is swapped in — all without a teardown.
func TestBaseRouter_ApplyConfig(t *testing.T) {
	a := newFakeProcess("a")
	a.markReady()
	bproc := newFakeProcess("b")
	bproc.markReady()

	base := newTestBase(t, map[string]process.Process{"a": a, "b": bproc}, &stubPlanner{})

	// Desired process set = every model in cfg.Models; planner evicts nothing.
	base.plan = func(c config.Config) (scheduler.Swapper, map[string]config.ModelConfig, error) {
		want := make(map[string]config.ModelConfig, len(c.Models))
		for id, mc := range c.Models {
			want[id] = mc
		}
		return &stubPlanner{}, want, nil
	}
	created := map[string]*fakeProcess{}
	base.makeProcess = func(id string, _ config.ModelConfig) (process.Process, error) {
		p := newFakeProcess(id)
		created[id] = p
		return p, nil
	}

	// Keep a (changed args), remove b, add c. Bump HealthCheckTimeout to prove the
	// config swaps.
	newCfg := config.Config{
		HealthCheckTimeout: 9,
		Models: map[string]config.ModelConfig{
			"a": {Cmd: "new-args-for-a"},
			"c": {Cmd: "args-for-c"},
		},
	}
	if err := base.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	procs := base.procs()
	if procs["a"] != a {
		t.Fatalf("model a must be the same reused process object (not rebuilt)")
	}
	if a.stopCalls.Load() != 0 {
		t.Errorf("kept model a must not be stopped; stopCalls=%d", a.stopCalls.Load())
	}
	if lc := a.lastConfig.Load(); lc == nil || lc.Cmd != "new-args-for-a" {
		t.Errorf("kept model a must be retuned with the new config; got %v", lc)
	}
	if procs["c"] == nil || created["c"] == nil {
		t.Errorf("added model c must be newly created and present in the map")
	}
	if _, ok := procs["b"]; ok {
		t.Errorf("removed model b must be gone from the process map")
	}
	select {
	case <-bproc.stopStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("removed model b must be stopped")
	}
	if base.cfg().HealthCheckTimeout != 9 {
		t.Errorf("config not swapped live; HealthCheckTimeout=%d want 9", base.cfg().HealthCheckTimeout)
	}
}

// ApplyConfig must be a clean no-op when the new config is invalid: the router
// keeps its current processes and config untouched.
func TestBaseRouter_ApplyConfig_InvalidKeepsState(t *testing.T) {
	a := newFakeProcess("a")
	a.markReady()
	base := newTestBase(t, map[string]process.Process{"a": a}, &stubPlanner{})

	base.plan = func(config.Config) (scheduler.Swapper, map[string]config.ModelConfig, error) {
		return nil, nil, fmt.Errorf("bad config")
	}
	base.makeProcess = func(id string, _ config.ModelConfig) (process.Process, error) {
		return newFakeProcess(id), nil
	}

	if err := base.ApplyConfig(config.Config{HealthCheckTimeout: 9}); err == nil {
		t.Fatalf("ApplyConfig with invalid config should return an error")
	}
	if _, ok := base.procs()["a"]; !ok {
		t.Errorf("model a must survive a rejected ApplyConfig")
	}
	if a.stopCalls.Load() != 0 {
		t.Errorf("no process should be stopped on a rejected ApplyConfig")
	}
	if base.cfg().HealthCheckTimeout == 9 {
		t.Errorf("config must not change on a rejected ApplyConfig")
	}
}

func TestBaseRouter_UnloadSpecificModel(t *testing.T) {
	a := newFakeProcess("a")
	a.markReady()
	c := newFakeProcess("c")
	c.markReady()

	b := newTestBase(t, map[string]process.Process{"a": a, "c": c}, &stubPlanner{})
	b.Unload(time.Second, "a")

	if a.State() != process.StateStopped {
		t.Errorf("a should be stopped, got %q", a.State())
	}
	if c.State() != process.StateReady {
		t.Errorf("c should remain ready, got %q", c.State())
	}
}

// TestBaseRouter_Unload_StopsInParallel verifies that Unload fans out its
// Stop calls concurrently rather than stopping each process serially. Each
// fakeProcess.Stop is pinned via stopBlock; the test only releases them
// after observing every stopStarted, proving all three Stops were in
// flight simultaneously.
func TestBaseRouter_Unload_StopsInParallel(t *testing.T) {
	a := newFakeProcess("a")
	a.markReady()
	a.stopBlock = make(chan struct{})
	pb := newFakeProcess("b")
	pb.markReady()
	pb.stopBlock = make(chan struct{})
	pc := newFakeProcess("c")
	pc.markReady()
	pc.stopBlock = make(chan struct{})

	b := newTestBase(t, map[string]process.Process{"a": a, "b": pb, "c": pc}, &stubPlanner{})

	unloadDone := make(chan struct{})
	go func() {
		b.Unload(time.Second, "a", "b", "c")
		close(unloadDone)
	}()

	// All three Stop calls must start before any of them are allowed to
	// complete. If Unload was serial, only one stopStarted would fire
	// until we released its stopBlock, and this would deadlock.
	for _, p := range []*fakeProcess{a, pb, pc} {
		select {
		case <-p.stopStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("Stop on %s never started — Unload is not parallel", p.id)
		}
	}

	// Release them; Unload should now return.
	close(a.stopBlock)
	close(pb.stopBlock)
	close(pc.stopBlock)

	select {
	case <-unloadDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Unload did not return after stops released")
	}

	for _, p := range []*fakeProcess{a, pb, pc} {
		if p.State() != process.StateStopped {
			t.Errorf("%s state=%q want stopped", p.id, p.State())
		}
		if got := p.stopCalls.Load(); got != 1 {
			t.Errorf("%s stopCalls=%d want 1", p.id, got)
		}
	}
}

func TestBaseRouter_OnDemandStart(t *testing.T) {
	a := newFakeProcess("a")
	a.autoReady = true

	b := newTestBase(t, map[string]process.Process{"a": a}, &stubPlanner{})

	w := httptest.NewRecorder()
	b.ServeHTTP(w, newRequest("a"))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if got := a.runCalls.Load(); got != 1 {
		t.Errorf("runCalls=%d want 1", got)
	}
	if got := a.serveCalls.Load(); got != 1 {
		t.Errorf("serveCalls=%d want 1", got)
	}
}

func TestBaseRouter_ContextCancel(t *testing.T) {
	a := newFakeProcess("a")
	// autoReady=false so swap parks forever until we mark ready.

	b := newTestBase(t, map[string]process.Process{"a": a}, &stubPlanner{})

	ctx, cancel := context.WithCancel(context.Background())
	w1 := httptest.NewRecorder()
	done1 := make(chan struct{})
	go func() {
		b.ServeHTTP(w1, newRequestCtx(ctx, "a"))
		close(done1)
	}()

	w2 := httptest.NewRecorder()
	done2 := make(chan struct{})
	go func() {
		b.ServeHTTP(w2, newRequest("a"))
		close(done2)
	}()

	waitProcessed(t, b.testProcessed, 2) // both requests joined the active swap
	<-a.runStarted

	cancel()
	select {
	case <-done1:
	case <-time.After(time.Second):
		t.Fatal("cancelled ServeHTTP did not return after ctx cancel")
	}

	a.markReady()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("non-cancelled ServeHTTP did not complete after swap")
	}
	if w2.Code != http.StatusOK {
		t.Errorf("second request status=%d body=%q", w2.Code, w2.Body.String())
	}
}

func TestBaseRouter_ModelNotFound(t *testing.T) {
	a := newFakeProcess("a")
	b := newTestBase(t, map[string]process.Process{"a": a}, &stubPlanner{})

	w := httptest.NewRecorder()
	b.ServeHTTP(w, newRequest("unknown"))

	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d want %d body=%q", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestBaseRouter_Shutdown_StopsAllProcesses(t *testing.T) {
	a := newFakeProcess("a")
	a.markReady()
	go a.Run(0)
	pb := newFakeProcess("b")
	pb.markReady()
	go pb.Run(0)

	b := newTestBase(t, map[string]process.Process{"a": a, "b": pb}, &stubPlanner{})

	if err := b.Shutdown(time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := a.stopCalls.Load(); got != 1 {
		t.Errorf("a.stopCalls=%d want 1", got)
	}
	if got := pb.stopCalls.Load(); got != 1 {
		t.Errorf("b.stopCalls=%d want 1", got)
	}

	// Subsequent ServeHTTP should report 5xx.
	w := httptest.NewRecorder()
	b.ServeHTTP(w, newRequest("a"))
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusServiceUnavailable {
		t.Errorf("post-shutdown status=%d want 5xx body=%q", w.Code, w.Body.String())
	}

	// Second Shutdown should report already in progress.
	if err := b.Shutdown(0); err == nil {
		t.Errorf("second Shutdown returned nil, want error")
	}
}
