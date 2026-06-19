# internal/config

## Purpose

Parses, validates, and normalizes the llama-quartermaster YAML config into the in-memory `Config` struct consumed by the rest of the server. It owns macro expansion, `${PORT}` allocation, command sanitization (shlex into argv), group/matrix routing normalization, the swap-matrix DSL, request filters, remote peers, and the fork's per-listener catalogs.

## Key files

| File | Role |
|---|---|
| `config.go` | Top-level `Config` struct, `LoadConfig`/`LoadConfigFromReader`, macro + env-macro expansion, `${PORT}` allocation, `SanitizeCommand`, routing/group normalization, validation. |
| `model_config.go` | `ModelConfig` (per-model fields), defaults via `UnmarshalYAML`, `SanitizedCommand`, `ModelCapConfig`, `TimeoutsConfig`, `ModelFilters` (legacy `strip_params` shim). |
| `filters.go` | `Filters` type (`stripParams`/`setParams`/`setParamsByID`) and sanitizers that drop protected params and sort keys. Shared by models and peers. |
| `listeners.go` | Fork feature: `ListenerConfig` plus helpers (`validateListeners`, `ListenerModelSets`, `ListenerAddrs`) mapping a listen address to the groups/models it exposes. |
| `matrix.go` | `MatrixConfig` swap-matrix model, `ValidateMatrix` (var/evict-cost validation, dependency topo-sort, set expansion), `ResolvedEvictCosts`. |
| `matrix_dsl.go` | Lexer/recursive-descent parser/expander for the set DSL (`&`, `|`, `(...)`, `+ref`); `ParseAndExpandDSL`, `extractRefs`. |
| `peer.go` | `PeerConfig`/`PeerDictionaryConfig` for remote upstream peers, with URL + non-empty-models validation. |
| `performance.go` | `PerformanceConfig` for the system perf monitor (`disabled`, `every`), defaults + validation (min 5s). |

## Important types & functions

- `Config` — `config.go:120`. Root config object; `Models`, `Routing`, `Listeners`, `Peers`, `Macros`, `StartPort`, etc. Holds unexported `aliases` map.
- `LoadConfig(path)` / `LoadConfigFromReader(r)` — `config.go:222` / `config.go:231`. Single entry point: reads YAML, expands env macros (phase 1, string-level), unmarshals with defaults, validates, expands per-model macros, allocates ports, normalizes routing, and validates listeners.
- Macro expansion — per-model loop in `LoadConfigFromReader` at `config.go:336-396`. Merged macro list is `MODEL_ID` + global macros + model macros (model overrides global); substituted in LIFO order across `cmd`, `cmdStop`, `proxy`, `checkEndpoint`, filters, name, description, and (type-preserving) metadata.
- `${env.VAR}` substitution — `substituteEnvMacros` `config.go:868` (strips YAML comments before scanning so macros in comments are ignored).
- `${PORT}` allocation — `config.go:398-424`. Only allocated when `cmd` uses `${PORT}`; ports assigned sequentially from `StartPort` over sorted model IDs. `proxy` may use `${PORT}` only if `cmd` does.
- `SanitizeCommand(cmdStr)` — `config.go:698`. Strips `#` comment lines, joins `\`-continued lines, then shlex-splits into argv (Windows vs POSIX rules by `runtime.GOOS`). `ModelConfig.SanitizedCommand` (`model_config.go:147`) wraps it for `cmd`.
- `ModelConfig` — `model_config.go:65`; defaults (incl. `proxy: http://localhost:${PORT}`, platform-specific `cmdStop`) in its `UnmarshalYAML` `model_config.go:107`.
- `Filters` — `filters.go:15`; `SanitizedStripParams`, `SanitizedSetParams`, `SanitizedSetParamsByID` all drop `ProtectedParams` (`model`).
- `ListenerConfig` / `ListenerModelSets` — `listeners.go:12` / `listeners.go:55`. Per-address reachable model set = union of its groups' members.
- `MatrixConfig` / `ValidateMatrix` — `matrix.go:14` / `matrix.go:67`. `ExpandedSet` (`matrix.go:60`) is one valid concurrent-model combination.
- `ParseAndExpandDSL` — `matrix_dsl.go:316`. `&` (cartesian product) binds tighter than `|` (union); capped at `maxDSLExpansions` (1000).
- `RealModelName` / `FindConfig` — `config.go:204` / `config.go:214`. Resolve a requested name (model ID or alias) to a real model.

