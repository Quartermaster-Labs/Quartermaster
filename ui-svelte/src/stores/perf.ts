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

let timer: ReturnType<typeof setInterval> | null = null;
let lastTs: string | undefined;

export function startPerfPolling(intervalMs = 2000): () => void {
  const tick = async (): Promise<void> => {
    const data = await fetchPerformance(lastTs);
    if (!data) return;
    foreignVram.set(data.foreign ?? { mb: 0 });
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
