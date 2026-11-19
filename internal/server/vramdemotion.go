package server

import (
	"context"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/event"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// A load can pass the placement guard and still not land in VRAM. On Windows,
// when a process allocates past what the video memory manager will keep
// resident, the allocation does not fail: WDDM places the overflow in system
// memory and never moves it back. The model loads and answers, and decode at
// long context runs several times slower, because every step reads the whole
// KV and part of it now crosses PCIe (2026-11-19: 4.75 t/s at 64k instead of
// ~55, with 1.8 GB of llama-server sitting in system RAM).
//
// Nothing BEFORE the load predicts it. The adapter-wide free figure read 20.6 GB
// against a 19.5 GB estimate, and DXGI's per-process Budget is a near-fixed
// slice of the card (~91%) that does not net out what other processes hold.
// What the desktop's compositor and a streaming host really pin is not in
// either number. So the check runs AFTER the load, on the process's own
// counters, and feeds what it finds back into the next placement of that model.
//
// "After the load" means after the first request that generated, not after
// "ready". Quartermaster starts llama-server with --no-warmup, so at ready the
// compute graph has never run and part of the footprint is not committed yet:
// the 27B read 19032 MB in VRAM and 823 MB shared 3 s after ready, and 19789 /
// 922 after one 16-token reply. Measured at ready, that unfinished footprint
// against the full estimate looked like 0.8 GB demoted when nothing was.

const (
	// demotionCheckDelay lets PDH publish the allocations the first generating
	// request just committed.
	demotionCheckDelay = 2 * time.Second
	// demotionFloorMB is the smallest demotion worth reporting. Per-process
	// counters wobble by tens of MB, and the estimate is a model, not a meter.
	demotionFloorMB = 512
)

// demotedMB is how much of a loaded process sits in system memory that the plan
// put in VRAM. Each input alone over-reports:
//
//   - estimate minus dedicated is also large when the ESTIMATE is high (a
//     dynamic-resolution mmproj priced at its maximum image size, checkpoints
//     the sizer reserves but llama keeps in host RAM), with nothing demoted;
//   - shared usage is never zero for a healthy llama-server either: pinned host
//     buffers are deliberate (0.9 GB on the 27B with --no-mmproj-offload).
//
// A demotion shows in BOTH: VRAM comes up short AND the shortfall reappears as
// shared memory. The smaller of the two is what both agree on.
func demotedMB(estGB float64, dedicatedMB, sharedMB int) int {
	short := int(estGB*1024) - dedicatedMB
	return max(0, min(short, sharedMB))
}

// demotionPenalty is how much VRAM the next placement of a model assumes it
// cannot have, and the free reading of the load that was demoted.
type demotionPenalty struct {
	gb     float64
	freeGB float64
}

// demotionTracker joins the spawn-time plan to the post-load measurement. All
// state is in memory: a restart forgets every penalty, which is the right
// default for a condition that a reboot is usually what clears.
type demotionTracker struct {
	mu      sync.Mutex
	plans   map[string]demotionPenalty // model -> {est GB, raw free GB} of the last spawn
	penalty map[string]demotionPenalty
	live    map[string]int  // model -> demoted MB of the process running now
	armed   map[string]bool // model -> ready, its first generating request not seen yet
}

func newDemotionTracker() *demotionTracker {
	return &demotionTracker{
		plans:   map[string]demotionPenalty{},
		penalty: map[string]demotionPenalty{},
		live:    map[string]int{},
		armed:   map[string]bool{},
	}
}

// penaltyGB is the VRAM to hold back when placing model against a raw free
// reading of freeGB. A penalty retires itself once free has risen by at least
// the amount it holds back: whatever pinned the hidden VRAM (a streaming host,
// a heavy compositor) shows up in the free figure too, if only partly, so a
// reading that much higher means it has gone. Without that, one bad evening
// would cost the model its context until the next restart.
func (d *demotionTracker) penaltyGB(model string, freeGB float64) float64 {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.penalty[model]
	if !ok {
		return 0
	}
	if freeGB >= p.freeGB+p.gb {
		delete(d.penalty, model)
		return 0
	}
	return p.gb
}

// planned records what a spawn was placed against. est 0 (no llama placement
// ran) forgets the plan, so the post-load check has nothing to compare.
func (d *demotionTracker) planned(model string, estGB, freeGB float64) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if estGB <= 0 {
		delete(d.plans, model)
		return
	}
	d.plans[model] = demotionPenalty{gb: estGB, freeGB: freeGB}
}

