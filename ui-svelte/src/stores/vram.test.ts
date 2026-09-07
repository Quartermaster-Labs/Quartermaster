import { describe, it, expect } from "vitest";
import { loadedSegments } from "./vram";
import type { PlanEstimate } from "./api";

// A plausible estimate: 10 GB total = 6 weights + 2 KV + 0.5 draft + 0.5
// compute + 0.5 ckpt + 0.5 headroom.
function est(p: Partial<PlanEstimate> = {}): PlanEstimate {
  return {
    ctx: 32768, ngl: 99, nCpuMoe: 0,
    estVramGB: 10, estRamGB: 0, targetVramGB: 24, maxRamGB: 64,
    kvReserveGB: 2, checkpointGB: 0.5, draftGB: 0.5, computeBufGB: 0.5,
    mmprojGB: 0, overheadGB: 0.5, ramExceeded: false, isMoE: false,
    ...p,
  };
}
const labels = (segs: { label: string }[] | null) => (segs ?? []).map((s) => s.label);
const mb = (segs: { label: string; mb: number }[] | null, label: string) =>
  (segs ?? []).find((s) => s.label === label)?.mb ?? 0;

describe("loadedSegments", () => {
  // The bug: with two models loaded the rail fell back to one flat "Model(s)"
  // block, losing the whole component breakdown exactly when the card is
  // fullest. Components are summed across models, not dropped.
  it("splits by component with two models loaded", () => {
    const segs = loadedSegments(
      [{ id: "a", name: "alpha" }, { id: "b", name: "beta" }],
      { a: est(), b: est({ estVramGB: 5, kvReserveGB: 1, ctx: 8192 }) },
      15 * 1024,
    );
    expect(labels(segs)).toEqual([
      "Weights", "Draft", "Compute buffer", "KV cache", "Checkpoints", "Headroom",
    ]);
    expect(mb(segs, "KV cache")).toBeCloseTo(3 * 1024, 6); // 2 + 1
    // 10 + 5 estimated, 15 measured: nothing unaccounted, so no surplus.
    expect(mb(segs, "Overhead")).toBe(0);
    // The hover says whose VRAM shares the segment, with each model's own ctx.
    const kv = (segs ?? []).find((s) => s.label === "KV cache")!;
    expect(kv.detail).toContain("alpha + beta");
    expect(kv.detail).toContain("alpha ctx 32768");
    expect(kv.detail).toContain("beta ctx 8192");
  });

  it("charges the unexplained remainder to Overhead", () => {
    const segs = loadedSegments([{ id: "a" }], { a: est() }, 10.5 * 1024);
    expect(mb(segs, "Overhead")).toBeCloseTo(0.5 * 1024, 6);
    expect(mb(segs, "Weights")).toBeCloseTo(6 * 1024, 6);
  });

  it("scales components down when the measurement is under the estimate", () => {
    const segs = loadedSegments([{ id: "a" }], { a: est() }, 5 * 1024);
    expect(mb(segs, "Weights")).toBeCloseTo(3 * 1024, 6); // half of 6
    expect(mb(segs, "Overhead")).toBe(0);
  });

  // A model mid-load has no estimate yet: the caller needs the null so it can
  // paint the flat slice rather than a split that leaves that model's VRAM out.
  it("gives up when any loaded model has no estimate", () => {
    expect(loadedSegments([{ id: "a" }, { id: "b" }], { a: est() }, 15 * 1024)).toBeNull();
    expect(loadedSegments([], {}, 0)).toBeNull();
  });
});
