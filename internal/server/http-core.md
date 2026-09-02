# internal/server — HTTP core

The mux, the middleware chains, model dispatch, the catalog, and the cross-cutting fork additions
(auth scope, admin gating, per-listener catalogs, synthetic variants, VRAM protection). Routes are
listed in [`routes.md`](routes.md).

## Key files

| File | Role |
|---|---|
| `server.go` | `Server` struct, `New`, the route table (`routes`), local/peer dispatch (`localPeerHandler`), version-prefix stripping, preload kickoff, graceful shutdown. |
| `api.go` | `/v1/models` listing + capability rendering, `/running`, `/unload`, `/metrics` (Prometheus), `/upstream/...` passthrough, model-in-path resolution, preload machinery. |
| `apigroup.go` | UI-facing `/api/*` JSON endpoints: the SSE event stream, unload, metrics/performance/version/capture, and the `modelStatus` payload (fork: tags each model with `family`, `group`, `listeners`, plus the Models-table columns `quant`/`sizeGB`/`estVramGB`/`estRamGB`). |
| `modelmeta.go` | Model file facts for the Models table. `quantFromPath` reads the **first** quant-shaped `-`-part of the gguf name (what follows is a build tag (`-MTP`, `-00001-of-000NN`), and autogen only strips the quant when it ends the filename, so an id can carry it twice; a preceding `UD`/`i1` recipe marker is folded in, `UD-Q4_K_XL`). `modelKeys` adds the table's grouping axes from the gguf HEADER (`autogen.IdentityOf`) with the id rules as fallback — see the gotcha below. `fileSizeGB` is shard-set aware via the `-00001-of-000NN` glob. All feed `modelStatus`, which re-renders on every SSE tick, so sizes AND identities are cached in a `sync.Map` — one stat per path per process. |
| `auth.go` | Auth, request-context and CORS middleware; `Access-Control-Request-Headers` sanitization. |
| `admin.go` | Fork: the remote-address gate for the unauthenticated admin surface (`adminChain`). `SetAdminAccess(localOnly, allow)` + `ParseAdminAllow`. |
| `apikeyscope.go` | Fork: per-key model scoping — `buildKeyScopes` (`cfg.APIKeyModels` → key ⇒ allowed-model set, dropping empty lists as unrestricted), `withKeyScope`, `apiKeyModelSet` (`ok=false` = unrestricted). Read by the auth middleware, `handleListModels` and `localPeerHandler`. |
| `listener.go` | Fork: per-listener catalog scoping. `ServeListener` stores a listen address's allowed model set in request context; `listenerModelSet` reads it back. |
| `filters.go` | Request-body filter middleware (model-name rewrite, strip/set params) for JSON and multipart form requests. `resolveFilters` also hands back the model's `capabilities.reasoningEffort` ladder, and the JSON path runs `applyReasoningEffort` **after** `applyFilters` so a `setParams`/`stripParams` rule still has the last word. |
| `reasoning_effort.go` | Fork: translates OpenAI's `reasoning_effort` into the `chat_template_kwargs.reasoning_effort` the jinja actually reads — see below. |
| `family.go` | `modelFamily` — derives a stable grouping key (the gguf `-m`/`--model` path, via `config.ParseCmd`) so the UI groups variants of one model. |
| `variant.go` | Fork: **synthetic per-request model variants** — see below. |
| `inflight.go` | Atomic in-flight request counter + middleware emitting `InFlightRequestsEvent`. |
| `log.go` | Logger construction (`NewLoggers`), `/logs` + `/logs/stream` handlers, access-log middleware, `statusRecorder` (status/size capture + Flush/Hijack passthrough). |
| `metrics.go` | `metricsMonitor` (token-usage parsing, bounded ring buffer, capture storage) and `responseBodyCopier` (tees the upstream response for metrics while streaming to the client). |
| `metrics_middleware.go` | Middleware that resolves the model, buffers request body/headers for capture, restricts `Accept-Encoding`, tees the response, and records metrics after dispatch. |
| `usagedetails.go` | Fork: fills the standard OpenAI usage detail fields llama-server only reports in its own `timings` (`prompt_tokens_details.cached_tokens`) or not at all (`completion_tokens_details.reasoning_tokens`, estimated — see below). |
| `captures.go` | Request/response capture storage: per-route field masks, zstd+CBOR (de)compression, header redaction. |
| `livemetrics.go` | Fork: `liveTokenCounter` — scans streaming SSE chunks and emits throttled `LiveTokensEvent`s for a live tokens/sec readout. |
| `backendmetrics.go` | Fork: `backendMetricsMonitor` — polls each running llama-server's `/metrics`+`/slots` on a 2s ticker for KV-fill / slot-saturation / throughput gauges, **skipped while busy** (both share llama-server's inference task queue; `RequestsProcessing` comes from quartermaster's own in-flight counter instead). `/props` is fetched once per process lifetime (static). Caches per-model, emits `BackendMetricsEvent` over SSE; `GET /api/backend-metrics` snapshot. |
| `promptcanon.go` | Fork: **always-on** prompt-canonicalization middleware — see below. |
| `ui.go` | Embedded SPA serving from `ui_dist` (`//go:embed`), pre-compressed (br/gzip) file selection, SPA index.html fallback, favicon. |
| `upscale.go` | Fork: `POST /v1/images/upscale` — standalone ESRGAN upscale. **Exec-per-request, NOT a loaded/swapped model**: shells out to `realesrgan-ncnn-vulkan` (`kind:upscale` registry entry) per call, mutex-serialized, tile-capped VRAM. `{image,model?,scale?}` → `{image,model,scale}`; model files (`<name>.param`/`.bin`) discovered in `<exeDir>/models`. `hidewindow_{windows,other}.go` = `hideConsole(cmd)` so no console window pops. |
| `toolsapi.go` | Fork: `POST /v1/tools/search`, `/v1/tools/youtube/transcript`, `/v1/tools/youtube/search`, `/v1/tools/youtube/comments` — tool **execution** for external AI projects (executors in `internal/tools`). On `discoveryChain` = same API-key credential as the inference routes; responses are the model-ready shapes the turn loop feeds a model; errors OpenAI-shaped (`400` bad args / `ErrNoProviders`, `502` upstream, `503` `ErrDlpMissing`). Stateless per call — provider config rides in the body, never persisted. |
| `toolsbridge.go` | Fork: thin aliases re-exporting the `internal/tools` executors under the names `turns.go` already uses, plus the per-turn tool budgets (`maxYouTube`, `ytTurnTokens`, `ytMinTranscript`, `maxYtBrowse`, `maxYtComments`) — turn-layer policy, deliberately not in `internal/tools`. |
| `loras.go` | Fork: `filterLorasResponse` — buffers the `/sdapi/v1/loras` response and drops non-LoRA rows. sd-server lists every weight file under `--lora-model-dir`, which autogen points at the model gguf's own folder (zero-config drop-in), so checkpoints and encoders showed up in the picker as guaranteed failures. Filtered **by file identity, not guesswork**: any row resolving to a path some model launches with (`-m`, `--diffusion-model`, `--vae`, `--clip_l`, …) is removed, since a real LoRA is never a launch argument. `bufferedResponse` is for this one-shot JSON only — never reuse it on a streaming route. |
| `appwindow.go` | Fork: `GET /api/app/show` — raises the native desktop window (`-app`); `showAppWindow` hook + `SetShowAppHook`. Loopback-checked against `r.RemoteAddr` ONLY (never the forwarded-for headers) and 404 when there is no window, so a LAN/tailnet host can neither pop a window nor learn one exists. The `{"app":"quartermaster"}` marker in the body is what a second launch verifies before deciding the port is ours and exiting — see `cmd/quartermaster/singleinstance.go`. |
| `update.go` | Fork: `POST /api/update` — downloads + launches the release installer then shuts down gracefully (Windows release builds only); `updater` field + `SetShutdownHook`, status surfaced in `handleAPIVersion`. |
| `autostart{,_windows,_other}.go` | Fork: `GET`/`PUT /api/autostart` over the per-user Windows Run key (`HKCU\…\CurrentVersion\Run`); works without `-generate`. Every install writes the SAME value name `Quartermaster`, so at most one can autostart: a `PUT` whose stored entry points at a *different* exe returns **409 + the status** instead of clobbering it (the dashboard offers Take over → re-`PUT` with `takeover:true`). The written command is this process's own argv, relative paths absolutised, `-tray` forced on. Non-Windows: `supported:false`. |

