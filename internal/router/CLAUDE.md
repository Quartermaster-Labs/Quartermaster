# internal/router

## Purpose

The router is the request-routing and model-swapping core of llama-quartermaster. It sits behind the HTTP listeners and, for every incoming request, decides whether the target model can serve now and, if not, what must be loaded or evicted first. The scheduler inside it is the single state owner that all listeners share — in this fork that shared ownership is what makes cross-port, VRAM-exclusive eviction possible (one scheduler accounting for one GPU; see Gotchas).

A deep developer tutorial lives in `design.md` (same directory) — read it for the full rationale. This file is the quick map.

## Key files

| File | Role |
|---|---|
| `router.go` | `Router` / `LocalRouter` interfaces; shared error aliases. The public surface the server consumes. |
| `base.go` | `baseRouter` — owns channels, the single `run()` loop, process lifecycle, shutdown/unload teardown, `ServeHTTP`. Implements `scheduler.Effects` so the scheduler can produce side-effects through it. |
| `group.go` | `Group` router + `groupSwapper` — eviction from static group config (`swap` / `exclusive` / `persistent`). |
| `matrix.go` | `Matrix` router + `matrixSwapper` — eviction via the cost-based set solver. |
| `matrix_solver.go` | `matrixSolver` — pure, lock-free set/cost solver (no process deps) used by `matrixSwapper`. |
| `peer.go` | `Peer` router — pure reverse proxy to remote hosts; no local processes, no scheduler. |
| `loading.go` | `loadingWriter` — streams an SSE "loading model…" placeholder to the client while a swap is in flight. |
| `loading_remarks.go` | Static list of whimsical loading-status remarks for `loadingWriter`. |
| `scheduler/scheduler.go` | The three interfaces (`Scheduler`, `Swapper`, `Effects`), the event types (`HandlerReq`, `HandlerResp`, `SwapDone`, `ServeDoneEvent`), and `New()` (selects scheduler by config; only `fifo` today). |
| `scheduler/fifo.go` | `FIFO` — the default and only `Scheduler`: queue, in-flight tracking, and the request decision tree. |

## Important types & functions

- **`baseRouter`** (`base.go:33`) — the mechanism shared by `Group` and `Matrix` (both embed `*baseRouter`). Construction in `newBaseRouter` (`base.go:72`); the run loop in `run` (`base.go:112`); HTTP entry in `ServeHTTP` (`base.go:410`).
- **`doSwap`** (`base.go:242`) — the swap goroutine launched (fire-and-forget) by `StartSwap` (`base.go:179`). Stops the evict set in parallel, starts the target, waits for ready, then posts a `SwapDone` back into the run loop. This is the slow work kept off the run-loop goroutine.
- **`FIFO.OnRequest`** (`scheduler/fifo.go:92`) — the per-request decision tree: (1) unknown model → error, (2) join an in-flight swap for the same model, (3) fast-path serve if already ready and nothing to evict, (4) queue if it would collide with an in-flight swap, (5) queue if it would evict a still-busy process, (6) otherwise start a swap.
- **`FIFO.OnSwapDone` / `OnServeDone` / `OnUnload` / `OnShutdown`** (`scheduler/fifo.go:181`, `:202`, `:213`, `:261`) — the other run-loop event handlers; each re-drains the queue where appropriate.
- **`Swapper.EvictionFor`** (`scheduler/scheduler.go:38`) — the eviction policy: a pure function from `(target, running)` → models to stop. Implemented by `groupSwapper.EvictionFor` (`group.go:70`) and `matrixSwapper.EvictionFor` (`matrix.go:57`).
- **`scheduler.Effects`** (`scheduler/scheduler.go:74`) — the scheduler's only window onto the world (inspect state, start a swap, grant/error a caller, stop processes), implemented by `baseRouter`.

## Scheduling & eviction

A request enters `baseRouter.ServeHTTP`, which resolves the model ID, builds a `HandlerReq` with an **unbuffered** `Respond` channel, and hands it to the run loop. The single `run()` goroutine turns that into `Scheduler.OnRequest`, which walks the decision tree above. When a swap is needed, the scheduler records it as active, asks the `Swapper` for the evict set, and calls `Effects.StartSwap`, which launches `doSwap` in the background and returns immediately — so the run loop never blocks on a model load. When `doSwap` finishes it posts `SwapDone`; `OnSwapDone` then grants every waiter that joined that swap and re-drains the queue. In-flight requests are tracked via `trackedServe` (`base.go:230`), which posts a `ServeDoneEvent` when the handler returns; a swap that would evict a busy model is deferred until its in-flight count reaches zero.

