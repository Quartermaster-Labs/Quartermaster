package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/event"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/server"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
	"github.com/quartermaster-labs/quartermaster/internal/watcher"
)

var (
	version = "0"
	commit  = "abcd1234"
	date    = "unknown"
)

const shutdownTimeout = 30 * time.Second

// logTimeFormats maps the cfg.LogTimeFormat value to a Go time layout. An
// unset or unrecognised value yields "" — no timestamp prefix.
var logTimeFormats = map[string]string{
	"ansic":       time.ANSIC,
	"unixdate":    time.UnixDate,
	"rubydate":    time.RubyDate,
	"rfc822":      time.RFC822,
	"rfc822z":     time.RFC822Z,
	"rfc850":      time.RFC850,
	"rfc1123":     time.RFC1123,
	"rfc1123z":    time.RFC1123Z,
	"rfc3339":     time.RFC3339,
	"rfc3339nano": time.RFC3339Nano,
	"kitchen":     time.Kitchen,
	"stamp":       time.Stamp,
	"stampmilli":  time.StampMilli,
	"stampmicro":  time.StampMicro,
	"stampnano":   time.StampNano,
}

// listenerAddrsChanged reports whether two configs declare a different set of
// listen addresses. The bound sockets are fixed at startup, so a change means
// the operator must restart to (un)bind them; a live config apply can't.
func listenerAddrsChanged(a, b config.Config) bool {
	as, bs := a.ListenerAddrs(), b.ListenerAddrs()
	if len(as) != len(bs) {
		return true
	}
	set := make(map[string]bool, len(as))
	for _, x := range as {
		set[x] = true
	}
	for _, y := range bs {
		if !set[y] {
			return true
		}
	}
	return false
}

