package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/autogen"
	"github.com/radu0120/llama-quartermaster/internal/chain"
	"github.com/radu0120/llama-quartermaster/internal/config"
	"github.com/radu0120/llama-quartermaster/internal/logmon"
	"github.com/radu0120/llama-quartermaster/internal/perf"
	"github.com/radu0120/llama-quartermaster/internal/router"
	"github.com/radu0120/llama-quartermaster/internal/shared"
	"github.com/radu0120/llama-quartermaster/internal/update"
)

// updateRepo is the GitHub repo (owner/name) the auto-updater polls for releases.
const updateRepo = "Quartermaster-Labs/llama-quartermaster"

// Server owns the HTTP mux, cross-cutting middleware, and the local/peer model
// dispatch. It supersedes router.Server: it builds the local and peer routers
// directly and dispatches between them itself.
type Server struct {
	cfg config.Config

	muxlog      *logmon.Monitor
	proxylog    *logmon.Monitor
	upstreamlog *logmon.Monitor

	perf           *perf.Monitor
	inflight       *inflightCounter
	metrics        *metricsMonitor
	backendMetrics *backendMetricsMonitor
	slotCache      *slotCache
	promptCanon    *promptCanon
	build          BuildInfo

	local router.LocalRouter
	peer  router.Router

	// listenerModels maps a listen address to the set of real model IDs it
	// exposes. Empty when no listeners are configured (single --listen mode).
	// See listener.go.
	listenerModels map[string]map[string]bool

	mux     *http.ServeMux
	handler http.Handler

	// autogen, when set, enables the UI model-config endpoints (cogwheel
	// override editor + variant creation). nil when the server was not started
	// with -generate. See configapi.go.
	autogen *AutogenAdmin

	// updater polls GitHub releases for a newer build (Windows release builds
	// only). shutdownHook, when set, triggers a graceful process shutdown so the
	// downloaded installer can replace the running exe. See update.go.
	updater      *update.Checker
	shutdownHook func()

	// playground, when set, enables the standalone playground app (separate
	// port, per-user login + chat history). nil unless -playground-port is set.
	// See playground.go.
	playground *Playground

	shutdownCtx  context.Context
	shutdownFn   context.CancelFunc
	shuttingDown atomic.Bool
}

// modelPostJSONRoutes are endpoints with a model id in the JSON request body.
var modelPostJSONRoutes = []string{
	"/v1/chat/completions",
	"/v1/responses",
	"/v1/completions",
	"/v1/messages",
	"/v1/messages/count_tokens",
	"/v1/embeddings",
	"/reranking",
	"/rerank",
	"/v1/rerank",
	"/v1/reranking",
	"/infill",
	"/completion",
	"/v1/audio/speech",
	"/v1/audio/voices",
	"/v1/images/generations",
	"/sdapi/v1/txt2img",
	"/sdapi/v1/img2img",

	// versionless routes, the /v/ is stripped before the request is forwarded upstream
	// see issue #728
	"/v/chat/completions",
	"/v/responses",
	"/v/completions",
	"/v/messages",
	"/v/messages/count_tokens",
	"/v/embeddings",
	"/v/rerank",
	"/v/reranking",
}

// modelPostFormRoutes are multipart/form-data endpoints with a model id in the form data
var modelPostFormRoutes = []string{
	"/v1/audio/transcriptions",
	"/v1/images/edits",
}

// modelGetRoutes are model-dispatched GET endpoints (the model arrives as a
// query parameter).
var modelGetRoutes = []string{
	"/v1/audio/voices",
	"/sdapi/v1/loras",
}