## Config structure

Top-level YAML keys map to `Config`: `healthCheckTimeout`, `logRequests`, `logLevel`, `logToStdout`, `metricsMaxInMemory`, `captureBuffer`, `performance`, `globalTTL`, `startPort`, `macros`, `models`, `profiles`, `hooks`, `apiKeys`, `peers`, `listeners`, and the routing block.

Routing has two equivalent input styles, normalized into the canonical `Config.Routing` (`RoutingConfig`, `config.go:176`):
- Legacy top-level `groups:` / `matrix:` keys, or
- The newer `routing.router.use` (`group` | `matrix`) with `routing.router.settings`.

The two styles are mutually exclusive, and `groups` XOR `matrix` always holds. When neither groups nor matrix is given, `AddDefaultGroupToConfig` (`config.go:654`) places all orphan models in `(default)`. Scheduler is `routing.scheduler` (only `fifo` supported, with per-model priority).

Each `models:` entry (`ModelConfig`) carries `cmd`, `cmdStop`, `proxy`, `aliases`, `env`, `checkEndpoint`, `ttl`, `unlisted`, `useModelName`, `name`, `description`, `concurrencyLimit`, `filters`, `macros`, `metadata`, `sendLoadingState`, `timeouts`, and `capabilities`.

## Gotchas / conventions

- **Macros expand at config-load time, not spawn time.** This is the key constraint for the fork's dynamic-ctx goal: `${CTX}/${NGL}/${NCPUMOE}/${CTK}/${CTV}` injection must happen at spawn (keyed by `(modelID, ctx)`), so it cannot reuse the load-time substitution loop here — it needs a separate spawn-time pass.
- `${PORT}` and `${MODEL_ID}` are reserved macro names; user macros matching them are rejected. `${PID}` in `cmdStop` is left unsubstituted (replaced at runtime by the process layer).
- After substitution the loader hard-fails on any remaining `${...}` macro (`config.go:426-449`), so unknown macros are a load error, not a silent passthrough.
- `setParamsByID` keys auto-register as aliases for the model (`config.go:471-487`); conflicts with real model IDs or other aliases are errors.
- `ProtectedParams` (currently just `model`) can never be stripped or set via filters.
- Listeners require the **group** router (not matrix) and reference existing groups; an address absent from `ListenerModelSets` is unrestricted by convention. See the fork goal of multi-listener with separate catalogs.
- Platform-specific behavior: `SanitizeCommand` uses Windows vs POSIX shlex, and the default `cmdStop` is `taskkill ...` on Windows. Expect platform-tagged differences in tests.
- `*_test.go` files in this package (`*_test.go`) cover load/macro/matrix/listener behavior and should be run with `go test ./internal/config/...`.

## Connections

`internal/config` is a foundational, dependency-free leaf package imported widely:
- `internal/router` (`base.go`, `group.go`, `matrix*.go`) and `internal/router/scheduler` — consume groups, matrix `ExpandedSets`, evict costs, and scheduler priority.
- `internal/process` (`process_command.go`) — reads `ModelConfig` for spawning (`SanitizedCommand`, env, timeouts, `cmdStop`).
- `internal/server` (routes, `/v1/models`, filters, auth) — uses `Filters`, capabilities, peers, and `ListenerModelSets`/`ListenerAddrs` for per-listener catalogs.
- `internal/perf` — driven by `PerformanceConfig`.
- `internal/shared`, `cmd/monitor-test`, and the entry point `llama-quartermaster.go`.
