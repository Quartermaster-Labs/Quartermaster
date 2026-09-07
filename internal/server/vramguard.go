package server

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
	"github.com/quartermaster-labs/quartermaster/internal/process"
)

// vramguard.go is the runtime half of the OOM guard. The spawn-time half lives
// in autogen.LiveOffloadArgs, which sizes (or refuses) a single load against the
// VRAM free right now. That half only ever runs at exec, which leaves two holes
// this file closes:
//
//   - The router's multi-load admission (router.budgetEviction) charges the
//     static vramBudgetGB. With a game holding 8 GB of a 24 GB card it still
//     believes it has 24, so it evicts residents to make room that isn't there
//     and the spawn guard then refuses the load anyway — both models gone, none
//     loaded. vramGuard.ceilingGB feeds the router the live number instead.
//
//   - Nothing reacts AFTER a load. A model resident when a game starts is
//     silently demoted into shared memory by the driver: no error, no log, just
//     collapsed throughput. The watchdog sheds idle models when foreign VRAM
//     grows into the resident set's footprint.
//
// Both read the same sample, taken on the perf monitor's own cadence (>=5s), so
// the admission path stays a lock-free atomic read — it runs on every request
// and every queue drain and must not exec nvidia-smi.

// vramGuard samples how much of the GPU is held by processes that are NOT our
// children and publishes the ceiling the resident model set must fit under.
type vramGuard struct {
	s *Server

	// trusted marks the published reading usable. False when there is no GPU
	// telemetry, or when per-process attribution doesn't cover our own children —
	// the router then keeps using the static budget, which is what shipped before.
	trusted atomic.Bool
	// foreignMB is VRAM (MiB) held by processes that are not our children;
	// totalMB is the card. Both back the reading and the log lines that have to
	// explain WHY the ceiling moved.
	foreignMB atomic.Int64
	totalMB   atomic.Int64
	// foreignFloorMB is the LOW-WATER mark of foreignMB: the desktop's idle cost
	// (compositor, browser, tray apps). It is the baseline the budget was already
	// sized against, so only foreign usage ABOVE it counts as pressure — see
	// ceilingGB. Same shape as Server.systemVramMB, and the same ponytail: with a
	// game already running at startup the floor starts high and only corrects
	// itself once the card is seen idle.
	foreignFloorMB atomic.Int64

	// overSince is when the resident set first stopped fitting the ceiling; zero
	// while it fits. Goroutine-local to run(), no lock needed.
	overSince time.Time
	// untrusted suppresses repeat logging of the "can't attribute VRAM" state.
	untrusted bool
	// lastShed is when the watchdog last unloaded something; zero until it has.
	// It gates the cooldown that keeps a shed from becoming a loop. Same
	// goroutine as overSince.
	lastShed time.Time

	refusals vramRefusals
}

// newVramGuard builds the guard. Its tunables (oomGuardReserveGB /
// oomGuardEvict / oomGuardGraceSec) are read per-use from the Server's live
// offload settings rather than snapshotted here, so a dashboard edit reaches a
// guard that was started once at boot and outlives every hot reload.
func newVramGuard(s *Server) *vramGuard {
	return &vramGuard{s: s}
}

// settings is the live autogen Settings snapshot this guard judges against.
func (g *vramGuard) settings() autogen.Settings { return g.s.offloadSettingsVal() }

