import { describe, it, expect } from "vitest";
import { splitSpans, spanWeight, findPauses, spanTimes, activeSpan } from "./takeTiming";

const pieces = (text: string) => splitSpans(text).map((s) => text.slice(s.start, s.end));

describe("splitSpans", () => {
  it("cuts at sentence ends and line breaks, dropping the whitespace between", () => {
    expect(pieces("Hej där. Hur mår du?  Bra!\nNy rad")).toEqual(["Hej där.", "Hur mår du?", "Bra!", "Ny rad"]);
  });

  it("keeps a closing quote with its sentence", () => {
    expect(pieces('He said "stop." Then left.')).toEqual(['He said "stop."', "Then left."]);
  });

  it("does not cut inside a decimal or an abbreviation without a space", () => {
    expect(pieces("It costs 3.50 kr today.")).toEqual(["It costs 3.50 kr today."]);
  });

  it("splits an over-long sentence at commas, without leaving stubs", () => {
    const long =
      "This is a very long sentence that goes on and on about nothing in particular, and then it keeps going with another clause that adds more words, until it finally runs out of things to say at the very end.";
    const out = pieces(long);
    expect(out.length).toBeGreaterThan(1);
    expect(out.join(" ")).toBe(long);
    for (const p of out) expect(p.length).toBeGreaterThanOrEqual(40);
  });

  it("returns nothing for blank text", () => {
    expect(splitSpans("  \n ")).toEqual([]);
  });
});

describe("spanWeight", () => {
  it("counts letters plus a punctuation pause", () => {
    expect(spanWeight("Hej.")).toBe(3 + 6);
    expect(spanWeight("Hej,")).toBe(3 + 3);
    expect(spanWeight("Hej")).toBe(3 + 2);
  });
});

// Synthetic clip: tone where `voiced` is true, silence elsewhere.
function clip(rate: number, segments: [number, boolean][]): Float32Array {
  const total = segments.reduce((a, [d]) => a + d, 0);
  const out = new Float32Array(Math.round(total * rate));
  let i = 0;
  for (const [d, voiced] of segments) {
    const n = Math.round(d * rate);
    for (let k = 0; k < n; k++, i++) out[i] = voiced ? 0.5 * Math.sin(k / 3) : 0;
  }
  return out;
}

describe("findPauses", () => {
  const rate = 8000;

  it("finds leading/trailing silence and the pauses in between", () => {
    const s = clip(rate, [[0.3, false], [1, true], [0.4, false], [1, true], [0.5, false]]);
    const v = findPauses(s, rate);
    expect(v.speechStart).toBeCloseTo(0.3, 1);
    expect(v.speechEnd).toBeCloseTo(2.7, 1);
    expect(v.pauses).toHaveLength(1);
    expect(v.pauses[0].start).toBeCloseTo(1.3, 1);
    expect(v.pauses[0].end).toBeCloseTo(1.7, 1);
  });

  it("ignores gaps shorter than a real pause", () => {
    const s = clip(rate, [[1, true], [0.05, false], [1, true]]);
    expect(findPauses(s, rate).pauses).toEqual([]);
  });

  it("treats an all-silent clip as no speech map", () => {
    const v = findPauses(new Float32Array(rate), rate);
    expect(v).toEqual({ speechStart: 0, speechEnd: 1, pauses: [] });
  });
});

describe("spanTimes", () => {
  it("spreads proportionally over the duration with no voice map", () => {
    expect(spanTimes([1, 1, 2], 8)).toEqual([0, 2, 4, 8]);
  });

  it("snaps a boundary to a nearby pause", () => {
    // Estimate says 2.0, the speaker actually paused around 2.4.
    const v = { speechStart: 0, speechEnd: 4, pauses: [{ start: 2.3, end: 2.5 }] };
    const t = spanTimes([1, 1], 4, v);
    expect(t[1]).toBeCloseTo(2.4);
  });

  it("re-estimates later spans from a snapped boundary instead of drifting", () => {
    // Three equal spans over 0..9; the first pause is late (3.8), so the second
    // estimate should be measured from 3.8, not from the naive 3.
    const v = { speechStart: 0, speechEnd: 9, pauses: [{ start: 3.7, end: 3.9 }] };
    const t = spanTimes([1, 1, 1], 9, v);
    expect(t[1]).toBeCloseTo(3.8);
    expect(t[2]).toBeCloseTo(3.8 + (9 - 3.8) / 2);
  });

  it("ignores a pause too far from the estimate", () => {
    const v = { speechStart: 0, speechEnd: 10, pauses: [{ start: 9, end: 9.2 }] };
    expect(spanTimes([1, 1], 10, v)[1]).toBeCloseTo(5);
  });

  it("uses each pause at most once and stays monotonic", () => {
    const v = { speechStart: 0, speechEnd: 3, pauses: [{ start: 1, end: 1.2 }] };
    const t = spanTimes([1, 1, 1], 3, v);
    for (let i = 1; i < t.length; i++) expect(t[i]).toBeGreaterThanOrEqual(t[i - 1]);
    expect(t[2]).not.toBeCloseTo(1.1);
  });
});

describe("activeSpan", () => {
  const times = [0.5, 2, 4, 6];
  it("clamps before and after speech", () => {
    expect(activeSpan(times, 0)).toBe(0);
    expect(activeSpan(times, 99)).toBe(2);
  });
  it("picks the span containing t", () => {
    expect(activeSpan(times, 1.9)).toBe(0);
    expect(activeSpan(times, 2)).toBe(1);
    expect(activeSpan(times, 5)).toBe(2);
  });
  it("is -1 with no spans", () => {
    expect(activeSpan([0], 1)).toBe(-1);
  });
});
