# internal/perf

## Purpose

Live system and GPU/VRAM monitoring for the serving host. It samples CPU, memory, and per-GPU stats (utilization, used/total VRAM, temperature, fan, power) on a ticker, keeps a rolling ring buffer, and fans samples out to subscribers and Prometheus. It is the canonical, cross-platform source of **live free VRAM** for the fork's real-time offload calculation — it is already built in; do not reinvent GPU probing elsewhere.

## Key files

| File | Role |
|---|---|
| `types.go` | Core data structs: `GpuStat`, `SysStat`, `NetIOStat`. No build tag. |
| `monitor.go` | `Monitor` type, ring buffers, listener fan-out, `New`/`Start`/`Stop`/`UpdateConfig`/`Subscribe`/`Current`. Platform-agnostic; delegates to per-OS `getGpuStats`/`readSysStats`. |
| `gpu_parse.go` | Pure parsers reused across platforms: `ParseNvidiaSmiLine` (nvidia-smi CSV), `ParseIoregOutput` / `ParseMactopLine` (Apple Silicon). No build tag. |
| `prometheus.go` | `Monitor.MetricsHandler()` and the Prometheus text-format writers (`quartermaster_*` gauges/counters). No build tag. |
| `monitor_windows.go` | `//go:build` via filename. Windows `getGpuStats` (nvidia-smi loop, trimmed query → **DXGI fallback**) and `readSysStats`; `parseNvidiaSmiLineLite` (Windows-only CSV parser) overlays PDH util. |
| `dxgi_windows.go` | `//go:build windows`. Vendor-neutral VRAM backend (AMD/Intel) via DXGI COM: `DXGI_ADAPTER_DESC1.DedicatedVideoMemory` = total. **Used comes from PDH** (`GPU Adapter Memory\Dedicated Usage`, system-wide), NOT DXGI's per-process `QueryVideoMemoryInfo`. Collapses the driver's mirror LUIDs into one adapter; filters iGPUs below `minInferenceVramMB`; overlays PDH util by LUID. No temp/fan/power. |
| `monitor_darwin.go` | macOS `getGpuStats` (mactop → ioreg fallback) and `readSysStats`. Filename-tagged for darwin. |
| `monitor_unix.go` | `//go:build unix && !darwin`. Linux/BSD `getGpuStats` (LACT → nvidia-smi → rocm-smi → sysfs) and `readSysStats`; LACT socket protocol and rocm-smi CSV parsing. |
| `pdh_windows.go` | `//go:build windows`. PDH (`pdh.dll`) "GPU Engine" utilization counter — Task Manager's source, non-stalling. Provides `GpuUtilPct` for the Windows nvidia-smi path; defines `LUID`. |
| `computeapps.go` | Per-process GPU VRAM attribution (`QueryComputeApps`, `parseComputeApps`, `GpuProc`). No build tag. NVIDIA: `nvidia-smi --query-compute-apps`; non-NVIDIA falls back to `computeAppsPlatform` (`computeapps_windows.go` = PDH "GPU Process Memory" per-pid VRAM + gopsutil name; `computeapps_other.go` = nil). Data source for the server's foreign-VRAM / OOM protection. |

## Important types & functions

- `GpuStat` (`types.go`) — one GPU snapshot. The offload-relevant fields are `MemUsedMB` and `MemTotalMB` (`types.go`); free VRAM is `MemTotalMB - MemUsedMB`. Also carries `GpuUtilPct`, `MemUtilPct`, `TempC`/`VramTempC`, `FanSpeedPct`, `PowerDrawW`, and an `ID`/`Name`/`UUID`.
- `SysStat` (`types.go`) — CPU per-core, memory, swap, load average, and network IO.
- `Monitor` (`monitor.go`) — owns RW-locked ring buffers and listener sets.
  - `New` (`monitor.go`) — clamps `Every` to ≥100ms; sizes ring to ~1 hour of samples.
  - `Start` (`monitor.go`) — spins two goroutines: a sys ticker and a GPU reader fed by `getGpuStats`.
  - `Subscribe` (`monitor.go`) — returns `(sysChan, gpuChan, unsub)`; non-blocking sends (drops if a listener is slow).
  - `Current` (`monitor.go`) — returns a copy of buffered `[]SysStat` and a flattened `[]GpuStat` snapshot history. This is the read path for offload math and the UI.
- `GpuProc` (`computeapps.go`) — one process's VRAM snapshot `{PID, Name, MemMB}`, distinct from the per-GPU `GpuStat`.
- `QueryComputeApps` (`computeapps.go`) — runs `nvidia-smi --query-compute-apps=pid,used_memory,process_name` and parses (via `parseComputeApps`) the CSV into `[]GpuProc`. Uses NVML **process accounting** (not hw perf counters), so it does NOT stall in-flight generation — the same stall concern the D3DKMT/nvidia-smi note calls out for `utilization.gpu`/`power.draw`. When `nvidia-smi` is absent it falls back to `computeAppsPlatform`: on **Windows non-NVIDIA** (AMD/Intel) the PDH `\GPU Process Memory(*)\Dedicated Usage` counter (pid from the instance name, VRAM summed per pid, name via gopsutil) — so foreign-VRAM / OOM protection works on AMD; `nil` on darwin/unix → "no foreign processes".

