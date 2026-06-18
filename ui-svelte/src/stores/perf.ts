import { writable } from "svelte/store";
import type { GpuStat, SysStat } from "../lib/types";
import { fetchPerformance } from "./api";

// Latest sampled GPU/system stats, used by the always-on status rail + dashboard
// gauges. Polled rather than event-driven so the rail stays live regardless of
// which screen is open.
export const latestGpu = writable<GpuStat | null>(null);
export const latestSys = writable<SysStat | null>(null);

let timer: ReturnType<typeof setInterval> | null = null;
let lastTs: string | undefined;

export function startPerfPolling(intervalMs = 2000): () => void {
  const tick = async (): Promise<void> => {
    const data = await fetchPerformance(lastTs);
    if (!data) return;
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
