// Package setup drives quartermaster's first-run wizard: the questions a fresh
// install has to answer (where the models live, which compute backend to
// fetch) and the work that follows from the answers.
//
// # Why this is not part of internal/server
//
// The wizard runs BEFORE there is an install to serve from, and it has to keep
// running while the thing it is installing is written to disk. It therefore
// carries its own tiny HTTP surface and its own copy of the UI bundle rather
// than borrowing the server's.
//
// It is also deliberately ignorant of how it is displayed. cmd/quartermaster-setup
// puts a native WebView2 window in front of it on Windows and falls back to the
// default browser everywhere else, but nothing in this package imports a GUI
// toolkit — that is what keeps the desktop dependency out of the headless
// server binary (see TODO.md "Desktop app - second binary").
//
// # Why the platform work arrives as hooks
//
// Placing files is the one genuinely OS-shaped step: on Windows it means
// driving the Inno Setup installer silently so the Start Menu entry and the
// Add/Remove Programs record are created by the tool that knows how, while on
// unix it is a file copy. Both arrive as Options.Place rather than as build-
// tagged files here, so this package builds identically everywhere and the
// embedded installer blob stays in the command that ships it.
package setup

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Phase is the coarse state of the install, and the only thing the UI switches
// on. Finer detail rides along in Status.Step.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhasePlacing     Phase = "placing"     // running the installer / copying files
	PhaseConfiguring Phase = "configuring" // writing modelsRoot into the generate file
	PhaseBackends    Phase = "backends"    // downloading llama-server et al
	PhaseDone        Phase = "done"
	PhaseError       Phase = "error"
)

// Choices is what the wizard collected. It crosses the HTTP boundary as JSON.
type Choices struct {
	Dir        string   `json:"dir"`        // install directory
	ModelsRoot string   `json:"modelsRoot"` // may be empty: "I'll pick later"
	Variant    string   `json:"variant"`    // vulkan | cuda | rocm | metal | cpu
	Components []string `json:"components"` // backend component ids to install

	// Windows shortcut/startup options. Each maps to an Inno task name; on
	// other platforms placeCopy ignores them. They are plain bools with no
	// defaults on purpose -- the wizard UI owns the defaults (Start Menu on,
	// the rest off), so a value arriving here is always one a user could see.
	StartMenu   bool `json:"startMenu"`
	DesktopIcon bool `json:"desktopIcon"`
	Autostart   bool `json:"autostart"`
}

// Status is the whole of what the UI polls for. One struct, one endpoint: the
// wizard has a single linear job, so there is nothing to correlate by id.
type Status struct {
	Phase      Phase    `json:"phase"`
	Step       string   `json:"step"`             // human label for the current unit of work
	Detail     string   `json:"detail,omitempty"` // e.g. the asset being downloaded
	Downloaded int64    `json:"downloaded"`
	Total      int64    `json:"total"`
	Warnings   []string `json:"warnings,omitempty"`
	Error      string   `json:"error,omitempty"`
	InstallDir string   `json:"installDir,omitempty"`
}

// Options wires the platform-specific work and the defaults the wizard opens with.
type Options struct {
	// DefaultDir pre-fills the install location.
	DefaultDir string

	// Place puts the application's files into c.Dir. On Windows this runs the
	// embedded Inno installer silently; elsewhere it copies the binary. It must
	// be idempotent: a user who backs up and re-runs must not get a broken tree.
	//
	// It takes the whole Choices rather than just the directory because the
	// installer answers more than one question: StartMenu, DesktopIcon and
	// Autostart map to Inno /TASKS= names, and passing them any other way would
	// mean a second pass over the Start Menu and the registry after the install
	// had already finished.
	Place func(c Choices, log func(string)) error

	// Launch starts the finished install. Called after the user clicks through
	// the last step, immediately before the wizard exits.
	Launch func(dir string) error

	// Log receives progress lines. Optional.
	Log func(string)
}

// Wizard owns the wizard's state for one run of the setup program.
type Wizard struct {
	opts  Options
	token string

	mu     sync.Mutex
	st     Status
	busy   bool
	finish chan struct{} // closed when the user asks to launch and quit
}

// New builds a wizard. The token it mints gates every mutating endpoint; see
// api.go for why a loopback bind is not sufficient on its own.
func New(opts Options) *Wizard {
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return &Wizard{
		opts:   opts,
		token:  hex.EncodeToString(b[:]),
		st:     Status{Phase: PhaseIdle},
		finish: make(chan struct{}),
	}
}

// Token is the value the UI must echo back on every mutating request.
func (w *Wizard) Token() string { return w.token }

// Done is closed once the user has finished and the window should close.
func (w *Wizard) Done() <-chan struct{} { return w.finish }

// Status snapshots the current state.
func (w *Wizard) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.st
}

// set mutates the status under the lock.
func (w *Wizard) set(fn func(*Status)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(&w.st)
}

// step moves to a new phase and resets the byte counters, which belong to the
// download that is starting, not to the one that just finished.
func (w *Wizard) step(p Phase, label string) {
	w.opts.Log(label)
	w.set(func(s *Status) {
		s.Phase, s.Step, s.Detail = p, label, ""
		s.Downloaded, s.Total = 0, 0
	})
}

// fail records a terminal error. The wizard stays up so the user can read it
// and retry or quit; a setup program that vanishes on failure tells nobody why.
func (w *Wizard) fail(err error) {
	w.opts.Log("setup failed: " + err.Error())
	w.set(func(s *Status) {
		s.Phase, s.Error = PhaseError, err.Error()
	})
}

// warn attaches a non-fatal problem. Warnings accumulate and are shown on the
// final screen: an install that produced something unrunnable (an upstream
// archive missing its GPU runtime, say) still succeeded in placing the bits.
func (w *Wizard) warn(msg string) {
	w.opts.Log("warning: " + msg)
	w.set(func(s *Status) { s.Warnings = append(s.Warnings, msg) })
}
