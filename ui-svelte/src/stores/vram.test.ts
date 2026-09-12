import { describe, it, expect } from "vitest";
import { loadedSegments, systemFloorMb, systemDetail, llamaSized } from "./vram";
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

describe("systemFloorMb", () => {
  const base = {
    usedMb: 20 * 1024,
    strayForeignMb: 0,
    guardForeignMb: null as number | null,
    serverIdleMb: 0,
    browserBaselineMb: null as number | null,
    estTotalMb: null as number | null,
  };

  // The bug: the idle floor is sampled only while nothing is loaded, so an app
  // that claims VRAM after a model went resident (a game, Blender, Unity) never
  // moved it. Its 6 GB was counted as model usage and surfaced as "Overhead".
  it("prefers the live per-process reading over a stale idle floor", () => {
    const { mb, source } = systemFloorMb({
      ...base,
      guardForeignMb: 7 * 1024, // desktop 0.9 GB at sample time + 6 GB since
      serverIdleMb: 0.9 * 1024,
    });
    expect(mb).toBe(7 * 1024);
    expect(source).toBe("live");
    expect(systemDetail(source)).toContain("live");
  });

  // The stray llama-server already has its own red segment; counting it in
  // System as well would draw the same VRAM twice.
  it("subtracts the stray-inference slice from the live reading", () => {
    const { mb } = systemFloorMb({
      ...base,
      usedMb: 18 * 1024, // stray already carved out by the caller
      guardForeignMb: 5 * 1024,
      strayForeignMb: 2 * 1024,
    });
    expect(mb).toBe(3 * 1024);
  });

  it("falls back to the server idle floor, then the tab baseline, then the estimate", () => {
    expect(systemFloorMb({ ...base, serverIdleMb: 2048, browserBaselineMb: 4096 })).toEqual({
      mb: 2048,
      source: "idle",
    });
    expect(systemFloorMb({ ...base, browserBaselineMb: 4096 })).toEqual({
      mb: 4096,
      source: "baseline",
    });
    expect(systemFloorMb({ ...base, estTotalMb: 12 * 1024 })).toEqual({
      mb: 8 * 1024,
      source: "estimate",
    });
    // Nothing to go on: attribute it all to System rather than invent a model slice.
    expect(systemFloorMb(base)).toEqual({ mb: 20 * 1024, source: "unknown" });
  });

  it("clamps to the used total and to zero", () => {
    expect(systemFloorMb({ ...base, guardForeignMb: 99 * 1024 }).mb).toBe(20 * 1024);
    expect(systemFloorMb({ ...base, guardForeignMb: 100, strayForeignMb: 900 }).mb).toBe(0);
  });
});

describe("llamaSized", () => {
  it("estimates llama models, with or without a capabilities block", () => {
    expect(llamaSized({})).toBe(true);
    expect(llamaSized({ capabilities: { vision: true, function_calling: true } })).toBe(true);
    expect(llamaSized({ capabilities: { embeddings: true } })).toBe(true);
  });

  it("skips every non-llama backend the flags can name", () => {
    // One flag each is enough: these are the marks the server puts on a backend
    // that is not llama-server, and none of them has a llama load plan.
    expect(llamaSized({ capabilities: { image_generation: true } })).toBe(false);
    expect(llamaSized({ capabilities: { image_to_image: true } })).toBe(false);
    expect(llamaSized({ capabilities: { image_to_3d: true, vision: true } })).toBe(false);
    expect(llamaSized({ capabilities: { segmentation: true } })).toBe(false);
    expect(llamaSized({ capabilities: { audio_speech: true } })).toBe(false);
    expect(llamaSized({ capabilities: { audio_transcriptions: true } })).toBe(false);
  });
});
