# internal/logmon

## Purpose

A log monitor that doubles as an `io.Writer`: it tees everything written to it to an underlying writer (default `os.Stdout`), retains a rolling tail of recent output in a fixed-size circular buffer, and broadcasts each write to subscribers via the event bus (so the UI can stream logs). Also offers leveled logging helpers.

## Key files

| File | Role |
|---|---|
| `logging.go` | Everything: the `circularBuffer`, the `Monitor` writer/logger, the `DataEvent` carried over the event bus, and `Level` definitions. |

## Important types & functions

- `Monitor` struct — `logging.go:102`. Wraps a downstream `io.Writer`, a lazily-allocated `circularBuffer`, a private `event.Dispatcher`, and log-formatting settings (level, prefix, time format).
- `New()` / `NewWriter(stdout io.Writer)` — `logging.go:115` / `:119`. `New` targets `os.Stdout`. The internal dispatcher is created with a 1000-deep queue.
- `(*Monitor) Write(p []byte) (int, error)` — `logging.go:130`. Writes downstream, appends to the buffer (lazily allocating it), and broadcasts a copy of the bytes. Implements `io.Writer`.
- `(*Monitor) GetHistory() []byte` — `logging.go:153`. Returns the retained tail in chronological order.
- `(*Monitor) Clear()` — `logging.go:164`. Drops the buffer (GC-eligible); re-allocated on next write.
- `(*Monitor) OnLogData(callback func(data []byte)) context.CancelFunc` — `logging.go:170`. Subscribes to `DataEvent`s; returns an unsubscribe func.
- `SetPrefix` / `SetLogLevel` / `SetLogTimeFormat` — `logging.go:180-196`. Configure formatting.
- Leveled logging: `Debug`/`Info`/`Warn`/`Error` and their `*f` variants — `logging.go:217-236`; gated by the configured `Level`.
- `DataEvent` (`logging.go:16`, `Type()` → `DataEventID` = `0x04`) — the event payload carrying a chunk of log bytes.
- `circularBuffer` — `logging.go:26`. Fixed-capacity (`BufferSize` = 100 KiB, `logging.go:99`) byte ring with O(1) `Write` and O(n) `GetHistory`.

## Connections

- Depends on `internal/event` for fan-out (`OnLogData` subscribers, `broadcast`/`Publish`).
- Used by process/server layers as the stdout/stderr sink for spawned upstream processes and as the source feeding the UI's live log stream.

## Gotchas

- `DataEventID` (`0x04`) is part of the shared event-ID number space — see `internal/shared/events.go` for the other reserved IDs (`0x01`, `0x03`, `0x05`–`0x08`); avoid collisions.
- The dispatcher queue is only 1000 deep; a slow `OnLogData` consumer applies back-pressure and can block `Write` (and therefore the process whose output is being teed).
- `circularBuffer` is unsynchronized on its own; the `Monitor` guards it with `bufferMu`.
