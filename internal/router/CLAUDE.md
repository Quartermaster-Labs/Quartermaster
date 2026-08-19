# internal/router

## Purpose

The router is the request-routing and model-swapping core of quartermaster. It sits behind the HTTP listeners and, for every incoming request, decides whether the target model can serve now and, if not, what must be loaded or evicted first. The scheduler inside it is the single state owner that all listeners share — in this fork that shared ownership is what makes cross-port, VRAM-exclusive eviction possible (one scheduler accounting for one GPU; see Gotchas).

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
| `loading.go` | `loadingWriter` — streams an SSE "loading model…" placeholder to the client while a swap is in flight, as SSE **comment** frames (`: text`) so no conforming client can mistake it for model output. |
| `loading_remarks.go` | Static list of whimsical loading-status remarks for `loadingWriter`. |
| `scheduler/scheduler.go` | The three interfaces (`Scheduler`, `Swapper`, `Effects`), the event types (`HandlerReq`, `HandlerResp`, `SwapDone`, `ServeDoneEvent`), and `New()` (selects scheduler by config; only `fifo` today). |
| `scheduler/fifo.go` | `FIFO` — the default and only `Scheduler`: queue, in-flight tracking, the request decision tree, and the idle-grace hold. |

## Important types & functions

- **`baseRouter`** (`base.go`) — the mechanism shared by `Group` and `Matrix` (both embed `*baseRouter`). Construction in `newBaseRouter` (`base.go`); the run loop in `run` (`base.go`); HTTP entry in `ServeHTTP` (`base.go`).
- **`doSwap`** (`base.go`) — the swap goroutine launched (fire-and-forget) by `StartSwap` (`base.go`). Stops the evict set in parallel, starts the target, waits for ready, then posts a `SwapDone` back into the run loop. This is the slow work kept off the run-loop goroutine.
- **`FIFO.OnRequest`** (`scheduler/fifo.go`) — the per-request decision tree: (1) unknown model → error, (2) join an in-flight swap for the same model, (3) fast-path serve if already ready and nothing to evict, (4) queue if it would collide with an in-flight swap, (5) queue if it would evict a still-busy process, (6) otherwise start a swap.
- **`FIFO.OnSwapDone` / `OnServeDone` / `OnUnload` / `OnShutdown`** (`scheduler/fifo.go`, `:202`, `:213`, `:261`) — the other run-loop event handlers; each re-drains the queue where appropriate.
- **`Swapper.EvictionFor`** (`scheduler/scheduler.go`) — the eviction policy: a pure function from `(target, running)` → models to stop. Implemented by `groupSwapper.EvictionFor` (`group.go`) and `matrixSwapper.EvictionFor` (`matrix.go`).
- **`scheduler.Effects`** (`scheduler/scheduler.go`) — the scheduler's only window onto the world (inspect state, start a swap, grant/error a caller, stop processes), implemented by `baseRouter`.

## Scheduling & eviction

A request enters `baseRouter.ServeHTTP`, which resolves the model ID, builds a `HandlerReq` with an **unbuffered** `Respond` channel, and hands it to the run loop. The single `run()` goroutine turns that into `Scheduler.OnRequest`, which walks the decision tree above. When a swap is needed, the scheduler records it as active, asks the `Swapper` for the evict set, and calls `Effects.StartSwap`, which launches `doSwap` in the background and returns immediately — so the run loop never blocks on a model load. When `doSwap` finishes it posts `SwapDone`; `OnSwapDone` then grants every waiter that joined that swap and re-drains the queue. In-flight requests are tracked via `trackedServe` (`base.go`), which posts a `ServeDoneEvent` when the handler returns; a swap that would evict a busy model is deferred until its in-flight count reaches zero.

**The idle-grace hold (fork).** An agent loop is a burst of requests separated by however long its
tool calls take, and *between rounds it is indistinguishable from a finished conversation*. So two
competing loops on different models used to trade the GPU every single round — one tool call each,
then a full reload — spending more on swaps than either spent generating. The hold fixes that with
two knobs that belong to opposite sides of the trade:

- **Hold window** (`holdWindowDefault`, 10s; `fifo.holdMs`, `X-QM-Hold-Ms`). When a model's last
  in-flight request drains, `OnServeDone` marks it un-evictable for one window. Sized to a *tool
  call*, not a model load: a window shorter than the real gap never fires at all, because the next
  round always arrives after it lapsed.
