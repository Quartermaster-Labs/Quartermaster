# internal/watcher

## Purpose

A simple cross-platform config-file watcher based on `os.Stat` polling rather than inotify. Polling works reliably where inotify does not: Docker single-file bind mounts and k8s ConfigMap symlink projections (atomically swapped targets).

> Note: the directory is `internal/watcher` but the Go package is named **`configwatcher`**.

## Key files

| File | Role |
|---|---|
| `watcher.go` | The whole package: the `Watcher`, its `Run` poll loop, and the `stat`/`changed` helpers. |

## Important types & functions

- `Watcher` struct — `watcher.go`. Fields: `Path`, `Interval`, and `OnChange func()` callback.
- `DefaultInterval` — `watcher.go`. 2 seconds; used when `Interval <= 0`.
- `(*Watcher) Run(ctx context.Context)` — `watcher.go`. Blocks until `ctx` is canceled; polls on a ticker and invokes `OnChange` on each detected change. The baseline poll establishes initial state and does **not** fire `OnChange`.
- `stat(path) snapshot` — `watcher.go`. Reads existence/modtime/size; logs unexpected stat errors but treats `ErrNotExist` as simply "missing".
- `changed(prev, cur) bool` — `watcher.go`. Fires on modtime or size change, and on missing→present (file reappears). Stays quiet on present→missing (treats a vanished file as a transient rename-style write).

## Connections

Standard-library-only leaf package. Consumed by the application entry/config layer (`cmd/quartermaster/quartermaster.go` / `internal/config`) to trigger config reloads; the `OnChange` callback typically emits a `ConfigFileChangedEvent` (see `internal/shared/events.go`).

## Gotchas

- Change detection is modtime+size based; an in-place edit that preserves both is not detected.
- Present→missing deliberately does not fire, so callers won't see a reload on a brief delete during atomic rewrites — the subsequent missing→present transition fires instead.