// ceilingGB is the router-facing probe (router.LiveVramFn): the VRAM the
// resident model set may occupy right now. ok=false means "no trustworthy
// reading" — the router must then fall back to the static budget rather than
// guess low, since a wrong-low ceiling evicts every resident model for nothing.
//
// It is the configured budget minus the foreign usage that EXCEEDS the desktop's
// idle baseline, NOT minus foreign usage outright. That distinction is the whole
// correctness of the guard. vramBudgetGB is already chosen below the card's
// total precisely to leave the compositor its ~1-2 GB, and estVramGB carries its
// own overhead pads; subtracting that same baseline a second time would report
// pressure on an idle box, so a card legitimately full of our own well-planned
// models would read as an emergency and the watchdog would shed a model for
// nothing. Measured against the floor, an untroubled box yields exactly
// vramBudgetGB and nothing about the router's behaviour changes — the guard only
// speaks up when something else actually took the card.
//
// With no budget configured (multiResident off) the card's total stands in: one
// model at a time is the policy, and the watchdog still has a real ceiling to
// judge a game's arrival against.
func (g *vramGuard) ceilingGB() (float64, bool) {
	totalMB := g.totalMB.Load()
	if !g.trusted.Load() || totalMB <= 0 {
		return 0, false
	}
	budget := g.s.config().VramBudgetGB
	if budget <= 0 {
		budget = float64(totalMB) / 1024.0
	}
	excess := float64(g.foreignMB.Load()-g.foreignFloorMB.Load()) / 1024.0
	if excess <= 0 {
		return budget, true // nothing beyond the usual desktop: budget as configured
	}
	gb := budget - excess - g.settings().OomGuardReserveGB
	if gb < 0 {
		gb = 0
	}
	return gb, true
}

// shedCeilingGB is the watchdog's ceiling, and it is deliberately LOOSER than
// the admission ceiling above: it charges the foreign excess but NOT
// OomGuardReserveGB.
//
// The reserve is headroom for admitting a NEW model into whatever is left of the
// card. Charging it here too made the two halves disagree about the same
// resident model: with a 22.8GB budget and a 21.8GB resident, the first 0.2GB a
// browser tab took dropped the shed ceiling to 21.6GB, the watchdog unloaded the
// model, and the spawn guard - which sizes against live FREE VRAM, where 22.8GB
// was sitting available - re-admitted it unchanged on the next request. The
// model unloaded and reloaded every couple of minutes forever, which reads to a
// user exactly like a too-short ttl. Anything sized within OomGuardReserveGB of
// the budget was condemned to that loop.
//
// Worse, the reserve landed as a step, not a ramp: below the floor the ceiling
// was the full budget, one MiB above it the budget minus a whole extra GB. A
// ceiling that decides whether to KILL a running model has to be continuous in
// the pressure it is reacting to, or a trivial blip evicts a model that fits.
func (g *vramGuard) shedCeilingGB() (float64, bool) {
	totalMB := g.totalMB.Load()
	if !g.trusted.Load() || totalMB <= 0 {
		return 0, false
	}
	budget := g.s.config().VramBudgetGB
	if budget <= 0 {
		budget = float64(totalMB) / 1024.0
	}
	excess := float64(g.foreignMB.Load()-g.foreignFloorMB.Load()) / 1024.0
	if excess <= 0 {
		return budget, true
	}
	if gb := budget - excess; gb > 0 {
		return gb, true
	}
	return 0, true
}

// snapshot reports the current foreign tally, its idle baseline, and the card
// total for the dashboard. ok=false when there is no trustworthy reading.
func (g *vramGuard) snapshot() (foreignMB, floorMB, totalMB int64, ok bool) {
	if !g.trusted.Load() {
		return 0, 0, 0, false
	}
	return g.foreignMB.Load(), g.foreignFloorMB.Load(), g.totalMB.Load(), true
}

// run samples foreign VRAM on every GPU sample and drives the watchdog. Blocks
// until ctx is done; start it once, as a goroutine.
func (g *vramGuard) run(ctx context.Context) {
	if g.s.perf == nil {
		return
	}
	_, gpuCh, unsub := g.s.perf.Subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case gpus, ok := <-gpuCh:
			if !ok {
				return
			}
			g.sample(ctx, gpus)
		}
	}
}

