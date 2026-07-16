import { describe, it, expect } from "vitest";
import { lumaStats, applyLumaMatch } from "./colorMatch";

// Build an RGBA buffer from [r,g,b] triples.
function rgba(px: number[][]): number[] {
  const out: number[] = [];
  for (const [r, g, b] of px) out.push(r, g, b, 255);
  return out;
}

describe("colorMatch (luminance-only)", () => {
  it("matches luma mean + std to the reference", () => {
    const src = rgba([[0, 0, 0], [85, 85, 85], [170, 170, 170], [255, 255, 255]]);
    const ref = rgba([[90, 90, 90], [100, 100, 100], [100, 100, 100], [110, 110, 110]]);
    const refStats = lumaStats(ref);
    applyLumaMatch(src, lumaStats(src), refStats);

    const after = lumaStats(src);
    expect(after.mean).toBeCloseTo(refStats.mean, 1);
    expect(after.std).toBeCloseTo(refStats.std, 1);
  });

  it("does NOT force the reference's hue (a grey ref can't desaturate a red pixel)", () => {
    // One saturated red pixel; grey reference. Hue = channel spread must survive.
    const src = rgba([[200, 40, 40]]);
    const ref = rgba([[128, 128, 128]]);
    const before = src[0] - src[1]; // R - G, the redness
    applyLumaMatch(src, lumaStats(src), lumaStats(ref));
    const after = src[0] - src[1];
    // Equal offset per channel preserves the R-G gap exactly (barring clamping).
    expect(after).toBeCloseTo(before, 5);
  });

  it("matchContrast=false shifts mean only, leaves contrast (std) alone", () => {
    const src = rgba([[0, 0, 0], [85, 85, 85], [170, 170, 170], [255, 255, 255]]);
    const ref = rgba([[90, 90, 90], [100, 100, 100], [100, 100, 100], [110, 110, 110]]);
    const srcStd = lumaStats(src).std;
    const refStats = lumaStats(ref);
    applyLumaMatch(src, lumaStats(src), refStats, false);

    const after = lumaStats(src);
    expect(after.mean).toBeCloseTo(refStats.mean, 1); // brightness matched
    expect(after.std).toBeCloseTo(srcStd, 1); // contrast unchanged (not ref's)
  });

  it("clamps to 0..255", () => {
    const src = rgba([[0, 0, 0], [128, 128, 128], [255, 255, 255]]);
    applyLumaMatch(src, lumaStats(src), { mean: 128, std: 500 });
    for (const v of src) {
      expect(v).toBeGreaterThanOrEqual(0);
      expect(v).toBeLessThanOrEqual(255);
    }
  });
});
