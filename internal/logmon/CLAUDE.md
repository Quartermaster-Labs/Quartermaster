# internal/logmon

## Purpose

A log monitor that doubles as an `io.Writer`: it tees everything written to it to an underlying writer (default `os.Stdout`), retains a rolling tail of recent output in a fixed-size circular buffer, and broadcasts each write to subscribers via the event bus (so the UI can stream logs). Also offers leveled logging helpers.

## Key files

| File | Role |
|---|---|
| `logging.go` | Everything: the `circularBuffer`, the `Monitor` writer/logger, the `DataEvent` carried over the event bus, and `Level` definitions. |

## Important types & functions

- `Monitor` struct — `logging.go`. Wraps a downstream `io.Writer`, a lazily-allocated `circularBuffer`, a private `event.Dispatcher`, and log-formatting settings (level, prefix, time format).
- `New()` / `NewWriter(stdout io.Writer)` — `logging.go` / `:119`. `New` targets `os.Stdout`. The internal dispatcher is created with a 1000-deep queue.
- `(*Monitor) Write(p []byte) (int, error)` — `logging.go`. Writes downstream, appends to the buffer (lazily allocating it), and broadcasts a copy of the bytes. Implements `io.Writer`.
- `(*Monitor) GetHistory() []byte` — `logging.go`. Returns the retained tail in chronological order.
- `(*Monitor) Clear()` — `logging.go`. Drops the buffer (GC-eligible); re-allocated on next write.
- `(*Monitor) OnLogData(callback func(data []byte)) context.CancelFunc` — `logging.go`. Subscribes to `DataEvent`s; returns an unsubscribe func.
- `SetPrefix` / `SetLogLevel` / `SetLogTimeFormat` — `logging.go`. Configure formatting.
- Leveled logging: `Debug`/`Info`/`Warn`/`Error` and their `*f` variants — `logging.go`; gated by the configured `Level`.
- `DataEvent` (`logging.go`, `Type()` → `DataEventID` = `0x04`) — the event payload carrying a chunk of log bytes.
- `circularBuffer` — `logging.go`. Fixed-capacity (`BufferSize` = 100 KiB, `logging.go`) byte ring with O(1) `Write` and O(n) `GetHistory`.

## Connections

- Depends on `internal/event` for fan-out (`OnLogData` subscribers, `broadcast`/`Publish`).
- Used by process/server layers as the stdout/stderr sink for spawned upstream processes and as the source feeding the UI's live log stream.

## Gotchas

- `DataEventID` (`0x04`) is part of the shared event-ID number space — see `internal/shared/events.go` for the other reserved IDs (`0x01`, `0x03`, `0x05`–`0x08`); avoid collisions.
- **`Write` never blocks on subscribers.** `Write` does a non-blocking send onto an internal `broadcastCh` (cap 1024); a dedicated `broadcastLoop` goroutine owns the back-pressuring `event.Publish`. If subscribers stall, the publish blocks the loop goroutine — not `Write` — so a slow UI log stream can never stall the upstream process's stdout drain (would stall llama.cpp). Overflow drops the live chunk and accumulates a byte count emitted as an in-stream `— N bytes dropped —` marker when delivery resumes; `GetHistory()` still holds the data for reconnecting clients. The loop goroutine lives for the Monitor's lifetime (no `Close`); per-model `procLog` Monitors are created once at router construction, so the count is bounded.
- `circularBuffer` is unsynchronized on its own; the `Monitor` guards it with `bufferMu`.
