package scheduler

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// defaultConcurrencyLimit caps simultaneous in-flight requests per model when
// the model config leaves concurrencyLimit unset.
const defaultConcurrencyLimit = 10

// The hold: an agent loop is a burst of requests separated by however long its
// tool calls take, and between rounds it looks exactly like a finished
// conversation. Without a hold, two competing loops on different models trade
// the GPU every single round -- one tool call each, then a full reload -- which
// costs far more in swaps than either loop spends generating.
//
// holdWindowDefault is how long a model stays un-evictable after its last
// request drains. Sized to a tool call, not to a model load: too short and the
// hold never fires at all, because the next round always arrives after it
// lapsed.
//
// patienceDefault bounds the other side. A hold is renewed by every round, so
// on its own it would let one loop keep the GPU indefinitely; a queued request
// stops honouring holds once it has waited this long, takes the GPU, and
// becomes the incumbent itself. Patience belongs to the WAITER rather than the
// incumbent because only the waiter knows what the delay costs it -- five
// minutes is right for a background agent and unbearable for someone watching
// a chat, and both can be queued for the same model at once.
const (
	holdWindowDefault = 10 * time.Second
	patienceDefault   = 5 * time.Minute
)

// activeSwap tracks one in-flight swap and the callers waiting on it.
type activeSwap struct {
	modelID string
	evict   []string
	waiters []HandlerReq
	// aborting is set once we've asked Effects to abort this (abandoned) swap,
	// so repeated reap passes don't fire AbortSwap again before its SwapDone.
	aborting bool
}

// FIFO is the default scheduler. Requests are handled in a first-in, first-out order.
// To reduce swapping requests for a model that is already running will be handled
// immediately by the running process.
//
// Requests into this schedule are handled like this:
//
// A B C A B C --> A A B B C C
//
// The strategy is simple and reduces the number of swaps required.
type FIFO struct {
	name    string
	logger  *logmon.Monitor
	planner Swapper
	cfg     config.FifoConfig
	effects Effects

	limits   map[string]int
	active   map[string]*activeSwap
	inFlight map[string]int
	queued   []HandlerReq

	// imageModels is the set of model IDs whose Capabilities.Out includes
	// "image" (sd-server diffusion models). Their render peak VRAM (sampler +
	// VAE decode) runs above the steady-state --max-vram cap, so no other model
	// may spawn alongside an in-flight render — see OnRequest step (3b).
	imageModels map[string]bool

	// useTick is a monotonic counter stamped into lastUse whenever a model is
	// granted a handler or swapped in. It orders runningSet least-recently-used
	// first, which is what lets a budget-aware Swapper pick victims. A counter
	// rather than a clock: it needs only an ordering, and it keeps tests
	// deterministic without a fake clock.
	useTick int64
	lastUse map[string]int64

	// hold maps model ID -> the instant its idle-grace window expires. Set when
	// a model's last in-flight request drains, cleared when it lapses or the
	// model stops. Only models with a live entry are protected.
	hold map[string]time.Time
	// holdFor maps model ID -> the window to apply when its next request
	// drains, recorded at grant time because ServeDoneEvent carries only the
	// model ID while the window is a property of the REQUEST that asked for it.
	holdFor map[string]time.Duration
}

// NewFIFO builds a FIFO scheduler. Per-model concurrency limits are derived
// from models: each model's ConcurrencyLimit overrides defaultConcurrencyLimit
// when set to a value greater than zero.
func NewFIFO(name string, logger *logmon.Monitor, planner Swapper, cfg config.FifoConfig, models map[string]config.ModelConfig, eff Effects) *FIFO {
	limits, imageModels := deriveModelParams(models)
	return &FIFO{
		name:        name,
		logger:      logger,
		planner:     planner,
		cfg:         cfg,
		effects:     eff,
		limits:      limits,
		active:      make(map[string]*activeSwap),
		inFlight:    make(map[string]int),
		imageModels: imageModels,
		lastUse:     make(map[string]int64),
		hold:        make(map[string]time.Time),
		holdFor:     make(map[string]time.Duration),
	}
}