// BuildInfo carries version metadata surfaced by GET /api/version.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func New(cfg config.Config, muxlog *logmon.Monitor, proxylog *logmon.Monitor, upstreamlog *logmon.Monitor, perfMon *perf.Monitor, build BuildInfo) (*Server, error) {
	var local router.LocalRouter
	var err error

	switch cfg.Routing.Router.Use {
	case "matrix":
		local, err = router.NewMatrix(cfg, proxylog, upstreamlog)
		if err != nil {
			return nil, fmt.Errorf("creating matrix router: %w", err)
		}
	default: // "group"
		local, err = router.NewGroup(cfg, proxylog, upstreamlog)
		if err != nil {
			return nil, fmt.Errorf("creating group router: %w", err)
		}
	}

	peer, err := router.NewPeer(cfg, proxylog)
	if err != nil {
		return nil, fmt.Errorf("creating peer router: %w", err)
	}

	shutdownCtx, shutdownFn := context.WithCancel(context.Background())
	s := &Server{
		cfg:            cfg,
		muxlog:         muxlog,
		proxylog:       proxylog,
		upstreamlog:    upstreamlog,
		perf:           perfMon,
		inflight:       &inflightCounter{},
		metrics:        newMetricsMonitor(proxylog, cfg.MetricsMaxInMemory, cfg.CaptureBuffer),
		build:          build,
		local:          local,
		peer:           peer,
		listenerModels: cfg.ListenerModelSets(),
		shutdownCtx:    shutdownCtx,
		shutdownFn:     shutdownFn,
	}
	s.backendMetrics = newBackendMetricsMonitor(s.runningProxies, proxylog)
	go s.backendMetrics.run(s.shutdownCtx)
	slotParticipates := func(id string) bool {
		mc, ok := cfg.Models[id]
		return ok && strings.Contains(mc.Cmd, "--slot-save-path")
	}
	// slotRecurrent reports whether a model loads a hybrid/recurrent gguf
	// (GatedDeltaNet/SSM, full_attention_interval>0). Memoized per gguf path —
	// ReadGgufMetadataCached is itself size+mtime cached, but skip even that for
	// models whose family we've already classified.
	var recurMu sync.Mutex
	recurCache := map[string]bool{}
	slotRecurrent := func(id string) bool {
		mc, ok := cfg.Models[id]
		if !ok {
			return false
		}
		gguf := modelFamily(mc.Cmd)
		if gguf == "" {
			return false
		}
		recurMu.Lock()
		defer recurMu.Unlock()
		if v, seen := recurCache[gguf]; seen {
			return v
		}
		meta, err := autogen.ReadGgufMetadataCached(gguf)
		v := err == nil && meta.FullAttnInterval > 0
		recurCache[gguf] = v
		return v
	}
	s.slotCache = newSlotCache(cfg.SlotCache, s.runningProxies, slotParticipates, slotRecurrent, proxylog)
	s.promptCanon = newPromptCanon()
	local.SetPreEvict(s.slotCache.saveOnEvict)    // save slot KV before a swap/unload kills the process
	local.SetPostLoad(s.slotCache.restoreOnLoad)  // restore slot KV after a cold load, before serving
	s.metrics.onRecord = s.slotCache.confirmReuse // confirm restores actually reused KV (cached_tokens)
	s.updater = update.New(updateRepo, build.Version, func(m string) { proxylog.Info(m) })
	go s.updater.Run(s.shutdownCtx)
	s.routes()
	s.startPreload()
	return s, nil
}

// SetShutdownHook wires the graceful-shutdown trigger used by the auto-updater
// after it launches the installer (so the running exe can be replaced).
func (s *Server) SetShutdownHook(fn func()) { s.shutdownHook = fn }

// WireDynamicOffload installs the spawn-time argv rewriter that re-derives each
// model's GPU/CPU layer placement from the VRAM free RIGHT NOW (via the perf
// monitor), so a config plan sized when more VRAM was free can't OOM. It only
// ever offloads more than the baked plan, and refuses a spawn outright when a
// model can't fit even fully offloaded. No-op without a perf monitor (no live
// reading to act on). Call once after New (and after each hot-reload re-New).
func (s *Server) WireDynamicOffload(settings autogen.Settings) {
	if s.perf == nil {
		return
	}
	s.local.SetSpawnArgs(func(modelID string, args []string) ([]string, error) {
		freeGB, ok := s.freeVramGB()
		return autogen.LiveOffloadArgs(settings, args, freeGB, ok, func(m string) {
			s.proxylog.Infof("<%s> %s", modelID, m)
		})
	})
}