// sample recomputes the ceiling from one GPU reading and, when the resident set
// no longer fits it, runs the watchdog. Split out from run so the accounting is
// testable without a perf monitor.
func (g *vramGuard) sample(ctx context.Context, gpus []perf.GpuStat) {
	best, ok := g.pooledGPU(gpus)
	if !ok {
		g.trusted.Store(false)
		return
	}

	foreign, ok := g.foreignMB4(ctx, best)
	if !ok {
		// Per-process attribution doesn't cover our own children, so every MiB
		// they hold would read as foreign and collapse the ceiling. Publish
		// "unknown" instead; the router falls back to the static budget.
		g.trusted.Store(false)
		if !g.untrusted {
			g.untrusted = true
			g.s.proxylog.Debugf("vramguard: no per-process VRAM attribution for our own children; falling back to the static budget")
		}
		return
	}
	g.untrusted = false

	g.foreignMB.Store(foreign)
	g.totalMB.Store(int64(best.MemTotalMB))
	if cur := g.foreignFloorMB.Load(); !g.trusted.Load() || foreign < cur {
		g.foreignFloorMB.Store(foreign)
	}
	// Store last: it is what marks the other three as valid.
	g.trusted.Store(true)

	g.watchdog()
}

// foreignMB4 attributes the GPU's used memory between our children and everyone
// else, returning the foreign share. ok=false when the attribution can't be
// trusted: we have processes running but the per-process source doesn't list
// them (no source at all on darwin/unix-non-NVIDIA, or a pid that hasn't claimed
// VRAM yet mid-start).
func (g *vramGuard) foreignMB4(ctx context.Context, best perf.GpuStat) (int64, bool) {
	pids := g.s.local.RunningPIDs()
	if len(pids) == 0 {
		// Nothing of ours is on the card, so all of it is foreign. This needs no
		// per-process source at all and is the one reading we can always trust.
		return int64(best.MemUsedMB), true
	}
	procs := perf.QueryComputeApps(ctx)
	if len(procs) == 0 {
		return 0, false
	}
	byPID := make(map[int]int, len(procs))
	for _, p := range procs {
		byPID[p.PID] += p.MemMB
	}
	var ours int64
	for _, pid := range pids {
		mb, seen := byPID[pid]
		if !seen {
			// A child the source can't see would be counted as foreign, which is
			// exactly the mistake that evicts everything. Refuse the whole reading.
			return 0, false
		}
		ours += int64(mb)
	}
	foreign := int64(best.MemUsedMB) - ours
	if foreign < 0 {
		foreign = 0
	}
	return foreign, true
}

// watchdog sheds resident models when foreign VRAM has grown into their
// footprint. It fires only after the condition has held for the grace period:
// VRAM pressure is spiky (a shader compile, a browser tab painting a video) and
// unloading a model on a transient spike costs more than it saves.
//
// Only IDLE models are ever stopped. Killing a model with a request in flight
// trades a slow answer for a failed one, which is strictly worse — if nothing
// idle is left to shed, the guard logs and accepts the degradation.
func (g *vramGuard) watchdog() {
	if evict := g.settings().OomGuardEvict; evict != nil && !*evict {
		return
	}
	ceiling, ok := g.shedCeilingGB()
	if !ok {
		g.overSince = time.Time{}
		return
	}
	// A model shed here is reloaded by the very next request, so a shed that does
	// not clear real pressure is a pure loss: the user pays a full reload (and a
	// KV cache restore) for nothing. Two guards against that. The slack keeps a
	// resident set that is merely a rounding error over the ceiling - estVramGB is
	// an estimate, not a measurement - from being killed on the difference. The
	// cooldown bounds the damage if the arithmetic is wrong anyway: whatever we
	// shed, we do not shed again until the pressure has had time to be real.
	victims, residentGB := g.sheddable(ceiling + vramGuardShedSlackGB)
	if len(victims) == 0 {
		g.overSince = time.Time{}
		return
	}
	if !g.lastShed.IsZero() && time.Since(g.lastShed) < g.cooldown() {
		return
	}
	if g.overSince.IsZero() {
		g.overSince = time.Now()
		g.s.proxylog.Infof("vramguard: resident models (%.1fGB) no longer fit the live ceiling (%.1fGB, %.1fGB held by other GPU apps) - watching for %ds",
			residentGB, ceiling, float64(g.foreignMB.Load())/1024.0, g.settings().OomGuardGraceSec)
		return
	}
	if time.Since(g.overSince) < time.Duration(g.settings().OomGuardGraceSec)*time.Second {
		return
	}
	g.overSince = time.Time{}
	g.lastShed = time.Now()

	g.s.proxylog.Warnf("vramguard: unloading %v to free VRAM - resident %.1fGB over a %.1fGB live ceiling (%.1fGB held by other GPU apps)",
		victims, residentGB, ceiling, float64(g.foreignMB.Load())/1024.0)
	// Asynchronous: Unload blocks until each process has stopped, and the sampler
	// goroutine also feeds the ceiling the router reads while it evicts.
	go g.s.local.Unload(vramGuardUnloadTimeout, victims...)
}

