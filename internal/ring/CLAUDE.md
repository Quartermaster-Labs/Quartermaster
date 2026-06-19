# internal/ring

## Purpose

A minimal generic fixed-capacity ring (circular) buffer. Pushing past capacity overwrites the oldest entry. Not safe for concurrent use — callers synchronize externally.

## Key files

| File | Role |
|---|---|
| `buffer.go` | The entire generic `Buffer[T]` implementation. |
| `buffer_test.go` | Tests and benchmarks (per task note; not documented here). |

## Important types & functions

- `Buffer[T any]` struct — `buffer.go:3`. Backing slice plus `head` and `size` indices. Used as a value (constructor returns `Buffer[T]`, methods take a pointer receiver).
- `NewBuffer[T any](capacity int) Buffer[T]` — `buffer.go:9`. Capacity is clamped to a minimum of 1.
- `(*Buffer[T]) Push(v T)` — `buffer.go:17`. Appends; once full, overwrites the oldest element and advances `head`.
- `(*Buffer[T]) Slice() []T` — `buffer.go:29`. Returns all current entries in insertion order as a freshly allocated slice (`nil` when empty).

## Connections

Pure standard-library leaf package (no imports). A general-purpose utility for keeping a bounded, ordered window of recent values (e.g. recent metrics/log samples) where the newest N entries are wanted in order.

## Gotchas

- No internal locking — guard with a mutex if shared across goroutines.
- `NewBuffer` returns by value; copying a `Buffer` after pushes copies the backing slice header (shared underlying array) — treat the returned value as owned by one logical user.