**Abandoned-swap abort.** When a request queues behind an in-flight swap whose waiters have all disconnected (`activeSwap.waiters` empty), `reapAbandonedSwaps` (`fifo.go`) calls `Effects.AbortSwap` to Stop that loading model now instead of letting it finish only to be evicted for the queued request. AbortSwap is best-effort/async (`base.go`): stopping a `StateStarting` process aborts its start, `doSwap`'s `WaitReady` returns the error, and the resulting `SwapDone` clears the swap and re-drains the queue. Reap runs from `OnRequest` (collision case), `OnCancel`, and end of `drainQueue`. Swaps with live waiters are never aborted — their caller wants that model (no priority preemption).

The **eviction policy** is decoupled from scheduling. `groupSwapper` (`group.go:70`) reads static group settings: same-group siblings are stopped when the group has `swap=true`; cross-group members are stopped only when the *target's* group is `exclusive`, and even then a running `persistent` group is left alone. (This deliberately preserves the legacy gotcha that loading a non-exclusive model does not evict exclusive groups.) `matrixSwapper` instead delegates to `matrixSolver.Solve` (`matrix_solver.go:51`), which picks the lowest-cost valid model set containing the target. For the fork's cross-port VRAM-exclusive behavior, model an `exclusive` group so loading any member evicts the others.

## Gotchas / conventions

- **One scheduler, shared by all listeners.** The architectural invariant of this fork: there must be exactly one `run()` loop / scheduler instance, and every HTTP listener routes through it. Two scheduler instances = two independent VRAM accountings = collisions on the single GPU. Never instantiate per-listener routers. (See the fork goal in the repo `CLAUDE.md`.)
- **No locks in the scheduler.** Every `Scheduler` and `Effects` method runs on the single `run()` goroutine, serialized by the channel `select`. Scheduler state is plain maps/slices touched only from these callbacks — do **not** mutate scheduler state from a spawned goroutine. Slow work goes to `Effects.StartSwap` and comes back as a `SwapDone` event.
- **Live config reload (`ApplyConfig`).** `baseRouter.config` and `baseRouter.processes` are `atomic.Pointer`s (read lock-free via `cfg()`/`procs()`), NOT frozen at construction. `LocalRouter.ApplyConfig(newCfg)` funnels a config swap through the run loop (`applyConfigCh` → `handleApplyConfig`) so it serialises with every scheduling decision: it validates via the concrete router's `plan(cfg)` closure first (bad config = clean no-op), then diffs the process set (retunes kept models via `process.SetConfig` for their next spawn, `makeProcess`+`applyHooks` for added ones, async `Stop` for removed ones), swaps both pointers COW, and calls `Scheduler.ApplyConfig` (planner + per-model limits/image set, preserving in-flight state). Running upstreams are never torn down — a changed model's new launch args apply on its next load. This retires the old destructive `server.New`+`Shutdown` reload. Concrete routers (`Group`/`Matrix`) supply `plan` + `makeProcess` right after `newBaseRouter`.
- **`Swapper.EvictionFor` must be pure** — no logging, no mutation, called many times per request (once per request and again for every queued request on each drain). Log only in `OnSwapStart`, which fires exactly once per real swap.
- **The `GrantServe` boolean contract.** The caller's `Respond` channel is unbuffered, so a successful send proves the caller is still there and took the handler. Only increment `inFlight` when `GrantServe` returns true — a false return means the caller left, no `ServeDoneEvent` will arrive, and incrementing would strand the counter and permanently block future evictions.
- **Two contexts in `baseRouter`.** `shutdownCtx` governs request machinery (stop granting / reject callers); `procCtx` governs process lifetime and is cancelled only *after* graceful `Stop()` reaps children. Don't conflate them.
- **`Peer` is the odd one out** — it has no scheduler, no processes, and no `run()` loop; it is a plain reverse proxy and only implements `Router`, not `LocalRouter`.

## Connections

- **Depends on** `internal/config` (router/scheduler/group/matrix settings, model configs), `internal/process` (the managed OS processes it starts/stops), `internal/logmon` (logging), and `internal/shared` (request context, errors). The live-VRAM data source (`internal/perf`) feeds the fork's offload calculations and informs eviction decisions.
- **Called by** `internal/server` — the server constructs the concrete router (`NewGroup` / `NewMatrix` / `NewPeer`) and dispatches every listener's requests into the shared router's `ServeHTTP`.
