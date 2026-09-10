import { derived, writable } from "svelte/store";
import type { GpuStat, PooledVram, SysStat } from "../lib/types";
import { fetchPerformance } from "./api";

// Latest sampled GPU/system stats, used by the always-on status rail + dashboard
// gauges. Polled rather than event-driven so the rail stays live regardless of
// which screen is open.
export const latestGpu = writable<GpuStat | null>(null);
export const latestSys = writable<SysStat | null>(null);

// VRAM pooled across every inference-eligible adapter, straight from the server.
export const pooledVram = writable<PooledVram | null>(null);

// The one VRAM reading every gauge should use: pooled when the server offers it,
// otherwise the newest single device.
//
// latestGpu is the last entry of a flat per-device history, so on a multi-GPU box
// it is whichever card the monitor enumerated last. A 12 GB + 16 GB pair drew a
// 16 GB bar while the router was admitting against 28 (issue #4). It stays the
// fallback for a server too old to send gpu_pooled, and for temperature, power
// and utilisation, which stay per-card because a pooled figure for those would
// describe no physical device.
export const vramTotals = derived(
  [pooledVram, latestGpu],
  ([$pooled, $gpu]): { usedMb: number; totalMb: number; devices: number } | null => {
    if ($pooled && $pooled.total_mb > 0) {
      return { usedMb: $pooled.used_mb, totalMb: $pooled.total_mb, devices: $pooled.devices };
    }
    if ($gpu) return { usedMb: $gpu.mem_used_mb, totalMb: $gpu.mem_total_mb, devices: 1 };
    return null;
  },
);

// GPU memory (MiB) held by foreign llama-server/sd-server processes we didn't
// spawn. Drives a red "Foreign" segment on the VRAM gauge.
export const foreignVram = writable<{ mb: number; procs?: { pid: number; name: string; mem_mb: number }[] }>({
  mb: 0,
});

// Idle system-VRAM floor (MiB) measured server-side (min used while no model
// running), captured regardless of whether a dashboard tab is open. 0 = not yet
// observed. The VRAM gauge prefers this over its own browser-only baseline.
export const systemVram = writable<number>(0);

// GPU memory (MiB) held by everything that is NOT one of our children, measured
// per-process on every server sample and published by the OOM guard. This is the
// LIVE answer to "what are the OS and other apps using", where systemVram above
// is only a floor sampled while no model was loaded - it freezes for as long as a
// model stays resident, so a game or a Blender/Unity session opened afterwards
// never moves it and its VRAM leaks into the gauge's model slice (and from there
// into the "Overhead" residual). null = the guard has no trustworthy reading (no
// per-process source, or one of our children not yet visible to it), which is the
// gauge's cue to fall back to the idle floor.
export const guardForeignVram = writable<number | null>(null);

let timer: ReturnType<typeof setInterval> | null = null;
let lastTs: string | undefined;

export function startPerfPolling(intervalMs = 2000): () => void {
  const tick = async (): Promise<void> => {
    const data = await fetchPerformance(lastTs);
    if (!data) return;
    foreignVram.set(data.foreign ?? { mb: 0 });
    // undefined (old server) and null (no telemetry) both mean "no pooled
    // reading"; vramTotals falls back to the single device for either.
    pooledVram.set(data.gpu_pooled ?? null);
    if (typeof data.system_mb === "number") systemVram.set(data.system_mb);
    guardForeignVram.set(
      typeof data.guard?.foreign_mb === "number" ? data.guard.foreign_mb : null,
    );
    if (data.gpu_stats?.length) {
      const g = data.gpu_stats[data.gpu_stats.length - 1];
      latestGpu.set(g);
      lastTs = g.timestamp;
    }
    if (data.sys_stats?.length) {
      latestSys.set(data.sys_stats[data.sys_stats.length - 1]);
    }
  };
  void tick();
  timer = setInterval(() => void tick(), intervalMs);
  return () => {
    if (timer) clearInterval(timer);
    timer = null;
    lastTs = undefined;
  };
}