// deriveModelParams computes the per-model concurrency limits and the image-model
// set from a config's model map — the two purely config-derived lookups FIFO
// keeps. Shared by NewFIFO and ApplyConfig.
func deriveModelParams(models map[string]config.ModelConfig) (limits map[string]int, imageModels map[string]bool) {
	limits = make(map[string]int, len(models))
	imageModels = make(map[string]bool)
	for id, mc := range models {
		limit := defaultConcurrencyLimit
		if mc.ConcurrencyLimit > 0 {
			limit = mc.ConcurrencyLimit
		}
		limits[id] = limit
		for _, out := range mc.Capabilities.Out {
			if out == "image" {
				imageModels[id] = true
				break
			}
		}
	}
	return limits, imageModels
}

// ApplyConfig live-swaps the config-derived inputs while leaving active/queued/
// in-flight state untouched. See Scheduler.ApplyConfig.
func (s *FIFO) ApplyConfig(conf config.Config, planner Swapper) {
	s.planner = planner
	s.cfg = conf.Routing.Scheduler.Settings.Fifo
	s.limits, s.imageModels = deriveModelParams(conf.Models)
}

// OnRequest decides what to do with one incoming ServeHTTP request. It never
// blocks indefinitely: any work that has to wait (starting a process, stopping
// siblings, waiting for ready) is deferred to a swap goroutine and reported back
// via OnSwapDone.
//
// The decision tree, in order:
//
//  1. Unknown model — respond with ErrModelNotFound and move on.
//
//  2. A swap to the same model is already in flight — attach this waiter so
//     one swap serves all callers that asked for the same model.
//
//  3. Fast path — the target process is already ready, the planner sees
//     nothing to evict, and no in-flight swap is evicting it. Hand back its
//     ServeHTTP immediately.
//
//  4. Would collide with an in-flight swap (we'd stop their target, or they're
//     stopping us) — park in the queue for OnSwapDone to drain.
//
//  5. Would evict a process that is still handling requests — park in the
//     queue. OnServeDone will retry when the busy process drains.
//
//     5b. Would evict a model inside its hold window while this caller still has
//     patience left — park in the queue. The hold lapsing, or this caller's
//     patience running out, brings us back here via OnWake.
//
//  6. Otherwise — start a new swap. This may run in parallel with other active
//     swaps when their evict sets don't intersect.
func (s *FIFO) OnRequest(req HandlerReq) {
	// (1) Unknown model.
	state, ok := s.effects.ModelState(req.Model)
	if !ok {
		s.logger.Debugf("%s: model %s not handled by this router", s.name, req.Model)
		s.effects.GrantError(req, ErrModelNotFound)
		return
	}

	// (2) Join an in-flight swap for the same model.
	if sw, ok := s.active[req.Model]; ok {
		s.logger.Debugf("%s: joining in-flight swap for model %s (%d waiters)", s.name, req.Model, len(sw.waiters)+1)
		sw.waiters = append(sw.waiters, req)
		return
	}

	running := s.runningSet(req.Model)
	evict := s.planner.EvictionFor(req.Model, running)

	// (3) Fast path: ready, nothing to evict, and nobody is evicting us.
	if state == process.StateReady && len(evict) == 0 && !collidesWith(req.Model, evict, s.active) {
		s.logger.Debugf("%s: fast-path serving model %s (already ready)", s.name, req.Model)
		s.grantHandler(req, req.Model)
		return
	}

	// (3b) An image generation is rendering. It holds the GPU, and its true
	// peak VRAM (sampler + VAE decode) runs above the steady-state --max-vram
	// cap, so spawning any other model alongside it can OOM the render
	// mid-sample. Defer every non-image spawn until the render's serve
	// completes — independent of what EvictionFor computed, so a policy that
	// fails to mark the image model for eviction (co-resident set, stale plan)
	// still can't preempt it. The fast path above is exempt: it serves an
	// already-ready model without a spawn, adding no VRAM pressure.
	if s.imageRenderInFlight() && !s.imageModels[req.Model] {
		s.logger.Debugf("%s: queuing request for model %s (image generation rendering)", s.name, req.Model)
		s.enqueue(req)
		return
	}

	// (4) Collision with an in-flight swap — queue. If a colliding swap has been
	// abandoned (its waiters all disconnected), abort it now rather than letting
	// it load to completion only to be evicted for this request.
	if collidesWith(req.Model, evict, s.active) {
		s.logger.Debugf("%s: queuing request for model %s (collides with in-flight swap)", s.name, req.Model)
		s.enqueue(req)
		s.reapAbandonedSwaps()
		return
	}

	// (5) Would evict a busy process — queue until it drains.
	if conflictsWithInFlight(evict, s.inFlight) {
		s.logger.Debugf("%s: queuing request for model %s (would evict in-flight process)", s.name, req.Model)
		s.enqueue(req)
		return
	}

	// (5b) Would evict a model that is still inside its hold window.
	if held, wait := s.heldAgainst(req, evict, time.Now()); held != "" {
		s.logger.Debugf("%s: queuing request for model %s (%s held for another %s)", s.name, req.Model, held, wait.Round(time.Millisecond))
		s.enqueue(req)
		s.effects.Wake(wait)
		return
	}

	// (6) Start a new (possibly parallel) swap.
	s.logger.Debugf("%s: starting swap for model %s, evicting %v", s.name, req.Model, evict)
	s.startSwap(req, evict, running)
}

