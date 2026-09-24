import { describe, it, expect } from "vitest";
import { FireField, paletteRamp, DARK_PALETTE, type FireInput } from "./fireField";

// Seeded, so a failure reproduces instead of flaking.
function mulberry32(seed: number): () => number {
  return () => {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const input = (p: Partial<FireInput>): FireInput => ({ mode: "idle", progress: -1, tps: 0, tokens: 0, ...p });

function run(f: FireField, inp: FireInput, steps: number, dt = 1 / 32): void {
  for (let i = 0; i < steps; i++) f.step(inp, dt);
}

function meanHeat(f: FireField): number {
  let s = 0;
  for (let r = 0; r < f.rows; r++) for (let c = 0; c < f.cols; c++) s += f.at(c, r);
  return s / (f.rows * f.cols);
}

describe("FireField", () => {
  it("keeps every cell in [0, 1] under the hottest input", () => {
    const f = new FireField(mulberry32(1));
    f.resize(60, 12);
    run(f, input({ mode: "generating", tps: 500, tokens: 0 }), 200);
    for (const h of f.heat) {
      expect(h).toBeGreaterThanOrEqual(0);
      expect(h).toBeLessThanOrEqual(1);
    }
  });

  it("burns while generating and dies down to embers at idle", () => {
    const f = new FireField(mulberry32(2));
    f.resize(60, 12);
    run(f, input({ mode: "generating", tps: 60 }), 120);
    const lit = meanHeat(f);
    run(f, input({ mode: "idle" }), 400);
    const out = meanHeat(f);
    expect(lit).toBeGreaterThan(0.15);
    expect(out).toBeLessThan(lit / 5);
  });

  it("lights only the loaded share of the base while loading", () => {
    const f = new FireField(mulberry32(3));
    f.resize(100, 10);
    run(f, input({ mode: "loading", progress: 0.3 }), 150);
    const bottom = f.rows - 1;
    const left = Array.from({ length: 25 }, (_, c) => f.at(c, bottom)).reduce((a, b) => a + b) / 25;
    const right = Array.from({ length: 25 }, (_, c) => f.at(75 + c, bottom)).reduce((a, b) => a + b) / 25;
    expect(left).toBeGreaterThan(0.2);
    expect(right).toBe(0);
  });

  it("spawns sparks from token deltas and drops the backlog on a new request", () => {
    const f = new FireField(mulberry32(4));
    f.resize(60, 12);
    f.step(input({ mode: "generating", tps: 40, tokens: 0 }), 1 / 32);
    expect(f.sparks.length).toBe(0);
    f.step(input({ mode: "generating", tps: 40, tokens: 3 }), 1 / 32);
    expect(f.sparks.length).toBeGreaterThan(0);
    expect(f.sparks.length).toBeLessThanOrEqual(3);

    // A big batch, then a new request (count goes backwards) before it drains:
    // the old request's backlog must not keep spraying.
    f.step(input({ mode: "generating", tps: 40, tokens: 400 }), 1 / 32);
    f.step(input({ mode: "generating", tps: 40, tokens: 2 }), 1 / 32);
    run(f, input({ mode: "generating", tps: 40, tokens: 2 }), 64);
    expect(f.sparks.length).toBe(0);
  });

  it("emits no sparks when they are switched off (reduced motion)", () => {
    const f = new FireField(mulberry32(5));
    f.resize(60, 12);
    for (let t = 0; t <= 200; t += 10) f.step(input({ mode: "generating", tps: 40, tokens: t }), 1 / 6, false);
    expect(f.sparks.length).toBe(0);
  });

  it("caps live sparks", () => {
    const f = new FireField(mulberry32(6));
    f.resize(60, 12);
    for (let t = 0; t < 400; t++) f.step(input({ mode: "generating", tps: 80, tokens: t * 50 }), 1 / 32);
    expect(f.sparks.length).toBeLessThanOrEqual(160);
  });
});

describe("paletteRamp", () => {
  it("has one colour per heat level", () => {
    expect(paletteRamp(DARK_PALETTE)).toHaveLength(48);
  });
});