// freeVramGB returns the free VRAM (GB) of the largest GPU from the most recent
// perf sample. ok is false when there's no GPU telemetry yet.
func (s *Server) freeVramGB() (float64, bool) {
	if s.perf == nil {
		return 0, false
	}
	_, gpus := s.perf.Current()
	latest := make(map[int]perf.GpuStat)
	for _, g := range gpus {
		if prev, seen := latest[g.ID]; !seen || g.Timestamp.After(prev.Timestamp) {
			latest[g.ID] = g
		}
	}
	best := -1
	var bestStat perf.GpuStat
	for _, g := range latest {
		if g.MemTotalMB <= 0 {
			continue
		}
		if best < 0 || g.MemTotalMB > bestStat.MemTotalMB {
			best = g.ID
			bestStat = g
		}
	}
	if best < 0 {
		return 0, false
	}
	free := bestStat.MemTotalMB - bestStat.MemUsedMB
	if free < 0 {
		free = 0
	}
	return float64(free) / 1024.0, true
}

// runningProxies maps each running local model to its resolved upstream base
// URL (cfg.Models[id].Proxy has ${PORT} already substituted at config load).
func (s *Server) runningProxies() map[string]string {
	running := s.local.RunningModels()
	out := make(map[string]string, len(running))
	for id := range running {
		if mc, ok := s.cfg.Models[id]; ok && mc.Proxy != "" {
			out[id] = mc.Proxy
		}
	}
	return out
}

// localPeerHandler dispatches a model-routed request to the local or peer
// router. The model is resolved once via shared.FetchContext.
func (s *Server) localPeerHandler(w http.ResponseWriter, r *http.Request) {
	stripVersionPrefix(r)

	data, err := shared.FetchContext(r, s.cfg)
	if err != nil {
		shared.SendError(w, r, shared.ErrNoModelInContext)
		return
	}

	// Reject models that this listener does not expose. Peer models are not in
	// any local group, so a restricted listener never routes to them.
	if models, scoped := listenerModelSet(r); scoped && !models[data.ModelID] {
		s.proxylog.Debugf("dispatch: model %q not exposed on this listener", data.ModelID)
		shared.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q is not available on this listener", data.Model))
		return
	}
	// Reject models the request's API key is not scoped to reach.
	if models, scoped := apiKeyModelSet(r); scoped && !models[data.ModelID] {
		s.proxylog.Debugf("dispatch: model %q not permitted for this API key", data.ModelID)
		shared.SendResponse(w, r, http.StatusForbidden, fmt.Sprintf("model %q is not available for this API key", data.Model))
		return
	}

	switch {
	case s.local.Handles(data.ModelID):
		s.proxylog.Debugf("dispatch: using local process for model: %s", data.ModelID)
		s.local.ServeHTTP(w, r)
	case s.peer.Handles(data.ModelID):
		s.proxylog.Debugf("dispatch: using peer for model: %s", data.ModelID)
		s.peer.ServeHTTP(w, r)
	default:
		shared.SendError(w, r, router.ErrNoRouterFound)
	}
}

// stripVersionPrefix rewrites versionless /v/... requests to their /... form
// before forwarding upstream (issue #728).
func stripVersionPrefix(r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v/") {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v")
	}
}