// holdWindow is how long req's model should be protected once req drains: the
// caller's own X-QM-Hold-Ms if it sent one, else the configured default.
func (s *FIFO) holdWindow(req HandlerReq) time.Duration {
	if req.Hold != 0 {
		if req.Hold < 0 {
			return 0
		}
		return req.Hold
	}
	if s.cfg.HoldMs != nil {
		return time.Duration(*s.cfg.HoldMs) * time.Millisecond
	}
	return holdWindowDefault
}

// patience is how long req tolerates waiting behind another model's hold.
func (s *FIFO) patience(req HandlerReq) time.Duration {
	if req.Patience != 0 {
		if req.Patience < 0 {
			return 0
		}
		return req.Patience
	}
	if s.cfg.PatienceMs != nil {
		return time.Duration(*s.cfg.PatienceMs) * time.Millisecond
	}
	return patienceDefault
}

// heldAgainst reports whether serving req would evict a model that is still
// inside its hold window, returning that model and how long the caller must
// wait for it. A caller past its patience is told "no": it has waited long
// enough and now preempts the incumbent, which is what stops a hold renewed
// every round from starving it. The wait returned is never longer than the
// caller's remaining patience, so the OnWake it schedules lands when this
// request becomes servable, whichever of the two deadlines comes first.
func (s *FIFO) heldAgainst(req HandlerReq, evict []string, now time.Time) (string, time.Duration) {
	if len(evict) == 0 || len(s.hold) == 0 {
		return "", 0
	}
	left := s.patienceLeft(req, now)
	if left <= 0 {
		return "", 0
	}
	for _, id := range evict {
		until, ok := s.hold[id]
		if !ok {
			continue
		}
		wait := until.Sub(now)
		if wait <= 0 {
			continue
		}
		if wait > left {
			wait = left
		}
		return id, wait
	}
	return "", 0
}

// patienceLeft is how much of req's patience remains. A request with no Arrived
// stamp (an internal caller, or a test building the struct by hand) is treated
// as having just arrived rather than as having waited forever.
func (s *FIFO) patienceLeft(req HandlerReq, now time.Time) time.Duration {
	p := s.patience(req)
	if p <= 0 {
		return 0
	}
	if req.Arrived.IsZero() {
		return p
	}
	return p - now.Sub(req.Arrived)
}

// expireHolds drops lapsed windows so the map tracks only live holds.
func (s *FIFO) expireHolds(now time.Time) {
	for id, until := range s.hold {
		if !now.Before(until) {
			delete(s.hold, id)
		}
	}
}