// sheddable returns the models to unload so the resident set fits ceilingGB, and
// the resident set's current estimated footprint. Empty victims means it already
// fits (or nothing may be shed).
//
// Selection mirrors budgetEviction's accounting so the two halves agree on what
// "fits" means, with four exclusions: persistent-group members (never
// evictable), models with no estVramGB (ASR/SAM/TTS run on the CPU — unloading
// them frees no VRAM), models with a request in flight, and models that are
// still STARTING. A starting model reports no in-flight requests because its
// caller is parked on the swap rather than on the upstream, so shedding it would
// silently kill the very load the user just asked for. It is still CHARGED — it
// is claiming VRAM as it loads. Largest-first among what is left, so the fewest
// models are lost to reach the ceiling.
func (g *vramGuard) sheddable(ceilingGB float64) ([]string, float64) {
	cfg := g.s.config()
	groups := cfg.Routing.Router.Settings.Groups
	modelGroup := make(map[string]string)
	for gid, gc := range groups {
		for _, mid := range gc.Members {
			modelGroup[mid] = gid
		}
	}

	type cand struct {
		id string
		gb float64
	}
	var total float64
	var cands []cand
	for id, st := range g.s.local.RunningModels() {
		gb := cfg.Models[id].EstVramGB
		if gb <= 0 {
			continue // CPU-resident: holds no VRAM to reclaim
		}
		total += gb
		if st != process.StateReady {
			continue // starting/stopping: not ours to interrupt
		}
		if groups[modelGroup[id]].Persistent {
			continue
		}
		if n, ok := g.s.local.Inflight(id); ok && n > 0 {
			continue // busy: a failed request is worse than a slow one
		}
		cands = append(cands, cand{id, gb})
	}
	if total <= ceilingGB {
		return nil, total
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].gb > cands[j].gb })

	var victims []string
	freed := total
	for _, c := range cands {
		if freed <= ceilingGB {
			break
		}
		freed -= c.gb
		victims = append(victims, c.id)
	}
	return victims, total
}

// vramGuardShedSlackGB is how far the resident set may exceed the shed ceiling
// before the watchdog acts. estVramGB is a planning estimate with its own pads,
// so an overshoot this small is noise, and shedding on noise costs a reload to
// reclaim VRAM nothing was waiting for.
const vramGuardShedSlackGB = 0.5

// cooldown is the quiet period after a shed. A model that came back is charged
// against the ceiling again immediately, so without this the guard can shed the
// same model on the next sample, forever. Two grace periods, floored at a
// minute: long enough that a model reloaded by a waiting request gets to serve
// it, short enough that a game arriving mid-session is still handled promptly.
func (g *vramGuard) cooldown() time.Duration {
	d := 2 * time.Duration(g.settings().OomGuardGraceSec) * time.Second
	if d < time.Minute {
		d = time.Minute
	}
	return d
}