// routes builds the mux, registers every route, and wraps the mux with the
// global CORS middleware.
func (s *Server) routes() {

	authMW := CreateAuthMiddleware(s.cfg)
	modelChain := chain.New(
		authMW,
		CreateRequestContextMiddleware(s.cfg),
		CreateFilterMiddleware(s.cfg),
		CreateFormFilterMiddleware(s.cfg),
		CreateInflightMiddleware(s.inflight),
		CreateMetricsMiddleware(s.metrics, s.cfg),
		s.promptCanon.middleware, // canonicalize the prompt before slotcache/upstream see it
		s.slotCache.middleware,
	)
	// API keys gate the external inference API only — they let other apps reach
	// the models. The local dashboard / admin / SSE endpoints stay open (apiChain
	// has no auth), so enabling keys never locks the operator out of their own UI.
	apiChain := chain.New()
	// Discovery (/v1/models) is part of what an external app uses, so it carries
	// auth + per-key model scoping like the inference routes.
	discoveryChain := chain.New(authMW)

	mux := http.NewServeMux()
	dispatch := http.HandlerFunc(s.localPeerHandler)

	for _, path := range modelPostJSONRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelPostFormRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelGetRoutes {
		mux.Handle("GET "+path, modelChain.Then(dispatch))
	}

	// llama-quartermaster API + custom endpoints.
	mux.Handle("GET /v1/models", discoveryChain.ThenFunc(s.handleListModels))
	mux.Handle("GET /logs", apiChain.ThenFunc(s.handleLogs))
	mux.Handle("GET /logs/stream", apiChain.ThenFunc(s.handleLogStream))
	mux.Handle("GET /logs/stream/{logMonitorID...}", apiChain.ThenFunc(s.handleLogStream))

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /wol-health", handleHealth)
	mux.HandleFunc("GET /{$}", handleRootRedirect)

	// Embedded UI.
	mux.Handle("GET /ui/", apiChain.ThenFunc(s.handleUI))
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)

	// Prometheus metrics (wrapped by apiChain, matches the legacy endpoint).
	mux.Handle("GET /metrics", apiChain.ThenFunc(s.handleMetrics))

	// Operations endpoints.
	mux.Handle("GET /unload", apiChain.ThenFunc(s.handleUnload))
	mux.Handle("GET /running", apiChain.ThenFunc(s.handleRunning))

	// Upstream passthrough.
	mux.HandleFunc("GET /upstream", handleUpstreamRedirect)
	mux.Handle("/upstream/{upstreamPath...}", apiChain.ThenFunc(s.handleUpstream))

	// API group (API-key protected) consumed by the UI.
	mux.Handle("POST /api/models/unload", apiChain.ThenFunc(s.handleAPIUnloadAll))
	mux.Handle("POST /api/models/unload/{model...}", apiChain.ThenFunc(s.handleAPIUnloadModel))
	mux.Handle("GET /api/events", apiChain.ThenFunc(s.handleAPIEvents))
	mux.Handle("GET /api/metrics", apiChain.ThenFunc(s.handleAPIMetrics))
	mux.Handle("GET /api/backend-metrics", apiChain.ThenFunc(s.handleAPIBackendMetrics))
	mux.Handle("GET /api/performance", apiChain.ThenFunc(s.handleAPIPerformance))
	mux.Handle("GET /api/version", apiChain.ThenFunc(s.handleAPIVersion))
	mux.Handle("POST /api/update", apiChain.ThenFunc(s.handleAPIUpdate))
	mux.Handle("GET /api/captures/{id}", apiChain.ThenFunc(s.handleAPICapture))
	mux.Handle("GET /api/websearch", apiChain.ThenFunc(s.handleAPIWebSearch))

	// Standalone playground (separate port): which app to render + not-serious
	// per-user login & chat history. /api/mode is always safe; the rest 501/401
	// when the playground is disabled or the caller isn't logged in.
	mux.Handle("GET /api/mode", apiChain.ThenFunc(s.handlePlaygroundMode))
	mux.Handle("POST /auth/login", apiChain.ThenFunc(s.handlePlaygroundLogin))
	mux.Handle("POST /auth/logout", apiChain.ThenFunc(s.handlePlaygroundLogout))
	mux.Handle("GET /auth/me", apiChain.ThenFunc(s.handlePlaygroundMe))
	mux.Handle("GET /api/chats", apiChain.ThenFunc(s.handlePlaygroundChats))
	mux.Handle("PUT /api/chats", apiChain.ThenFunc(s.handlePlaygroundChats))
	mux.Handle("GET /api/prefs", apiChain.ThenFunc(s.handlePlaygroundPrefs))
	mux.Handle("PUT /api/prefs", apiChain.ThenFunc(s.handlePlaygroundPrefs))

	// Per-model config editor (cogwheel) — read launch params + effective
	// override, save curated overrides, reset to autogen default, add named
	// variants. All no-op with 501 when -generate is not in use (s.autogen nil).
	mux.Handle("GET /api/models/{model}/config", apiChain.ThenFunc(s.handleAPIModelConfigGet))
	mux.Handle("PUT /api/models/{model}/override", apiChain.ThenFunc(s.handleAPIModelOverridePut))
	mux.Handle("DELETE /api/models/{model}/override", apiChain.ThenFunc(s.handleAPIModelOverrideDelete))
	mux.Handle("PUT /api/models/{model}/variant", apiChain.ThenFunc(s.handleAPIModelVariantPost))
	mux.Handle("GET /api/models/{model}/estimate", apiChain.ThenFunc(s.handleAPIModelEstimate))
	mux.Handle("PUT /api/models/{model}/preview", apiChain.ThenFunc(s.handleAPIModelCmdPreview))

	// Global settings editor (dashboard GPU-memory card): read effective
	// settings + defaults, save a manual VRAM target/headroom patch, reset it.
	mux.Handle("GET /api/settings", apiChain.ThenFunc(s.handleAPISettingsGet))
	mux.Handle("PUT /api/settings", apiChain.ThenFunc(s.handleAPISettingsPut))
	mux.Handle("DELETE /api/settings", apiChain.ThenFunc(s.handleAPISettingsDelete))
	mux.Handle("PUT /api/settings/slotcache", apiChain.ThenFunc(s.handleAPISlotCachePut))
	mux.Handle("GET /api/kvcache", apiChain.ThenFunc(s.handleAPIKvCache))
	mux.Handle("GET /api/canon", apiChain.ThenFunc(s.handleAPICanon))
	// Per-category scan folder (Models tab folder icon) — opens the host's native
	// folder dialog, then sets settings.categoryRoots[category].
	mux.Handle("POST /api/settings/root/pick", apiChain.ThenFunc(s.handleAPISettingsRootPick))
	// Generic native folder dialog that returns the chosen path without persisting
	// (slot-cache directory field binds it, then saves on demand).
	mux.Handle("POST /api/pick-folder", apiChain.ThenFunc(s.handleAPIPickFolder))

	// Fleet-wide default variants (e.g. game) — surfaced per-model in the editor
	// but saved globally to settings.defaultVariants.
	mux.Handle("PUT /api/default-variants", apiChain.ThenFunc(s.handleAPIDefaultVariantsPut))

	// API-key manager (admin-only): list/create/delete named keys with optional
	// per-model scoping. 501 without -generate.
	mux.Handle("GET /api/apikeys", apiChain.ThenFunc(s.handleAPIKeysGet))
	mux.Handle("POST /api/apikeys", apiChain.ThenFunc(s.handleAPIKeyUpsert))
	mux.Handle("DELETE /api/apikeys/{name}", apiChain.ThenFunc(s.handleAPIKeyDelete))

	s.mux = mux
	s.handler = chain.New(CreateRequestLogMiddleware(s.proxylog), CreateCORSMiddleware()).Then(mux)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// CloseStreams cancels long-lived response streams (Server-Sent Events) so a
// graceful httpServer.Shutdown can drain without blocking on them. It does not
// tear down routers; call Shutdown for that. Safe to call repeatedly.
func (s *Server) CloseStreams() {
	s.shutdownFn()
}

// Shutdown stops the local and peer routers in parallel. It is idempotent;
// repeated calls return nil without re-running shutdown.
//
// Callers must drain inflight HTTP requests (httpServer.Shutdown) before
// calling this, otherwise inflight requests 502 when their processes are torn
// down. Call CloseStreams before httpServer.Shutdown so SSE streams do not
// block the drain.
func (s *Server) Shutdown(timeout time.Duration) error {
	if !s.shuttingDown.CompareAndSwap(false, true) {
		return nil
	}
	s.shutdownFn()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, rt := range []router.Router{s.local, s.peer} {
		if rt == nil {
			continue
		}
		wg.Add(1)
		go func(rt router.Router) {
			defer wg.Done()
			if err := rt.Shutdown(timeout); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(rt)
	}

	wg.Wait()
	return errors.Join(errs...)
}
