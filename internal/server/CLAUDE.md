# internal/server

## Purpose

Owns the HTTP layer: it builds the mux, applies cross-cutting middleware (auth, CORS, request
context, request-body filters, in-flight counting, metrics/captures), and dispatches model-routed
requests between the local router and remote peers. It also serves the OpenAI-compatible catalog,
the operations/UI JSON API, the embedded Svelte UI, and — fork additions — per-listener catalog
scoping, live token streaming, the `-generate` config-editor endpoints, the model hub browser, the
slot KV cache, the standalone playground app, and the `/v1/tools/*` tool-execution API
that external AI projects use to run the web-search / YouTube tools here instead of
re-implementing them.

## Which doc

| Doc | Covers |
|---|---|
| [`routes.md`](routes.md) | The full route table: what is registered where, and on which middleware chain |
| [`http-core.md`](http-core.md) | The mux, chains and dispatch — `Server`/`New`/`localPeerHandler`, auth + admin gating, per-listener scoping, metrics teeing, in-place reload, synthetic `?ctx=`/backend variants, VRAM protection, reasoning-effort translation, prompt canonicalization, the embedded UI |
| [`configapi.md`](configapi.md) | The `-generate` config editor + managed backend installs (`configapi*.go`, `backendsapi.go`, the pickers) |
| [`hubapi.md`](hubapi.md) | `/api/hub/*` — search, download jobs, the pre-download context sizer, reveal-folder |
| [`slotcache.md`](slotcache.md) | Slot KV-cache persistence: preamble seeding, save/restore paths, pruning, the recurrent-arch seed skip |
| `videojobs.go` (package comment) | Async video renders: the job registry, the scheduler lease, the watcher goroutine |
| [`playground.md`](playground.md) | The playground app + the **server-owned turn runner**: tool loop, reasoning-box titles, tool-call replay, the quartermaster MCP, assistant memory |
| [`tools.md`](tools.md) | The chat tools' fetch paths (executors in `internal/tools`): web-search chain, `fetch_page`/SSRF guard, YouTube, calc/units/datetime, weather, currency, feeds, imgproxy — and the `/v1/tools/*` execution API (`toolsapi.go`) |

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
- **A video render OUTLIVES its request, and that breaks two router assumptions.** sd-server's
  job API answers `POST /sdcpp/v1/vid_gen` in milliseconds and samples for minutes afterwards, so
  (1) the model looks idle the instant the reply lands and gets evicted or TTL'd out from under the
  render, and (2) `GET /sdcpp/v1/jobs/{id}` names no model and is unroutable. `videojobs.go` fixes
  both with a scheduler **lease** held for the life of the job plus a job-id -> model-id registry,
  and a watcher goroutine that polls to completion. **We poll, not the client**: a closed browser
  tab would otherwise pin the model forever, and the watcher's poll doubles as the TTL keepalive
  (only a real request through the process refreshes its `lastUse`). Terminal documents are served
  from the watcher's cache for `videoJobGrace` because the lease is already gone by then.
- **audio.cpp's `--config` is written at SPAWN, not at generate** (`audiocppconfig.go`).
  `audiocpp_server` has no `--model` flag, so the model is named only inside a JSON file. The
  generated YAML carries a typed `audiocpp:` block and the spawn hook materializes it under
  `<CacheDir>/audiocpp/`, which keeps one source of truth and leaves no stale generated files
  behind. It shares the process layer's ONE `SetSpawnArgs` slot with the live-VRAM placement
  guard (`WireDynamicOffload`) and runs first, because it only ever appends. The file is
  rewritten per spawn and deliberately not deleted on stop.
- **An audio.cpp voice IS a wav in `--voice-dir`** (`audiocppvoices.go`). audio.cpp serves GET
  `/v1/audio/voices` by scanning that directory live and has no POST route, so quartermaster
  answers the playground's clone POST itself: it writes `<voice-dir>/<name>.wav` plus a
  `<name>|<transcript>` line in `prompt_text`, WITHOUT starting the model (the scan is per
  request). Without this the clone-only packages, Qwen3-TTS Base among them, can be loaded but
  never spoken with. Requests for every other speech engine pass straight through.
  The GET is **answered**, not forwarded: for our models the list is two directories on disk
  (`--voice-dir` wavs and `<model path>/embeddings`), because we generate audio.cpp's config
  and never emit `voice_presets`. That buys two things. Each name gets the `kind` qwentts.cpp
  reports and audio.cpp does not, which the playground reads to tell a base model from a fixed
  speaker pack (unannotated, the first clone flipped a clone-only model into "fixed pack" and
  removed the Clone and delete buttons). And listing voices no longer loads the model, which is
  surfaced as the `voice_list_offline` capability so the playground stops waiting for a running
  model before it refreshes.
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
- `internal/tools` — the web-search provider chain and YouTube executors, shared by the turn loop
  (via the aliases in `toolsbridge.go`) and the `/v1/tools/*` API (`toolsapi.go`).
- `internal/cache`, `internal/ring` — the capture cache and the metrics ring buffer.

Called by: `cmd/quartermaster/quartermaster.go` (the entry point) constructs the `Server` via `New`, wires `NewLoggers` and
`SetAutogenAdmin`, and drives each listen address through `ServeListener`.
