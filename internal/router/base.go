package router

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/router/scheduler"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

type shutdownReq struct {
	timeout time.Duration
	respond chan error
}

type unloadReq struct {
	targets []string
	timeout time.Duration
	respond chan struct{}
}

type applyConfigReq struct {
	cfg     config.Config
	respond chan error
}

// baseRouter owns the channels, run-loop, and process machinery shared by every
// concrete router. Concrete routers embed *baseRouter and supply a
// scheduler.Swapper describing how eviction sets are decided. baseRouter
// implements scheduler.Effects so the scheduler can call back for side-effects.
type baseRouter struct {
	name string
	// config and processes are read lock-free from many goroutines (ServeHTTP,
	// the perf monitor's RunningModels/RunningPIDs, backend metrics). They are
	// atomic.Pointers so ApplyConfig can swap them for a live config reload:
	// readers snapshot via cfg()/procs(), writes are copy-on-write on the run
	// goroutine. Before ApplyConfig existed these were frozen at construction.
	config    atomic.Pointer[config.Config]
	processes atomic.Pointer[map[string]process.Process]
	logger    *logmon.Monitor
	schedule  scheduler.Scheduler

	// plan and makeProcess are supplied by the concrete router (Group/Matrix) so
	// ApplyConfig can rebuild router-specific state generically. plan derives the
	// eviction planner plus the model→config set of every process that should
	// exist under a config; makeProcess constructs one managed process (it
	// captures the upstream logger the concrete router owns).
	plan        func(config.Config) (scheduler.Swapper, map[string]config.ModelConfig, error)
	makeProcess func(id string, mc config.ModelConfig) (process.Process, error)

	// The per-process hooks, retained so ApplyConfig can wire them onto processes
	// it newly creates for added models. Set once (before serving) via SetPreEvict
	// /SetPostLoad/SetSpawnArgs; stored atomically since ApplyConfig reads them on
	// the run goroutine while Set* writes from the constructing goroutine.
	preEvictFn  atomic.Pointer[func(string)]
	postLoadFn  atomic.Pointer[func(string)]
	spawnArgsFn atomic.Pointer[func(string, []string) ([]string, error)]

	// shutdownCtx governs the request machinery: cancelling it tells grant()
	// and ServeHTTP to stop granting and reject callers. It is deliberately
	// separate from procCtx — see procCtx below.
	shutdownCtx  context.Context
	shutdownFn   context.CancelFunc
	shuttingDown atomic.Bool

	// procCtx is the parent context for every managed process and governs
	// process lifetime only. handleShutdown stops processes gracefully via
	// Stop() and cancels procCtx afterwards, so teardown is never a context
	// cancel racing the graceful path (which collapsed the grace to 100ms and
	// let the caller return before children were reaped — see process run loop).
	procCtx    context.Context
	procCancel context.CancelFunc

	handlerCh     chan scheduler.HandlerReq
	cancelCh      chan scheduler.HandlerReq
	shutdownCh    chan shutdownReq
	unloadCh      chan unloadReq
	applyConfigCh chan applyConfigReq
	swapDoneCh    chan scheduler.SwapDone
	serveDoneCh   chan scheduler.ServeDoneEvent

	runDone chan struct{}

	// testProcessed, when non-nil, receives one event after each handlerReq
	// or swapDone has been fully processed by run(). Tests use it to wait
	// for run() to reach a deterministic state without sleeping. serveDone
	// events are intentionally NOT signalled here so test event counts
	// remain stable.
	testProcessed chan struct{}
}

func newBaseRouter(
	name string,
	conf config.Config,
	processes map[string]process.Process,
	logger *logmon.Monitor,
	planner scheduler.Swapper,
) (*baseRouter, error) {
	shutdownCtx, shutdownFn := context.WithCancel(context.Background())
	procCtx, procCancel := context.WithCancel(context.Background())
	b := &baseRouter{
		name:          name,
		logger:        logger,
		shutdownCtx:   shutdownCtx,
		shutdownFn:    shutdownFn,
		procCtx:       procCtx,
		procCancel:    procCancel,
		handlerCh:     make(chan scheduler.HandlerReq),
		cancelCh:      make(chan scheduler.HandlerReq),
		shutdownCh:    make(chan shutdownReq),
		unloadCh:      make(chan unloadReq),
		applyConfigCh: make(chan applyConfigReq),
		swapDoneCh:    make(chan scheduler.SwapDone),
		serveDoneCh:   make(chan scheduler.ServeDoneEvent),
		runDone:       make(chan struct{}),
	}
	b.config.Store(&conf)
	// The concrete router fills `processes` (the same map) after we return, before
	// serving — safe since construction is single-threaded; later swaps are COW.
	b.processes.Store(&processes)
	sched, err := scheduler.New(conf, name, logger, planner, b)
	if err != nil {
		return nil, err
	}
	b.schedule = sched
	return b, nil
}