## Important types & functions

- `Server` (`server.go`) — owns mux/handler, local `router.LocalRouter` + peer `router.Router`,
  `metricsMonitor`, `inflightCounter`, `perf.Monitor`, `listenerModels`, optional `autogen` admin.
- `New(...)` — constructs routers (group or matrix per `cfg.Routing.Router.Use`) + peer, builds
  routes, fires preload.
- `localPeerHandler` — resolves the model once via `shared.FetchContext`, enforces per-listener
  scoping, routes local or peer.
- `routes` — assembles the middleware chains and registers every route; wraps the mux with
  request-log + CORS.
- `Shutdown` / `CloseStreams` — idempotent parallel router teardown; `CloseStreams` cancels SSE so
  the HTTP drain doesn't block.
- `handleListModels` / `renderCapabilities` (`api.go`) — `/v1/models` with capability rendering +
  per-listener filtering.
- `modelStatus` / `groupIndex` / `handleAPIEvents` (`apigroup.go`) — the per-model UI payload + the
  SSE multiplexer (modelStatus, logData, metrics, inflight, liveTokens, backendMetrics).
- Middleware constructors: `CreateAuthMiddleware`, `CreateRequestContextMiddleware`,
  `CreateCORSMiddleware` (`auth.go`), `CreateFilterMiddleware`/`CreateFormFilterMiddleware`
  (`filters.go`), `CreateInflightMiddleware` (`inflight.go`), `CreateMetricsMiddleware`
  (`metrics_middleware.go`), `CreateRequestLogMiddleware` (`log.go`).
