import { describe, it, expect } from "vitest";
import { get } from "svelte/store";
import { latestGpu, pooledVram, vramTotals } from "./perf";
import type { GpuStat } from "../lib/types";

function gpu(p: Partial<GpuStat> = {}): GpuStat {
  return {
    timestamp: "2026-09-07T12:00:00Z",
    id: 1, name: "NVIDIA GeForce RTX 4070 Ti SUPER", uuid: "GPU-b",
    temp_c: 34, vram_temp_c: 0, gpu_util_pct: 0, mem_util_pct: 0,
    mem_used_mb: 4, mem_total_mb: 16376, fan_speed_pct: 0, power_draw_w: 7,
    ...p,
  };
}

describe("vramTotals", () => {
  // The bug: gpu_stats is a flat per-device history, so the gauge's "newest
  // entry" was whichever card the monitor enumerated last. A 12 GB + 16 GB pair
  // drew a 16 GB bar while the router admitted against 28 (issue #4).
  it("prefers the server's pooled reading over the last enumerated card", () => {
    latestGpu.set(gpu());
    pooledVram.set({ used_mb: 8, total_mb: 12288 + 16376, devices: 2 });
    expect(get(vramTotals)).toEqual({ usedMb: 8, totalMb: 28664, devices: 2 });
  });

  // A server too old to send gpu_pooled still has to draw a bar, and a
  // single-GPU box is the same code path.
  it("falls back to the single device when there is no pooled reading", () => {
    latestGpu.set(gpu());
    pooledVram.set(null);
    expect(get(vramTotals)).toEqual({ usedMb: 4, totalMb: 16376, devices: 1 });
  });

  // A zero total is not a card, it is a monitor that has not reported yet.
  // Trusting it would paint a full bar and read as "nothing fits".
  it("ignores a pooled reading with no total", () => {
    latestGpu.set(gpu());
    pooledVram.set({ used_mb: 0, total_mb: 0, devices: 0 });
    expect(get(vramTotals)).toEqual({ usedMb: 4, totalMb: 16376, devices: 1 });
  });

  it("reports nothing when there is no telemetry at all", () => {
    latestGpu.set(null);
    pooledVram.set(null);
    expect(get(vramTotals)).toBeNull();
  });
});
