# internal/process

## Purpose

Manages the lifecycle of a single upstream llama.cpp subprocess: spawning it with the configured command, health-checking it to readiness, reverse-proxying HTTP traffic to it, and tearing it down (on idle TTL, explicit stop, or app shutdown). Each model instance is backed by one `ProcessCommand`.

## Key files

| File | Role |
|---|---|
| `process.go` | `Process` interface and `ProcessState` enum (the public contract). |
| `process_command.go` | `ProcessCommand` implementation — the single-writer state machine, spawn, health check, reverse proxy, stop/kill, TTL. |
| `runtime_unix.go` | (`//go:build !windows`) Per-process teardown via process-group signals (`Setpgid`, SIGTERM/SIGKILL to `-pid`). |
| `runtime_windows.go` | (`//go:build windows`) Per-process teardown via `taskkill /t` (and `/f`); sets `CREATE_NO_WINDOW`. |
| `treecleanup_other.go` | (`//go:build !windows`) `SetupTreeCleanup` no-op — orphan cleanup handled by process groups. |
| `treecleanup_windows.go` | (`//go:build windows`) `SetupTreeCleanup` assigns the process to a Job Object with `KILL_ON_JOB_CLOSE` so children die with llama-quartermaster. |

## Important types & functions

- **`Process` interface** (`process.go:23`) — `Run`, `WaitReady`, `Stop`, `State`, `ServeHTTP`, `Logger`. `ProcessState` values: `Stopped`, `Starting`, `Ready`, `Stopping`, `Shutdown` (`process.go:13-21`).
- **`ProcessCommand` struct** (`process_command.go:62-88`) — holds `id`, `config config.ModelConfig`, `parentCtx`, loggers, request channels, and atomics: `state`, `handler` (reverse-proxy handler), `lastUse`, `inflight`.
- **`New`** (`process_command.go:92`) — constructs a `ProcessCommand` and launches the `run()` goroutine.
- **`run`** (`process_command.go:126`) — the single-writer goroutine owning all mutable lifecycle state. Every public method is a thin client that sends on `runCh`/`stopCh`/`waitReadyCh` and waits for a response, serializing all transitions through one point. Handles parent-context shutdown, unexpected upstream exit (`cmdDone`), WaitReady queueing, Run/start, and Stop.
- **`doStart`** (`process_command.go:357`) — builds the argv via `SanitizedCommand`, constructs the reverse proxy + transport, execs the command (`exec.CommandContext`, `process_command.go:417`), then polls the health endpoint until ready. Returns a `startResult`. Runs in its own goroutine so an in-flight start can be aborted by a Stop or `parentCtx` cancellation.
- **Health check** (`process_command.go:466-511`) — if `CheckEndpoint == "none"`, skips checking; otherwise waits 250ms, then polls `CheckEndpoint` through the reverse proxy once per second until HTTP 200 or `healthCheckTimeout` deadline. Early-exits on start-context cancel (`ErrStartAborted`) or premature upstream exit.
- **TTL handling** (`process_command.go:269-288`) — when `config.UnloadAfter > 0`, a goroutine started on entry to `StateReady` ticks every second and calls `Stop` once the process has been idle (zero `inflight`, `lastUse` older than the TTL) for `UnloadAfter` seconds. Self-terminates when state leaves `StateReady`.
- **`sendStopSignal`** (`process_command.go:519`) — runs the configured `CmdStop` (with `${PID}` substituted) if set, else calls `terminateProcessTree` (SIGTERM group / `taskkill /t`).
- **`killProcess`** (`process_command.go:578`) — sends the graceful stop signal directly (not via context cancel, to avoid capping the grace period at `cmd.WaitDelay`), waits up to `gracefulTimeout`, then escalates to `killProcessTree` (SIGKILL group / `taskkill /f /t`), and finally waits on `cmdDone`.
- **`ServeHTTP`** (`process_command.go:672`) — atomically loads the handler (503 `llama-quartermaster-error` if not ready), increments `inflight`, forwards, and records `lastUse` for TTL.

## Lifecycle

