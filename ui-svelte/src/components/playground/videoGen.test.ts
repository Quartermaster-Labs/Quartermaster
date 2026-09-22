import { describe, it, expect } from "vitest";
import {
  FRAME_OPTIONS,
  H3_FRAME_OPTIONS,
  LTX_FRAME_OPTIONS,
  clipLabel,
  frameOptionsFor,
  fpsOptionsFor,
  isH3,
  isLtx,
  maxFramesFor,
  snapFrames,
  supportsFrameRefs,
  videoTokens,
} from "./videoGen";

// The contract these tests exist to defend is the one the source states: the
// picker must never offer a frame count the backend would silently change. That
// makes "every offered value is a fixed point of snapFrames" the load-bearing
// property, and it is checked for all three grids rather than at sample points,
// because an off-by-one in a grid shows up at one rung and nowhere else.
describe("frame grids are fixed points of snapFrames", () => {
  const families: [string, number[]][] = [
    ["ltx-2.5-22b-distilled-transformer-q4_k_m", LTX_FRAME_OPTIONS],
    ["minimax_h3_fl2va_pruned-q4_k_m", H3_FRAME_OPTIONS],
    ["wan2.2-t2v-a14b-q4_k_m", FRAME_OPTIONS],
  ];

  for (const [id, grid] of families) {
    it(`${id.split("-")[0]} offers only values the backend keeps`, () => {
      for (const n of grid) expect(snapFrames(n, id)).toBe(n);
    });

    it(`${id.split("-")[0]} offers nothing past its own ceiling`, () => {
      expect(Math.max(...grid)).toBeLessThanOrEqual(maxFramesFor(id));
    });
  }
});

describe("snapFrames", () => {
  const ltx = "ltx-2.5-22b";
  const h3 = "minimax_h3_fl2va";
  const wan = "wan2.2-t2v";

  // LTX is the one family that FLOORS. sd.cpp truncates an off-grid count for
  // it and aligns up for everyone else, so snapping the other way here would
  // label a clip with frames the file never had.
  it("floors onto the 8k+1 grid for LTX", () => {
    expect(snapFrames(100, ltx)).toBe(97);
    expect(snapFrames(104, ltx)).toBe(97);
    expect(snapFrames(105, ltx)).toBe(105);
  });

  it("ceils onto the 17k+5 grid for H3", () => {
    expect(snapFrames(25, h3)).toBe(39);
    expect(snapFrames(22, h3)).toBe(22);
  });

  it("rounds onto the 4n+1 grid for everyone else", () => {
    expect(snapFrames(50, wan)).toBe(49);
    expect(snapFrames(51, wan)).toBe(53);
  });

  // Clamping happens before snapping, so an over-large request lands ON the
  // ceiling rather than one rung below it or one rung past the table.
  it("clamps to the family ceiling without overshooting it", () => {
    expect(snapFrames(10_000, ltx)).toBe(481);
    expect(snapFrames(10_000, h3)).toBe(345);
    expect(snapFrames(10_000, wan)).toBe(241);
  });

  it("holds each family's floor for an absurdly small request", () => {
    expect(snapFrames(0, ltx)).toBe(9);
    expect(snapFrames(0, h3)).toBe(5);
  });
});

// This block used to assert a 153-frame ceiling as "not a taste call", derived
// from reading positional_embedding_max_pos[0]=20 as 20 latent frames. Both
// halves were wrong: max_pos is the DIVISOR in LTX's normalized-fractional rope
// (get_fractional_positions divides the index grid by it), so there is no table
// to index past, and the 20 pairs with an identical
// audio_positional_embedding_max_pos on latents that share no frame count, which
// only makes sense as seconds. LTX-2.5 is specified for 6-20 seconds.
//
// The real invariants are the grid and the specified duration, so that is what
// this checks now.
describe("the LTX ceiling is the specified 20-second duration", () => {
  const LTX_MAX_SECONDS = 20;
  const LTX_DEFAULT_FPS = 24;

  it("is 20 seconds expressed on the 8k+1 grid", () => {
    const max = maxFramesFor("ltx-2.5-22b");
    expect(max).toBe(LTX_MAX_SECONDS * LTX_DEFAULT_FPS + 1);
    expect((max - 1) % 8).toBe(0);
  });

  it("labels the round second marks at the family's default rate", () => {
    expect(clipLabel(121, 24)).toBe("5.0s \u00b7 121f");
    expect(clipLabel(241, 24)).toBe("10.0s \u00b7 241f");
    expect(clipLabel(361, 24)).toBe("15.0s \u00b7 361f");
    expect(clipLabel(481, 24)).toBe("20.0s \u00b7 481f");
  });

  it("offers every round second mark as a rung", () => {
    for (const f of [121, 241, 361, 481]) {
      expect(LTX_FRAME_OPTIONS).toContain(f);
    }
  });
});

describe("family detection", () => {
  it("separates the three families", () => {
    expect(isLtx("ltx-2.5-22b-distilled-transformer-q4_k_m")).toBe(true);
    expect(isH3("minimax_h3_fl2va_pruned-q4_k_m")).toBe(true);
    expect(isLtx("wan2.2-t2v-a14b")).toBe(false);
    expect(isH3("wan2.2-t2v-a14b")).toBe(false);
  });

  it("routes each family to its own grid", () => {
    expect(frameOptionsFor("ltx-2.5-22b")).toBe(LTX_FRAME_OPTIONS);
    expect(frameOptionsFor("minimax_h3")).toBe(H3_FRAME_OPTIONS);
    expect(frameOptionsFor("wan2.2-t2v")).toBe(FRAME_OPTIONS);
  });

  // H3's rate is overridden by sd.cpp's request handler, so offering the other
  // rows would be a lie rather than a limitation.
  it("offers H3 only the rate it will actually run at", () => {
    expect(fpsOptionsFor("minimax_h3")).toEqual([24]);
    expect(fpsOptionsFor("ltx-2.5-22b").length).toBeGreaterThan(1);
  });

  // LTX carries frame conditioning in every checkpoint instead of in a separate
  // i2v variant, so the family name is the capability here.
  it("finds frame conditioning in LTX without an i2v marker in the name", () => {
    expect(supportsFrameRefs("ltx-2.5-22b-distilled-transformer-q4_k_m")).toBe(true);
    expect(supportsFrameRefs("wan2.2-i2v-a14b")).toBe(true);
    expect(supportsFrameRefs("wan2.2-t2v-a14b")).toBe(false);
  });
});

// LTX compresses 32x spatially and 8x temporally with no patch embed on top,
// where the others go 16x and 4x. Charging LTX the others' rate would paint
// every usable setting orange, so the gap is asserted rather than left implicit.
describe("videoTokens", () => {
  it("prices LTX at a quarter the spatial and half the temporal rate", () => {
    const ltx = videoTokens(1280, 704, 121, "ltx-2.5-22b");
    const wan = videoTokens(1280, 704, 121, "wan2.2-t2v");
    expect(ltx).toBe(40 * 22 * 16);
    expect(wan).toBe(80 * 44 * 31);
    expect(wan / ltx).toBeGreaterThan(7);
  });
});
