package process

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/event"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/peimports"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

var ErrStartAborted = fmt.Errorf("aborted")

// spawnDir is the working directory every upstream is launched in: the
// quartermaster executable's own directory.
//
// Why pin it instead of inheriting: quartermaster's cwd is whatever launched
// it. Started from a shell in the package dir that is the package dir, but
// started from the Windows Run key ("start with the system") it is
// C:\Windows\system32, and from the tray/installer shortcut it can be anything.
// Any relative path in a generated model command (-m, --mmproj, the backend exe
// itself) then resolves against a directory that has none of those files, so the
// spawn fails and the request 500s — while the dashboard, whose own paths were
// absolutised at startup, looks perfectly healthy. Pinning to the exe dir makes
// a model's launch line resolve identically no matter how quartermaster was
// started, and matches where the packaged layout puts the backends/config.
//
// Absolute paths (the common case) are unaffected.
var spawnDir = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return "" // inherit quartermaster's cwd, as before
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return dir
})

// resolveExe pins a relative backend path (e.g. "backends/llama-server.exe") to
// the exe dir. cmd.Dir does NOT cover this: exec resolves the program path
// against the CALLING process's cwd, not against Dir, so the same
// launched-from-anywhere problem spawnDir fixes for arguments would still bite
// the binary itself. A bare command name ("llama-server", no separator) is left
// alone so PATH lookup keeps working, as is anything already absolute or any
// path that does resolve from the current cwd.
func resolveExe(exe string) string {
	if exe == "" || filepath.IsAbs(exe) || !strings.ContainsAny(exe, `/\`) {
		return exe
	}
	dir := spawnDir()
	if dir == "" {
		return exe
	}
	if _, err := os.Stat(exe); err == nil {
		return exe // resolves from the current cwd already
	}
	pinned := filepath.Join(dir, exe)
	if _, err := os.Stat(pinned); err != nil {
		return exe // not there either; let exec produce the original error
	}
	return pinned
}

// cmdWaitDelay is the upper bound the runtime will wait for child I/O to
// drain after the process exits before force-closing the stdout/stderr
// pipes. Required so that cmd.Wait() returns even when a forked grandchild
// inherits and holds the pipes open (e.g. a shell wrapper that backgrounds
// the real binary). killProcess sends the stop signal directly (not via the
// cmd context), so this delay is measured from process exit rather than from
// the stop request, and stays independent of the caller's graceful timeout.
const cmdWaitDelay = 10 * time.Second

// parentCancelGraceTimeout is the graceful timeout used when the process is
// torn down because parentCtx was cancelled (final router teardown or app
// shutdown). In the normal flow the process has already been stopped via
// Stop() by this point, so killProcess is a no-op kill; the short grace just
// bounds the rare case where a process is still alive when its context is cut.
const parentCancelGraceTimeout = time.Second

type runReq struct {
	timeout time.Duration
	respond chan error
}

type stopReq struct {
	timeout time.Duration
	respond chan error
}

type waitReadyReq struct {
	respond chan error
}

type startResult struct {
	cmd       *exec.Cmd
	cmdDone   chan struct{}
	cancel    context.CancelFunc
	handlerFn http.HandlerFunc
	args      []string // final argv the process spawned with (post rewrite)
	err       error
}

type ProcessCommand struct {
	id string
	// liveConfig holds the model config. SetConfig replaces it for a live config
	// reload; the next spawn (doStart) picks up the new command/flags while a
	// running upstream keeps serving with the config it started under. Stored
	// atomically so SetConfig is callable off the run goroutine; every internal
	// reader snapshots it once via cfg().
	liveConfig atomic.Pointer[config.ModelConfig]
	parentCtx  context.Context

	processLogger *logmon.Monitor
	proxyLogger   *logmon.Monitor

	// waitDelay is assigned to cmd.WaitDelay when starting the upstream
	// process. Defaults to cmdWaitDelay; tests override it to keep the
	// pipe-close backstop from dominating their runtime.
	waitDelay time.Duration

	runCh       chan runReq
	stopCh      chan stopReq
	waitReadyCh chan waitReadyReq

	// current ProcessState. Written only by run(); read by State() via atomic load.
	state atomic.Value

	// stores the active reverse-proxy handler when the process is running.
	// Written only by run(); read by ServeHTTP via atomic load.
	handler atomic.Pointer[http.HandlerFunc]

	// launchedArgs holds the actual argv the live process spawned with (post
	// argv-rewrite). Set on StateReady, cleared on teardown. Read by LaunchedCmd
	// so the UI can show what a running model is REALLY serving under, which
	// differs from the current config after a live SetConfig or offload rewrite.
	launchedArgs atomic.Pointer[[]string]

	lastUse  atomic.Int64 // unix nano timestamp of last ServeHTTP completion
	inflight atomic.Int64 // current in-flight ServeHTTP calls
	pid      atomic.Int32 // OS pid of the live upstream; 0 when not running

	// preStop, when set, runs once just before a running process is killed by a
	// Stop (TTL idle, eviction, or explicit). Fired while still StateReady so it
	// can reach the live upstream (e.g. save its slot KV). Atomic: set from another
	// goroutine at startup, read by run().
	preStop atomic.Pointer[func()]

	// postStart, when set, runs once each time the process reaches StateReady,
	// before WaitReady callers are woken - so it can prime the upstream (e.g.
	// restore a saved slot KV) before the first request is forwarded.
	postStart atomic.Pointer[func()]

	// spawnArgs, when set, rewrites the upstream argv at each doStart (after
	// sanitization, before exec) - e.g. recompute -ngl/--n-cpu-moe from live free
	// VRAM. An error aborts the start. Atomic: set from another goroutine at
	// startup, read by doStart.
	spawnArgs atomic.Pointer[func(args []string) ([]string, error)]
}

var _ Process = (*ProcessCommand)(nil)

func New(
	parentCtx context.Context,
	id string,
	conf config.ModelConfig,
	processLogger *logmon.Monitor,
	proxyLogger *logmon.Monitor,
) (*ProcessCommand, error) {
	p := &ProcessCommand{
		id:            id,
		parentCtx:     parentCtx,
		processLogger: processLogger,
		proxyLogger:   proxyLogger,

		runCh:       make(chan runReq),
		stopCh:      make(chan stopReq),
		waitReadyCh: make(chan waitReadyReq),
		waitDelay:   cmdWaitDelay,
	}
	p.liveConfig.Store(&conf)
	p.state.Store(StateStopped)

	go p.run()
	return p, nil
}

func (p *ProcessCommand) Logger() *logmon.Monitor { return p.processLogger }

// cfg snapshots the current model config. Callers that read several fields
// should call it once and use the returned value so a concurrent SetConfig
// can't tear a multi-field read.
func (p *ProcessCommand) cfg() config.ModelConfig { return *p.liveConfig.Load() }

// SetConfig swaps the model config live. A running upstream keeps serving under
// the config it spawned with; the new config takes effect on the next spawn.
// See Process.SetConfig.
func (p *ProcessCommand) SetConfig(c config.ModelConfig) { p.liveConfig.Store(&c) }

// LaunchedCmd returns the actual argv the live process spawned with, or "" when
// not running. See Process.LaunchedCmd.
func (p *ProcessCommand) LaunchedCmd() string {
	if a := p.launchedArgs.Load(); a != nil {
		return strings.Join(*a, " ")
	}
	return ""
}

// SetPreStop installs the pre-teardown hook. See Process.SetPreStop.
func (p *ProcessCommand) SetPreStop(fn func()) { p.preStop.Store(&fn) }

// SetPostStart installs the post-ready hook. See Process.SetPostStart.
func (p *ProcessCommand) SetPostStart(fn func()) { p.postStart.Store(&fn) }

// SetSpawnArgs installs the spawn-time argv rewriter. See Process.SetSpawnArgs.
func (p *ProcessCommand) SetSpawnArgs(fn func(args []string) ([]string, error)) {
	p.spawnArgs.Store(&fn)
}

// run is the single-writer goroutine that owns all mutable lifecycle state
// (current ProcessState, the running *exec.Cmd, the active reverse-proxy
// handler, and the list of WaitReady subscribers). Every public method
// (Run / Stop / State / WaitReady) is a thin client that sends a request on
// one of the channels below and waits for a response - this funnels concurrent
// callers through a single serialization point so the state machine never
// observes a race.
func (p *ProcessCommand) run() {
	// Mutable state - only read/written from this goroutine. ServeHTTP reads
	// p.handler concurrently, which is why handler is an atomic.Pointer.
	// p.state mirrors `state` so State() can observe transitions; setState
	// writes both.
	state := StateStopped
	setState := func(s ProcessState) {
		old := state
		state = s
		p.state.Store(s)
		if old != s {
			event.Emit(shared.ProcessStateChangeEvent{
				ProcessName: p.id,
				OldState:    string(old),
				NewState:    string(s),
			})
		}
	}
	var (
		cmd          *exec.Cmd
		cmdDone      <-chan struct{}
		cmdCancel    context.CancelFunc
		readyWaiters []waitReadyReq
		// runResp parks the in-flight Run caller's response channel. The
		// interface contract is that Run blocks until the process is
		// terminated, so we hold this until Stop, parentCtx, or an
		// upstream exit unblocks it via respondRun.
		runResp chan<- error
	)

	// notifyWaiters wakes every blocked WaitReady caller with the given result.
	// Used on transitions out of StateStarting (ready, failed, aborted, or
	// shutdown) - anything that resolves the "is it ready yet?" question.
	notifyWaiters := func(err error) {
		for _, w := range readyWaiters {
			select {
			case w.respond <- err:
			default:
			}
		}
		readyWaiters = nil
	}

	// respondRun delivers the final Run result, if a Run caller is parked.
	respondRun := func(err error) {
		if runResp != nil {
			runResp <- err
			runResp = nil
		}
	}

	for {
		select {
		// Shutdown: parent context cancelled. Tear down any running process,
		// wake any pending WaitReady callers with an error, then exit the
		// goroutine permanently. Subsequent public-method calls will fail
		// because parentCtx.Done() unblocks their send-side selects.
		case <-p.parentCtx.Done():
			// Mark shutdown before killProcess so concurrent State() readers
			// stop treating this process as ready while the (possibly slow)
			// teardown is in progress.
			setState(StateShutdown)
			if cmd != nil {
				p.handler.Store(nil)
				p.launchedArgs.Store(nil)
				p.killProcess(cmd, cmdCancel, cmdDone, parentCancelGraceTimeout)
				cmd = nil
				cmdDone = nil
				cmdCancel = nil
			}
			notifyWaiters(fmt.Errorf("[%s] shutdown", p.id))
			respondRun(fmt.Errorf("[%s] shutdown", p.id))
			return

		// Upstream exited on its own (not via Stop). Drop handler state,
		// transition to Stopped, and unblock the parked Run caller.
		// cmdDone is nil while no process is running, so this case is
		// dormant outside of StateReady.
		case <-cmdDone:
			if cmdCancel != nil {
				cmdCancel()
			}
			cmd = nil
			cmdDone = nil
			cmdCancel = nil
			p.handler.Store(nil)
			p.launchedArgs.Store(nil)
			setState(StateStopped)
			respondRun(fmt.Errorf("[%s] upstream exited unexpectedly", p.id))

		// WaitReady: if we're already in a terminal-for-this-question state,
		// respond immediately; otherwise queue the caller and let a future
		// state transition wake them via notifyWaiters.
		case req := <-p.waitReadyCh:
			switch state {
			case StateReady:
				req.respond <- nil
			case StateShutdown:
				req.respond <- fmt.Errorf("[%s] shutdown", p.id)
			default:
				readyWaiters = append(readyWaiters, req)
			}

		// Run: start the upstream process. Only valid from StateStopped.
		// doStart can take a long time (health-check polling), so it runs in
		// a separate goroutine and we wait on resultCh. While waiting we also
		// listen for an incoming Stop - that's how callers cancel an in-flight
		// start.
		case req := <-p.runCh:
			if state != StateStopped {
				req.respond <- fmt.Errorf("[%s] could not be started in %s state", p.id, state)
				continue
			}
			setState(StateStarting)

			startCtx, cancelStart := context.WithCancel(context.Background())
			resultCh := make(chan startResult, 1)
			go func() {
				resultCh <- p.doStart(startCtx, req.timeout)
			}()

			// pendingStop holds a Stop request that arrived mid-start, so we
			// can respond to it AFTER we've finished tearing the start down.
			var pendingStop *stopReq
			select {
			// doStart finished on its own - either successfully (latch
			// cmd/handler and move to Ready) or with an error (back to
			// Stopped). Either way wake WaitReady subscribers and reply
			// to the Run caller.
			case res := <-resultCh:
				if res.err == nil {
					cmd = res.cmd
					cmdDone = res.cmdDone
					cmdCancel = res.cancel
					fn := res.handlerFn
					p.handler.Store(&fn)
					la := res.args
					p.launchedArgs.Store(&la)
					setState(StateReady)
					// Prime the upstream (e.g. restore slot KV) before waking any
					// WaitReady caller, so the first forwarded request reuses it.
					if fp := p.postStart.Load(); fp != nil {
						(*fp)()
					}
					notifyWaiters(nil)
					// Park the Run response - Run blocks until the process
					// terminates, so we only fire this when Stop, parentCtx,
					// or the upstream exit takes the process down.
					runResp = req.respond

					// Start TTL goroutine if configured - self-terminates
					// when state leaves StateReady. Snapshot the TTL at ready
					// time; a mid-run SetConfig retunes it on the next spawn.
					if unloadAfter := p.cfg().UnloadAfter; unloadAfter > 0 {
						ttlDuration := time.Duration(unloadAfter) * time.Second
						go func() {
							ticker := time.NewTicker(time.Second)
							defer ticker.Stop()
							for range ticker.C {
								if p.State() != StateReady {
									return
								}
								if p.inflight.Load() != 0 {
									continue
								}
								if time.Since(time.Unix(0, p.lastUse.Load())) > ttlDuration {
									p.proxyLogger.Infof("<%s> Unloading model, TTL of %ds reached", p.id, unloadAfter)
									p.Stop(10 * time.Second)
									return
								}
							}
						}()
					}
				} else {
					// The error travels back to whoever asked for the model, but
					// that is an HTTP body the operator never sees. A failed load
					// is exactly what someone opens this log to diagnose.
					if !errors.Is(res.err, ErrStartAborted) {
						p.proxyLogger.Errorf("<%s> failed to start: %v", p.id, res.err)
					}
					setState(StateStopped)
					notifyWaiters(res.err)
					req.respond <- res.err
				}

			// Stop arrived while doStart was still running. Cancel the
			// start context to abort it, then wait for doStart to return.
			// If doStart had already crossed the finish line before
			// cancellation took effect, it returns a live cmd that we
			// must kill ourselves. The Run caller gets ErrAbort; the Stop
			// caller is parked in pendingStop and answered below.
			case stop := <-p.stopCh:
				cancelStart()
				res := <-resultCh
				if res.cmd != nil {
					p.killProcess(res.cmd, res.cancel, res.cmdDone, stop.timeout)
				}
				setState(StateStopped)
				notifyWaiters(ErrStartAborted)
				req.respond <- ErrStartAborted
				pendingStop = &stop

			// Parent context cancelled (e.g. config reload) while doStart
			// was still running. Stop() returns early when parentCtx is
			// done and never sends on stopCh, so we must handle shutdown
			// here to avoid leaving doStart running indefinitely.
			case <-p.parentCtx.Done():
				cancelStart()
				// Mark shutdown before tearing the process down: killProcess
				// may block (e.g. taskkill on Windows is slow to spawn), and
				// callers observing State() should see StateShutdown promptly
				// rather than a stale StateStarting.
				setState(StateShutdown)
				res := <-resultCh
				if res.cmd != nil {
					p.killProcess(res.cmd, res.cancel, res.cmdDone, parentCancelGraceTimeout)
				}
				notifyWaiters(fmt.Errorf("[%s] shutdown", p.id))
				respondRun(fmt.Errorf("[%s] shutdown", p.id))
				return
			}
			// cancelStart is idempotent; calling it again here ensures the
			// context is released even on the success path (govet leak check).
			cancelStart()
			if pendingStop != nil {
				pendingStop.respond <- nil
			}

		// Stop: tear down a running process.
		case stop := <-p.stopCh:
			if cmd != nil {
				// Fire the pre-stop hook while still StateReady so it can reach the
				// live upstream (e.g. save slot KV) before we kill it.
				if fp := p.preStop.Load(); fp != nil {
					(*fp)()
				}
				// Counterpart to the "starting"/"ready" pair: without this the
				// only unload the log ever mentioned was the TTL one, so a model
				// evicted to make room for another just vanished.
				p.proxyLogger.Infof("<%s> stopping", p.id)
				setState(StateStopping)
				p.killProcess(cmd, cmdCancel, cmdDone, stop.timeout)
				cmd = nil
				cmdDone = nil
				cmdCancel = nil
				p.handler.Store(nil)
				p.launchedArgs.Store(nil)
			}
			// Stop is a no-op (and not an error) when already Stopped - this
			// is what makes it idempotent for callers that don't track state.
			setState(StateStopped)
			respondRun(nil)
			stop.respond <- nil
		}
	}
}

// rewritesVoicesPath reports whether this upstream needs /v1/audio/voices mapped
// onto /v1/voices. Two different speech engines ship a binary called tts-server:
// qwentts.cpp (loads a talker with --model plus a paired --codec, voices under
// /v1/voices) and mmwillet/TTS.cpp (loads a self-contained gguf with
// --model-path, voices under /v1/audio/voices). The flag is the only reliable
// discriminator - the exe name is identical - and it is token-exact, so a path
// that merely contains the string cannot trip it.
func rewritesVoicesPath(args []string) bool {
	for _, a := range args {
		if a == "--model-path" || strings.HasPrefix(a, "--model-path=") {
			return false
		}
	}
	return true
}

func (p *ProcessCommand) doStart(startCtx context.Context, healthCheckTimeout time.Duration) startResult {
	cfg := p.cfg()
	if cfg.Proxy == "" {
		return startResult{err: fmt.Errorf("upstream proxy missing")}
	}

	args, err := cfg.SanitizedCommand()
	if err != nil {
		return startResult{err: fmt.Errorf("unable to get sanitized command: %w", err)}
	}

	// Rewrite the argv against live conditions (e.g. recompute GPU/CPU layer
	// placement from free VRAM). An error here refuses the start rather than
	// letting a stale plan OOM.
	if fp := p.spawnArgs.Load(); fp != nil {
		args, err = (*fp)(args)
		if err != nil {
			return startResult{err: err}
		}
	}

	proxyURL, err := url.Parse(cfg.Proxy)
	if err != nil {
		return startResult{err: fmt.Errorf("invalid proxy URL %q: %w", cfg.Proxy, err)}
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(proxyURL)
	// qwentts.cpp's tts-server exposes GET /v1/voices, but quartermaster's
	// model-routed catalog uses the OpenAI-style /v1/audio/voices path. Map it so
	// the playground's voice list reaches the backend. Harmless for non-speech
	// backends - they never get this path.
	//
	// NOT harmless for TTS.cpp, the other engine behind a binary also named
	// tts-server: it serves /v1/audio/voices itself and has no /v1/voices, so the
	// rewrite turned its voice list into a 404 and the Speech tab showed only the
	// default speaker. Skip it there - see rewritesVoicesPath.
	rewriteVoices := rewritesVoicesPath(args)
	origDirector := reverseProxy.Director
	reverseProxy.Director = func(r *http.Request) {
		origDirector(r)
		if !rewriteVoices {
			return
		}
		// GET/POST /v1/audio/voices -> /v1/voices; DELETE /v1/audio/voices/{name}
		// -> /v1/voices/{name} (tts-server names the voice in the path).
		if r.URL.Path == "/v1/audio/voices" {
			r.URL.Path = "/v1/voices"
		} else if rest, ok := strings.CutPrefix(r.URL.Path, "/v1/audio/voices/"); ok {
			r.URL.Path = "/v1/voices/" + rest
		}
	}
	reverseProxy.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(cfg.Timeouts.Connect) * time.Second,
			KeepAlive: time.Duration(cfg.Timeouts.KeepAlive) * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   time.Duration(cfg.Timeouts.TLSHandshake) * time.Second,
		ResponseHeaderTimeout: time.Duration(cfg.Timeouts.ResponseHeader) * time.Second,
		ExpectContinueTimeout: time.Duration(cfg.Timeouts.ExpectContinue) * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       time.Duration(cfg.Timeouts.IdleConn) * time.Second,
	}
	reverseProxy.ModifyResponse = func(resp *http.Response) error {
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			resp.Header.Set("X-Accel-Buffering", "no")
		}
		return nil
	}
	// httputil.ReverseProxy panics with http.ErrAbortHandler when the upstream
	// disconnects after response headers have been sent. Recover here so the
	// streaming termination is treated as a normal client/upstream disconnect.
	// see: https://github.com/golang/go/issues/23643
	handlerFn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					p.proxyLogger.Infof("<%s> recovered from upstream disconnection during streaming", p.id)
				} else {
					p.proxyLogger.Warnf("<%s> recovered from panic: %v", p.id, rec)
				}
			}
		}()
		reverseProxy.ServeHTTP(w, r)
	})

	// cmdCtx + cmd.Cancel are wired as a safety net: if the context is ever
	// cancelled while the process is alive, cmd.Cancel sends SIGTERM / CmdStop
	// and the runtime escalates to SIGKILL after cmd.WaitDelay. In the normal
	// teardown path killProcess sends the stop signal directly instead, so
	// cmd.WaitDelay only acts as the inherited-pipe backstop measured from
	// process exit (see killProcess).
	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	resolvedExe := resolveExe(args[0])
	cmd := exec.CommandContext(cmdCtx, resolvedExe, args[1:]...)
	cmd.Stderr = p.processLogger
	cmd.Stdout = p.processLogger
	cmd.Dir = spawnDir()
	cmd.Env = append(cmd.Environ(), cfg.Env...)
	// Self-contained backends (Vulkan/ROCm bundles) need their own directory
	// on the loader path for TRANSITIVE deps — see exeLibEnv.
	cmd.Env = exeLibEnv(cmd.Env, resolvedExe)
	cmd.Cancel = func() error { return p.sendStopSignal(cmd) }
	cmd.WaitDelay = p.waitDelay
	setProcAttributes(cmd)

	p.proxyLogger.Debugf("<%s> Executing start command: %s, cwd: %s, env: %s", p.id, strings.Join(args, " "), cmd.Dir, strings.Join(cfg.Env, ", "))

	// A load is the longest thing quartermaster does — tens of seconds of
	// nothing while a GGUF is read off disk. Announcing the spawn (and, below,
	// how long it took to answer) is the difference between "it's working" and
	// "it's hung": before this the proxy log said nothing at all until the
	// health check passed.
	startedAt := time.Now()
	p.proxyLogger.Infof("<%s> starting %s", p.id, filepath.Base(resolveExe(args[0])))

	cmdDone := make(chan struct{})
	if err := cmd.Start(); err != nil {
		cmdCancel()
		return startResult{err: fmt.Errorf("failed to start command '%s': %w", strings.Join(args, " "), err)}
	}
	p.pid.Store(int32(cmd.Process.Pid))

	go func() {
		waitErr := cmd.Wait()
		p.pid.Store(0)
		switch st := p.State(); {
		case waitErr == nil:
			p.proxyLogger.Debugf("<%s> process exited cleanly", p.id)
		case st == StateStopping || st == StateShutdown:
			// Expected: we force-terminated the process. A forced kill exits
			// the child with a non-zero code (e.g. taskkill /f on Windows
			// yields exit status 1), so this is not an error.
			p.proxyLogger.Debugf("<%s> process stopped by quartermaster: %v", p.id, waitErr)
		default:
			// Not a state we asked for: the backend died on its own. This used
			// to be Debug, so a model that crashed mid-session left the proxy
			// log silent and the operator digging through the upstream stream
			// to find out why their next request 502'd.
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				p.proxyLogger.Warnf("<%s> backend exited on its own with code %d (see the model's upstream log)", p.id, exitErr.ExitCode())
			} else {
				p.proxyLogger.Warnf("<%s> backend exited on its own: %v", p.id, waitErr)
			}
		}
		close(cmdDone)
	}()

	abort := func(err error) startResult {
		p.killProcess(cmd, cmdCancel, cmdDone, 5*time.Second)
		return startResult{err: err}
	}
	prematureExit := func() startResult {
		cmdCancel()
		// A backend whose DLL graph is incomplete dies with STATUS_DLL_NOT_FOUND
		// before it runs a line of its own code: no stdout, no stderr, nothing in
		// the process log. "Exited prematurely" is then the only thing anyone
		// sees, and it points at the model rather than at the packaging. Naming
		// the missing library costs one header walk on a path we already lost.
		if hint := peimports.Hint(resolveExe(args[0])); hint != "" {
			p.proxyLogger.Errorf("<%s> %s", p.id, hint)
			return startResult{err: fmt.Errorf("upstream command exited prematurely: %s", hint)}
		}
		return startResult{err: fmt.Errorf("upstream command exited prematurely")}
	}

	if startCtx.Err() != nil {
		return abort(ErrStartAborted)
	}

	checkEndpoint := strings.TrimSpace(cfg.CheckEndpoint)
	if checkEndpoint == "none" {
		p.proxyLogger.Infof("<%s> started as pid %d (no health check configured)", p.id, cmd.Process.Pid)
		return startResult{cmd: cmd, cmdDone: cmdDone, cancel: cmdCancel, handlerFn: handlerFn, args: args}
	}

	// Wait 250ms for the command to start up before health checking
	select {
	case <-startCtx.Done():
		return abort(ErrStartAborted)
	case <-time.After(250 * time.Millisecond):
	}

	deadline := time.Now().Add(healthCheckTimeout)
	for {
		select {
		case <-startCtx.Done():
			return abort(ErrStartAborted)
		case <-cmdDone:
			return prematureExit()
		default:
		}

		if time.Now().After(deadline) {
			return abort(fmt.Errorf("health check timed out after %v", healthCheckTimeout))
		}

		req, _ := http.NewRequestWithContext(startCtx, "GET", cfg.CheckEndpoint, nil)
		rr := httptest.NewRecorder()
		reverseProxy.ServeHTTP(rr, req)
		resp := rr.Result()
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			p.proxyLogger.Infof("<%s> ready in %s (pid %d)", p.id,
				time.Since(startedAt).Round(100*time.Millisecond), cmd.Process.Pid)
			p.proxyLogger.Debugf("<%s> health check passed on %s%s", p.id, cfg.Proxy, cfg.CheckEndpoint)
			break
		} else if startCtx.Err() != nil {
			return abort(ErrStartAborted)
		}

		select {
		case <-startCtx.Done():
			return abort(ErrStartAborted)
		case <-cmdDone:
			return prematureExit()
		case <-time.After(time.Second):
		}
	}

	return startResult{cmd: cmd, cmdDone: cmdDone, cancel: cmdCancel, handlerFn: handlerFn, args: args}
}

// sendStopSignal runs the configured CmdStop (if any) or sends SIGTERM to
// the upstream process. Wired up as cmd.Cancel so it fires whenever the
// cmd's context is cancelled.
func (p *ProcessCommand) sendStopSignal(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		p.processLogger.Debugf("<%s> sendStopSignal() called with nil cmd or process, nothing to stop", p.id)
		return nil
	}
	pid := cmd.Process.Pid
	cmdStop := p.cfg().CmdStop
	if cmdStop != "" {
		p.processLogger.Debugf("<%s> sendStopSignal() using CmdStop %q for pid %d", p.id, cmdStop, pid)
		stopArgs, err := config.SanitizeCommand(
			strings.ReplaceAll(cmdStop, "${PID}", fmt.Sprintf("%d", pid)),
		)
		if err == nil {
			p.processLogger.Debugf("<%s> sendStopSignal() running stop command: %s", p.id, strings.Join(stopArgs, " "))
			stopCmd := exec.Command(stopArgs[0], stopArgs[1:]...)
			stopCmd.Env = cmd.Env
			setProcAttributes(stopCmd)
			runErr := stopCmd.Run()
			if runErr != nil {
				p.processLogger.Errorf("<%s> sendStopSignal() stop command failed: %v", p.id, runErr)
			} else {
				p.processLogger.Debugf("<%s> sendStopSignal() stop command completed for pid %d", p.id, pid)
			}
			return runErr
		}
		// fall through to SIGTERM if sanitize failed
		p.processLogger.Errorf("<%s> sendStopSignal() failed to sanitize CmdStop %q: %v, falling back to terminateProcessTree", p.id, cmdStop, err)
	}
	// On Unix this SIGTERMs the whole process group so a forked grandchild
	// (e.g. a shell wrapper that backgrounds the real binary) is taken down
	// with the parent rather than orphaned.
	p.processLogger.Debugf("<%s> sendStopSignal() no CmdStop configured, calling terminateProcessTree for pid %d", p.id, pid)
	termErr := terminateProcessTree(cmd)
	if termErr != nil {
		p.processLogger.Errorf("<%s> sendStopSignal() terminateProcessTree failed for pid %d: %v", p.id, pid, termErr)
	}
	return termErr
}

// killProcess terminates the upstream process. The flow:
//
//  1. Send the graceful stop signal (CmdStop / SIGTERM) directly - NOT by
//     cancelling cmdCtx. Cancelling the context would start cmd.WaitDelay
//     immediately, which force-kills the process WaitDelay after the signal
//     and would silently cap gracefulTimeout at WaitDelay whenever
//     gracefulTimeout is the longer of the two.
//  2. We wait up to gracefulTimeout for the process to exit on its own.
//  3. If still alive, we SIGKILL the process group directly (Unix) so any
//     forked descendant is force-terminated alongside the parent.
//  4. We wait on cmdDone. cmd.WaitDelay (set when the cmd was built) is the
//     critical backstop here: once the process exits, if a forked grandchild
//     inherited the stdout/stderr pipes and is still holding them, the runtime
//     force-closes the pipes WaitDelay after the exit and cmd.Wait() unblocks.
//     Because we never cancelled the context, that WaitDelay timer measures
//     from process exit (see os/exec awaitGoroutines), not from this call.
//     Without WaitDelay this select would hang forever (the v219 bug).
//
// cancel() is still invoked (deferred) to release the context, but only after
// the process has exited and os/exec's ctx watcher has already torn down, so it
// never re-fires cmd.Cancel.
func (p *ProcessCommand) killProcess(cmd *exec.Cmd, cancel context.CancelFunc, cmdDone <-chan struct{}, gracefulTimeout time.Duration) {
	if cancel == nil {
		return
	}
	defer cancel()

	// Deliver CmdStop / SIGTERM in a goroutine so a slow or hanging CmdStop
	// cannot block the run() goroutine; the gracefulTimeout + Process.Kill
	// path below still guarantees teardown.
	if cmd != nil {
		go func() {
			p.proxyLogger.Debugf("[%s] sending stop signal with timeout %v", p.id, gracefulTimeout)
			if err := p.sendStopSignal(cmd); err != nil {
				p.proxyLogger.Warnf("[%s] stop signal failed: %v", p.id, err)
			}
		}()
	}

	timer := time.NewTimer(gracefulTimeout)
	defer timer.Stop()

	select {
	case <-cmdDone:
		return
	case <-timer.C:
	}

	if cmd != nil {
		// SIGKILL the whole process group on Unix so any descendant that
		// ignored or outlived the graceful signal is force-terminated too.
		_ = killProcessTree(cmd)
	}
	<-cmdDone
}

func (p *ProcessCommand) ID() string {
	return p.id
}

func (p *ProcessCommand) Run(timeout time.Duration) error {
	req := runReq{
		timeout: timeout,
		respond: make(chan error, 1),
	}
	select {
	case p.runCh <- req:
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
	select {
	case err := <-req.respond:
		return err
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
}

func (p *ProcessCommand) WaitReady(ctx context.Context) error {
	req := waitReadyReq{respond: make(chan error, 1)}
	select {
	case p.waitReadyCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
	select {
	case err := <-req.respond:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *ProcessCommand) Stop(timeout time.Duration) error {
	req := stopReq{
		timeout: timeout,
		respond: make(chan error, 1),
	}
	select {
	case p.stopCh <- req:
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
	return <-req.respond
}

func (p *ProcessCommand) State() ProcessState {
	if s, ok := p.state.Load().(ProcessState); ok {
		return s
	}
	return StateStopped
}

// PID returns the OS pid of the live upstream process, or 0 when not running.
func (p *ProcessCommand) PID() int {
	return int(p.pid.Load())
}

// Inflight returns the current number of in-flight ServeHTTP calls.
func (p *ProcessCommand) Inflight() int64 {
	return p.inflight.Load()
}

func (p *ProcessCommand) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fn := p.handler.Load()
	if fn == nil {
		http.Error(w, fmt.Sprintf("quartermaster-error: [%s] process is not ready", p.id), http.StatusServiceUnavailable)
		return
	}
	p.inflight.Add(1)
	defer func() {
		p.lastUse.Store(time.Now().UnixNano())
		p.inflight.Add(-1)
	}()
	(*fn)(w, r)
}