- **Waiter patience** (`patienceDefault`, 5min; `fifo.patienceMs`, `X-QM-Patience-Ms`). A hold is
  renewed by every round, so on its own it would let one loop keep the GPU forever. A queued request
  stops honouring holds once it has waited out its patience, preempts the incumbent, and becomes the
  incumbent itself. **Patience belongs to the waiter, not the incumbent** — only the waiter knows
  what the delay costs it, five minutes is right for a background agent and unbearable for someone
  watching a chat, and both can be queued for the same model at once. The playground stamps 60s on
  its own turns (`playgroundPatienceMs`); an unattended API client gets the 5-minute default.

Enforcement is one more queue predicate, step **(5b)** of `OnRequest` and the matching arm of
`drainQueue`, via `heldAgainst`. Requests granted per caller: `holdFor[model]` is recorded at grant
time, because `ServeDoneEvent` carries only a model ID while the window is a property of the
*request* that asked for it. Holds are released outright when the model actually stops
(`releaseHold` from `OnSwapDone`'s evict set and `OnUnload`) — protecting a process that no longer
exists would keep a queued request waiting for nothing.

**`Effects.Wake` / `Scheduler.OnWake`.** A hold expiring is the only decision the scheduler makes on
a *wall clock* rather than on an event that arrives by itself. `Wake(d)` arms a timer on the
baseRouter that nudges a buffered-1 `wakeCh`; the run loop turns that into `OnWake`, which expires
lapsed holds and re-drains. Without it a held model would stay protected until something unrelated
happened to drain the queue. The wait requested is capped at the caller's *remaining patience*, so
the wake lands on whichever deadline comes first.

**Abandoned-swap abort.** When a request queues behind an in-flight swap whose waiters have all disconnected (`activeSwap.waiters` empty), `reapAbandonedSwaps` (`fifo.go`) calls `Effects.AbortSwap` to Stop that loading model now instead of letting it finish only to be evicted for the queued request. AbortSwap is best-effort/async (`base.go`): stopping a `StateStarting` process aborts its start, `doSwap`'s `WaitReady` returns the error, and the resulting `SwapDone` clears the swap and re-drains the queue. Reap runs from `OnRequest` (collision case), `OnCancel`, and end of `drainQueue`. Swaps with live waiters are never aborted — their caller wants that model (no priority preemption).

**VRAM-budget-aware multi-load (fork).** When `config.VramBudgetGB > 0`, `groupSwapper.EvictionFor` runs `budgetEviction` (`group.go`) *before* the static policy: the target is admitted whenever its `ModelConfig.EstVramGB` plus the resident set fits the budget, and only enough residents are evicted — least-recently-used first — to make it fit. That replaces "loading B evicts its whole exclusive group whether or not B would have fit". Four rules keep it safe: an **unknown target** estimate (0 — hand-written config, or a class the sizer doesn't model) returns `ok=false` and falls through to the legacy static policy, since admitting an unmeasured model against a budget is the case that OOMs the box; an **unknown resident** is always evicted (its footprint can't be accounted for); `persistent` groups are **charged but never evicted**; and eviction stops the moment the sum fits. The estimates come from autogen's emitted `estVramGB:` (see `internal/autogen/CLAUDE.md`), with synthetic `?ctx=` variants re-sized at mint time (`internal/server/variant.go`). Because ONE scheduler sees every group's residents, this accounting spans all listeners — the fork's cross-port eviction no longer rides on `exclusive`.

**LRU ordering is part of the `Swapper` contract.** `FIFO` stamps a monotonic `useTick` into `lastUse` on every grant and swap, and presents `running` to `EvictionFor` **least-recently-used first**, ties broken alphabetically (a never-served model sorts first — it is the coldest thing resident). A counter, not a clock, so tests are deterministic. Order-independent policies (the matrix solver, the legacy group policy) are unaffected; the budget policy just walks the slice front-to-back.

The **eviction policy** is decoupled from scheduling. `groupSwapper` (`group.go`) reads static group settings: same-group siblings are stopped when the group has `swap=true`; cross-group members are stopped only when the *target's* group is `exclusive`, and even then a running `persistent` group is left alone. (This deliberately preserves the legacy gotcha that loading a non-exclusive model does not evict exclusive groups.) `matrixSwapper` instead delegates to `matrixSolver.Solve` (`matrix_solver.go`), which picks the lowest-cost valid model set containing the target. For the fork's cross-port VRAM-exclusive behavior, model an `exclusive` group so loading any member evicts the others.

## Gotchas / conventions

- **One scheduler, shared by all listeners.** The architectural invariant of this fork: there must be exactly one `run()` loop / scheduler instance, and every HTTP listener routes through it. Two scheduler instances = two independent VRAM accountings = collisions on the single GPU. Never instantiate per-listener routers. (See the fork goal in the repo `CLAUDE.md`.)
- **No locks in the scheduler.** Every `Scheduler` and `Effects` method runs on the single `run()` goroutine, serialized by the channel `select`. Scheduler state is plain maps/slices touched only from these callbacks — do **not** mutate scheduler state from a spawned goroutine. Slow work goes to `Effects.StartSwap` and comes back as a `SwapDone` event.
- **Live config reload (`ApplyConfig`).** `baseRouter.config` and `baseRouter.processes` are `atomic.Pointer`s (read lock-free via `cfg()`/`procs()`), NOT frozen at construction. `LocalRouter.ApplyConfig(newCfg)` funnels a config swap through the run loop (`applyConfigCh` → `handleApplyConfig`) so it serialises with every scheduling decision: it validates via the concrete router's `plan(cfg)` closure first (bad config = clean no-op), then diffs the process set (retunes kept models via `process.SetConfig` for their next spawn, `makeProcess`+`applyHooks` for added ones, async `Stop` for removed ones), swaps both pointers COW, and calls `Scheduler.ApplyConfig` (planner + per-model limits/image set, preserving in-flight state). Running upstreams are never torn down — a changed model's new launch args apply on its next load. This retires the old destructive `server.New`+`Shutdown` reload. Concrete routers (`Group`/`Matrix`) supply `plan` + `makeProcess` right after `newBaseRouter`.
- **The hold is currently granted per request, not per outcome.** Any request that drains grants
  its model a window, including the last round of a loop and a one-shot chat turn — so a model can
  sit protected for 10s after the work is genuinely over, and a user switching models in the UI can
  wait it out. The playground already suppresses it for turns that offer no tools
  (`X-QM-Hold-Ms: 0`, `streamSSE`), which cannot loop by construction. The general fix is to gate
  the hold on `finish_reason == "tool_calls"` from the response tee; not built yet.
- **`Swapper.EvictionFor` must be pure** — no logging, no mutation, called many times per request (once per request and again for every queued request on each drain). Log only in `OnSwapStart`, which fires exactly once per real swap.
- **The `GrantServe` boolean contract.** The caller's `Respond` channel is unbuffered, so a successful send proves the caller is still there and took the handler. Only increment `inFlight` when `GrantServe` returns true — a false return means the caller left, no `ServeDoneEvent` will arrive, and incrementing would strand the counter and permanently block future evictions.
- **Two contexts in `baseRouter`.** `shutdownCtx` governs request machinery (stop granting / reject callers); `procCtx` governs process lifetime and is cancelled only *after* graceful `Stop()` reaps children. Don't conflate them.
- **The loading placeholder must never emit `data:`.** It exists to hold the connection open
  while a model loads; SSE comments do that and every conforming parser drops them. It used to send
  synthetic `reasoning_content` deltas, which prepended fabricated reasoning to the assistant
  message and carried none of the chunk fields a strict client expects. Progress goes beside the
  stream, not inside it.
- **`ServeHTTP` snapshots residency before queuing.** `isModelReady` is read *before* the request
  goes to `handlerCh` — after that the run loop can load the model out from under the read. It
  feeds both the loading placeholder and the `X-QM-Model-Loaded` header, which report the state the
  request arrived to. `X-QM-Model`/`X-QM-Model-Loaded` are set before the placeholder flushes the
  header; `X-QM-Wait-Ms` is set at grant time, so it lands only when no placeholder ran.
- **`Peer` is the odd one out** — it has no scheduler, no processes, and no `run()` loop; it is a plain reverse proxy and only implements `Router`, not `LocalRouter`.

## Connections

- **Depends on** `internal/config` (router/scheduler/group/matrix settings, model configs), `internal/process` (the managed OS processes it starts/stops), `internal/logmon` (logging), and `internal/shared` (request context, errors). The live-VRAM data source (`internal/perf`) feeds the fork's offload calculations and informs eviction decisions.
- **Called by** `internal/server` — the server constructs the concrete router (`NewGroup` / `NewMatrix` / `NewPeer`) and dispatches every listener's requests into the shared router's `ServeHTTP`.