// cfg snapshots the live config. Readers that touch several fields call it once.
func (b *baseRouter) cfg() config.Config { return *b.config.Load() }

// procs snapshots the live process map. It is copy-on-write: callers may read
// and range the returned map freely; ApplyConfig never mutates a published map,
// it swaps in a new one.
func (b *baseRouter) procs() map[string]process.Process { return *b.processes.Load() }

// SetPreEvict installs a save-before-stop hook on every managed process. The
// process fires it (with its model ID bound) just before tearing down for ANY
// reason — TTL idle unload, eviction, or explicit Stop — so the slot KV cache
// can snapshot the conversation before the upstream dies. Call once before
// serving; the process map is fixed at construction.
func (b *baseRouter) SetPreEvict(fn func(modelID string)) {
	b.preEvictFn.Store(&fn)
	for id, p := range b.procs() {
		id := id
		p.SetPreStop(func() { fn(id) })
	}
}

// SetPostLoad installs a restore-after-ready hook on every managed process. The
// process fires it (model ID bound) each time it reaches Ready, before the
// triggering request is served — so the slot KV cache can restore a saved
// conversation on cold load. Call once before serving.
func (b *baseRouter) SetPostLoad(fn func(modelID string)) {
	b.postLoadFn.Store(&fn)
	for id, p := range b.procs() {
		id := id
		p.SetPostStart(func() { fn(id) })
	}
}

// SetSpawnArgs installs a spawn-time argv rewriter on every managed process,
// binding each process's model ID. The process fires it at doStart to re-derive
// flags (e.g. GPU/CPU layer placement from live free VRAM) before exec. Call
// once before serving; the process map is fixed at construction.
func (b *baseRouter) SetSpawnArgs(fn func(modelID string, args []string) ([]string, error)) {
	b.spawnArgsFn.Store(&fn)
	for id, p := range b.procs() {
		id := id
		p.SetSpawnArgs(func(args []string) ([]string, error) { return fn(id, args) })
	}
}

// applyHooks wires the retained per-process hooks onto a single process — used
// by ApplyConfig for a model it newly creates, so an added model gets the same
// slot-KV save/restore and dynamic-offload guards as models built at startup.
func (b *baseRouter) applyHooks(id string, p process.Process) {
	if fp := b.preEvictFn.Load(); fp != nil {
		fn := *fp
		p.SetPreStop(func() { fn(id) })
	}
	if fp := b.postLoadFn.Load(); fp != nil {
		fn := *fp
		p.SetPostStart(func() { fn(id) })
	}
	if fp := b.spawnArgsFn.Load(); fp != nil {
		fn := *fp
		p.SetSpawnArgs(func(args []string) ([]string, error) { return fn(id, args) })
	}
}

func (b *baseRouter) notifyProcessed() {
	if b.testProcessed != nil {
		b.testProcessed <- struct{}{}
	}
}

func (b *baseRouter) run() {
	defer close(b.runDone)

	for {
		select {
		case req := <-b.shutdownCh:
			b.handleShutdown(req)
			return

		case req := <-b.handlerCh:
			b.schedule.OnRequest(req)
			b.notifyProcessed()

		case req := <-b.cancelCh:
			b.schedule.OnCancel(req)
			b.notifyProcessed()

		case req := <-b.unloadCh:
			b.schedule.OnUnload(req.targets, req.timeout)
			close(req.respond)
			b.notifyProcessed()

		case req := <-b.applyConfigCh:
			req.respond <- b.handleApplyConfig(req.cfg)
			b.notifyProcessed()

		case ev := <-b.swapDoneCh:
			b.schedule.OnSwapDone(ev)
			b.notifyProcessed()

		case ev := <-b.serveDoneCh:
			b.schedule.OnServeDone(ev)
		}
	}
}