// measured stores a post-load result and reports whether the UI-visible value
// changed. A demotion adds to the model's penalty rather than replacing it: a
// reload that was already held back and still demoted needs more room, not
// the same amount again.
func (d *demotionTracker) measured(model string, mb int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := d.live[model] != mb
	if mb == 0 {
		delete(d.live, model)
		return changed
	}
	d.live[model] = mb
	p := d.penalty[model]
	p.gb += float64(mb) / 1024
	p.freeGB = d.plans[model].freeGB
	d.penalty[model] = p
	return changed
}

// arm marks a freshly ready process as owed one check.
func (d *demotionTracker) arm(model string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed[model] = true
}

// take reports whether model's process is owed its check, and disarms it, so
// only the first generating request of each process runs one.
func (d *demotionTracker) take(model string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.armed[model] {
		return false
	}
	delete(d.armed, model)
	return true
}

// stopped forgets the running process's figure and any check it was still
// owed; the penalty stays for the next spawn. Reports whether a visible figure
// was forgotten.
func (d *demotionTracker) stopped(model string) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.armed, model)
	_, had := d.live[model]
	delete(d.live, model)
	return had
}

func (d *demotionTracker) plan(model string) (float64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.plans[model]
	return p.gb, ok
}

// liveMB is the demoted MB of model's running process, 0 when none or unknown.
func (d *demotionTracker) liveMB(model string) int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.live[model]
}

// watchDemotion arms a check for every model that becomes ready, runs it on
// that process's first generating request, and clears the figure of every
// model that stops. It runs for the server's lifetime.
//
// Two signals, whichever comes first: a streamed reply's first token
// (LiveTokensEvent), so a long digest is checked seconds in rather than when it
// ends, and any completed request (ActivityLogEvent), which covers non-streamed
// calls. Both come from the metrics path, which every inference route and the
// playground's loopback calls pass through.
func (s *Server) watchDemotion(ctx context.Context) {
	fire := func(model string) {
		if s.demotion.take(model) {
			go s.checkDemotion(ctx, model)
		}
	}
	unsubs := []func(){
		event.On(func(e shared.ProcessStateChangeEvent) {
			if e.NewState == string(process.StateReady) {
				s.demotion.arm(e.ProcessName)
				return
			}
			if s.demotion.stopped(e.ProcessName) {
				event.Emit(shared.ModelStatusChangedEvent{Model: e.ProcessName})
			}
		}),
		event.On(func(e shared.LiveTokensEvent) {
			if e.OutputTokens > 0 {
				fire(e.Model)
			}
		}),
		event.On(func(e ActivityLogEvent) { fire(e.Metrics.Model) }),
	}
	go func() {
		<-ctx.Done()
		for _, u := range unsubs {
			u()
		}
	}()
}

func (s *Server) checkDemotion(ctx context.Context, model string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(demotionCheckDelay):
	}
	est, ok := s.demotion.plan(model)
	if !ok {
		return
	}
	pid := s.local.RunningPIDs()[model]
	if pid <= 0 {
		return
	}
	ded, sh, ok := perf.ProcessGpuMemory(pid)
	if !ok {
		return
	}
	mb := demotedMB(est, ded, sh)
	if mb < demotionFloorMB {
		// Info, not Debug: like dynoffload's "baked plan kept" line, a check that
		// only speaks when it fires cannot be told apart from one that never ran.
		s.proxylog.Infof("<%s> vram check: %d MB in VRAM, %d MB in system memory, est %.1f GB - resident as planned", model, ded, sh, est)
		mb = 0
	} else {
		s.proxylog.Warnf("<%s> %.1f GB of this model was placed in SYSTEM memory at load (%d MB in VRAM, %d MB shared, est %.1f GB). "+
			"Windows did this although VRAM read free; long-context decode will be several times slower. "+
			"Reload the model: the next load holds back that much VRAM.", model, float64(mb)/1024, ded, sh, est)
	}
	if s.demotion.measured(model, mb) {
		event.Emit(shared.ModelStatusChangedEvent{Model: model})
	}
}