// releaseHold drops a model's hold outright, for when the model is stopped
// (unloaded, or evicted by a swap): protecting a process that no longer exists
// would keep a queued request waiting for nothing.
func (s *FIFO) releaseHold(modelID string) {
	delete(s.hold, modelID)
	delete(s.holdFor, modelID)
}

// OnCancel removes a request whose client has disconnected from the queue and
// from every in-flight swap's waiters. If the request was the sole waiter of an
// active swap, the swap goroutine is left to complete on its own — OnSwapDone
// will find no waiters and simply clean up. This prevents drainQueue from ever
// starting a model load for a caller that is no longer there.
func (s *FIFO) OnCancel(req HandlerReq) {
	removed := false

	// Prune from the queue.
	if len(s.queued) > 0 {
		kept := s.queued[:0]
		for _, q := range s.queued {
			if q.Respond == req.Respond {
				removed = true
				continue
			}
			kept = append(kept, q)
		}
		s.queued = kept
	}

	// Prune from any active swap's waiters.
	for _, sw := range s.active {
		filtered := sw.waiters[:0]
		for _, w := range sw.waiters {
			if w.Respond == req.Respond {
				removed = true
				continue
			}
			filtered = append(filtered, w)
		}
		sw.waiters = filtered
	}

	if removed {
		s.logger.Debugf("%s: cancelled request for model %s pruned from scheduler", s.name, req.Model)
		broadcastQueuePositions(s.queued)
		// A cancel may have emptied an active swap's waiters while a queued
		// request still needs that model's slot — abort the now-abandoned load.
		s.reapAbandonedSwaps()
	}
}

// OnSwapDone fans the result out to every waiter that joined this swap, removes
// the swap from the active map, then walks the queue once, promoting any items
// that no longer collide with the remaining active set. FIFO order is preserved:
// items still blocked stay in place.
func (s *FIFO) OnSwapDone(ev SwapDone) {
	sw, ok := s.active[ev.ModelID]
	if !ok {
		return
	}
	delete(s.active, ev.ModelID)
	// Whatever this swap evicted is stopped now; a hold on a process that no
	// longer exists would keep queued requests waiting for nothing.
	for _, id := range sw.evict {
		s.releaseHold(id)
	}

	for _, w := range sw.waiters {
		if ev.Err != nil {
			s.effects.GrantError(w, ev.Err)
		} else {
			s.grantHandler(w, ev.ModelID)
		}
	}

	s.drainQueue()
}

// OnServeDone decrements the per-model in-flight count and, when that drops to
// zero, retries the queue: requests whose swap was deferred because they would
// have evicted this (now-idle) process can now proceed.
func (s *FIFO) OnServeDone(ev ServeDoneEvent) {
	s.inFlight[ev.ModelID]--
	if s.inFlight[ev.ModelID] <= 0 {
		delete(s.inFlight, ev.ModelID)
		// Going idle is the moment an agent loop is mid-tool-call, and the
		// moment the scheduler used to hand the GPU away. Protect the model for
		// one window, then drain anyway: a queued request that is out of
		// patience still goes now, one that is not waits for the Wake.
		if w := s.holdFor[ev.ModelID]; w > 0 {
			s.hold[ev.ModelID] = time.Now().Add(w)
			s.effects.Wake(w)
		}
		s.drainQueue()
	}
}

// OnWake re-examines the queue after a hold window or a waiter's patience has
// elapsed. Both are wall-clock deadlines that no other event announces.
func (s *FIFO) OnWake() {
	s.expireHolds(time.Now())
	s.drainQueue()
}