// grant sends a response back to the caller of ServeHTTP and tells us
// whether the caller was still there to receive it.
//
// Each ServeHTTP creates a fresh, UNBUFFERED respond channel and parks in
// a select waiting on it. "Unbuffered" is the important word: a send only
// completes when the other side is actively receiving. So if this send
// succeeds, we know for a fact the caller picked up the response and will
// act on it. If the caller has already given up (its request context was
// cancelled, e.g. the HTTP client disconnected) or the router is shutting
// down, the send never lands, one of the other select cases fires, and we
// report back that the grant did NOT happen.
//
// That distinction matters for in-flight bookkeeping — see GrantServe.
func (b *baseRouter) grant(req scheduler.HandlerReq, resp scheduler.HandlerResp) bool {
	select {
	case req.Respond <- resp:
		return true
	case <-req.Ctx.Done():
		return false
	case <-b.shutdownCtx.Done():
		return false
	}
}

// ModelState implements scheduler.Effects.
func (b *baseRouter) ModelState(modelID string) (process.ProcessState, bool) {
	p, ok := b.procs()[modelID]
	if !ok {
		var zero process.ProcessState
		return zero, false
	}
	return p.State(), true
}

// StartSwap implements scheduler.Effects, launching the swap goroutine.
func (b *baseRouter) StartSwap(modelID string, evict []string) {
	go b.doSwap(modelID, evict)
}

// AbortSwap implements scheduler.Effects. It stops the target of an in-flight
// swap asynchronously so we don't finish loading a model nobody is waiting for
// just to evict it immediately for a queued model. Stopping a StateStarting
// process aborts its start (ErrStartAborted); doSwap's WaitReady then returns
// that error and posts a SwapDone, after which OnSwapDone clears the swap and
// re-drains the queue. Done in a goroutine so the run loop never blocks on Stop.
func (b *baseRouter) AbortSwap(modelID string) {
	p, ok := b.procs()[modelID]
	if !ok {
		return
	}
	go func() {
		if err := p.Stop(b.healthCheckTimeout()); err != nil {
			b.logger.Warnf("%s: aborting swap for %s failed: %v", b.name, modelID, err)
		}
	}()
}

// GrantError implements scheduler.Effects.
func (b *baseRouter) GrantError(req scheduler.HandlerReq, err error) {
	b.grant(req, scheduler.HandlerResp{Err: err})
}

// GrantServe implements scheduler.Effects. It hands the caller a wrapped
// p.ServeHTTP (via trackedServe) so the run loop hears about the request
// finishing, and reports whether the caller received it. The scheduler bumps
// its in-flight count only on a true return: if grant() returns false the
// caller already walked away and trackedServe will never run, so no matching
// decrement will ever arrive — incrementing would strand the counter at >0 and
// the router would never again be willing to evict this model.
func (b *baseRouter) GrantServe(req scheduler.HandlerReq, modelID string) bool {
	p := b.procs()[modelID]
	return b.grant(req, scheduler.HandlerResp{HandleFunc: b.trackedServe(modelID, p)})
}

// StopProcesses implements scheduler.Effects, stopping the named processes in
// parallel and blocking until all have stopped.
func (b *baseRouter) StopProcesses(timeout time.Duration, ids []string) {
	var wg sync.WaitGroup
	procs := b.procs()
	for _, id := range ids {
		p, ok := procs[id]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(id string, p process.Process) {
			defer wg.Done()
			if err := p.Stop(timeout); err != nil {
				b.logger.Warnf("%s: stopping %s failed: %v", b.name, id, err)
			}
		}(id, p)
	}
	wg.Wait()
}

// trackedServe is the wrapper that closes the loop on in-flight tracking.
// It runs p.ServeHTTP normally; the only added behaviour is a deferred
// send on serveDoneCh after the handler returns. That send is what tells
// the run loop "this model now has one fewer request in flight — go look
// at the queue again, you may be able to start a swap you previously had
// to defer."
//
// The select on shutdownCtx.Done() is a release valve: if the router is
// already shutting down, nobody is reading serveDoneCh, so we drop the
// notification rather than blocking the HTTP goroutine forever.
func (b *baseRouter) trackedServe(modelID string, p process.Process) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			select {
			case b.serveDoneCh <- scheduler.ServeDoneEvent{ModelID: modelID}:
			case <-b.shutdownCtx.Done():
			}
		}()
		p.ServeHTTP(w, r)
	}
}