// vramGuardUnloadTimeout bounds the graceful stop of a model the watchdog sheds.
// Generous on purpose: the point is to hand VRAM back, and a half-killed process
// that leaks its allocation defeats that.
const vramGuardUnloadTimeout = 30 * time.Second

// pooledGPU collapses a sample history into the ONE reading the guard reasons
// about: the summed total and used memory of every eligible adapter. Pooled,
// because the sizer plans a model against the pooled budget and emits a matching
// --tensor-split (issue #4); a ceiling derived from the biggest card alone would
// shed models that fit across the pair perfectly well. With multiGpu off the set
// collapses back to the single device the sizer pins to, which is the behaviour
// that shipped before.
//
// The returned stat is synthetic: only MemTotalMB and MemUsedMB are meaningful,
// and they are exactly what sample and foreignMB4 read. Per-process attribution
// needs no per-device split either, since QueryComputeApps reports a pid's usage
// across every adapter it touches.
func (g *vramGuard) pooledGPU(gpus []perf.GpuStat) (perf.GpuStat, bool) {
	return pooledGPUStat(gpus, g.settings().MultiGpuEnabled())
}

func pooledGPUStat(gpus []perf.GpuStat, multi bool) (perf.GpuStat, bool) {
	eligible := autogen.EligibleGpuStats(gpus, multi)
	if len(eligible) == 0 {
		return perf.GpuStat{}, false
	}
	out := perf.GpuStat{ID: eligible[0].ID, Name: eligible[0].Name, Timestamp: eligible[0].Timestamp}
	for _, e := range eligible {
		out.MemTotalMB += e.MemTotalMB
		out.MemUsedMB += e.MemUsedMB
		if e.Timestamp.After(out.Timestamp) {
			out.Timestamp = e.Timestamp
		}
	}
	if out.MemUsedMB > out.MemTotalMB {
		out.MemUsedMB = out.MemTotalMB
	}
	return out, true
}

// vramRefusals memoises "insufficient VRAM" spawn refusals. Without it every
// retried request pays the full post-eviction reclaim probe loop (~4s of
// nvidia-smi execs) only to be refused again on the same reading — and a client
// that retries in a loop keeps a probe running permanently.
//
// The memo is invalidated by TIME or by VRAM coming back, whichever lands first:
// the whole point of the refusal is that another app holds the card, and the
// moment the user closes that app the next request must load normally rather
// than inherit a stale no.
type vramRefusals struct {
	mu sync.Mutex
	m  map[string]vramRefusal
}

type vramRefusal struct {
	err    error
	freeGB float64
	at     time.Time
}

// vramRefusalTTL is how long a refusal stands on an unchanged free-VRAM reading.
// Short: it is only there to collapse a retry storm, not to cache a verdict.
const vramRefusalTTL = 30 * time.Second

// get returns a still-valid memoised refusal for modelID. freeGB is the current
// (cached, free) reading — a materially higher one than the refusal was made
// against means VRAM was handed back, so the memo is dropped and the caller
// re-runs the real guard.
func (r *vramRefusals) get(modelID string, freeGB float64, freeOK bool) (error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[modelID]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > vramRefusalTTL || (freeOK && freeGB > e.freeGB+vramReclaimEpsilonGB) {
		delete(r.m, modelID)
		return nil, false
	}
	return e.err, true
}

// put records a refusal against the free reading that produced it.
func (r *vramRefusals) put(modelID string, err error, freeGB float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = make(map[string]vramRefusal)
	}
	r.m[modelID] = vramRefusal{err: err, freeGB: freeGB, at: time.Now()}
}

// clear drops any memoised refusal for modelID — called when a spawn is allowed
// through, so a model that loads never leaves a stale no behind it.
func (r *vramRefusals) clear(modelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, modelID)
}

// vramGuardRefusalNote decorates a memoised refusal so a user reading the log
// can tell a cached no from a freshly-probed one.
func vramGuardRefusalNote(err error) error {
	return fmt.Errorf("%w (cached; retrying will not help until VRAM is freed)", err)
}