// OnUnload reconciles router-owned state with the impending Stop, performs the
// Stop (synchronously, via Effects) so callers of Unload remain blocked until
// each targeted process has exited, then drains the queue.
func (s *FIFO) OnUnload(targets []string, timeout time.Duration) {
	unloadErr := fmt.Errorf("%s: model unloaded", s.name)

	targetSet := make(map[string]bool, len(targets))
	for _, id := range targets {
		targetSet[id] = true
	}

	// Release waiters of any in-flight swap whose target is being unloaded.
	// The swap goroutine itself is left to finish on its own; when its
	// SwapDone arrives, OnSwapDone will find no entry in active and drop it.
	for id := range targetSet {
		sw, ok := s.active[id]
		if !ok {
			continue
		}
		for _, w := range sw.waiters {
			s.effects.GrantError(w, unloadErr)
		}
		delete(s.active, id)
	}

	// Drop queued requests addressed to unloaded models. Requests for other
	// models stay queued and may benefit from drainQueue at the end.
	if len(s.queued) > 0 {
		kept := s.queued[:0]
		for _, w := range s.queued {
			if targetSet[w.Model] {
				s.effects.GrantError(w, unloadErr)
				continue
			}
			kept = append(kept, w)
		}
		s.queued = kept
	}

	// Stop the targeted processes. Done synchronously so Unload's caller can
	// rely on "after Unload returns, the process is stopped". inFlight is
	// intentionally NOT cleared here: each dying handler will fire its tracked
	// serve and reach OnServeDone in the normal way.
	s.effects.StopProcesses(timeout, targets)
	for _, id := range targets {
		s.releaseHold(id)
	}

	// Removing entries from active above may have unblocked queued requests
	// that previously collided with the now-cancelled swaps.
	s.drainQueue()
}

// OnShutdown grants err to every waiter still held by the scheduler.
func (s *FIFO) OnShutdown(err error) {
	for _, sw := range s.active {
		for _, w := range sw.waiters {
			s.effects.GrantError(w, err)
		}
	}
	for _, w := range s.queued {
		s.effects.GrantError(w, err)
	}
}

// grantHandler hands the caller a tracked handler for modelID and, only if the
// caller was still there to receive it, bumps the in-flight count. Incrementing
// when the grant failed would strand the counter and block future evictions.
// Requests that would exceed the model's concurrency limit are rejected with a
// shared.NewConcurrencyLimitError (HTTP 429 with Retry-After).
func (s *FIFO) grantHandler(req HandlerReq, modelID string) {
	if s.inFlight[modelID] >= s.limit(modelID) {
		s.effects.GrantError(req, shared.ConcurrencyLimitError{})
		return
	}

	if err := shared.SetReqData(req.Ctx, "fifo_priority", strconv.Itoa(s.cfg.Priority[req.Model])); err != nil {
		s.logger.Debugf("failed to set fifo_priority metadata: %v", err)
	}

	if s.effects.GrantServe(req, modelID) {
		s.inFlight[modelID]++
		s.touch(modelID)
		// Recorded per grant, not per model config: the window belongs to
		// whoever is driving this model right now, and the last caller to be
		// granted is the one whose next round we would be protecting.
		s.holdFor[modelID] = s.holdWindow(req)
	}
}

// touch stamps a model as most-recently-used. Called on every granted handler
// and on every swap start, so a model that was only just loaded is never the
// first LRU victim of the request queued behind it.
func (s *FIFO) touch(modelID string) {
	s.useTick++
	s.lastUse[modelID] = s.useTick
}

// limit returns the per-model concurrency cap, defaulting to
// defaultConcurrencyLimit when the model has no explicit entry.
func (s *FIFO) limit(modelID string) int {
	if l, ok := s.limits[modelID]; ok {
		return l
	}
	return defaultConcurrencyLimit
}

// startSwap records the swap as active and launches it via Effects. running is
// the set EvictionFor saw, forwarded to OnSwapStart so the planner logs against
// the same picture it decided on.
func (s *FIFO) startSwap(initial HandlerReq, evict, running []string) {
	s.active[initial.Model] = &activeSwap{
		modelID: initial.Model,
		evict:   evict,
		waiters: []HandlerReq{initial},
	}
	s.touch(initial.Model)
	s.planner.OnSwapStart(initial.Model, running)
	s.effects.StartSwap(initial.Model, evict)
}