func (b *baseRouter) doSwap(modelID string, toStop []string) {
	timeout := b.healthCheckTimeout()

	procs := b.procs()
	var wg sync.WaitGroup
	for _, mID := range toStop {
		p, ok := procs[mID]
		if !ok {
			continue // model removed by a concurrent ApplyConfig; nothing to stop
		}
		wg.Add(1)
		go func(p process.Process, id string) {
			defer wg.Done()
			if err := p.Stop(timeout); err != nil {
				b.logger.Warnf("%s: stopping %s failed: %v", b.name, id, err)
			}
		}(p, mID)
	}
	wg.Wait()

	target, ok := procs[modelID]
	if !ok {
		// The swap target was removed by an ApplyConfig mid-swap. Report it so
		// OnSwapDone clears the swap and errors any waiters.
		select {
		case b.swapDoneCh <- scheduler.SwapDone{ModelID: modelID, Err: fmt.Errorf("%s: model %q removed during swap", b.name, modelID)}:
		case <-b.shutdownCtx.Done():
		}
		return
	}
	if target.State() == process.StateStopped {
		go func() {
			if err := target.Run(timeout); err != nil {
				b.logger.Warnf("%s: running %s exited: %v", b.name, modelID, err)
			}
		}()
	}

	err := target.WaitReady(b.shutdownCtx)

	select {
	case b.swapDoneCh <- scheduler.SwapDone{ModelID: modelID, Err: err}:
	case <-b.shutdownCtx.Done():
	}
}

