# internal/perf

## Purpose

Live system and GPU/VRAM monitoring for the serving host. It samples CPU, memory, and per-GPU stats (utilization, used/total VRAM, temperature, fan, power) on a ticker, keeps a rolling ring buffer, and fans samples out to subscribers and Prometheus. It is the canonical, cross-platform source of **live free VRAM** for the fork's real-time offload calculation — it is already built in; do not reinvent GPU probing elsewhere.

## Key files

| File | Role |
|---|---|
| `types.go` | Core data structs: `GpuStat`, `SysStat`, `NetIOStat`. No build tag. |
| `monitor.go` | `Monitor` type, ring buffers, listener fan-out, `New`/`Start`/`Stop`/`UpdateConfig`/`Subscribe`/`Current`. Platform-agnostic; delegates to per-OS `getGpuStats`/`readSysStats`. |
| `gpu_parse.go` | Pure parsers reused across platforms: `ParseNvidiaSmiLine` (nvidia-smi CSV), `ParseIoregOutput` / `ParseMactopLine` (Apple Silicon), and the rocm-smi trio `parseRocmSmiLine` / `mergeGpuStat` / `parseRocmSmiCSV` (identical text cannot share a file across build tags without duplication). No build tag. |
| `prometheus.go` | `Monitor.MetricsHandler()` and the Prometheus text-format writers (`quartermaster_*` gauges/counters). No build tag. |
| `monitor_windows.go` | `//go:build` via filename. Windows `getGpuStats` (nvidia-smi loop, trimmed query → **DXGI fallback**) and `readSysStats`; `parseNvidiaSmiLineLite` (Windows-only CSV parser) overlays PDH util. |
| `dxgi_windows.go` | `//go:build windows`. Vendor-neutral VRAM backend (AMD/Intel) via DXGI COM: `DXGI_ADAPTER_DESC1.DedicatedVideoMemory` = total, `SharedSystemMemory` = the aperture. **Both usages come from PDH** (`GPU Adapter Memory\Dedicated Usage` and `\Shared Usage`, system-wide), NOT DXGI's per-process `QueryVideoMemoryInfo`. Collapses the driver's mirror LUIDs into one adapter; overlays PDH util by LUID. Reports **every** adapter with dedicated VRAM, iGPU included — eligibility is the device policy's job, not this file's. No temp/fan/power. |
| `monitor_darwin.go` | macOS `getGpuStats` (mactop → ioreg fallback) and `readSysStats`. Filename-tagged for darwin. |
| `monitor_unix.go` | `//go:build unix && !darwin`. Linux/BSD `getGpuStats` (LACT → nvidia-smi → rocm-smi → sysfs) and `readSysStats`; LACT socket protocol and rocm-smi CSV parsing. |
| `pdh_windows.go` | `//go:build windows`. PDH (`pdh.dll`) "GPU Engine" utilization counter — Task Manager's source, non-stalling. Provides `GpuUtilPct` for the Windows nvidia-smi path; defines `LUID`. |
| `computeapps.go` | Per-process GPU VRAM attribution (`QueryComputeApps`, `parseComputeApps`, `GpuProc`). No build tag. NVIDIA: `nvidia-smi --query-compute-apps`; non-NVIDIA falls back to `computeAppsPlatform` (`computeapps_windows.go` = PDH "GPU Process Memory" per-pid VRAM + gopsutil name; `computeapps_other.go` = nil). Data source for the server's foreign-VRAM / OOM protection. |

## Important types & functions