// enqueue inserts req into the queue in priority order: it goes just before the
// first queued item whose priority is strictly lower, so higher-priority models
// are serviced first while equal-priority requests keep their arrival (FIFO)
// order. Priorities come from the FifoConfig; unlisted models default to 0.
func (s *FIFO) enqueue(req HandlerReq) {
	p := s.cfg.Priority[req.Model]
	i := len(s.queued)
	for j, q := range s.queued {
		if s.cfg.Priority[q.Model] < p {
			i = j
			break
		}
	}
	s.queued = append(s.queued, HandlerReq{})
	copy(s.queued[i+1:], s.queued[i:])
	s.queued[i] = req
	broadcastQueuePositions(s.queued)
}

// drainQueue walks the queued requests in order, re-running the OnRequest
// decision tree against the (now smaller) active set. Items that can now start
// or join become satisfied; items still blocked remain queued in original order
// so they get another chance on the next swap completion.
func (s *FIFO) drainQueue() {
	if len(s.queued) == 0 {
		return
	}
	pending := s.queued
	now := time.Now()
	var remaining []HandlerReq
	for _, req := range pending {
		state, ok := s.effects.ModelState(req.Model)
		if !ok {
			s.effects.GrantError(req, ErrModelNotFound)
			continue
		}
		if sw, ok := s.active[req.Model]; ok {
			s.logger.Debugf("%s: queued request for model %s now joining in-flight swap", s.name, req.Model)
			notifyPosition(req, 0)
			sw.waiters = append(sw.waiters, req)
			continue
		}
		running := s.runningSet(req.Model)
		evict := s.planner.EvictionFor(req.Model, running)
		if state == process.StateReady && len(evict) == 0 && !collidesWith(req.Model, evict, s.active) {
			s.logger.Debugf("%s: queued request for model %s now served fast-path", s.name, req.Model)
			notifyPosition(req, 0)
			s.grantHandler(req, req.Model)
			continue
		}
		if s.imageRenderInFlight() && !s.imageModels[req.Model] {
			remaining = append(remaining, req)
			continue
		}
		if collidesWith(req.Model, evict, s.active) {
			remaining = append(remaining, req)
			continue
		}
		if conflictsWithInFlight(evict, s.inFlight) {
			remaining = append(remaining, req)
			continue
		}
		if held, wait := s.heldAgainst(req, evict, now); held != "" {
			remaining = append(remaining, req)
			s.effects.Wake(wait)
			continue
		}
		s.logger.Debugf("%s: queued request for model %s now starting swap, evicting %v", s.name, req.Model, evict)
		notifyPosition(req, 0)
		s.startSwap(req, evict, running)
	}
	s.queued = remaining
	broadcastQueuePositions(s.queued)
	s.reapAbandonedSwaps()
}

// reapAbandonedSwaps aborts any in-flight swap that has lost all its waiters
// (its client(s) disconnected) while a queued request is waiting to evict it.
// Finishing such a load wastes time: it loads a model nobody wants only to stop
// it immediately for the queued model. Aborting makes the swap goroutine post a
// SwapDone, after which OnSwapDone clears it and drainQueue lets the waiting
// request proceed. Swaps that still have waiters are left alone — their callers
// want that model — so this never preempts a wanted load.
func (s *FIFO) reapAbandonedSwaps() {
	for id, sw := range s.active {
		if len(sw.waiters) != 0 || sw.aborting {
			continue
		}
		if !s.queueNeedsEvict(id) {
			continue
		}
		s.logger.Infof("%s: aborting abandoned swap for model %s; a queued request needs its slot", s.name, id)
		sw.aborting = true
		s.effects.AbortSwap(id)
	}
}

// queueNeedsEvict reports whether any queued request's eviction set includes
// modelID — i.e. some waiting request can only proceed once modelID is gone.
func (s *FIFO) queueNeedsEvict(modelID string) bool {
	for _, req := range s.queued {
		running := s.runningSet(req.Model)
		if containsString(s.planner.EvictionFor(req.Model, running), modelID) {
			return true
		}
	}
	return false
}

