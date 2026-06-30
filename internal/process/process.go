package process

import (
	"context"
	"net/http"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/logmon"
)

type ProcessState string

const (
	StateStopped  ProcessState = ProcessState("stopped")
	StateStarting ProcessState = ProcessState("starting")
	StateReady    ProcessState = ProcessState("ready")
	StateStopping ProcessState = ProcessState("stopping")

	// process is shutdown and will not be restarted
	StateShutdown ProcessState = ProcessState("shutdown")
)

type Process interface {
	// Run starts the process blocks until the process is terminated.
	// The timeout parameter controls how long to wait for the process to get
	// to a ready state to process traffic
	Run(timeout time.Duration) error

	// WaitReady blocks until the process is ready to serve requests
	// or the context is cancelled. It returns nil when the process is ready
	WaitReady(context.Context) error

	// Stop blocks until the process has terminated. It returns nil when
	// the process terminated as expected (exit 0)
	Stop(timeout time.Duration) error

	// State returns the current state of the process
	// Note: this is a snapshot of the state at the time of the call
	// and may change at any time after the call returns.
	State() ProcessState

	// PID returns the OS pid of the live upstream process, or 0 when it is
	// not running. Used to tell our own llama-server children apart from
	// foreign ones when accounting GPU memory.
	PID() int

	// ServeHTTP forwards requests to the underlying process
	// Calling it when the process is not ready will result in a
	// 503 response with a body indicating it is a llama-quartermaster-error
	ServeHTTP(http.ResponseWriter, *http.Request)

	// Logger returns the monitor that captures this process's stdout/stderr.
	Logger() *logmon.Monitor

	// SetPreStop installs a hook run once just before the process is torn down
	// (by TTL, eviction, or explicit Stop), while it is still serving. Used to
	// snapshot live state — e.g. persist the slot KV — before the upstream dies.
	// Call once before serving; safe for concurrent set via atomic store.
	SetPreStop(fn func())

	// SetPostStart installs a hook run once each time the process becomes Ready,
	// before any queued request is granted. Used to prime live state — e.g.
	// restore a saved slot KV — so the first forwarded request reuses it instead
	// of reprefilling. Call once before serving; safe for concurrent set.
	SetPostStart(fn func())

	// SetSpawnArgs installs a hook that rewrites the upstream argv at each spawn,
	// after sanitization and before exec. Used to re-derive GPU/CPU placement
	// from live free VRAM so a stale baked plan can't OOM. Returning an error
	// aborts the start (the caller refuses rather than crashing). nil hook = the
	// argv is used verbatim. Call once before serving; safe for concurrent set.
	SetSpawnArgs(fn func(args []string) ([]string, error))
}