- `GpuStat` (`types.go`) — one GPU snapshot. The offload-relevant fields are `MemUsedMB` and `MemTotalMB` (`types.go`); free VRAM is `MemTotalMB - MemUsedMB`. Also carries `GpuUtilPct`, `MemUtilPct`, `TempC`/`VramTempC`, `FanSpeedPct`, `PowerDrawW`, and an `ID`/`Name`/`UUID`.

  `SharedTotalMB` / `SharedUsedMB` are the SECOND pool: system memory the device can address (AMD GTT, and the host aperture a discrete card maps too) — **not** the device's own memory. They are reported raw and NOT part of `MemTotalMB`; whether they count toward inference VRAM is a policy decision made in `internal/autogen` (`GpuPolicy.SharedMemory`), because the same signal means "this is the whole GPU" on an APU (issue #37) and "this is slow host memory" on a card. Both platforms fill them: rocm-smi's `GTT Total*` columns, and on Windows DXGI's `SharedSystemMemory` (size) with PDH `\GPU Adapter Memory(*)\Shared Usage` (usage, by LUID). An nvidia-smi card reports none — NVML has no aperture figure — so the fields stay 0 there and every consumer behaves exactly as it did before.
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
| unix (Linux/BSD) | LACT (unix socket) → nvidia-smi → rocm-smi (`--showmeminfo vram gtt`) → sysfs (unimplemented) | `monitor_unix.go`, `gpu_parse.go` |

When no backend works, `getGpuStats` returns `ErrNoGpuTool` and the monitor logs at info and continues with sys stats only.

## Gotchas / conventions

- **Build tags.** Each `getGpuStats`/`readSysStats` lives in exactly one OS file, selected either by `_windows.go`/`_darwin.go` filename suffix or an explicit `//go:build` line (`monitor_unix.go`, `pdh_windows.go`). `types.go`, `gpu_parse.go`, and `prometheus.go` are platform-neutral and compile everywhere.
- **PDH util (Windows).** `pdh_windows.go` reads `\GPU Engine(*)\Utilization Percentage`, groups per adapter `LUID` (parsed from the instance name), and `busiest()` returns the most-active adapter's util — during inference that's the discrete GPU. It is best-effort: if PDH init fails, `GpuUtilPct` stays 0. It has an `init()` size assertion (`pdhCounterValueItem` == 24 bytes); util is a rate counter so the first sample is 0 until a second collect lands. **Don't add `utilization.gpu`/`power.draw` back to the nvidia-smi query** — that's what caused the WDDM stalls.
- **Non-blocking fan-out.** Channels are buffered size 1 and every send uses `select { ... default: }` — slow consumers drop samples rather than block the sampler. `Subscribe` callers must call the returned `unsub` to avoid leaking listeners.
- **Prometheus export.** `MetricsHandler` (`prometheus.go`) reads `Current()`, emits the latest `SysStat` plus `latestPerGPU` de-duplicated GPU rows as `quartermaster_*` metrics; label values go through `sanitizeLabel`. MB fields are converted to bytes via `mbToBytes`.
- **mactop memory caveat.** mactop reports whole-system memory, so the darwin path overlays ioreg's GPU-attributed unified memory (`overlayIoregMem`) so both backends report consistent `MemUsedMB`/`MemTotalMB`.