// runningSet is the live model set handed to the Swapper: every process the
// baseRouter reports as running, unioned with the targets of in-flight swaps
// (excluding excludeActive, the model whose own swap is being decided — its
// in-flight entry must not count as "already running").
//
// Order is LEAST-RECENTLY-USED FIRST, ties broken alphabetically. A Swapper that
// evicts on group membership alone ignores the order; a budget-aware one
// (groupSwapper under vramBudgetGB) walks the slice front-to-back to free the
// least valuable residents first. The tie-break keeps it deterministic — never
// hand the planner map iteration order.
func (s *FIFO) runningSet(excludeActive string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(id string) {
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for id := range s.effects.RunningModels() {
		add(id)
	}
	for _, id := range activeTargets(s.active, excludeActive) {
		add(id)
	}
	sort.Slice(out, func(i, j int) bool {
		ui, uj := s.lastUse[out[i]], s.lastUse[out[j]]
		if ui != uj {
			return ui < uj // never used (0) sorts first: evict it before a used one
		}
		return out[i] < out[j]
	})
	return out
}

// activeTargets returns the IDs of every in-flight swap target except exclude.
// The planner uses this to account for models committed to but not yet reflected
// in process state.
func activeTargets(active map[string]*activeSwap, exclude string) []string {
	if len(active) == 0 {
		return nil
	}
	out := make([]string, 0, len(active))
	for id := range active {
		if id == exclude {
			continue
		}
		out = append(out, id)
	}
	return out
}

// collidesWith reports whether a new swap with this target and evict set can
// safely run alongside the currently active swaps. Same-target callers should
// JOIN (handled before this) — they do not collide with themselves.
func collidesWith(target string, evict []string, active map[string]*activeSwap) bool {
	for id, sw := range active {
		if id == target {
			continue
		}
		if containsString(evict, id) {
			return true
		}
		if containsString(sw.evict, target) {
			return true
		}
		if slicesOverlap(evict, sw.evict) {
			return true
		}
	}
	return false
}

// slicesOverlap reports whether xs and ys share any common element.
func slicesOverlap(xs, ys []string) bool {
	for _, x := range xs {
		if containsString(ys, x) {
			return true
		}
	}
	return false
}

// imageRenderInFlight reports whether any image-output model is currently
// serving a request. While one is, its GPU/VRAM peak makes co-resident spawns
// unsafe, so the scheduler defers other models' swaps (OnRequest step 3b).
func (s *FIFO) imageRenderInFlight() bool {
	for m, n := range s.inFlight {
		if n > 0 && s.imageModels[m] {
			return true
		}
	}
	return false
}

// conflictsWithInFlight reports whether any model in evict is still handling
// requests. Stopping a busy process would cancel its callers' connections, so
// the scheduler defers the swap until those callers finish.
func conflictsWithInFlight(evict []string, inFlight map[string]int) bool {
	for _, m := range evict {
		if inFlight[m] > 0 {
			return true
		}
	}
	return false
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// broadcastQueuePositions sends each queued request its current 1-indexed
// position.
func broadcastQueuePositions(queued []HandlerReq) {
	for i, req := range queued {
		notifyPosition(req, i+1)
	}
}

// notifyPosition tells one request where it stands: a 1-indexed queue position,
// or 0 for "no longer queued" -- promoted into a swap, or served outright. The
// zero matters as much as the positions do: it is the only signal that the wait
// stopped being a wait for a turn and became a model load, and the caller
// (loadingWriter) narrates those two differently.
//
// Sends are non-blocking: if the channel is full, the old value is drained
// first so the consumer always sees the latest position.
func notifyPosition(req HandlerReq, pos int) {
	select {
	case req.PositionCh <- pos:
	default:
		select {
		case <-req.PositionCh:
		default:
		}
		select {
		case req.PositionCh <- pos:
		default:
		}
	}
}
