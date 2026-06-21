# internal/chain

## Purpose

Composes `http.Handler` middleware into a single handler. A reusable middleware stack is built once and applied around terminal handlers when registering routes on an `http.ServeMux`.

## Key files

| File | Role |
|---|---|
| `chain.go` | Defines the `Middleware` type and the `Chain` builder (`New`, `Then`, `ThenFunc`). |

## Important types & functions

- `Middleware func(next http.Handler) http.Handler` — `chain.go:13`. A middleware may run logic before/after `next`, or short-circuit by never calling it (auth failure, CORS preflight).
- `Chain` struct — `chain.go:27`. Immutable middleware stack; the zero value is valid and applies no middleware.
- `New(mws ...Middleware) Chain` — `chain.go:31`. Builds a chain; copies the slice so callers can't mutate it afterward.
- `(Chain) Then(final http.Handler) http.Handler` — `chain.go:40`. Wraps `final` and returns the composed handler.
- `(Chain) ThenFunc(f http.HandlerFunc) http.Handler` — `chain.go:49`. Shorthand for `Then(http.HandlerFunc(f))`.

## Connections

Pure standard-library leaf package (`net/http` only). Consumed by HTTP setup code that registers OpenAI/config routes (e.g. `internal/server`) to attach auth, CORS, metrics, and similar cross-cutting middleware.

## Gotchas

- Execution order is left-to-right: `mws[0]` runs first and wraps the rest. `Then` applies them in reverse-index order internally to achieve that.
