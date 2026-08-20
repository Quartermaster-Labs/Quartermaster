package router

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

var (
	ErrNoRouterFound     = shared.ErrNoRouterFound
	ErrNoPeerModelFound  = shared.ErrNoPeerModelFound
	ErrNoLocalModelFound = shared.ErrNoLocalModelFound
)

type Router interface {
	// Shutdown blocks until the router has shutdown returning nil
	// when the router has shutdown successfully.
	//
	// timeout controls how long to wait for inflight requests to finish. After
	// the timeout all inflight requests will be cancelled.
	Shutdown(timeout time.Duration) error

	// ServeHTTP implements the http.Handler and requests coming in will
	// trigger any model swapping and routing logic.
	ServeHTTP(http.ResponseWriter, *http.Request)

	// Handles reports whether this router can serve requests for the given model.
	Handles(model string) bool
}

// LocalRouter is a Router backed by local processes whose state can be
// inspected and which can be individually stopped. Peer routers, which only
// forward to remote hosts, do not implement it.
type LocalRouter interface {
	Router

	// RunningModels returns the current state of every process that is not
	// stopped or shut down, keyed by model ID.
	RunningModels() map[string]process.ProcessState

	// RunningPIDs returns the OS pids of every non-stopped local process, so
	// callers can tell our own llama-server children apart from foreign GPU
	// processes.
	RunningPIDs() []int

	// Unload stops the named models, or every running model when none are
	// named. It blocks until each targeted process has stopped.
	Unload(timeout time.Duration, models ...string)

	// ProcessLogger returns the log monitor for the named model's process.
	// modelID must be a real (non-alias) config key. Returns false when the
	// model is not known to this router.
	ProcessLogger(modelID string) (*logmon.Monitor, bool)

	// Inflight returns the named model's current in-flight request count.
	// Returns false when the model is not known to this router.
	Inflight(modelID string) (int64, bool)

	// LaunchedCmd returns the actual argv the named model's running process
	// spawned with (post rewrite), or "" when it is not running. Returns false
	// when the model is not known to this router.
	LaunchedCmd(modelID string) (string, bool)

	// SetPreEvict installs a hook called with a model ID just before its process
	// is stopped for eviction/unload, while still Ready. Call once before serving.
	SetPreEvict(fn func(modelID string))

	// SetPostLoad installs a hook called with a model ID each time its process
	// becomes Ready, before the triggering request is served. Call once before
	// serving. Used to restore a saved slot KV on cold load.
	SetPostLoad(fn func(modelID string))

	// SetLiveVramBudget installs the live VRAM-ceiling probe the budget admission
	// rule tightens against, so a foreign GPU client (a game, a browser) shrinks
	// the budget the resident model set is admitted into instead of the router
	// evicting for room that is not actually there. No-op on routers with no
	// budget policy. Call once before serving.
	SetLiveVramBudget(fn LiveVramFn)

	// SetSpawnArgs installs a hook that rewrites a model's upstream argv at each
	// spawn (after sanitization, before exec) — e.g. recompute -ngl/--n-cpu-moe
	// from live free VRAM. Returning an error refuses that spawn. Call once
	// before serving.
	SetSpawnArgs(fn func(modelID string, args []string) ([]string, error))

	// ApplyConfig live-patches the router to a reloaded config without tearing
	// down running processes: it rebuilds the eviction planner + scheduler
	// params, diffs the process set (adds/removes/retunes), and swaps config and
	// process map atomically. Running upstreams keep serving; a changed model's
	// new launch args take effect on its next load. Returns an error (leaving the
	// router untouched) when the config is invalid.
	ApplyConfig(cfg config.Config) error
}

// LiveVramFn reports the VRAM (GB) the resident model set may occupy right now:
// the GPU's total minus whatever is held by processes that are NOT our children
// (desktop compositor, a game, a browser, a stray llama-server). ok=false means
// "no trustworthy reading" — callers must then fall back to the static budget
// rather than guess, because a wrong-low ceiling evicts everything for nothing.
type LiveVramFn func() (float64, bool)

// liveVramHolder carries a LiveVramFn from the server (which owns the perf
// monitor) to the Swapper. It exists because the Swapper is rebuilt from scratch
// on every ApplyConfig while the probe is installed once at startup: both sides
// hold this one box instead of the function itself.
//
// A nil holder, or one never filled in, reads as "no reading" — that is the
// case for routers with no budget policy (Matrix) and for any build with no
// perf monitor.
type liveVramHolder struct {
	fn atomic.Pointer[LiveVramFn]
}

func (h *liveVramHolder) ceiling() (float64, bool) {
	if h == nil {
		return 0, false
	}
	fn := h.fn.Load()
	if fn == nil {
		return 0, false
	}
	return (*fn)()
}

func (h *liveVramHolder) set(fn LiveVramFn) {
	if h == nil || fn == nil {
		return
	}
	h.fn.Store(&fn)
}