func main() {
	flagConfig := flag.String("config", "", "path to config file (required)")
	flagListen := flag.String("listen", "", "listen address (default :8080 or :8443 for TLS)")
	flagCertFile := flag.String("tls-cert-file", "", "TLS certificate file")
	flagKeyFile := flag.String("tls-key-file", "", "TLS key file")
	flagVersion := flag.Bool("version", false, "show version and exit")
	flagWatchConfig := flag.Bool("watch-config", false, "reload config on file change")
	flagGenerate := flag.String("generate", "", "path to autogen control file (settings + overrides); generates -config from local GGUFs on startup (hash-gated)")
	flagModelsDir := flag.String("models-dir", "", "models root for -generate (overrides settings.modelsRoot)")
	flagWatchModels := flag.Bool("watch-models", true, "periodically re-scan the models folder and hot-reload when it changes (requires -generate); on by default, pass -watch-models=false to disable")
	flagWatchModelsInterval := flag.Duration("watch-models-interval", 5*time.Second, "poll interval for -watch-models")
	flagPlaygroundPort := flag.String("playground-port", "", "serve the standalone playground app (per-user login + chat history) on this extra address, e.g. :8081")
	flagNoUpdateCheck := flag.Bool("no-update-check", false, "disable checking GitHub for new releases (Windows release builds only)")
	flagTray := flag.Bool("tray", false, "run as a desktop app: show a system-tray icon with Open/Exit (Windows only; no-op elsewhere)")
	flagAdminAllow := flag.String("admin-allow", "", "extra IPs/CIDRs (comma separated) allowed to reach the dashboard/admin endpoints when listening beyond loopback, e.g. 100.64.0.0/10 for a tailnet")
	flagAdminOpen := flag.Bool("admin-open", false, "serve the unauthenticated dashboard/admin endpoints to every remote host (legacy behaviour; the inference API is unaffected)")
	flag.Parse()

	if *flagNoUpdateCheck {
		os.Setenv("LQ_NO_UPDATE_CHECK", "1")
	}

	if *flagVersion {
		fmt.Printf("version: %s (%s), built at %s\n", version, commit, date)
		os.Exit(0)
	}

	if *flagConfig == "" {
		slog.Error("-config is required")
		os.Exit(1)
	}

	useTLS := *flagCertFile != "" || *flagKeyFile != ""
	if (*flagCertFile != "" && *flagKeyFile == "") || (*flagCertFile == "" && *flagKeyFile != "") {
		slog.Error("both -tls-cert-file and -tls-key-file must be provided for TLS")
		os.Exit(1)
	}

	listenAddr := *flagListen
	if listenAddr == "" {
		if useTLS {
			listenAddr = ":8443"
		} else {
			listenAddr = ":8080"
		}
	}

	configPath := *flagConfig

	// Autogen: when -generate is set, (re)generate -config from the local GGUF
	// tree before loading. Hash-gated, so an unchanged models folder + control
	// file skips the scan. -config is the output path here.
	if *flagGenerate != "" {
		// Detect the GPU class once so the sizer only charges CUDA-context overhead
		// on a CUDA (NVIDIA) GPU, not on Vulkan/ROCm. Best-effort, before the sizer runs.
		autogen.DetectGpuCompute(func(m string) { slog.Info(m) })
		if _, err := autogen.EnsureConfig(*flagGenerate, configPath, *flagModelsDir, func(m string) { slog.Info(m) }); err != nil {
			slog.Error("autogen failed", "error", err)
			os.Exit(1)
		}
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		slog.Error("failed to load config", "path", configPath, "error", err)
		os.Exit(1)
	}

	// Loggers are wired per cfg.LogToStdout: proxy/upstream feed muxLog, which
	// owns the combined history served by /logs. They outlive config reloads,
	// so a LogToStdout change requires a restart to take effect.
	muxLog, proxyLog, upstreamLog := server.NewLoggers(cfg.LogToStdout)

	if len(cfg.Profiles) > 0 {
		proxyLog.Warn("Profile functionality has been removed in favor of Groups. See the README for more information.")
	}

	applyLogSettings := func(cfg config.Config) {
		level := logmon.LevelInfo
		switch strings.ToLower(strings.TrimSpace(cfg.LogLevel)) {
		case "debug":
			level = logmon.LevelDebug
		case "warn":
			level = logmon.LevelWarn
		case "error":
			level = logmon.LevelError
		}
		timeFormat := logTimeFormats[strings.ToLower(strings.TrimSpace(cfg.LogTimeFormat))]
		for _, lg := range []*logmon.Monitor{proxyLog, upstreamLog} {
			lg.SetLogLevel(level)
			lg.SetLogTimeFormat(timeFormat)
		}
	}

	applyLogSettings(cfg)
	proxyLog.Debugf("PID: %d", os.Getpid())

	// On Windows, bind the process tree to a Job Object so every upstream
	// process is reaped when quartermaster exits — even on a forced kill. No-op
	// elsewhere. Non-fatal: a failure just falls back to per-process teardown.
	if err := process.SetupTreeCleanup(); err != nil {
		proxyLog.Warnf("failed to set up process tree cleanup: %v", err)
	}

	// perfMon outlives config reloads; its config is updated in place.
	var perfMon *perf.Monitor
	if !cfg.Performance.Disabled {
		perfMon, err = perf.New(cfg.Performance, proxyLog)
		if err != nil {
			slog.Error("failed to create performance monitor", "error", err)
			os.Exit(1)
		}
		perfMon.Start()
	} else {
		proxyLog.Info("performance monitoring is disabled")
	}

	buildInfo := server.BuildInfo{Version: version, Commit: commit, Date: date}

	initialSrv, err := server.New(cfg, muxLog, proxyLog, upstreamLog, perfMon, buildInfo)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	// wireDynOffload installs spawn-time live-VRAM offload recompute on a server.
	// Only in -generate mode: the placement flags are autogen's to re-derive, and
	// settings (overhead/compute-buffer) come from the control file. Off for a
	// hand-written -config, where the operator owns the flags.
	wireDynOffload := func(srv *server.Server) {
		if *flagGenerate == "" {
			return
		}
		gf, gerr := autogen.LoadGenerateFile(*flagGenerate, *flagModelsDir)
		if gerr != nil {
			proxyLog.Warnf("dynamic offload disabled: %v", gerr)
			return
		}
		srv.WireDynamicOffload(gf.Settings)
	}
	wireDynOffload(initialSrv)

	// activeSrv is swapped atomically during hot reload.
	var activeMu sync.RWMutex
	activeSrv := initialSrv

	// listenAddrs is the set of addresses to bind. When the config declares a
	// `listeners:` block we bind one server per address, all sharing the single
	// activeSrv (and therefore one router/scheduler) — the invariant that keeps
	// cross-listener VRAM accounting and eviction correct. Otherwise we fall
	// back to the single --listen address.
	var listenAddrs []string
	if len(cfg.Listeners) > 0 {
		if *flagListen != "" {
			proxyLog.Warn("ignoring --listen because the config file declares a 'listeners' block")
		}
		listenAddrs = cfg.ListenerAddrs()
	} else {
		listenAddrs = []string{listenAddr}
	}

	// Standalone playground: serve it on an extra listen address. It shares the
	// single activeSrv (one router/scheduler — the invariant), it's just another
	// bound address that the server tags as the playground app.
	var playground *server.Playground
	if *flagPlaygroundPort != "" {
		pgAddr := *flagPlaygroundPort
		if !strings.Contains(pgAddr, ":") {
			pgAddr = ":" + pgAddr
		}
		// Anchor user data to the bundle root (exe dir), NOT the config path:
		// config now lives in a config/ subfolder, and playground-data must not
		// follow it there. os.Executable failure falls back to CWD ("."), which
		// start.cmd/NSSM already set to the bundle root.
		exePath, _ := os.Executable()
		dataDir := filepath.Join(filepath.Dir(exePath), "playground-data")
		playground = &server.Playground{Addr: pgAddr, DataDir: dataDir, SelfBase: server.LoopbackBase(listenAddr)}
		playground.Migrate() // flat inline-base64 layout -> per-user folders + media files
		playground.InitTurns(proxyLog)
		initialSrv.SetPlayground(playground)
		listenAddrs = append(listenAddrs, pgAddr)
		proxyLog.Infof("playground app enabled on %s (data dir: %s)", pgAddr, dataDir)
	}

	// Admin surface gating. The dashboard / ops / config-editor endpoints carry
	// no auth on purpose (API keys must never lock the operator out of their own
	// UI), so a non-loopback bind — which is how the inference API is published to
	// the LAN or a tailnet — would otherwise hand model control to every host that
	// can reach the port. When any API listener binds beyond loopback we restrict
	// those endpoints to this host, plus whatever -admin-allow adds. -admin-open
	// restores the old wide-open behaviour. The playground app (its own port, its
	// own login) is exempt, so it is excluded from this check.
	adminAllow, err := server.ParseAdminAllow(*flagAdminAllow)
	if err != nil {
		slog.Error("invalid -admin-allow", "error", err)
		os.Exit(1)
	}
	adminLocalOnly := false
	if !*flagAdminOpen {
		for _, addr := range listenAddrs {
			if playground != nil && addr == playground.Addr {
				continue
			}
			if !shared.IsLoopbackAddr(addr) {
				adminLocalOnly = true
				break
			}
		}
	}
	initialSrv.SetAdminAccess(adminLocalOnly, adminAllow)
	if adminLocalOnly {
		msg := "listening beyond loopback: dashboard and admin endpoints are restricted to this host"
		if len(adminAllow) > 0 {
			msg += fmt.Sprintf(" (plus %s)", *flagAdminAllow)
		}
		proxyLog.Info(msg + "; the inference API stays reachable (gate it with apiKeys)")
	}

	httpServers := make([]*http.Server, 0, len(listenAddrs))
	for _, addr := range listenAddrs {
		addr := addr
		httpServers = append(httpServers, &http.Server{
			Addr: addr,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				activeMu.RLock()
				srv := activeSrv
				activeMu.RUnlock()
				srv.ServeListener(addr, w, r)
			}),
			// ReadHeaderTimeout bounds how long a connection may dribble out its
			// request headers (Slowloris); IdleTimeout reaps parked keep-alives.
			// Deliberately NOT set: ReadTimeout (a large multipart upload — image
			// edits, audio transcription — is a legitimately slow body) and
			// WriteTimeout (streaming completions and SSE run for minutes, and
			// WriteTimeout is an absolute deadline from the start of the request,
			// so any non-zero value would sever long generations).
			ReadHeaderTimeout: 20 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
		})
	}

	// autogenAdmin enables the UI per-model config editor endpoints. Declared
	// before reload so the closure can re-attach it to each rebuilt server;
	// constructed after reload (it captures the reload func). nil unless
	// -generate is in use.
	var autogenAdmin *server.AutogenAdmin

	// triggerShutdown initiates a graceful shutdown from outside the signal
	// handler (used by the auto-updater after it launches the installer). It is
	// assigned once sigChan exists; reusing the signal path avoids duplicating
	// the teardown sequence.
	var triggerShutdown func()

	// reload guards against overlapping reloads triggered by concurrent signals
	// or file-watcher callbacks.
	var reloading bool
	var reloadMu sync.Mutex

	reload := func() {
		reloadMu.Lock()
		if reloading {
			reloadMu.Unlock()
			return
		}
		reloading = true
		reloadMu.Unlock()
		defer func() {
			reloadMu.Lock()
			reloading = false
			reloadMu.Unlock()
		}()

		proxyLog.Info("applying configuration")

		newCfg, err := config.LoadConfig(configPath)
		if err != nil {
			proxyLog.Warnf("failed to reload config: %v", err)
			return
		}

		if len(newCfg.Profiles) > 0 {
			proxyLog.Warn("Profile functionality has been removed in favor of Groups. See the README for more information.")
		}

		if perfMon != nil {
			perfMon.UpdateConfig(newCfg.Performance)
		}

		// Live-patch the ONE long-lived server in place: keep every running model
		// process alive AND keep the server itself (SSE streams, metrics history,
		// slotCache, background goroutines) running — only the config pointer and
		// the cfg-derived handler swap. No reconnect, no state reset, no eviction.
		// A changed model's new launch args take effect on its next load. An
		// invalid config leaves the running config fully intact.
		activeMu.RLock()
		srv := activeSrv
		activeMu.RUnlock()
		if err := srv.ApplyConfig(newCfg); err != nil {
			proxyLog.Warnf("failed to apply config (keeping running config): %v", err)
			return
		}

		applyLogSettings(newCfg)

		// Per-listener catalog scoping refreshed live via ApplyConfig, but binding/
		// unbinding a physical listen socket still needs a restart. Warn if the
		// declared listener set changed (rare for a settings edit).
		if listenerAddrsChanged(cfg, newCfg) {
			proxyLog.Warn("listener addresses changed in config; restart to bind/unbind listen sockets (per-listener model scoping already updated live)")
		}

		// Refresh the UI catalog so a live model add/remove shows up. Cheap and
		// idempotent: on a plain settings edit the payload is identical to what the
		// UI already holds (same models/states/launch args), so it repaints nothing.
		event.Emit(shared.ConfigFileChangedEvent{State: shared.ReloadingStateEnd})

		proxyLog.Info("configuration applied")
	}

	// Enable the UI model-config editor only when generating from a control
	// file: it edits the sidecar next to -generate, regenerates -config, and
	// hot-reloads via the closure above.
	if *flagGenerate != "" {
		autogenAdmin = &server.AutogenAdmin{
			GeneratePath: *flagGenerate,
			ConfigPath:   configPath,
			ModelsDir:    *flagModelsDir,
			Reload:       reload,
		}
		initialSrv.SetAutogenAdmin(autogenAdmin)
	}

	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	defer watcherCancel()

	if *flagWatchConfig {
		absConfigPath, err := filepath.Abs(configPath)
		if err != nil {
			slog.Error("watch-config: failed to resolve config path", "error", err)
			os.Exit(1)
		}
		proxyLog.Info("watching configuration for changes (poll-based, 2s interval)")
		go func() {
			(&configwatcher.Watcher{
				Path:     absConfigPath,
				Interval: configwatcher.DefaultInterval,
				OnChange: reload,
			}).Run(watcherCtx)
		}()
	}

	// Periodically re-scan the models folder: when a GGUF is added/removed (or the
	// generate file / sidecar changes), regenerate -config and hot-reload. Gated on
	// the autogen inputs hash (same one EnsureConfig caches), so unchanged models —
	// and AutoVram's VRAM drift, which the hash ignores — never trigger a needless
	// reload that would evict the loaded model. Requires -generate.
	if *flagWatchModels {
		// watch-models defaults on, so only nag about the -generate requirement when
		// the user explicitly asked for it; a plain -config launch stays quiet.
		watchModelsSet := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "watch-models" {
				watchModelsSet = true
			}
		})
		switch {
		case *flagGenerate == "":
			if watchModelsSet {
				proxyLog.Warn("-watch-models ignored: it requires -generate")
			}
		default:
			if *flagWatchConfig {
				proxyLog.Warn("-watch-models and -watch-config are both set: a models-triggered regen will reload twice (once per watcher)")
			}
			interval := *flagWatchModelsInterval
			if interval < time.Second {
				interval = time.Second
			}
			proxyLog.Infof("watching models folder for changes (poll-based, %s interval)", interval)
			go func() {
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				// Seed from the hash EnsureConfig wrote at startup so the first
				// tick doesn't regen an already-current config.
				last := autogen.CachedConfigHash(configPath)
				for {
					select {
					case <-watcherCtx.Done():
						return
					case <-ticker.C:
						cur, err := autogen.CurrentInputsHash(*flagGenerate, *flagModelsDir)
						if err != nil {
							proxyLog.Warnf("watch-models: hashing inputs failed: %v", err)
							continue
						}
						if cur == last {
							continue
						}
						proxyLog.Info("watch-models: inputs changed, regenerating config")
						if _, err := autogen.EnsureConfig(*flagGenerate, configPath, *flagModelsDir, func(m string) { proxyLog.Info(m) }); err != nil {
							proxyLog.Warnf("watch-models: regen failed: %v", err)
							continue
						}
						last = cur
						reload()
					}
				}
			}()
		}
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Route the updater's shutdown request through the same signal path the OS
	// uses, so the full graceful teardown runs. Non-blocking: a shutdown already
	// in flight makes the send a no-op.
	triggerShutdown = func() {
		select {
		case sigChan <- syscall.SIGTERM:
		default:
		}
	}
	activeSrv.SetShutdownHook(triggerShutdown)

	for _, hs := range httpServers {
		hs := hs
		go func() {
			var startErr error
			if useTLS {
				proxyLog.Infof("quartermaster listening with TLS on https://%s", hs.Addr)
				startErr = hs.ListenAndServeTLS(*flagCertFile, *flagKeyFile)
			} else {
				proxyLog.Infof("quartermaster listening on http://%s", hs.Addr)
				startErr = hs.ListenAndServe()
			}
			if startErr != nil && !errors.Is(startErr, http.ErrServerClosed) {
				slog.Error("http server error", "addr", hs.Addr, "error", startErr)
				os.Exit(1)
			}
		}()

		if !shared.IsLoopbackAddr(hs.Addr) {
			_, port, _ := net.SplitHostPort(hs.Addr)
			proxyLog.Infof("quartermaster is reachable by all hosts on the network, use loopback (e.g. localhost:%s) to restrict to this host only", port)
		}
	}

	exitChan := make(chan struct{})

	go func() {
		for {
			sig := <-sigChan
			switch sig {
			case syscall.SIGHUP:
				proxyLog.Info("received SIGHUP, reloading config")
				go reload()
			case syscall.SIGINT, syscall.SIGTERM:
				proxyLog.Infof("received signal %v, shutting down", sig)
				watcherCancel()

				// Backstop against a stalled shutdown: force the process to
				// exit once the whole graceful sequence has had its full budget.
				// On Windows the Job Object reaps upstream processes on exit, so
				// a forced exit still cleans up rather than orphaning children.
				go func() {
					time.Sleep(shutdownTimeout + 5*time.Second)
					proxyLog.Warnf("graceful shutdown exceeded %v, forcing exit", shutdownTimeout)
					os.Exit(1)
				}()

				activeMu.RLock()
				srv := activeSrv
				activeMu.RUnlock()

				// Close long-lived SSE streams first so httpServer.Shutdown can
				// drain without blocking on them for the full timeout.
				srv.CloseStreams()

				// Both phases share a single deadline so total shutdown is
				// bounded by shutdownTimeout rather than 2x it.
				deadline := time.Now().Add(shutdownTimeout)
				shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
				defer cancel()
				var hsWG sync.WaitGroup
				for _, hs := range httpServers {
					hsWG.Add(1)
					go func(hs *http.Server) {
						defer hsWG.Done()
						if err := hs.Shutdown(shutdownCtx); err != nil {
							proxyLog.Warnf("http server (%s) shutdown error: %v", hs.Addr, err)
						}
					}(hs)
				}
				hsWG.Wait()

				// Clamp the remaining budget to a small positive value: a
				// non-positive timeout makes the router fall back to its own
				// healthCheckTimeout, which would defeat the shared deadline.
				remaining := time.Until(deadline)
				if remaining <= 0 {
					remaining = time.Millisecond
				}
				if err := srv.Shutdown(remaining); err != nil {
					proxyLog.Warnf("router shutdown error: %v", err)
				}

				if perfMon != nil {
					perfMon.Stop()
				}

				close(exitChan)
				return
			}
		}
	}()

	// Desktop mode: hold the main thread with a system-tray icon (Open/Exit)
	// until shutdown. Without -tray, just wait for exitChan. The tray's "Exit"
	// routes through triggerShutdown, so teardown is identical either way.
	if *flagTray {
		scheme := "http"
		if useTLS {
			scheme = "https"
		}
		host := "localhost"
		if _, port, err := net.SplitHostPort(listenAddrs[0]); err == nil && port != "" {
			host = "localhost:" + port
		}
		runTray(scheme+"://"+host, triggerShutdown, exitChan)
	} else {
		<-exitChan
	}
	proxyLog.Info("shutdown complete")
}