func (b *baseRouter) handleShutdown(req shutdownReq) {
	shutdownErr := fmt.Errorf("%s is shutting down", b.name)

	// Cancel shutdownCtx first so any waiter that is currently parked on
	// its respond channel can exit via its own shutdownCtx.Done() branch.
	// The OnShutdown grants below then either land (waiter happened to receive
	// before noticing shutdown) or fall through immediately via grant's
	// shutdownCtx case — either way the waiter sees a non-OK response.
	// This does NOT touch processes: their lifetime is procCtx, cancelled
	// only after the graceful Stop() calls below have reaped them.
	b.shutdownFn()

	b.schedule.OnShutdown(shutdownErr)

	stopTimeout := req.timeout
	if stopTimeout <= 0 {
		stopTimeout = b.healthCheckTimeout()
	}

	var wg sync.WaitGroup
	for i, p := range b.procs() {
		wg.Add(1)
		go func(id string, p process.Process) {
			defer wg.Done()
			if err := p.Stop(stopTimeout); err != nil {
				b.logger.Warnf("%s failed to stop process %s: %v", b.name, id, err)
			}
		}(i, p)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	if req.timeout > 0 {
		select {
		case <-done:
		case <-time.After(req.timeout):
			<-done
		}
	} else {
		<-done
	}

	// Every process is stopped (children reaped via Stop()). Cancel procCtx so
	// the process run-loop goroutines exit; they are already StateStopped, so
	// this is a clean no-op kill rather than a forced teardown.
	b.procCancel()

	req.respond <- nil
}

func (b *baseRouter) healthCheckTimeout() time.Duration {
	t := time.Duration(b.cfg().HealthCheckTimeout) * time.Second
	if t <= 0 {
		return 30 * time.Second
	}
	return t
}

func (b *baseRouter) Handles(model string) bool {
	_, ok := b.procs()[model]
	return ok
}

func (b *baseRouter) ProcessLogger(modelID string) (*logmon.Monitor, bool) {
	if p, ok := b.procs()[modelID]; ok {
		return p.Logger(), true
	}
	return nil, false
}

// Inflight returns the named model's current in-flight request count. The
// processes map keys are fixed at construction and Inflight() reads an
// atomic, so this is safe to call without the run loop.
func (b *baseRouter) Inflight(modelID string) (int64, bool) {
	if p, ok := b.procs()[modelID]; ok {
		return p.Inflight(), true
	}
	return 0, false
}

// LaunchedCmd returns the actual argv the named model's running process spawned
// with, or "" when it is not running. procs() is atomic and LaunchedCmd() reads
// an atomic, so this is safe to call off the run loop.
func (b *baseRouter) LaunchedCmd(modelID string) (string, bool) {
	if p, ok := b.procs()[modelID]; ok {
		return p.LaunchedCmd(), true
	}
	return "", false
}

// RunningModels returns the current state of every process that is not stopped
// or shut down. The processes map keys are fixed at construction and State()
// is a snapshot, so this is safe to call without the run loop.
func (b *baseRouter) RunningModels() map[string]process.ProcessState {
	running := make(map[string]process.ProcessState)
	for id, p := range b.procs() {
		st := p.State()
		if st == process.StateStopped || st == process.StateShutdown {
			continue
		}
		running[id] = st
	}
	return running
}

// RunningPIDs returns the OS pids of every non-stopped local process. Used to
// distinguish our own llama-server children from foreign ones when accounting
// GPU memory. State() is a snapshot and PID() reads an atomic, so this is safe
// to call without the run loop.
func (b *baseRouter) RunningPIDs() []int {
	var pids []int
	for _, p := range b.procs() {
		switch p.State() {
		case process.StateStopped, process.StateShutdown:
			continue
		}
		if pid := p.PID(); pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// Unload stops the named models, or every running model when none are named.
// It blocks until each targeted process has stopped.
//
// The request is funneled through the run loop so eviction is coordinated
// with the rest of the router's state: pending swap waiters for an
// unloaded model are released with an error, queued requests for unloaded
// models are dropped, and any deferred swaps that were waiting on those
// models become eligible to start.
//
// In-flight requests being served by an unloaded process are not waited
// for — Stop kills the upstream, those callers see whatever error the
// reverse proxy surfaces and may retry. Their trackedServe defers fire
// normally and decrement inFlight as the dying handlers return.
func (b *baseRouter) Unload(timeout time.Duration, models ...string) {
	targets := models
	if len(targets) == 0 {
		procs := b.procs()
		targets = make([]string, 0, len(procs))
		for id := range procs {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return
	}

	req := unloadReq{targets: targets, timeout: timeout, respond: make(chan struct{})}
	select {
	case b.unloadCh <- req:
	case <-b.runDone:
		return
	}
	<-req.respond
}

// ApplyConfig live-patches the router to newCfg WITHOUT tearing down running
// processes. It rebuilds the eviction planner + scheduler params, diffs the
// process set (starts added models, stops removed ones, retunes kept ones for
// their next spawn), then atomically swaps the config + process map. Running
// upstreams keep serving; a kept model whose launch args changed picks the new
// args up on its next load. Funnelled through the run loop so it serialises with
// every scheduling decision (the no-locks-in-scheduler invariant). Returns an
// error — leaving the router untouched — when newCfg is invalid (e.g. a model in
// two groups). Retiring the destructive server rebuild, this is how a config
// reload takes effect while the app keeps running.
func (b *baseRouter) ApplyConfig(newCfg config.Config) error {
	req := applyConfigReq{cfg: newCfg, respond: make(chan error, 1)}
	select {
	case b.applyConfigCh <- req:
	case <-b.runDone:
		return fmt.Errorf("%s is shutting down", b.name)
	}
	return <-req.respond
}

// handleApplyConfig runs on the run goroutine. It validates newCfg fully before
// mutating anything, so an invalid config is a clean no-op.
func (b *baseRouter) handleApplyConfig(newCfg config.Config) error {
	planner, want, err := b.plan(newCfg)
	if err != nil {
		return err
	}

	old := b.procs()
	newProcs := make(map[string]process.Process, len(want))
	for id, mc := range want {
		if p, ok := old[id]; ok {
			p.SetConfig(mc) // retune; new launch args apply on this model's next spawn
			newProcs[id] = p
			continue
		}
		p, err := b.makeProcess(id, mc)
		if err != nil {
			return fmt.Errorf("creating process for %q: %w", id, err)
		}
		b.applyHooks(id, p)
		newProcs[id] = p
	}

	// Processes present before but absent from the new config: stop them after the
	// swap so the scheduler stops routing to them first. Best-effort, off the run
	// goroutine so a slow Stop can't stall scheduling.
	var removed []process.Process
	for id, p := range old {
		if _, keep := want[id]; !keep {
			removed = append(removed, p)
		}
	}

	b.config.Store(&newCfg)
	b.processes.Store(&newProcs)
	b.schedule.ApplyConfig(newCfg, planner)

	if len(removed) > 0 {
		go func(procs []process.Process, timeout time.Duration) {
			var wg sync.WaitGroup
			for _, p := range procs {
				wg.Add(1)
				go func(p process.Process) {
					defer wg.Done()
					if err := p.Stop(timeout); err != nil {
						b.logger.Warnf("%s: stopping removed process failed: %v", b.name, err)
					}
				}(p)
			}
			wg.Wait()
		}(removed, b.healthCheckTimeout())
	}
	return nil
}

func (b *baseRouter) Shutdown(timeout time.Duration) error {
	if !b.shuttingDown.CompareAndSwap(false, true) {
		return fmt.Errorf("%s shutdown already in progress", b.name)
	}
	req := shutdownReq{timeout: timeout, respond: make(chan error, 1)}
	select {
	case b.shutdownCh <- req:
	case <-b.runDone:
		return nil
	}
	return <-req.respond
}

func (b *baseRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if b.shuttingDown.Load() {
		shared.SendError(w, req, fmt.Errorf("%s is shutting down", b.name))
		return
	}

	arrived := time.Now()

	data, err := shared.FetchContext(req, b.cfg())
	if err != nil {
		shared.SendError(w, req, err)
		return
	}

	// Snapshot residency before the request is handed to the scheduler: once it
	// is queued the run loop can load the model out from under this read, and
	// both the loading placeholder and the X-QM-Model-Loaded header want to
	// know the state this request arrived to, not the state it caused.
	isModelReady := false
	if p, ok := b.procs()[data.ModelID]; ok {
		isModelReady = p.State() == process.StateReady
	}

	hr := scheduler.HandlerReq{
		Model: data.ModelID,
		Ctx:   req.Context(),
		// Unbuffered: a successful send on Respond proves the waiter is
		// alive and consuming. grant() relies on this to avoid handing a
		// handleFunc to a cancelled waiter and leaking the inFlight count.
		Respond:    make(chan scheduler.HandlerResp),
		PositionCh: make(chan int, 1),
	}

	select {
	case b.handlerCh <- hr:
	case <-req.Context().Done():
		return
	case <-b.shutdownCtx.Done():
		shared.SendError(w, req, fmt.Errorf("%s is shutting down", b.name))
		return
	}

	shouldShowLoading := data.Streaming && data.SendLoadingState && isLoadingPath(req.URL.Path) && !isModelReady

	// Publish what only the scheduler knows, before anything can write the
	// response header. X-QM-* is namespaced and additive: an OpenAI client
	// never sees it, a client that wants to know why a request was slow can
	// read it without parsing the body. The loading placeholder flushes the
	// header itself, so the wait time below lands only when it is not running.
	respHeader := w.Header()
	respHeader.Set("X-QM-Model", data.ModelID)
	if isModelReady {
		respHeader.Set("X-QM-Model-Loaded", "0")
	} else {
		respHeader.Set("X-QM-Model-Loaded", "1")
	}

	var lw *loadingWriter
	cancelLoad := func() {}
	if shouldShowLoading {
		var swapCtx context.Context
		swapCtx, cancelLoad = context.WithCancel(req.Context())
		lw = newLoadingWriter(b.logger, data.ModelID, w, req)
		go lw.start(swapCtx)
		go func() {
			for {
				select {
				case pos := <-hr.PositionCh:
					lw.setUpdate(fmt.Sprintf("Queue position: #%d", pos))
				case <-swapCtx.Done():
					return
				}
			}
		}()
	}

	// finishLoading stops the loading stream and fences its goroutine off from
	// the ResponseWriter before the real handler (or ServeHTTP's return)
	// reclaims it. release() must run even when waitForCompletion times out:
	// otherwise a still-streaming goroutine flushes a finalized response and
	// panics on the recycled *bufio.Writer.
	finishLoading := func() {
		cancelLoad()
		if lw != nil {
			lw.waitForCompletion(1 * time.Second)
			lw.release()
		}
	}

	var resp scheduler.HandlerResp
	select {
	case resp = <-hr.Respond:
		finishLoading()
	case <-req.Context().Done():
		finishLoading()
		// Notify the scheduler so it can prune this request from its queue
		// and swap waiters. Without this, a queued request whose client left
		// would sit in the scheduler until drainQueue eventually starts a
		// wasted model load for it.
		select {
		case b.cancelCh <- hr:
		case <-b.shutdownCtx.Done():
		}
		return
	case <-b.shutdownCtx.Done():
		finishLoading()
		shared.SendError(w, req, fmt.Errorf("%s is shutting down", b.name))
		return
	}

	if resp.Err != nil {
		shared.SendError(w, req, resp.Err)
		return
	}

	// Queue time plus any model load: everything between arriving here and the
	// upstream being handed the request.
	respHeader.Set("X-QM-Wait-Ms", strconv.FormatInt(time.Since(arrived).Milliseconds(), 10))
	resp.HandleFunc(w, req)
}