1. **Spawn** — `Run(timeout)` triggers `run()` (only valid from `StateStopped`) → `StateStarting`. `doStart` sanitizes the command into argv, sets up the reverse proxy, and `cmd.Start()`s the upstream with platform proc attributes.
2. **Health** — `doStart` polls `CheckEndpoint` until 200 (or skips when `"none"`). Success latches `cmd`/`handler`, transitions to `StateReady`, and wakes any `WaitReady` callers. Failure or abort returns to `StateStopped`.
3. **Serve** — while `StateReady`, `ServeHTTP` proxies requests to the upstream and tracks `inflight`/`lastUse`.
4. **TTL / evict** — the optional TTL goroutine calls `Stop` after idle timeout; the router/scheduler may also call `Stop` to evict for another model. An unexpected upstream exit (`cmdDone`) drops back to `StateStopped` and unblocks the parked `Run` caller.
5. **Stop / cleanup** — `Stop` → `StateStopping` → `killProcess` (graceful signal, grace period, force-kill the tree, wait for `cmdDone`) → `StateStopped`. `parentCtx` cancellation forces `StateShutdown` and a permanent goroutine exit.

## Gotchas / conventions

- **Build tags.** `runtime_unix.go`/`treecleanup_other.go` are `!windows`; `runtime_windows.go`/`treecleanup_windows.go` are `windows`. `setProcAttributes`, `terminateProcessTree`, `killProcessTree`, and `SetupTreeCleanup` each have one definition per platform — keep the signatures identical across both.
- **Process-tree cleanup differs by OS.** Unix puts the child in its own process group (`Setpgid`) and signals `-pid` to reap forked grandchildren. Windows has no process groups, so it shells out to `taskkill /t` (graceful) / `taskkill /f /t` (force). Additionally, `SetupTreeCleanup` on Windows uses a Job Object (`KILL_ON_JOB_CLOSE`) as a parent-side backstop so all children die if llama-quartermaster crashes; on Unix it is a no-op.
- **`cmd.WaitDelay`** (default `cmdWaitDelay` = 10s) is the backstop that force-closes inherited stdout/stderr pipes after the process exits, so `cmd.Wait()` returns even when a forked grandchild holds the pipes open (the "v219 hang" bug). `killProcess` deliberately signals directly rather than cancelling the context so this delay is measured from process exit, not from the stop request.
- **Command sanitization.** Argv comes from `config.ModelConfig.SanitizedCommand()` → shlex split → `[]string` argv (no shell). `CmdStop` is sanitized the same way via `config.SanitizeCommand` after `${PID}` substitution.
- **Health check polling** waits 250ms before the first probe, then probes once per second through the reverse proxy until 200 or the deadline. `CheckEndpoint == "none"` disables it entirely.
- **Single-writer invariant.** Only `run()` mutates lifecycle state. `state` and `handler` are atomics solely so `State()`/`ServeHTTP` can read them concurrently without taking the writer's path. Don't mutate them elsewhere.

## Connections

- **Depends on `internal/config`** — a `ProcessCommand` is built from a `config.ModelConfig` (`Proxy`, `Cmd`/`SanitizedCommand`, `CmdStop`, `CheckEndpoint`, `Timeouts`, `Env`, `UnloadAfter`). Uses `logmon.Monitor` for process and proxy logging and emits `shared.ProcessStateChangeEvent` on transitions.
- **Called by the router/scheduler** (`internal/router`, `internal/router/scheduler`) — the scheduler owns the set of `Process` instances and drives `Run`/`WaitReady`/`Stop`/`ServeHTTP`, including eviction.

## Fork notes (llama-quartermaster)

**Live config swap + launched-args readout.** `config` is an `atomic.Pointer[ModelConfig]` (`liveConfig`), read once per use via `p.cfg()`. `SetConfig` swaps it for a hot reload; a running upstream keeps serving under the config it spawned with, and the new command/flags take effect on the next `doStart`. `LaunchedCmd()` returns the ACTUAL argv the live process spawned with (`launchedArgs`, set on `StateReady`, cleared on teardown) — what it's really serving under, which differs from `cfg()` after a `SetConfig` or a spawn-time offload rewrite. Surfaced up through `router.LaunchedCmd` → `modelStatus.runningCmd` so the UI shows the running args, not the pending config.

**Spawn-time argv rewrite (`SetSpawnArgs`).** `doStart` rewrites the argv after `SanitizedCommand()` and before exec via the optional `spawnArgs` hook (atomic, mirrors `SetPreStop`/`SetPostStart`; fanned out per-model by `baseRouter.SetSpawnArgs`). A returned error **aborts the start** (→ `startResult{err}` → `StateStopped`, refused not crashed). The live consumer is `server.WireDynamicOffload` → `autogen.LiveOffloadArgs`, which recomputes `-ngl`/`--n-cpu-moe` from free VRAM so a stale baked plan can't OOM (see `internal/autogen/CLAUDE.md`). This is the realized form of the old "resolve `${NGL}/${NCPUMOE}` at spawn" goal — done by editing the concrete flags, not macros (config rejects unresolved `${...}` at load). Context size is still baked per-profile.
