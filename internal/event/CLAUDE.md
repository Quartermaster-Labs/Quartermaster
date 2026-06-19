# internal/event

## Purpose

A generic, in-process, type-safe event bus (publish/subscribe). Each subscriber runs its own goroutine with a bounded queue and back-pressure on publishers. Adapted from [kelindar/event](https://github.com/kelindar/event) v1.5.2, with the original `time.Ticker`-based queue draining replaced by a fully event-driven (`sync.Cond`) design to fix high CPU usage (upstream issue #189).

## Key files

| File | Role |
|---|---|
| `event.go` | Core implementation: `Event` interface, `Dispatcher`, copy-on-write subscriber registry, consumer/group machinery, generic `Subscribe`/`SubscribeTo`/`Publish`. |
| `default.go` | A package-level default `Dispatcher` plus convenience wrappers `On`, `OnType`, `Emit`. |
| `README.md` | Provenance note (forked from kelindar/event; ticker removed). |

## Important types & functions

- `Event` interface — `event.go:17`. Contract: `Type() uint32`. Each event struct returns a stable type ID.
- `Dispatcher` struct — `event.go:30`. Holds an `atomic.Pointer[registry]` for lock-free reads; the mutex guards only subscribe/unsubscribe writes.
- `NewDispatcher()` / `NewDispatcherConfig(maxQueue int)` — `event.go:38` / `event.go:43`. Latter sets the per-consumer max queue length (back-pressure threshold).
- `(*Dispatcher) Close() error` — `event.go:57`. Subscribing after close panics (`errClosed`).
- `Subscribe[T Event](broker, handler) context.CancelFunc` — `event.go:96`. Infers the event type from `T`'s zero value `Type()`; returns a cancel func that unsubscribes.
- `SubscribeTo[T Event](broker, eventType, handler)` — `event.go:102`. Subscribe with an explicit type ID.
- `Publish[T Event](broker, ev)` — `event.go:155`. Broadcasts to the matching consumer group (no-op if no subscribers).
- Default-dispatcher wrappers — `default.go`: `On` (`:16`), `OnType` (`:22`), `Emit` (`:28`); `Default` is created with a 25000-deep queue (`default.go:11`).
- Internal: `registry` (sorted COW arrays, `event.go:22`), `findGroup` (lock-free binary search, `event.go:73`), `consumer.Listen` (double-buffered drain loop, `event.go:189`), `group.Broadcast` (back-pressure via `cond.Wait`, `event.go:231`).

## Connections

Foundational leaf package; imports only the standard library. Used across the project wherever decoupled fan-out is needed:
- `internal/logmon` — log `DataEvent` broadcasting.
- `internal/shared/events.go` — defines the shared event payload structs (process state changes, config reload, activity, preload, in-flight, live tokens) that flow through this bus.
- `internal/server` and `internal/router` — emit/consume those shared events.

## Gotchas

- Subscriber type IDs must be unique per event struct. Subscribing two different Go types under the same `uint32` panics with a conflict message (`groupOf`/`errConflict`, `event.go:172`/`:318`).
- Each subscriber gets a dedicated goroutine; cancel via the returned `CancelFunc` to stop it and free the queue.
- Publishers **block** (back-pressure) when every consumer's queue is at `maxQueue` — a slow/stuck handler can stall publishers. Size `maxQueue` accordingly.
- Handlers run on the consumer goroutine; they must be safe to call serially and should not block indefinitely.
