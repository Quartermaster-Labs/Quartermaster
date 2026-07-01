import { writable } from "svelte/store";
import type { GpuStat, SysStat } from "../lib/types";
import { fetchPerformance } from "./api";

// Latest sampled GPU/system stats, used by the always-on status rail + dashboard
// gauges. Polled rather than event-driven so the rail stays live regardless of
// which screen is open.
export const latestGpu = writable<GpuStat | null>(null);
export const latestSys = writable<SysStat | null>(null);

// GPU memory (MiB) held by foreign llama-server/sd-server processes we didn't
// spawn. Drives a red "Foreign" segment on the VRAM gauge.
export const foreignVram = writable<{ mb: number; procs?: { pid: number; name: string; mem_mb: number }[] }>({
  mb: 0,
});

// Idle system-VRAM floor (MiB) measured server-side (min used while no model
// running), captured regardless of whether a dashboard tab is open. 0 = not yet
// observed. The VRAM gauge prefers this over its own browser-only baseline.
export const systemVram = writable<number>(0);

let timer: ReturnType<typeof setInterval> | null = null;
let lastTs: string | undefined;

export function startPerfPolling(intervalMs = 2000): () => void {
  const tick = async (): Promise<void> => {
    const data = await fetchPerformance(lastTs);
    if (!data) return;
    foreignVram.set(data.foreign ?? { mb: 0 });
    if (typeof data.system_mb === "number") systemVram.set(data.system_mb);
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
