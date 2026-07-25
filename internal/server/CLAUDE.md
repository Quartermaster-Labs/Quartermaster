# internal/server

## Purpose

Owns the HTTP layer: it builds the mux, applies cross-cutting middleware (auth, CORS, request context, request-body filters, in-flight counting, metrics/captures), and dispatches model-routed requests between the local router and remote peers. It also serves the OpenAI-compatible catalog, the operations/UI JSON API, the embedded Svelte UI, and (fork additions) per-listener catalog scoping, live token streaming, and the `-generate` config-editor endpoints.

## Key files

| File | Role |
|---|---|
| `server.go` | `Server` struct, `New`, route table (`routes`), local/peer dispatch (`localPeerHandler`), version-prefix stripping, preload kickoff, graceful shutdown. |
| `api.go` | `/v1/models` listing + capability rendering, `/running`, `/unload`, `/metrics` (Prometheus), `/upstream/...` passthrough, model-in-path resolution, preload machinery. |
| `apigroup.go` | UI-facing `/api/*` JSON endpoints: SSE event stream, unload, metrics/performance/version/capture, and the `modelStatus` payload (fork: tags each model with `family`, `group`, `listeners`). |
| `configapi.go` | Fork: `-generate` config-editor endpoints — per-model override/variant editor, load-plan estimate, global settings editor (VRAM target/headroom/max-RAM + `ttlSec` idle-eviction, 0 = never). All 501 when `s.autogen == nil`. |
| `auth.go` | Auth, request-context, and CORS middleware; `Access-Control-Request-Headers` sanitization. |
| `admin.go` | Fork: remote-address gate for the unauthenticated admin surface (`adminChain`). `SetAdminAccess(localOnly, allow)` + `ParseAdminAllow`; loopback (and `-admin-allow` CIDRs) only, playground requests exempt. Enabled by `main` whenever an API listener binds beyond loopback. |
| `captures.go` | Request/response capture storage: per-route field masks, zstd+CBOR (de)compression, header redaction. |
| `family.go` | `modelFamily` — derives a stable grouping key (the gguf `-m`/`--model` path) so the UI groups variants of one model. |
| `filters.go` | Request-body filter middleware (model-name rewrite, strip/set params) for JSON and multipart form requests. |
| `inflight.go` | Atomic in-flight request counter + middleware emitting `InFlightRequestsEvent`. |
| `listener.go` | Fork: per-listener catalog scoping. `ServeListener` stores a listen address's allowed model set in request context; `listenerModelSet` reads it back. |
| `livemetrics.go` | Fork: `liveTokenCounter` — scans streaming SSE chunks and emits throttled `LiveTokensEvent`s for a live tokens/sec readout. |
| `log.go` | Logger construction (`NewLoggers`), `/logs` + `/logs/stream` handlers, access-log middleware, `statusRecorder` (status/size capture + Flush/Hijack passthrough). |
| `metrics.go` | `metricsMonitor` (token-usage parsing, bounded ring buffer, capture storage) and `responseBodyCopier` (tees upstream response for metrics while streaming to client). |
| `metrics_middleware.go` | Middleware that resolves the model, buffers request body/headers for capture, restricts `Accept-Encoding`, tees the response, and records metrics after dispatch. |
| `ui.go` | Embedded SPA serving from `ui_dist` (`//go:embed`), pre-compressed (br/gzip) file selection, SPA index.html fallback, favicon. |
| `slotcache.go` | Fork: slot KV-cache persistence — saves a llama-server slot's KV to disk so an expensive conversation survives eviction, and seeds cold/warm loads from a per-agent system+tools **preamble cache**. See "Slot KV cache" below. |
| `kvcacheapi.go` | `GET /api/kvcache` — the monitoring snapshot (counters, recent events, on-disk files) for the Observe → KV Cache tab. |
| `promptcanon.go` | Fork: **always-on** prompt-canonicalization middleware (`promptCanon.middleware`) — strips sub-day timestamps from every chat request's system prompt so the stable prefix stays byte-identical turn-to-turn (KV reuse for ANY client/model, not just slotcache participants). Non-lossy (date granularity kept), idempotent. `GET /api/canon` snapshot (counters + event ring) for Observe → Context → Prompt Canonicalization. Distinct from the slotcache's own normalization. |
| `backendmetrics.go` | Fork: `backendMetricsMonitor` — polls each running llama-server's `/metrics`+`/slots` on a 2s ticker for KV-fill / slot-saturation / throughput gauges (skipped while busy — both share llama-server's inference task queue; `RequestsProcessing` is filled from quartermaster's own per-model in-flight counter instead), fetches `/props` once per process lifetime (no queue contention, but static — no point re-polling), caches per-model, emits `BackendMetricsEvent` over SSE. `GET /api/backend-metrics` snapshot. |
| `websearch.go` | Fork: `GET /api/websearch` — same-origin SearXNG proxy (`/search?format=json`) for the playground web-search tool, dodging CORS. |
| `upscale.go` | Fork: `POST /v1/images/upscale` — standalone ESRGAN upscale (`4x-UltraSharp` etc.). **Exec-per-request**, NOT a loaded/swapped model: shells out to `realesrgan-ncnn-vulkan` (the `kind:upscale` backend-registry entry) per call, mutex-serialized, tile-capped VRAM. `{image,model?,scale?}`→`{image,model,scale}`; model files (`<name>.param`/`.bin`) discovered in `<exeDir>/models`. `hidewindow_{windows,other}.go` = `hideConsole(cmd)` so the CLI doesn't pop a console window. |
| `autostart{,_windows,_other}.go` | Fork: "start with the system" — `GET`/`PUT /api/autostart`, backed by the per-user Windows Run key (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`). **Not** autogen config (works without `-generate`): every install writes the SAME value name `Quartermaster`, so at most one install can autostart. A `PUT` whose stored entry points at a *different* exe returns **409 + the status** instead of clobbering it; the dashboard then shows the owner path + a Take over button (re-`PUT` with `takeover:true`). The written command is this process's own argv with relative path args absolutised and `-tray` forced on. Non-Windows: `supported:false`, toggle hidden. |
| `playground.go` | Fork: standalone playground app on the `-playground-port` listen address. `Playground` struct + `SetPlayground`/`markPlayground`; plaintext per-user login (`/auth/login`,`/auth/logout`,`/auth/me`, `pg_user` cookie, `users.json`), server-backed chat history + prefs (`/api/chats`, `/api/prefs`), and `/api/mode` (`{playground, playgroundPort}`) so one bundle serves dashboard or playground per port. **Storage:** per-user folders `DataDir/users/<user>/{chats,imagechats,speechchats,prefs}.json`; generated media (inline `data:` base64) is split out on write into `media/<hash>.<ext>` files (`extractMedia`, regex over raw bytes — structure-agnostic, byte-preserves numbers/timestamps, dedup by content hash) and served via `GET /api/media/{file...}` (`http.ServeFile`, per-user, Range-capable). Boot-time `Migrate()` folds the old flat `DataDir/<kind>/<user>.json` inline-base64 layout into this. |
| `turns.go` | Fork: **server-owned turn runner** for the playground chat. A turn runs as a server goroutine (`turnManager`, one `activeTurn` per user) that streams ONE completion — plus the whole tool loop (web/wiki search), reasoning-budget finalize, and qm-tools approval gate — straight into `chats.json` (single source of truth, merge-guarded via `guardedChatsPut`) and to any attached SSE viewer, so a closed/refreshed/reconnected tab no longer loses or stops the answer. Endpoints: `POST /api/chats/turn` (start), `GET /api/chats/turn/stream` (SSE snapshot+tail), `GET /api/chats/turn/state`, `DELETE /api/chats/turn` (stop), `POST /api/chats/turn/approve` (accept/deny a pending `quartermaster_configure` change). The self-call loops back through the normal proxy with the configured API key injected. See `turns_design.md`. |
| `update.go` | Fork: `POST /api/update` — downloads + launches the release installer then graceful shutdown (Windows release builds only); `updater` field + `SetShutdownHook`, status surfaced in `handleAPIVersion`. |
| `pickfolder_{windows,linux,other}.go` | Fork: native folder-picker dialog (`pickFolder()` — WinForms / zenity / unsupported) backing `POST /api/pick-folder` and `POST /api/settings/root/pick` in the `-generate` config editor. |

## Important types & functions

- `Server` (`server.go:24`) — owns mux/handler, local `router.LocalRouter` + `router.Router` peer, `metricsMonitor`, `inflightCounter`, `perf.Monitor`, `listenerModels`, and optional `autogen` admin.
- `New(...)` (`server.go:109`) — constructs routers (group or matrix per `cfg.Routing.Router.Use`) + peer, builds routes, fires preload.
- `localPeerHandler` (`server.go:154`) — resolves the model once via `shared.FetchContext`, enforces per-listener scoping, then routes to local or peer.
- `routes` (`server.go:193`) — assembles the middleware chains and registers every route; wraps the mux with request-log + CORS.
- `Shutdown` / `CloseStreams` (`server.go:291` / `:280`) — idempotent parallel router teardown; `CloseStreams` cancels SSE so HTTP drain doesn't block.
- `handleListModels` (`api.go:136`) — OpenAI `/v1/models` with capability rendering and per-listener catalog filtering.
- `renderCapabilities` (`api.go:45`) — maps `ModelCapConfig` into architecture/capabilities/supported_parameters/context_length fields.
- `modelStatus` (`apigroup.go:64`) + `groupIndex` (`apigroup.go:41`) — per-model UI payload tagged with group + exposing listeners.
- `handleAPIEvents` (`apigroup.go:232`) — SSE multiplexer (modelStatus, logData, metrics, inflight, liveTokens, backendMetrics).
- `AutogenAdmin` (`configapi.go:17`) + `SetAutogenAdmin` (`configapi.go:26`) — gate and wiring for the `-generate` editor endpoints (regen + hot-reload).
- `metricsMonitor` (`metrics.go:60`) + `responseBodyCopier` (`metrics.go:416`) — metrics capture core.
- `liveTokenCounter` (`livemetrics.go:22`) — streaming token estimator.
- Middleware constructors: `CreateAuthMiddleware` (`auth.go:16`), `CreateRequestContextMiddleware` (`auth.go:46`), `CreateCORSMiddleware` (`auth.go:63`), `CreateFilterMiddleware`/`CreateFormFilterMiddleware` (`filters.go:29`/`:79`), `CreateInflightMiddleware` (`inflight.go:23`), `CreateMetricsMiddleware` (`metrics_middleware.go:16`), `CreateRequestLogMiddleware` (`log.go:213`).
- `ServeListener` (`listener.go:22`) — per-listener entry point used by the multi-listener startup.

## HTTP routes

Registered in `routes` (`server.go:193-271`). Model-dispatched routes (`modelChain` → `localPeerHandler`) carry the full middleware chain; everything else uses `apiChain` (auth only).

Model-dispatched (lists in `server.go:58-100`):
- `POST` JSON routes — `/v1/chat/completions`, `/v1/responses`, `/v1/completions`, `/v1/messages`(+`/count_tokens`), `/v1/embeddings`, `/rerank`(+variants), `/infill`, `/completion`, `/v1/audio/speech`, `/v1/images/generations`, `/sdapi/v1/{txt2img,img2img}`, and `/v/...` versionless equivalents.
- `POST` form routes — `/v1/audio/transcriptions`, `/v1/images/edits`.
- `GET` routes — `/v1/audio/voices`, `/sdapi/v1/loras`.

Not model-dispatched but auth-gated (discoveryChain):
- `POST /v1/images/upscale` (`upscale.go` `handleUpscale`) — standalone ESRGAN upscale, exec-per-request (no scheduler/VRAM-swap). Distinct from `/v1/segment` (SAM), which IS a model-dispatched backend.

API / operations / UI:
- `GET /v1/models` (`server.go:221` → `api.go:136`) — catalog, scoped per listener.
- `GET /logs`, `GET /logs/stream[/{logMonitorID...}]` (`server.go:222-224`).
- `GET /health`, `GET /wol-health`, `GET /{$}` redirect (`server.go:226-228`).
- `GET /ui/`, `GET /favicon.ico` (`server.go:231-232`) — embedded SPA.
- `GET /metrics` (`server.go:235`) — Prometheus perf metrics.
- `GET /unload`, `GET /running` (`server.go:238-239`).
- `GET /upstream`, `/upstream/{upstreamPath...}` (`server.go:242-243`).
- `POST /api/models/unload[/{model...}]`, `GET /api/events`, `GET /api/metrics`, `GET /api/performance`, `GET /api/version`, `GET /api/captures/{id}` (`server.go:246-252`).
- Config editor (501 without `-generate`): `GET /api/models/{model}/config`, `PUT`/`DELETE /api/models/{model}/override`, `PUT /api/models/{model}/variant`, `GET /api/models/{model}/estimate` (`server.go:257-261`).
- Global settings editor: `GET`/`PUT`/`DELETE /api/settings` (`server.go:265-267`).
- Autostart: `GET`/`PUT /api/autostart` (`autostart.go`) — Windows login startup, deduped across installs; NOT `-generate`-gated.
- API-key manager (local admin, `-generate` only): `GET /api/apikeys`, `POST /api/apikeys`, `DELETE /api/apikeys/{name}` (`configapi_apikeys.go`).
- Context / observe extras: `GET /api/canon` (prompt-canon snapshot, `promptcanon.go`), `GET /api/backend-metrics` (`backendmetrics.go`), `GET /api/websearch` (SearXNG proxy, `websearch.go`).
- Self-update: `POST /api/update` (`update.go`, Windows release builds only).
- Config-editor extras (`-generate` only): `PUT /api/models/{model}/preview` (cmd preview), `PUT /api/models/{model}/adhoc-cmd` (one-time flag-override cmd for scripts — no persistence, no reload), `PUT`/`DELETE /api/models/{model}/adhoc-load` (inject that one-off cmd into the LIVE router so the real proxy serves the model with it — in-memory only, DELETE or any file reload reverts), `PUT`/`DELETE /api/models/{model}/display-name` (set/clear a base model's advertised name), `PUT /api/settings/slotcache`, `PUT /api/settings/backends` (backend exe paths — llama/sd/tts-server; Vulkan/ROCm build on non-NVIDIA GPUs), `PUT /api/default-variants`, `POST /api/pick-folder`, `POST /api/settings/root/pick` (`pickfolder_*.go`).
- Playground app (on `-playground-port`): `GET /api/mode`, `POST /auth/login`, `POST /auth/logout`, `GET /auth/me`, `GET`/`PUT /api/chats`, `GET`/`PUT /api/imagechats`, `GET`/`PUT /api/speechchats` (per-user server-backed chat/image/speech threads), `GET`/`PUT /api/prefs`, `GET /api/media/{file...}` (split-out generated media) (`playground.go`); server-run chat turns `POST /api/chats/turn` + `/stream`,`/state`,`/approve` + `DELETE` (`turns.go`, above).

## Gotchas / conventions

- **Auth scope (fork).** API keys gate the **external inference API only** — the model-dispatch routes (`modelChain`) plus discovery (`/v1/models`, on `discoveryChain`). The dashboard / admin / ops / SSE / `/ui/` routes (`apiChain`) are **open** so enabling keys never locks the operator out of their own UI. `CreateAuthMiddleware` is a pass-through with no configured keys; keys accepted via `Authorization: Bearer`, `Authorization: Basic` (password field), or `x-api-key`. A valid key may be **model-scoped** via `cfg.APIKeyModels` (key → allowed model IDs): the middleware attaches the allowed set to the request context (`apikeyscope.go`), and `handleListModels` + `localPeerHandler` intersect it with the listener scope so a scoped key only sees/reaches its models. Empty/absent scope = full access. Keys + scopes are emitted into the generated config by autogen and managed via the local `/api/apikeys` endpoints (`configapi_apikeys.go`).
- **Admin surface is gated by REMOTE address, not by listener (fork).** Since API keys never cover `apiChain`, publishing the API to the LAN/tailnet (`-listen 0.0.0.0:1250`) would otherwise expose the dashboard, `/upstream/*`, captures, unload, and the whole config editor to every host that can reach the port. The API and the dashboard share one port, so the split can't be per-listener: `adminChain` (`s.requireAdmin`, `admin.go`) 403s any admin route whose `r.RemoteAddr` isn't loopback or inside an `-admin-allow` CIDR (e.g. `100.64.0.0/10` to admin over a tailnet). `main` turns it on automatically when a non-playground listen address is non-loopback; `-admin-open` restores the legacy wide-open behaviour. Left ungated: `/v1/*` (inference + discovery, keys' job), `/health`, `/api/version`, `/metrics`, `/favicon.ico`, and every playground route (own port, own login — `isPlaygroundRequest` is exempt outright). Adding a new `/api/*` ops or editor route means wiring it to `adminChain`, not `apiChain`.
- **Per-listener catalog filtering.** Restriction lives in the request context, not the handler. `ServeListener` injects the address's allowed model set; `handleListModels` and `localPeerHandler` read it via `listenerModelSet`. `ok=false` means unrestricted (legacy single `--listen`). Peer models are omitted from restricted listeners. All listeners share one `Server` (one router/scheduler) — the invariant that makes cross-listener VRAM accounting/eviction correct.
- **Metrics teeing.** `CreateMetricsMiddleware` resolves the model up front (priming `shared.FetchContext`'s context fast path), restricts `Accept-Encoding` to gzip/deflate so the buffered body stays parseable, and wraps the writer in `responseBodyCopier`. Both `responseBodyCopier` and `statusRecorder` forward `Flush`/`Hijack` so SSE and websocket upgrades keep working. Captures are off unless `CaptureBuffer > 0`.
- **Config editor is `-generate`-only.** Every `configapi.go` handler short-circuits with 501 when `s.autogen` is nil. A successful edit upserts the sidecar override/settings, calls `autogen.EnsureConfig`, then hot-reloads (same path as SIGHUP) — slow because it re-reads gguf metadata, but acceptable for a settings save.
- **Reload is in-place (`Server.ApplyConfig`).** The hot-reload rebuilds NOTHING it can keep: the ONE long-lived `Server` (SSE streams, metrics/activity history, slotCache saved-KV, background goroutines, running processes) survives a config change — only the config pointer and the cfg-derived handler swap. `s.cfg` and `s.listenerModels` are `atomic.Pointer`s read via `s.config()`; `s.handler` is an `atomic.Pointer[http.Handler]`. `ApplyConfig(newCfg)` validates + applies to the shared router first (`s.local.ApplyConfig` — add/remove/retune models in place, no eviction; a changed model's new launch args apply on its NEXT load), then swaps `s.cfg`, refreshes `s.listenerModels`, and calls `s.routes()` to rebuild + atomically swap the handler (so auth/filter/scoping middleware tracks the new config without dropping in-flight requests or SSE). An invalid config leaves everything untouched. `main.reload` just calls `activeSrv.ApplyConfig` — no Server swap, no `CloseStreams`, no re-wiring of admin/dynoffload/playground hooks. Every save path (cogwheel, SIGHUP, `-watch-config`, `-watch-models`) flows through this, then emits one `ConfigFileChangedEvent` so the UI catalog picks up a live add/remove (identical payload on a plain edit → repaints nothing). This retired the old destructive `server.New`+`Shutdown` reload AND the interim immutable-`server.Reload`-per-reload (which dropped SSE + reset metrics history on every save). Deliberately NOT live-swapped: a bound listen socket (main warns via `listenerAddrsChanged`; per-listener model *scoping* DOES refresh) and peer targets (built once in `New`; UI can't edit them) — both need a restart.
- **`RunningCmd` in `modelStatus`.** Each running model's `apiModel` carries `runningCmd` = the actual argv the process spawned with (`s.local.LaunchedCmd(id)` → `process.LaunchedCmd`), set only while running. It differs from the config command after a live settings edit or a spawn-time offload rewrite, so the UI staging card shows what's REALLY loaded, not the pending config.
- **Embedded UI.** `ui_dist` is `//go:embed`-ed; the Makefile `ui` target copies the Svelte build in, and a placeholder keeps the embed valid before any build. Pre-compressed `.br`/`.gz` siblings are preferred; extensionless misses fall back to `index.html` for SPA routing.
- **Access-log skips.** `/wol-health`, `/api/performance`, `/metrics` are excluded from the access log to avoid drowning it in poll traffic. Path is captured before `next` runs because `/upstream` rewrites the URL in place.
- **Versionless routes.** `/v/...` paths are rewritten to `/...` by `stripVersionPrefix` before forwarding (issue #728).
- **Family / group tagging (fork).** `modelFamily` keys models by their gguf path so the UI can collapse ctx/game/judge variants; `modelStatus` additionally tags each model with its swap group and the listener addresses exposing it.
- **OOM / VRAM protection (fork).** Three cooperating pieces surfaced through `/api/performance`:
  - **Foreign VRAM** — `foreignGPU`/`foreignVram`/`isInferenceProc` (`apigroup.go`) tally GPU memory held by `llama-server`/`sd-server` processes this instance did NOT spawn (via `perf.QueryComputeApps` minus `router.RunningPIDs()`), returned as `"foreign"` (red UI gauge) so the sizer knows VRAM it can't reclaim.
  - **Idle floor** — `trackSystemVram` (`server.go`) keeps the min idle used-VRAM on the largest GPU while no model runs, returned as `"system_mb"` — the baseline non-inference VRAM the budget must reserve.
  - **Dynamic offload guard** — `WireDynamicOffload`/`freeVramGB` (`server.go`) is the spawn-time argv rewriter (wired into `autogen.LiveOffloadArgs`) that re-derives GPU/CPU layer placement from live free VRAM so a stale baked plan can't OOM, and **refuses** a spawn that can't fit rather than crashing. See `internal/autogen/CLAUDE.md` "Dynamic offload is a spawn-time guard".
- **Prompt canonicalization is a separate, always-on middleware (fork).** `promptCanon.middleware` (`promptcanon.go`) runs unconditionally *before* slotcache/upstream, stripping sub-day timestamps from the system prompt for every chat request — independent of whether the model participates in the slot cache. Do not confuse it with the slotcache's own `normalizeTimestamps` (that one is scoped to participating models' anchoring). Stats via `GET /api/canon`.

## Slot KV cache (fork — `slotcache.go`)

Persists a llama-server **slot's KV** to disk so an expensive prefill isn't thrown
away when the single live slot is reused. llama-server has **one slot (`/slots/0`)**;
any new request can evict the resident conversation. This subsystem snapshots the KV
before it's lost and restores it (instead of reprefilling) when that conversation —
or one sharing its preamble — returns.

**When it's active.** Two gates: `cfg.SlotCache.Enable` (global, with `dir`/
`minSaveTokens`/`maxDiskGB`/`maxSessions` knobs) **and** the per-model
`participates(model)` check — true only when the model's generated cmd carries
`--slot-save-path` (the per-model checkbox). A non-participating model is left
completely alone. Disabled cache = branchless no-op middleware.

**Wiring.** `slotCache.middleware` sits in the model-dispatch chain. Cross-swap
persistence also needs two router process hooks: **pre-stop → `saveOnEvict`** (snapshot
before the process dies) and **post-start → `restoreOnLoad`** (restore once the new
process is Ready, before the triggering request is served). Without those hooks the
cold path is dead — if preamble/conversation restore never fires, check they're called.

### Two file categories

1. **Conversation snapshots** — `model__<key>.bin` (+ `.meta` preamble sidecar). One
   per ongoing chat. Keyed by `sessionAnchor`: the `X-Conversation-Id` header if the
   client sends one (preferred — survives compaction, no opening collisions), else
   `sha256(firstSystem + firstUser)`. Bounded by an LRU budget (`enforceCaps`).
2. **Preamble caches** — `model__preamble_<hash>.bin`. A **new category**: one
   system+tools-only KV per `(model, preamble)`, i.e. per agent/environment (pi,
   playground chat, any OpenAI-chat-schema or Anthropic `/v1/messages` harness). Seeds
   *every* cold/warm load that shares that preamble, so a brand-new conversation reuses
   the static prefix instead of reprefilling it. `hash = sha256("preamble\x00"+preamble)[:16]`;
   `preamble = system + "\x00tools\x00" + toolsJSON`. Differentiation is **purely
   content** — nothing knows which harness sent it; identical bytes share one file.

### Save path (conversation snapshots)

- **WARM** (`onSwitch`, model already loaded): a new conversation arrives → save the
  outgoing one if worth it, restore the incoming one if on disk.
- **COLD** (`saveOnEvict`): evicting model A to load B kills A's process with no A
  request to trigger a save — the pre-stop hook snapshots it.

"Worth saving" = live KV ≥ `minSaveTokens` — **cost is the only gate**, in both `onSwitch`
and `saveOnEvict`. There is no turn-count gate: a single-turn chat with a long answer (or
a big restored context) is still expensive to reprefill. An earlier `continued` gate
(human sent ≥2 user turns) lived only on the warm path, and silently dropped such a
conversation from occupant tracking when another anchor took the slot (e.g. a title-gen
request sharing the preamble) — so a later unload's `saveOnEvict` couldn't recover it
either, and the cold continuation fell back to the preamble (0 reuse on hybrid). Removed.

### Restore / seed path (preamble caches + Tier-1)

On a load with no exact conversation file, both warm (`onSwitch`) and cold
(`restoreOnLoad`) try, in order:

1. **`ensurePreambleSeed`** — if this agent's `preamble_<hash>.bin` exists, restore it
   (`preamble-hit`). Else **mint** it: `synthPrefill` POSTs a system+tools-only
   `max_tokens:1` chat (llama-server can only save the *whole* live slot, so a clean
   preamble-only KV needs a synthetic prefill while the slot is safe to clobber), then
   save the resident KV as the preamble cache (`preamble-mint`). Gated on a non-empty
   system prompt **and** `len(preamble) ≥ seedMinPrefixBytes` (2 KB).
2. **`bestSeed`** (Tier-1 fallback) — restore a prior session sharing a ≥2 KB
   leading preamble prefix, chosen to **minimize over-restore** (KV beyond the
   shared prefix): tail-free preamble caches first, then longest shared prefix,
   then smallest `.bin`. Handles preambles too short/system-less to mint a clean
   preamble cache. **Why minimize over-restore:** restoring a long sibling
   *conversation* whose tail diverges from the new prompt is wasted I/O on
   plain-attention models and actively harmful on hybrid/recurrent ones — the
   restored state sits at the sibling's full length, the new prompt matches only
   the shared preamble, and the un-rewindable layers emit llama.cpp's
   `non-consecutive token position N after M` + full reprocess (0 reuse). A
   preamble cache has no conversation tail so it never over-restores.
   - **Recurrent/hybrid models skip the slot cache ENTIRELY** (`recurrentSkip`, H1)
     — save, exact restore, AND partial-prefix seeding. Whole-slot restore reuses
     **0 tokens** on GatedDeltaNet/SSM (they can only restore recurrent state at its
     exact saved length; llama-server reprocesses the whole prompt otherwise —
     upstream llama.cpp #21831). This was **measured, not assumed**: on
     Qwen3.6-35B-A3B a *warm, same-process, exact* whole-slot restore of a
     31,993-token prompt reused 0 tokens (`confirm-miss`, prefill 86.1s→85.5s, 1.0×).
     So save/restore was net-negative here — a multi-GB write under the global lock
     for zero benefit — and is now a clean no-op. The middleware bails before even
     reading the body (`promptCanon` already stabilizes the prefix for the native
     warm reuse that IS the win on these archs). Detection: `newSlotCache`'s
     `recurrent` predicate reads the model's gguf (`autogen.ReadGgufMetadataCached`,
     memoized per gguf) and treats `FullAttnInterval > 0` as recurrent. Re-enable
     (drop the `recurrentSkip` guards in `middleware`/`saveOnEvict`/`restoreOnLoad`)
     only if #21831 lands upstream.

After any restore, `awaitConfirm[model]` is set; the **next** request's upstream
`cached_tokens` (via `confirmReuse`, called from the metrics monitor) is the honest
proof the KV was actually reused (`confirm` / `confirm-miss`), not just loaded.

### Pruning (three mechanisms)

- **`enforceCaps`** — LRU by mtime to stay within `maxDiskGB` / `maxSessions`.
  Preamble caches are **exempt** (sticky shared seeds).
- **`prunePreambleFiles`** — blind backstop: keep newest `maxPreambleGenerations` (3)
  preamble files per model.
- **`dropStalePreambles`** — on mint, delete a prior preamble whose stored preamble is
  the **same agent apart from a small dynamic span** (`supersedesPreamble`: shared
  prefix + shared suffix, non-matching middle ≤ `preambleDynDeltaMax` 512 B on both
  sides). Catches a daily date bump (even mid-prompt) without nuking a different agent
  that merely shares identical tools. Needs the full preamble in `.meta`, so preamble
  sidecars are stored **uncapped** (conversation `.meta` stays capped at `metaMaxBytes`).

### Gotchas

- **We mutate the forwarded prompt (timestamp normalization).** `sessionAnchor`
  strips the time-of-day from any ISO datetime in the **system prompt** (e.g. pi's
  per-session memory snapshot `session_start at 2026-06-29 12:35:44` → `...2026-06-29`)
  via `normalizeTimestamps`/`isoTimeOfDay`, and rewrites the body it re-attaches so the
  upstream sees the date-only form too. Without this, an agent that stamps the wall
  clock into its preamble changes the preamble hash every run (every `/clear`), forcing
  a fresh 360 MB preamble-KV mint each time instead of a cache hit. Scope is **system
  prompt only** — user messages keep their timestamps (pasted logs may need the
  seconds). Tradeoff: the model sees date granularity, not the exact time. Bare dates
  (no time-of-day) are untouched. Always on when the slot cache participates; add a
  config gate only if date-only forwarding ever bites.
- **Single slot.** All save/restore hit `/slots/0`. One global `sc.mu` serializes them
  — a multi-GB save blocks other models' requests for its duration (fine for single-user
  local inference; upgrade path = per-model locks).
- **Cold mint template mismatch.** `synthPrefill` always mints via OpenAI
  `/v1/chat/completions`. A harness served through a *different* upstream template
  (Anthropic-native `/v1/messages`) may tokenize the preamble differently → restored KV
  won't prefix-match → shows as `confirm-miss`, no correctness harm. Upgrade path: mint
  via the request's own endpoint.
- **Anthropic system.** `sessionAnchor` falls back to the top-level `"system"` field
  when there's no system-role message, so `/v1/messages` harnesses still get a preamble.
- **Stats lock.** `record()` uses a separate `statsMu` so it's callable from inside
  `sc.mu`-held sections without reentrancy.
- **Hybrid / SWA / recurrent models.** Some families don't use plain full attention:
  sliding-window (Gemma), linear-hybrid (Qwen3.6-35B-A3B = Gated DeltaNet ×3 : Gated
  Attention ×1, only ~16 of 64 layers carry KV; Qwen3-Next), recurrent (Mamba, RWKV).
  It was long assumed that whole-slot restore (wholesale at a fixed position) sidesteps
  the upstream prefix-cache bugs (llama.cpp #21831, #19794, #21468) on these archs. **The
  probe disproved that for GatedDeltaNet** (Qwen3.6): even an exact, warm, same-process
  whole-slot restore reuses 0 tokens — the server reprocesses the whole prompt (measured
  86.1s→85.5s on a 32k prompt, `confirm-miss`). So for `FullAttnInterval>0` models the
  cache is skipped entirely (`recurrentSkip`, H1 above) rather than doing net-negative
  work. **SWA (Gemma, `kvConstGB>0` non-recurrent) and plain-attention models are NOT
  gated** — `recurrent()` is false for them and their whole-slot restore does reuse. The
  KV Cache tab's confirm-hit/miss ratio is the ground truth — if a new arch shows
  `confirm-miss` waste, extend `recurrent`/`recurrentSkip` to cover it. Repro:
  `scripts/kvcache_probe.py switch` (warm) or `swap` (cross-process).
  - **Warm-slot skip (`preamble-warm`).** `onSwitch` does NOT restore the disk preamble
    when the slot already holds that exact preamble live (`occupant.preamble == preamble`,
    a different conversation from the same agent). A disk restore there would clobber valid
    live state with a worse copy — on hybrid models the restored preamble yields 0 reuse
    for a new continuation. Skipping lets the upstream reuse the shared prefix natively
    (full reuse) — and on hybrid models that **warm native reuse is where the win comes
    from**, not the disk restore. The disk preamble pays off on a genuinely-cold load
    (fresh process, no live state) **for plain-attention models**, where a restored
    preamble re-extends cleanly; on hybrid models a cold disk restore still yields ~0
    reuse for a new suffix (inherent, no correctness harm).

## Connections

Depends on:
- `internal/router` — local (`NewGroup`/`NewMatrix`) and peer routers; the scheduler/state owner this package dispatches into.
- `internal/config` — `Config`, model/capability/filter config, `RealModelName`, `SanitizeCommand`, `ListenerModelSets`.
- `internal/shared` — `FetchContext`/`SetContext`/`ReadContext`, error/response helpers, event payload types (`LiveTokensEvent`, `InFlightRequestsEvent`, `ProcessStateChangeEvent`, etc.).
- `internal/event` — pub/sub bus used by the SSE stream and inflight/metrics/live-token emitters.
- `internal/chain` — middleware composition.
- `internal/logmon` — proxy/upstream/mux log monitors backing `/logs`.
- `internal/perf` — system/GPU stats for `/api/performance` and `/metrics`.
- `internal/autogen` — sidecar override/settings I/O, gguf metadata, load-plan estimation (config editor only).
- `internal/cache`, `internal/ring` — capture cache and metrics ring buffer.

Called by: `quartermaster.go` (root entry) constructs the `Server` via `New`, wires `NewLoggers` and `SetAutogenAdmin`, and drives each listen address through `ServeListener`.
