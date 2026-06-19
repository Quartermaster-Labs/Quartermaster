# internal/shared

## Purpose

A leaf package of cross-cutting helpers shared by the server/router layers without creating import cycles: the canonical event payload structs and their type IDs, request-context extraction, content-negotiated error responses, the rich `HTTPError` seam, and a loopback-address check.

## Key files

| File | Role |
|---|---|
| `events.go` | Shared event-ID constants and event payload structs that flow over `internal/event`. Kept dependency-free so it stays a leaf (states carried as strings, not `internal/process` types). |
| `http.go` | Request-context (`ReqContextData`) extraction/storage, model resolution, API-key extraction, and content-negotiated error rendering. |
| `httperror.go` | The `HTTPError` interface and `ConcurrencyLimitError` (429) implementation. |
| `loopback.go` | `IsLoopbackAddr` listen-address classification. |

## Important types & functions

### events.go
- Event IDs — `events.go:3-8`: `ProcessStateChangeEventID` (`0x01`), `ConfigFileChangedEventID` (`0x03`), `ActivityLogEventID` (`0x05`), `ModelPreloadedEventID` (`0x06`), `InFlightRequestsEventID` (`0x07`), `LiveTokensEventID` (`0x08`).
- Event structs (each implements `event.Event` via `Type()`): `ProcessStateChangeEvent` (`:13`), `ConfigFileChangedEvent` (`:30`, with `ReloadingState` enum `:23`), `ModelPreloadedEvent` (`:38`), `InFlightRequestsEvent` (`:47`), `LiveTokensEvent` (`:59`, live tokens/sec readout for streaming requests).

### http.go
- `ReqContextData` struct — `http.go:23`. Per-request bag: API key, model name/ID, streaming flag, loading-state flag, and a mutable `Metadata` map.
- `FetchContext(r, cfg) (ReqContextData, error)` — `http.go:98`. Returns cached context or extracts it from the request, resolves the real model name via `cfg.RealModelName`, stores it back on the request, else `ErrNoModelInContext`.
- `SetContext` / `ReadContext` — `http.go:120` / `:124`. Context get/set under `ReqContextKey`.
- `SetReqData(ctx, key, value) error` — `http.go:133`. Mutates the request's `Metadata` map (the metrics middleware copies it into the activity log).
- `extractContext(r)` — `http.go:154`. Pulls `model`/`stream` from GET query or POST body (JSON / multipart / urlencoded), always restoring `r.Body`.
- `ExtractAPIKey(r)` — `http.go:211`. Prefers Basic (password field), then Bearer, then `x-api-key`.
- `SendError(w, r, err)` — `http.go:42`. If `err` is an `HTTPError`, writes it verbatim; otherwise maps sentinel errors (`ErrNoModelInContext`, `ErrNoRouterFound`, `ErrNoPeerModelFound`, `ErrNoLocalModelFound`) to statuses.
- `SendResponse(w, r, status, message)` — `http.go:68`. Content-negotiated (text/plain, text/html, JSON) error body.

### httperror.go
- `HTTPError` interface — `httperror.go:15`. `error` + `StatusCode()` / `Header()` / `Body()`; lets a producer (e.g. the scheduler) shed a request with a complete, rich response that the renderer writes verbatim.
- `ConcurrencyLimitError` — `httperror.go:25`. A 429 with `Retry-After` (default 1s) and a JSON hint body; zero-value-friendly.

### loopback.go
- `IsLoopbackAddr(listenAddr string) bool` — `loopback.go:8`. True only if the address binds exclusively to loopback; wildcard/empty hosts (`:8080`, `0.0.0.0:...`, `[::]:...`) return false; resolves hostnames like `localhost`.

## Connections

- Imports `internal/config` (model resolution in `FetchContext`) and `github.com/tidwall/gjson` (JSON body field extraction).
- Event structs implement `internal/event`'s `Event` contract and are published/consumed there.
- Consumed broadly by `internal/server` (metrics middleware, config/activity APIs, error rendering) and `internal/router`/scheduler (`HTTPError`/`ConcurrencyLimitError` for shedding, request context).

## Gotchas

- The event-ID constants here form one shared number space with `internal/logmon`'s `DataEventID` (`0x04`); keep IDs unique across both.
- `SetReqData` requires the `Metadata` map to already exist (i.e. `FetchContext`/`extractContext` ran); it errors rather than allocating.
- This package must stay a leaf w.r.t. `internal/process` — that's why process states are passed as strings in `ProcessStateChangeEvent`.
