# quartermaster

## What this is

Started as a fork of [llama-swap](https://github.com/mostlygeek/llama-swap) (MIT) → **quartermaster**. Has since diverged enough that it is its own project — **not tracking upstream, no plan to merge back.**

Goal: an **all-in-one local inference engine** — not just text model swapping, but image, audio, and more, with **flexible automated model defaults** (runtime-computed ctx / offload / KV instead of hand-baked config variants).

Current work & roadmap: see `TODO.md` (local, not tracked).

## Where things live

Go project. Entry `cmd/quartermaster/` (`quartermaster.go`, plus `bundle.go`: the launch flags a
packaged install supplies for itself, so the exe is double-clickable and needs no launcher script).
Its `favicon.ico`, `versioninfo.json` and `resource_windows_amd64.syso` sit in that same directory
because Go links a `.syso` only from the main package dir and `//go:embed` cannot reach above it.
Logic under `internal/`. Each subsystem has its own `CLAUDE.md` — read that for files, types, `file:line` refs, and gotchas.

| Subsystem | Doc | What it does |
|---|---|---|
| Config generation | [`internal/autogen/CLAUDE.md`](internal/autogen/CLAUDE.md) | Discover GGUFs, estimate VRAM, emit startup config |
| HTTP server / routes / UI | [`internal/server/CLAUDE.md`](internal/server/CLAUDE.md) | OpenAI routes, `/v1/models`, multi-listener catalogs, live metrics, config API, embedded UI |
| Native window | [`internal/nativewin/CLAUDE.md`](internal/nativewin/CLAUDE.md) | The chrome-less WebView2 app frame + OS dialogs, shared by the wizard and the app window |
| Backend installer | [`internal/backends/CLAUDE.md`](internal/backends/CLAUDE.md) | Download/update/roll back backend binaries from upstream releases |
| Model hub browser | [`internal/hub/CLAUDE.md`](internal/hub/CLAUDE.md) | Search Hugging Face, pick a quant, resumable download into the models folder |
| Config model / macros | [`internal/config/CLAUDE.md`](internal/config/CLAUDE.md) | YAML load, macro expand, `${PORT}` alloc, listeners, filters, swap matrix DSL |
| Tool executors | [`internal/tools/CLAUDE.md`](internal/tools/CLAUDE.md) | Web-search provider chain + YouTube executors, shared by the turn loop and the `/v1/tools/*` API |
| Router / scheduler | [`internal/router/CLAUDE.md`](internal/router/CLAUDE.md) | The ONE shared scheduler — request → swap/load, group eviction |
| Process lifecycle | [`internal/process/CLAUDE.md`](internal/process/CLAUDE.md) | Spawn/health/TTL/evict of llama.cpp subprocesses |
| GPU / VRAM monitor | [`internal/perf/CLAUDE.md`](internal/perf/CLAUDE.md) | Live VRAM (Windows D3DKMT/PDH, darwin, unix), prometheus |
| Web UI | [`ui-svelte/CLAUDE.md`](ui-svelte/CLAUDE.md) | Svelte 5 + Vite dashboard + model playground |
| PE imports | `internal/peimports/peimports.go` (package doc) | Walks a Windows binary's import graph to name the DLL it is missing — turns a silent `0xC0000135` exit into "needs the AMD ROCm/HIP runtime" |
| Cache | [`internal/cache/CLAUDE.md`](internal/cache/CLAUDE.md) | FIFO byte cache |
| Handler chain | [`internal/chain/CLAUDE.md`](internal/chain/CLAUDE.md) | `http.Handler` middleware composer |
| Event bus | [`internal/event/CLAUDE.md`](internal/event/CLAUDE.md) | Generic pub/sub |
| Log monitor | [`internal/logmon/CLAUDE.md`](internal/logmon/CLAUDE.md) | Circular log buffer + broadcast |
| Ring buffer | [`internal/ring/CLAUDE.md`](internal/ring/CLAUDE.md) | Generic ring buffer |
| Shared helpers | [`internal/shared/CLAUDE.md`](internal/shared/CLAUDE.md) | Event structs, http helpers, errors, loopback |
| Config watcher | [`internal/watcher/CLAUDE.md`](internal/watcher/CLAUDE.md) | Stat-polling config reload (pkg `configwatcher`) |
| First-run wizard | [`internal/setup/CLAUDE.md`](internal/setup/CLAUDE.md) | Native-window setup program: asks, installs backends, drives the Inno installer silently (`cmd/quartermaster-setup`) |
| Self-update | [`internal/update/CLAUDE.md`](internal/update/CLAUDE.md) | GitHub release poll + in-place binary swap + self-relaunch. All platforms; **release** builds only — dev/local never polls |

## Architectural invariant

Multi-listener + cross-port eviction require **ONE process with N listeners sharing ONE router/scheduler.** Two quartermaster instances = two schedulers = no shared VRAM accounting = collision. Never split into instances.

## Build / test

Go module (`go.mod`). `Makefile` at root. Build binaries into `./build/`. Windows perf code behind build tags.

- `gofmt -w <file>` before committing.
- `go test -v -run <pattern>` for new tests. Test names: `TestProxyManager_<name>`, `TestProcessGroup_<name>`, etc.
- `make test-dev` — quick run (`go test` + `staticcheck`); fix all static-check errors.
- `make test-all` — full run incl. long concurrency tests; before completing work.
- `make test-ui` — after UI changes in `ui-svelte/`.

## Conventions

- Summaries: only include details needing further action; else "Done."
- PRs (`gh` CLI): short, focused on changes, no test plan, summary in commit-message style.
- Commit format:

  ```
  scope: short subject

  Longer description of X and Y.

  - key change 1
  - key change 2
  ```

- Code reviews: High / Medium / Low severity, labelled H1/M2/L3. High = must-fix (security, races, critical bugs); Medium = recommended; Low = nits. One suggestion per item, top 3 highest-priority first.