> **Windows GPU backend = nvidia-smi (VRAM/temp/fan) + PDH (util).** A full D3DKMT backend (raw gdi32 syscalls) was tried and removed: on Optimus/hybrid laptops the discrete GPU's dedicated VRAM is routed through the WDDM aperture and is invisible to D3DKMT segment queries (it reports phantom shared-memory totals and only the iGPU). nvidia-smi reads NVML directly and reports correct VRAM. Two NVML fields were also dropped from the query: `utilization.gpu` and `power.draw` force the driver to sample hardware perf counters, which preempts an in-flight llama.cpp generation and shows up as token-stream stalls / late requests. Util is recovered from PDH "GPU Engine" counters (WDDM scheduler accounting, no stall); **power is not reported on Windows** (no cheap non-stalling source). Non-NVIDIA Windows GPUs (AMD/Intel) are covered by the DXGI fallback (`dxgi_windows.go`) — VRAM total+used from DXGI, util from PDH, no temp/fan/power.

## Platform matrix

| OS | Primary → fallbacks | Files |
|---|---|---|
| Windows | nvidia-smi (loop; VRAM/temp/fan) → DXGI (VRAM only, any vendor) + PDH (util) | `monitor_windows.go`, `dxgi_windows.go`, `pdh_windows.go` |
| darwin (Apple Silicon) | mactop (headless JSON) → ioreg (`IOGPU`) | `monitor_darwin.go`, `gpu_parse.go` |
| unix (Linux/BSD) | LACT (unix socket) → nvidia-smi → rocm-smi → sysfs (unimplemented) | `monitor_unix.go`, `gpu_parse.go` |

When no backend works, `getGpuStats` returns `ErrNoGpuTool` and the monitor logs at info and continues with sys stats only.

## Gotchas / conventions

- **Build tags.** Each `getGpuStats`/`readSysStats` lives in exactly one OS file, selected either by `_windows.go`/`_darwin.go` filename suffix or an explicit `//go:build` line (`monitor_unix.go`, `pdh_windows.go`). `types.go`, `gpu_parse.go`, and `prometheus.go` are platform-neutral and compile everywhere.
- **PDH util (Windows).** `pdh_windows.go` reads `\GPU Engine(*)\Utilization Percentage`, groups per adapter `LUID` (parsed from the instance name), and `busiest()` returns the most-active adapter's util — during inference that's the discrete GPU. It is best-effort: if PDH init fails, `GpuUtilPct` stays 0. It has an `init()` size assertion (`pdhCounterValueItem` == 24 bytes); util is a rate counter so the first sample is 0 until a second collect lands. **Don't add `utilization.gpu`/`power.draw` back to the nvidia-smi query** — that's what caused the WDDM stalls.
- **Non-blocking fan-out.** Channels are buffered size 1 and every send uses `select { ... default: }` — slow consumers drop samples rather than block the sampler. `Subscribe` callers must call the returned `unsub` to avoid leaking listeners.
- **Prometheus export.** `MetricsHandler` (`prometheus.go`) reads `Current()`, emits the latest `SysStat` plus `latestPerGPU` de-duplicated GPU rows as `quartermaster_*` metrics; label values go through `sanitizeLabel`. MB fields are converted to bytes via `mbToBytes`.
- **mactop memory caveat.** mactop reports whole-system memory, so the darwin path overlays ioreg's GPU-attributed unified memory (`overlayIoregMem`) so both backends report consistent `MemUsedMB`/`MemTotalMB`.

- **DXGI backend (Windows, non-NVIDIA).** Only reached when `nvidia-smi` is absent. COM via raw vtable calls (`comCall`), no external tool. Two hazards guarded by `init()` panics: hand-written struct offsets (`DedicatedVideoMemory`@272, `AdapterLuid`@296, `DXGI_QUERY_VIDEO_MEMORY_INFO` size 32) — a wrong offset reads garbage VRAM silently. The AMD driver enumerates one physical card as **N mirror adapters** with distinct LUIDs but identical name/VRAM/budget; `openDxgiAdapters` collapses them by `name|totalMB` (keeping every LUID+interface) so the dashboard shows one GPU and `usedMB`/util take the **max across mirrors** (live usage can surface on any single mirror LUID). **Used VRAM MUST come from PDH `GPU Adapter Memory\Dedicated Usage` (system-wide), not DXGI `QueryVideoMemoryInfo` — the latter reports only the calling process's usage (≈0 for the monitor), which read as "0.0 used" on the gauge.** DXGI is total-only here. Unlike the nvidia-smi loop, DXGI is polled on a ticker in `tryDxgiWindows`; the sampler goroutine `LockOSThread`s because it holds COM pointers across ticks. iGPUs with nonzero dedicated VRAM appear too, but `freeVramGBFromStats` (autogen) picks the largest-total adapter.

## Connections

- **Real-time offload calc / autogen / router.** Consumers read live VRAM via `Monitor.Current()` (or `Subscribe()`), using `GpuStat.MemTotalMB - MemUsedMB` as free VRAM to compute ngl/n_cpu_moe/KV at spawn time. This package is the single data source for that — see the fork notes in the root `CLAUDE.md`.
- **Prometheus / UI.** `MetricsHandler()` is mounted by the server for `/metrics`; the Svelte UI consumes the same `GpuStat`/`SysStat` JSON shapes.
- **Foreign-VRAM / OOM protection.** `QueryComputeApps` is the data source for `internal/server`'s foreign-VRAM tally: `apigroup.go` `foreignGPU()` calls it, subtracts the router's own `RunningPIDs()`, and flags stray `llama-server`/`sd-server` VRAM (red UI gauge) so the offload sizer avoids OOM. The consuming policy lives in the server, not here.
- **Config.** Driven by `config.PerformanceConfig` (`Every`, `Disabled`); `UpdateConfig` restarts the monitor on change.
