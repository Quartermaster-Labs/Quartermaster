# internal/cache

## Purpose

A small, thread-safe in-memory byte cache keyed by `int` IDs with a total byte-size budget. When adding an item would exceed the budget, the oldest entries are evicted FIFO until there is room.

## Key files

| File | Role |
|---|---|
| `cache.go` | The entire `Cache` implementation: storage map, FIFO order tracking, size accounting, and the public API. |

## Important types & functions

- `Cache` struct — `cache.go`. Holds an `items map[int][]byte`, an `order []int` insertion list for FIFO eviction, the running `size`, and `maxSize`. All access is guarded by a single `sync.Mutex`.
- `New(maxBytes int) *Cache` — `cache.go`. Constructs a cache with the given byte budget.
- `(*Cache) Add(id int, data []byte) error` — `cache.go`. Inserts/replaces an entry; evicts oldest entries FIFO to stay under `maxSize`. Returns `ErrExceedsMaxSize` if a single item is larger than the whole budget.
- `(*Cache) Get(id int) ([]byte, error)` — `cache.go`. Returns the stored bytes or `ErrNotFound`. Returns the stored slice directly (no copy).
- `(*Cache) Has(id int) bool` — `cache.go`.
- `(*Cache) Size() int` — `cache.go`. Current total bytes held.
- `(*Cache) Clear()` — `cache.go`.
- Sentinel errors `ErrExceedsMaxSize`, `ErrNotFound` — `cache.go`.

## Connections

Self-contained leaf package; depends only on `errors` and `sync` from the standard library. Used elsewhere in the project to hold byte payloads keyed by an integer ID (e.g. cached request/response bodies).

## Gotchas

- `Get` returns the internal slice without copying; callers must not mutate it.
- Eviction is strictly FIFO by insertion order, not LRU — a `Get` does not refresh recency. Re-adding an existing `id` does move it to the back of the order.