- **rocm-smi reads both pools, in one invocation.** `rocmSmiMemArgs` probes `--showmeminfo vram gtt` once per process (memoized, and resolved on the first poll so the extra invocation is never charged to a caller's sample timeout) and falls back to `vram` alone if the installed rocm-smi rejects the pair. `VRAM Total*` lands in `MemTotalMB`/`MemUsedMB`, `GTT Total*` in `SharedTotalMB`/`SharedUsedMB` (the fields above); the label match is a case-insensitive `Contains(col, "GTT")` in the parser's `default` branch, because the wording has changed between rocm-smi releases ("GTT Memory", "GTT Total Memory (B)"). `parseRocmSmiCSV` keys rows by device id through `mergeGpuStat` instead of appending: the tool may print ONE table per memory type, and appending produced two rows for device 0 — which downstream reading keeps only the FIRST of (on a timestamp tie `gpuSetFromStats` prefers the earlier row), silently throwing the GTT half away. The three functions are pure and live in `gpu_parse.go` so their tests run on Windows.

- **DXGI backend (Windows, non-NVIDIA).** Only reached when `nvidia-smi` is absent. COM via raw vtable calls (`comCall`), no external tool. Two hazards guarded by `init()` panics: hand-written struct offsets (`DedicatedVideoMemory`@272, `AdapterLuid`@296, `DXGI_QUERY_VIDEO_MEMORY_INFO` size 32) — a wrong offset reads garbage VRAM silently. The AMD driver enumerates one physical card as **N mirror adapters** with distinct LUIDs but identical name/VRAM/budget; `openDxgiAdapters` collapses them by `name|totalMB` (keeping every LUID+interface) so the dashboard shows one GPU and `usedMB`/util take the **max across mirrors** (live usage can surface on any single mirror LUID). **Used VRAM MUST come from PDH `GPU Adapter Memory\Dedicated Usage` (system-wide), not DXGI `QueryVideoMemoryInfo` — the latter reports only the calling process's usage (≈0 for the monitor), which read as "0.0 used" on the gauge.** DXGI is total-only here. Unlike the nvidia-smi loop, DXGI is polled on a ticker in `tryDxgiWindows`; the sampler goroutine `LockOSThread`s because it holds COM pointers across ticks. iGPUs with nonzero dedicated VRAM appear too (that 485 MB Ryzen entry on a box with a discrete card is expected, not a bug), and which of them count is `internal/autogen`'s device policy — `freeVramGBFromStats` there pools the ELIGIBLE set, and by default an integrated GPU is not a split target beside a real card.

  **The shared pool is DXGI's total + PDH's usage, and it fails CLOSED.** `DXGI_ADAPTER_DESC1.SharedSystemMemory` gives the aperture's size; the usage comes from `\GPU Adapter Memory(*)\Shared Usage`, the same counter family and the same `luid_0x…_phys_0` instance names as `Dedicated Usage`, so `initPdhGpuSharedMem` reuses `initPdhLuidCounter` and `sharedUsedMB` mirrors `usedMB` (max across mirror LUIDs). When that counter cannot be opened — or answers with no data at all — `sharedUsedMB` returns the WHOLE aperture, so the pool reads as full rather than free: a free aperture would be folded into the budget as memory the GPU can hold, while a full one only costs those layers their place on the device. **The 1 GB `minInferenceVramMB` filter is gone with the same reasoning** — it dropped sub-1 GB adapters (a 485 MB Ryzen iGPU among them) before the policy layer ever saw them, which on an APU-only Windows box is the Windows half of issue #37. Only the software (WARP) adapter and adapters reporting no dedicated memory at all are skipped now; `--tensor-split` ordinals do not depend on the enumeration index (`BackendIDs` matches the backend's own list by NAME), so admitting an iGPU cannot shuffle a card's ordinal.

## Connections

- **Real-time offload calc / autogen / router.** Consumers read live VRAM via `Monitor.Current()` (or `Subscribe()`), using `GpuStat.MemTotalMB - MemUsedMB` as free VRAM to compute ngl/n_cpu_moe/KV at spawn time. This package is the single data source for that — see the fork notes in the root `CLAUDE.md`.
- **Prometheus / UI.** `MetricsHandler()` is mounted by the server for `/metrics`; the Svelte UI consumes the same `GpuStat`/`SysStat` JSON shapes.
- **Foreign-VRAM / OOM protection.** `QueryComputeApps` is the data source for `internal/server`'s foreign-VRAM tally: `apigroup.go` `foreignGPU()` calls it, subtracts the router's own `RunningPIDs()`, and flags stray `llama-server`/`sd-server` VRAM (red UI gauge) so the offload sizer avoids OOM. The consuming policy lives in the server, not here.
- **Config.** Driven by `config.PerformanceConfig` (`Every`, `Disabled`); `UpdateConfig` restarts the monitor on change.
