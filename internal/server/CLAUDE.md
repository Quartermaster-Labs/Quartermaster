# internal/server

## Purpose

Owns the HTTP layer: it builds the mux, applies cross-cutting middleware (auth, CORS, request context, request-body filters, in-flight counting, metrics/captures), and dispatches model-routed requests between the local router and remote peers. It also serves the OpenAI-compatible catalog, the operations/UI JSON API, the embedded Svelte UI, and (fork additions) per-listener catalog scoping, live token streaming, and the `-generate` config-editor endpoints.

## Key files

| File | Role |
|---|---|
| `server.go` | `Server` struct, `New`, route table (`routes`), local/peer dispatch (`localPeerHandler`), version-prefix stripping, preload kickoff, graceful shutdown. |
| `api.go` | `/v1/models` listing + capability rendering, `/running`, `/unload`, `/metrics` (Prometheus), `/upstream/...` passthrough, model-in-path resolution, preload machinery. |
| `apigroup.go` | UI-facing `/api/*` JSON endpoints: SSE event stream, unload, metrics/performance/version/capture, and the `modelStatus` payload (fork: tags each model with `family`, `group`, `listeners`). |
| `configapi.go` | Fork: `-generate` config-editor endpoints — per-model override/variant editor, load-plan estimate, global settings (VRAM target/headroom) editor. All 501 when `s.autogen == nil`. |
| `auth.go` | Auth, request-context, and CORS middleware; `Access-Control-Request-Headers` sanitization. |
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

## Important types & functions

- `Server` (`server.go:24`) — owns mux/handler, local `router.LocalRouter` + `router.Router` peer, `metricsMonitor`, `inflightCounter`, `perf.Monitor`, `listenerModels`, and optional `autogen` admin.
- `New(...)` (`server.go:109`) — constructs routers (group or matrix per `cfg.Routing.Router.Use`) + peer, builds routes, fires preload.
- `localPeerHandler` (`server.go:154`) — resolves the model once via `shared.FetchContext`, enforces per-listener scoping, then routes to local or peer.
- `routes` (`server.go:193`) — assembles the middleware chains and registers every route; wraps the mux with request-log + CORS.
- `Shutdown` / `CloseStreams` (`server.go:291` / `:280`) — idempotent parallel router teardown; `CloseStreams` cancels SSE so HTTP drain doesn't block.
- `handleListModels` (`api.go:136`) — OpenAI `/v1/models` with capability rendering and per-listener catalog filtering.
- `renderCapabilities` (`api.go:45`) — maps `ModelCapConfig` into architecture/capabilities/supported_parameters/context_length fields.
- `modelStatus` (`apigroup.go:64`) + `groupIndex` (`apigroup.go:41`) — per-model UI payload tagged with group + exposing listeners.
- `handleAPIEvents` (`apigroup.go:232`) — SSE multiplexer (modelStatus, logData, metrics, inflight, liveTokens).
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
- API-key manager (local admin, `-generate` only): `GET /api/apikeys`, `POST /api/apikeys`, `DELETE /api/apikeys/{name}` (`configapi_apikeys.go`).

## Gotchas / conventions

- **Auth scope (fork).** API keys gate the **external inference API only** — the model-dispatch routes (`modelChain`) plus discovery (`/v1/models`, on `discoveryChain`). The dashboard / admin / ops / SSE / `/ui/` routes (`apiChain`) are **open** so enabling keys never locks the operator out of their own UI. `CreateAuthMiddleware` is a pass-through with no configured keys; keys accepted via `Authorization: Bearer`, `Authorization: Basic` (password field), or `x-api-key`. A valid key may be **model-scoped** via `cfg.APIKeyModels` (key → allowed model IDs): the middleware attaches the allowed set to the request context (`apikeyscope.go`), and `handleListModels` + `localPeerHandler` intersect it with the listener scope so a scoped key only sees/reaches its models. Empty/absent scope = full access. Keys + scopes are emitted into the generated config by autogen and managed via the local `/api/apikeys` endpoints (`configapi_apikeys.go`).
- **Per-listener catalog filtering.** Restriction lives in the request context, not the handler. `ServeListener` injects the address's allowed model set; `handleListModels` and `localPeerHandler` read it via `listenerModelSet`. `ok=false` means unrestricted (legacy single `--listen`). Peer models are omitted from restricted listeners. All listeners share one `Server` (one router/scheduler) — the invariant that makes cross-listener VRAM accounting/eviction correct.
- **Metrics teeing.** `CreateMetricsMiddleware` resolves the model up front (priming `shared.FetchContext`'s context fast path), restricts `Accept-Encoding` to gzip/deflate so the buffered body stays parseable, and wraps the writer in `responseBodyCopier`. Both `responseBodyCopier` and `statusRecorder` forward `Flush`/`Hijack` so SSE and websocket upgrades keep working. Captures are off unless `CaptureBuffer > 0`.
- **Config editor is `-generate`-only.** Every `configapi.go` handler short-circuits with 501 when `s.autogen` is nil. A successful edit upserts the sidecar override/settings, calls `autogen.EnsureConfig`, then hot-reloads (same path as SIGHUP) — slow because it re-reads gguf metadata, but acceptable for a settings save.
- **Embedded UI.** `ui_dist` is `//go:embed`-ed; the Makefile `ui` target copies the Svelte build in, and a placeholder keeps the embed valid before any build. Pre-compressed `.br`/`.gz` siblings are preferred; extensionless misses fall back to `index.html` for SPA routing.
- **Access-log skips.** `/wol-health`, `/api/performance`, `/metrics` are excluded from the access log to avoid drowning it in poll traffic. Path is captured before `next` runs because `/upstream` rewrites the URL in place.
- **Versionless routes.** `/v/...` paths are rewritten to `/...` by `stripVersionPrefix` before forwarding (issue #728).
- **Family / group tagging (fork).** `modelFamily` keys models by their gguf path so the UI can collapse ctx/game/judge variants; `modelStatus` additionally tags each model with its swap group and the listener addresses exposing it.

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

Called by: `llama-quartermaster.go` (root entry) constructs the `Server` via `New`, wires `NewLoggers` and `SetAutogenAdmin`, and drives each listen address through `ServeListener`.
