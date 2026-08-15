# internal/server

## Purpose

Owns the HTTP layer: it builds the mux, applies cross-cutting middleware (auth, CORS, request
context, request-body filters, in-flight counting, metrics/captures), and dispatches model-routed
requests between the local router and remote peers. It also serves the OpenAI-compatible catalog,
the operations/UI JSON API, the embedded Svelte UI, and — fork additions — per-listener catalog
scoping, live token streaming, the `-generate` config-editor endpoints, the model hub browser, the
slot KV cache and the standalone playground app.

## Which doc

| Doc | Covers |
|---|---|
| [`routes.md`](routes.md) | The full route table: what is registered where, and on which middleware chain |
| [`http-core.md`](http-core.md) | The mux, chains and dispatch — `Server`/`New`/`localPeerHandler`, auth + admin gating, per-listener scoping, metrics teeing, in-place reload, synthetic `?ctx=`/backend variants, VRAM protection, reasoning-effort translation, prompt canonicalization, the embedded UI |
| [`configapi.md`](configapi.md) | The `-generate` config editor + managed backend installs (`configapi*.go`, `backendsapi.go`, the pickers) |
| [`hubapi.md`](hubapi.md) | `/api/hub/*` — search, download jobs, the pre-download context sizer, reveal-folder |
| [`slotcache.md`](slotcache.md) | Slot KV-cache persistence: preamble seeding, save/restore paths, pruning, the recurrent-arch skip |
| [`playground.md`](playground.md) | The playground app + the **server-owned turn runner**: tool loop, reasoning-box titles, tool-call replay, the quartermaster MCP, assistant memory |
| [`tools.md`](tools.md) | The chat tools' fetch paths: web-search chain, `fetch_page`/SSRF guard, YouTube, calc/units/datetime, weather, currency, feeds, imgproxy |

Also here: `turns_design.md` — the turn runner's design notes.

## Invariants worth knowing before you touch this package

- **One `Server`, N listeners, ONE router/scheduler.** Multi-listener operation and cross-port
  eviction depend on it; per-listener restriction lives in the *request context*, never in a
  handler. See `../../CLAUDE.md`.
- **A new `/api/*` ops or editor route goes on `adminChain`, not `apiChain`.** API keys gate the
  inference API only — they never cover the admin surface, which is gated by remote address.
  Getting this wrong publishes the config editor to whatever the port is bound to.
- **Reload is in-place.** `Server.ApplyConfig` swaps the config pointer and the handler on the one
  long-lived `Server`; SSE streams, metrics history, saved KV and running processes survive. An
  invalid config touches nothing.
- **Ask a launch command questions via `config.ParseCmd`**, never `strings.Contains` — substring
  tests break on line-wrapped flags and match prefixes of longer flags.
- **Prompt bytes are cache state.** Tool descriptions, system-prompt lines and reasoning-effort
  levels sit in the KV-stable prefix; changing them invalidates every conversation. Anything
  volatile (today's date) belongs in the tool *result*, not the tool *description*.
- **Every tool argument is model text.** URLs get the `fetch_page` SSRF guard, currency codes and
  video ids are validated or rebuilt before reaching a URL or argv, and `calculate` is a closed
  grammar rather than an evaluator.
- **The config editor is `-generate`-only** — every handler 501s when `s.autogen == nil`.

## Connections

Depends on:

- `internal/router` — local (`NewGroup`/`NewMatrix`) and peer routers; the scheduler/state owner
  this package dispatches into.
- `internal/config` — `Config`, model/capability/filter config, `RealModelName`,
  `SanitizeCommand`, `ListenerModelSets`.
- `internal/shared` — `FetchContext`/`SetContext`/`ReadContext`, error/response helpers, event
  payload types (`LiveTokensEvent`, `InFlightRequestsEvent`, `ProcessStateChangeEvent`, …).
- `internal/event` — the pub/sub bus behind the SSE stream and the inflight/metrics/live-token
  emitters.
- `internal/chain` — middleware composition.
- `internal/logmon` — proxy/upstream/mux log monitors backing `/logs`.
- `internal/perf` — system/GPU stats for `/api/performance` and `/metrics`.
- `internal/autogen` — sidecar override/settings I/O, gguf metadata, load-plan estimation.
- `internal/hub` — Hugging Face search + resumable downloads behind `/api/hub/*`.
- `internal/backends` — managed backend installs behind `/api/backends/*`.
- `internal/cache`, `internal/ring` — the capture cache and the metrics ring buffer.

Called by: `quartermaster.go` (root entry) constructs the `Server` via `New`, wires `NewLoggers` and
`SetAutogenAdmin`, and drives each listen address through `ServeListener`.
