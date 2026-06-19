# internal/perf

## Purpose

Live system and GPU/VRAM monitoring for the serving host. It samples CPU, memory, and per-GPU stats (utilization, used/total VRAM, temperature, fan, power) on a ticker, keeps a rolling ring buffer, and fans samples out to subscribers and Prometheus. It is the canonical, cross-platform source of **live free VRAM** for the fork's real-time offload calculation — it is already built in; do not reinvent GPU probing elsewhere.

## Key files

| File | Role |
|---|---|
| `types.go` | Core data structs: `GpuStat`, `SysStat`, `NetIOStat`. No build tag. |
| `monitor.go` | `Monitor` type, ring buffers, listener fan-out, `New`/`Start`/`Stop`/`UpdateConfig`/`Subscribe`/`Current`. Platform-agnostic; delegates to per-OS `getGpuStats`/`readSysStats`. |
| `gpu_parse.go` | Pure parsers reused across platforms: `ParseNvidiaSmiLine` (nvidia-smi CSV), `ParseIoregOutput` / `ParseMactopLine` (Apple Silicon). No build tag. |
| `prometheus.go` | `Monitor.MetricsHandler()` and the Prometheus text-format writers (`llamaswap_*` gauges/counters). No build tag. |
| `monitor_windows.go` | `//go:build` via filename. Windows `getGpuStats` (nvidia-smi loop → D3DKMT fallback) and `readSysStats`. |
| `monitor_darwin.go` | macOS `getGpuStats` (mactop → ioreg fallback) and `readSysStats`. Filename-tagged for darwin. |
| `monitor_unix.go` | `//go:build unix && !darwin`. Linux/BSD `getGpuStats` (LACT → nvidia-smi → rocm-smi → sysfs) and `readSysStats`; LACT socket protocol and rocm-smi CSV parsing. |
| `d3dkmt_windows.go` | `//go:build windows`. D3DKMT GPU backend: gdi32 proc loading, adapter enumeration, segment/node/perf queries, util/fan/power/temp derivation, optional PDH overlay. |
| `d3dkmt_types.go` | D3DKMT struct/enum/LUID definitions mirroring the Windows kernel-mode thunk ABI. No build tag (struct defs are inert on other OSes). |
| `pdh_windows.go` | `//go:build windows`. PDH (`pdh.dll`) GPU Engine utilization counter: query setup, collection, LUID parsing from instance names. |

## Important types & functions

- `GpuStat` (`types.go:5`) — one GPU snapshot. The offload-relevant fields are `MemUsedMB` and `MemTotalMB` (`types.go:15-16`); free VRAM is `MemTotalMB - MemUsedMB`. Also carries `GpuUtilPct`, `MemUtilPct`, `TempC`/`VramTempC`, `FanSpeedPct`, `PowerDrawW`, and an `ID`/`Name`/`UUID`.
- `SysStat` (`types.go:27`) — CPU per-core, memory, swap, load average, and network IO.
- `Monitor` (`monitor.go:19`) — owns RW-locked ring buffers and listener sets.
  - `New` (`monitor.go:41`) — clamps `Every` to ≥100ms; sizes ring to ~1 hour of samples.
  - `Start` (`monitor.go:114`) — spins two goroutines: a sys ticker and a GPU reader fed by `getGpuStats`.
  - `Subscribe` (`monitor.go:95`) — returns `(sysChan, gpuChan, unsub)`; non-blocking sends (drops if a listener is slow).
  - `Current` (`monitor.go:186`) — returns a copy of buffered `[]SysStat` and a flattened `[]GpuStat` snapshot history. This is the read path for offload math and the UI.
- D3DKMT access (`d3dkmt_windows.go`):
  - `initD3DKMT` (`:30`) — `sync.Once` lazy-load of `gdi32.dll` and resolution of the `D3DKMT*` procs.
  - `tryD3DKMT` (`:312`) — enumerates adapters, opens handles, and streams `[]GpuStat`; VRAM comes from per-segment `d3dkmQuerySegmentStats` (`:208`), utilization from PDH if available else node running-time deltas (`d3dkmtNodeUtil` `:248`).

## Platform matrix

| OS | Primary → fallbacks | Files |
|---|---|---|
| Windows | nvidia-smi (loop) → **D3DKMT** (gdi32) + **PDH** (pdh.dll) for util | `monitor_windows.go`, `d3dkmt_windows.go`, `d3dkmt_types.go`, `pdh_windows.go` |
| darwin (Apple Silicon) | mactop (headless JSON) → ioreg (`IOGPU`) | `monitor_darwin.go`, `gpu_parse.go` |
| unix (Linux/BSD) | LACT (unix socket) → nvidia-smi → rocm-smi → sysfs (unimplemented) | `monitor_unix.go`, `gpu_parse.go` |

When no backend works, `getGpuStats` returns `ErrNoGpuTool` and the monitor logs at info and continues with sys stats only.

## Gotchas / conventions

- **Build tags.** Each `getGpuStats`/`readSysStats` lives in exactly one OS file, selected either by `_windows.go`/`_darwin.go` filename suffix or an explicit `//go:build` line (`monitor_unix.go`, `d3dkmt_windows.go`, `pdh_windows.go`). `types.go`, `gpu_parse.go`, `prometheus.go`, and `d3dkmt_types.go` are platform-neutral and compile everywhere.
- **D3DKMT is raw syscall, not cgo.** Functions are resolved off `gdi32.dll` via `windows.LazyDLL`/`LazyProc` and invoked with `unsafe.Pointer` arg structs; results are NTSTATUS codes (non-zero = error). The struct layouts are ABI-exact for x64 — `d3dkmt_windows.go` has `init()` size/offset assertions (`queryStatsBuffer` must be 808 bytes with `QueryId` at offset 804; the offset-804 bug history is documented inline) that **panic** on mismatch. Do not reorder or repad these structs.
- **PDH overlay.** When the PDH GPU Engine counter is available, its per-LUID utilization (summed across engine instances, clamped to 100%) takes precedence over D3DKMT node running-time deltas. `pdh_windows.go` also has an `init()` size assertion (`pdhCounterValueItem` == 24 bytes).
- **Non-blocking fan-out.** Channels are buffered size 1 and every send uses `select { ... default: }` — slow consumers drop samples rather than block the sampler. `Subscribe` callers must call the returned `unsub` to avoid leaking listeners.
- **Prometheus export.** `MetricsHandler` (`prometheus.go:14`) reads `Current()`, emits the latest `SysStat` plus `latestPerGPU` de-duplicated GPU rows as `llamaswap_*` metrics; label values go through `sanitizeLabel`. MB fields are converted to bytes via `mbToBytes`.
- **mactop memory caveat.** mactop reports whole-system memory, so the darwin path overlays ioreg's GPU-attributed unified memory (`overlayIoregMem`) so both backends report consistent `MemUsedMB`/`MemTotalMB`.

## Connections

- **Real-time offload calc / autogen / router.** Consumers read live VRAM via `Monitor.Current()` (or `Subscribe()`), using `GpuStat.MemTotalMB - MemUsedMB` as free VRAM to compute ngl/n_cpu_moe/KV at spawn time. This package is the single data source for that — see the fork notes in the root `CLAUDE.md`.
- **Prometheus / UI.** `MetricsHandler()` is mounted by the server for `/metrics`; the Svelte UI consumes the same `GpuStat`/`SysStat` JSON shapes.
- **Config.** Driven by `config.PerformanceConfig` (`Every`, `Disabled`); `UpdateConfig` restarts the monitor on change.