- `ServeListener` (`listener.go`) — the per-listener entry point for multi-listener startup.

## Auth, admin and scoping (fork)

- **Auth scope.** API keys gate the **external inference API only** — model-dispatch routes
  (`modelChain`) plus discovery (`/v1/models`, `discoveryChain`). Dashboard / admin / ops / SSE /
  `/ui/` (`apiChain`) are **open**, so enabling keys never locks the operator out of their own UI.
  `CreateAuthMiddleware` is a pass-through with no configured keys; keys are accepted via
  `Authorization: Bearer`, `Authorization: Basic` (password field), or `x-api-key`. A key may be
  **model-scoped** via `cfg.APIKeyModels`: the middleware attaches the allowed set to the request
  context (`apikeyscope.go`), and `handleListModels` + `localPeerHandler` intersect it with the
  listener scope. Empty/absent scope = full access. Keys + scopes are emitted into the generated
  config by autogen and managed via `/api/apikeys`.
- **The admin surface is gated by REMOTE address, not by listener.** API keys never cover
  `apiChain`, so publishing the API to the LAN/tailnet (`-listen 0.0.0.0:1250`) would otherwise
  expose the dashboard, `/upstream/*`, captures, unload and the config editor to every host that can
  reach the port — and since API + dashboard share one port, the split can't be per-listener.
  `adminChain` (`s.requireAdmin`, `admin.go`) 403s any admin route whose `r.RemoteAddr` isn't
  loopback or inside an `-admin-allow` CIDR (e.g. `100.64.0.0/10` for a tailnet). `main` enables it
  automatically when a non-playground listen address is non-loopback; `-admin-open` restores the
  wide-open behaviour. Ungated: `/v1/*` (the keys' job), `/health`, `/api/version`, `/metrics`,
  `/favicon.ico`, and every playground route (own port + login; `isPlaygroundRequest` exempt).
  **Adding a new `/api/*` ops or editor route means wiring it to `adminChain`, not `apiChain`.**
- **Per-listener catalog filtering** lives in the request context, not the handler. `ServeListener`
  injects the address's allowed model set; `handleListModels` and `localPeerHandler` read it via
  `listenerModelSet`. `ok=false` = unrestricted (legacy single `--listen`). Peer models are omitted
  from restricted listeners. All listeners share one `Server` — one router/scheduler — which is the
  invariant making cross-listener VRAM accounting and eviction correct.

## Dispatch and config gotchas

- **The Models table groups on what the GGUF says it is, not on its name.** `modelKeys`
  (`modelmeta.go`) stamps `modelKey` (one model = one row, a pill per quant) and `familyKey`
  (finetunes of one base) into every `/api/events` model, reading `autogen.IdentityOf` - the
  header's `general.basename`/`size_label`/`finetune` - and falling back to `autogen.ModelBaseKey`/
  `FamilyKey` for the ~1/3 of files that carry no identity KVs. **The UI must not re-derive
  either from the id**; guessing where a name ends and a quant begins is what put one model on
  five rows (`Qwen3.8-27B-mix-q-k-mtp`). The two key spaces are deliberately un-namespaced: a
  header key and an id key that spell the same string ARE the same model.
- **`quant` and `quantLabel` are not interchangeable, and only `quant` may merge.** `quant` stays
  the token in the FILENAME, because the table also fuses two folders' copies of a model on it and
  that fusion must rest on a name both files agreed on. `quantLabel` is the tensor-derived truth
  (`Metadata.QuantLabel`), sent only when the filename named nothing, and is display-only - two
  unrelated hand-built quants can both compute `Q3_K mix` without being one download.

- **Metrics teeing.** `CreateMetricsMiddleware` resolves the model up front (priming
  `shared.FetchContext`'s fast path), restricts `Accept-Encoding` to gzip/deflate so the buffered
  body stays parseable, and wraps the writer in `responseBodyCopier`. Both `responseBodyCopier` and
  `statusRecorder` forward `Flush`/`Hijack` so SSE and websocket upgrades keep working. Captures are
  off unless `CaptureBuffer > 0`.
- **Usage-detail enrichment only ever adds.** `CreateUsageDetailsMiddleware` sits inside the
  metrics tee and rewrites the outgoing usage object: `cached_tokens` from `timings.cache_n` (real)
  and `reasoning_tokens` from the output-token count split by reasoning-vs-content text length
  (an estimate, always written with a `reasoning_tokens_estimated: true` sibling — never emit one
  without the other). A field the upstream already reported is left alone. Everything else degrades
  to a byte-identical passthrough: non-200, compressed, non-JSON/SSE, unparseable. Single-shot JSON
  is withheld until the handler returns so `Content-Length` can be corrected; SSE is rewritten
  frame by frame (the usage chunk is last, so the text split is complete by then).
- **`X-QM-*` response headers** (`router/base.go`): `X-QM-Model`, `X-QM-Model-Loaded` (did this
  request pay for a load), `X-QM-Wait-Ms` (queue + load before dispatch). Set before the upstream
  writes its header; invisible to OpenAI clients. Cross-origin browsers cannot read them without an
  `Access-Control-Expose-Headers`, which is deliberately not set.
- **Reload is in-place (`Server.ApplyConfig`).** The ONE long-lived `Server` (SSE streams, metrics
  history, slotCache saved KV, goroutines, running processes) survives a config change — only the
  config pointer and the cfg-derived handler swap. `s.cfg`/`s.listenerModels` are `atomic.Pointer`s
  (read via `s.config()`); `s.handler` is `atomic.Pointer[http.Handler]`. `ApplyConfig(newCfg)`
  validates and applies to the shared router first (`s.local.ApplyConfig` — add/remove/retune in
  place, no eviction; new launch args apply on the model's NEXT load), then swaps `s.cfg`, refreshes
  `s.listenerModels`, and calls `s.routes()` to rebuild and atomically swap the handler without
  dropping in-flight requests or SSE. **Invalid config = nothing touched.** `main.reload` just calls
  `activeSrv.ApplyConfig`, and every save path (cogwheel, SIGHUP, `-watch-config`, `-watch-models`)
  flows through it, then emits one `ConfigFileChangedEvent`. NOT live-swapped: bound listen sockets
  (main warns via `listenerAddrsChanged`; per-listener *scoping* does refresh) and peer targets —
  both need a restart.
- **`RunningCmd` in `modelStatus`** = the actual argv the process spawned with
  (`s.local.LaunchedCmd(id)`), set only while running. It differs from the config command after a
  live settings edit or a spawn-time offload rewrite, so the UI staging card shows what is REALLY
  loaded.
- **Ask a command questions via `config.ParseCmd`, not `strings.Contains`.** Every consumer needing
  a fact out of a rendered launch command (`modelFamily`, `slotParticipates`/`slotRecurrent` in
  `server.go`, `cmdArgv` in `configapi_estimate.go`, `portFromCmd` in `configapi_adhoc.go`, the
  image/audio/MTP sniffs in `configapi.go`, `loras.go`) goes through the shared memoized `CmdInfo`.
  Substring tests break on line-wrapped flags and match prefixes of longer flags.
- **Family / group tagging.** `modelFamily` keys models by gguf path so the UI collapses
  ctx/game/judge variants; `modelStatus` also tags each model with its swap group and exposing
  listeners. It is a thin memoized wrapper over `config.ParseCmd(cmd).ModelPath` (it runs per SSE
  status build and per slot-cache request).
- **Embedded UI.** `ui_dist` is `//go:embed`-ed; the Makefile `ui` target copies the Svelte build
  in, and a placeholder keeps the embed valid before any build. Pre-compressed `.br`/`.gz` siblings
  are preferred; extensionless misses fall back to `index.html` for SPA routing.
  `uiMimeTypes`/`setUIContentType` **pin the Content-Type of every extension the build emits**
  instead of letting `http.ServeContent` fall through to `mime.TypeByExtension`, which on Windows is
  driven by the registry: there is no `.mjs` row, so the pdf.js worker was served as `text/plain`
  and the browser refused to start it. `.js` is registry-provided too, so the same failure was one
  machine away for the whole bundle.
- **Access-log skips.** `/wol-health`, `/api/performance`, `/metrics` are excluded (poll traffic).
  The path is captured *before* `next` runs, because `/upstream` rewrites the URL in place.
- **Versionless routes.** `/v/...` is rewritten to `/...` by `stripVersionPrefix` before forwarding
  (issue #728).

## Synthetic per-request variants (fork — `variant.go`)

`X-QM-Backend: <id>` and `?ctx=<N>`/`X-QM-Ctx` do NOT parameterize the running process — they
**mint a new model id** (`realID@<backend>`, `realID@ctx<N>`).

`ensureCtxVariant(realID, ctx)` mints on first use: a base-model copy whose cmd is re-rendered by
`renderAdhocCmd` for the requested context (`-ngl`/`--n-cpu-moe`/KV types **re-derived, not
inherited**). `requestedCtx` reads the size off the `?ctx=` suffix or `X-QM-Ctx`.
`ensureBackendVariant` does the `X-QM-Backend` exe swap; both share the COW `addVariantToConfig`.

- The variant is `Unlisted`, reuses the base's allocated `${PORT}`, and joins **every group the base
  is in** — that shared exclusive group is what makes port reuse and VRAM accounting correct, since
  base and variant can never be resident together.
- Minting is a read-modify-`ApplyConfig` cycle over the whole config, serialized on
  `Server.variantMu`. **Never mutate the live config's maps or a group's `Members` slice in place.**
- A ctx variant's `EstVramGB` is **re-sized from its own rendered cmd** (`estVramForCmd` →
  `EstimatePlan`), not inherited: a smaller ctx is a smaller KV reserve, and that field is what the
  router admits against under a VRAM budget. A backend variant does inherit it, since only argv[0]
  changes.
- Ctx variants are capped per base (`maxCtxVariants`) so a client sweeping `?ctx=` can't grow the
  config unbounded; they last until the next file reload.
- **The `?ctx=` suffix resolves to the REAL model id.** `config.RealModelName` strips it
  (`config.SplitCtxRequest`), so listener scoping, key scoping, filters and metrics labels see the
  base model. `localPeerHandler` reads the requested ctx **after** both scope checks — moving it
  earlier would let a client reach an unscoped model by appending a suffix. Malformed or
  out-of-range values are not clamped; they fail to resolve.

## OOM / VRAM protection (fork)

Four pieces, all surfaced through `/api/performance`:

- **Foreign VRAM** — `foreignGPU`/`foreignVram`/`isInferenceProc` (`apigroup.go`) tally GPU memory
  held by `llama-server`/`sd-server` processes this instance did NOT spawn
  (`perf.QueryComputeApps` minus `router.RunningPIDs()`), returned as `"foreign"` so the sizer knows
  about VRAM it can't reclaim.
- **Idle floor** — `trackSystemVram` (`server.go`) keeps the minimum idle used-VRAM on the largest
  GPU while no model runs (`"system_mb"`), the baseline the budget must reserve.
- **Dynamic offload guard** — `WireDynamicOffload`/`freeVramGB` (`server.go`), the spawn-time argv
  rewriter wired into `autogen.LiveOffloadArgs`: it re-derives GPU/CPU layer placement from live
  free VRAM and **refuses** a spawn that can't fit (see `internal/autogen/liveoffload.md`).
  `offloadWithReclaim` decides when a reading is trustworthy: a probe taken while the driver is
  still releasing an evicted process's VRAM reads low, and **a low reading that *fits* is the
  dangerous case** — no error, just a layer or two more on CPU, so the same config lands `-ngl 65`
  cold and `-ngl 64` after a swap. It therefore keeps probing until either the baked plan fits
  untouched or free VRAM stops climbing (`vramReclaimEpsilonGB`, reclaim finished). Costs one extra
  probe (~0.7 s) on a genuinely tight load.

- **Post-load watchdog** — `vramGuard` (`vramguard.go`) sheds IDLE residents when foreign VRAM
  grows into their footprint. It publishes **two** ceilings, and the difference is load-bearing:
  `ceilingGB` (admission, fed to the router as `LiveVramFn`) charges the foreign excess *and*
  `oomGuardReserveGB`; `shedCeilingGB` (the watchdog) charges the excess **only**. Charging the
  reserve on both sides made the halves disagree about one model — a 21.8 GB resident under a
  22.8 GB budget went over the shed ceiling the moment a browser tab took 0.2 GB, and the spawn
  guard (which sizes against live *free* VRAM) reloaded it unchanged on the next request: an
  endless unload/reload cycle that reads like a too-short `ttl`. Two further brakes: shedding needs
  the overshoot to clear `vramGuardShedSlackGB` (estVramGB is an estimate, not a measurement), and
  after a shed the guard sits out `cooldown()` (2× grace, ≥1 min) before shedding again.

## Reasoning effort: advertised, then translated (fork)

`renderCapabilities` publishes the model's levels as `capabilities.reasoning_effort` (plus
`reasoning_effort` in `supported_parameters`) on `/v1/models`, so a client can discover the ladder
rather than guess it; `filters.go` → `reasoning_effort.go` then translates the field on the way
through. Both read one source — the autogen-emitted `capabilities.reasoningEffort`, which is
non-empty only when the model's own baked chat template declares its levels AND no
`--chat-template-file` replaces it. **Peers advertise and translate nothing**: a peer renders its
own template, so effort is its business.

`reasoning_effort.go` exists because effort is a **chat-template feature, not a sampler knob** — the
level is a sentence injected at the top of the system block. Two things make plain pass-through
insufficient: llama-server 9886 ignores the top-level field entirely (native support landed
2026-08-14), and newer builds forward it **verbatim**, so an OpenAI-ladder value the template
doesn't know (`minimal`) hits its raise-on-unknown guard and returns a 500.

`normalizeReasoningEffort` therefore snaps the request onto a level the model's own template
declares (`effortRank` puts both ladders on one scale; ties go to the **richer** level, matching
Qwen's own `high`→`xhigh` alias), maps `none`/`off` to `enable_thinking:false`, and **refuses
anything unrankable** — an unrecognised value, or a model advertising no ladder, leaves the body
byte-identical. An explicit `chat_template_kwargs.reasoning_effort` always wins; the top-level field
is deleted once translated, so exactly one mechanism carries the value whatever the build.

**Cost worth stating to users: switching effort mid-conversation invalidates the whole KV prefix**
(the level sits in the stable prefix). It is a per-session setting, not a per-request dial.

## Prompt canonicalization (fork — `promptcanon.go`)

`promptCanon.middleware` is **always on** and runs *before* slotcache/upstream for every chat
request, regardless of slot-cache participation: it strips sub-day timestamps from the system prompt
so the stable prefix stays byte-identical turn to turn, giving KV reuse for ANY client/model rather
than only slotcache participants. Date granularity is kept, and it is idempotent. `GET /api/canon`
is the snapshot for Observe → Context.

Not the same as the slotcache's own `normalizeTimestamps`, which is scoped to participating models'
anchoring — see [`slotcache.md`](slotcache.md).
