// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { batchEnd, planSweep, pathAt, BurnTracker, BURN_MS, MAX_TRACE_MS, type WordBox } from "./burnReveal";

describe("batchEnd", () => {
  it("releases everything received up to the last complete word", () => {
    expect(batchEnd(0, "one two thr", false)).toBe(8);
  });

  it("holds back a word that is still streaming", () => {
    expect(batchEnd(0, "Hello wor", false)).toBe(6);
    expect(batchEnd(6, "Hello wor", false)).toBe(6);
  });

  it("releases the held word once the stream ends", () => {
    expect(batchEnd(6, "Hello wor", true)).toBe(9);
  });

  it("does not hold a long unbroken run forever", () => {
    const url = "https://example.com/" + "a".repeat(60);
    expect(batchEnd(0, url, false)).toBe(url.length);
  });
});

// One 10px word every 12px on a line; lines 20px apart.
const line = (n: number, y: number, x = 0): WordBox[] => Array.from({ length: n }, (_, i) => ({ x0: x + i * 12, x1: x + i * 12 + 10, y }));

describe("planSweep", () => {
  it("traces a short batch word by word at the fixed speed", () => {
    const words = line(3, 5);
    const plan = planSweep(words, { x: 0, y: 5 }, 1000, 1000);
    // 1 px/ms: each word lights as the mark reaches its middle.
    expect(plan.ignite).toEqual([1005, 1017, 1029]);
    expect(plan.end).toBe(1034);
    expect(pathAt(plan.path, 1017)).toEqual({ x: 17, y: 5 });
  });

  it("hops to the next line instead of rolling back across it", () => {
    const words = [...line(2, 5, 100), ...line(1, 25)];
    const plan = planSweep(words, { x: 100, y: 5 }, 0, 1000);
    // The third word starts a new line: no time spent getting back to x=0.
    expect(plan.ignite[2]).toBe(plan.ignite[1] + (122 - 117) + 5);
    expect(pathAt(plan.path, plan.end)).toEqual({ x: 10, y: 25 });
  });

  it("speeds up rather than fall behind on a long batch, still line by line", () => {
    const words = [...line(40, 5), ...line(40, 25), ...line(10, 45)];
    const plan = planSweep(words, null, 0, 600);
    // ~1100 px of text would take ~1.8 s at 600 px/s: capped instead.
    expect(plan.end).toBeCloseTo(MAX_TRACE_MS);
    // Lines still light in order: the second starts after the first ends.
    expect(plan.ignite[40]).toBeGreaterThan(plan.ignite[39]);
    expect(plan.ignite[80]).toBeGreaterThan(plan.ignite[79]);
    expect(pathAt(plan.path, plan.end)).toEqual({ x: 118, y: 45 });
  });

  it("is a no-op for an empty batch", () => {
    expect(planSweep([], { x: 3, y: 4 }, 7)).toEqual({ ignite: [], path: [{ x: 3, y: 4, t: 7 }], end: 7 });
  });
});

describe("BurnTracker", () => {
  const html = (root: HTMLElement, s: string) => (root.innerHTML = s);

  it("adopts existing text without burning it", () => {
    const root = document.createElement("div");
    html(root, "<p>already here</p>");
    const t = new BurnTracker();
    expect(t.update(root, 0, true)).toEqual([]);
    expect(root.querySelectorAll(".qm-burn").length).toBe(0);
  });

  it("wraps only the new text and returns its words", () => {
    const root = document.createElement("div");
    const t = new BurnTracker();
    html(root, "<p>one </p>");
    t.update(root, 0, true);
    html(root, "<p>one two three</p>");
    const spans = t.update(root, 100, true);
    expect(spans.map((s) => s.textContent)).toEqual(["two ", "three"]);
    expect(root.querySelectorAll(".qm-burn").length).toBe(2);
  });

  it("applies planned times, and keeps them on a rebuilt node", () => {
    const root = document.createElement("div");
    const t = new BurnTracker();
    html(root, "<p>a </p>");
    t.update(root, 0, true);
    html(root, "<p>a bc de</p>");
    const spans = t.update(root, 100, true);
    t.setTimes(spans, [100, 400], 100);
    expect(spans.map((s) => s.style.animationDelay)).toEqual(["0ms", "300ms"]);
    // {@html} re-renders the live block from scratch on the next batch.
    html(root, "<p>a bc de</p>");
    t.update(root, 250, true);
    const delays = [...root.querySelectorAll<HTMLElement>(".qm-burn")].map((s) => s.style.animationDelay);
    expect(delays).toEqual(["-150ms", "150ms"]);
  });

  it("stops wrapping once the last word has cooled", () => {
    const root = document.createElement("div");
    const t = new BurnTracker();
    html(root, "<p>x</p>");
    t.update(root, 0, true);
    html(root, "<p>x yz</p>");
    const spans = t.update(root, 10, true);
    t.setTimes(spans, [200], 10);
    html(root, "<p>x yz</p>");
    t.update(root, 200 + BURN_MS - 1, false);
    expect(t.active).toBe(true);
    html(root, "<p>x yz</p>");
    t.update(root, 200 + BURN_MS, false);
    expect(t.active).toBe(false);
    expect(root.querySelectorAll(".qm-burn").length).toBe(0);
  });

  it("settles to plain text once the reveal is over", () => {
    const root = document.createElement("div");
    const t = new BurnTracker();
    html(root, "<p>a </p>");
    t.update(root, 0, true);
    html(root, "<p>a bc <b>de</b></p>");
    t.setTimes(t.update(root, 10, true), [20, 30], 10);
    t.settle(root);
    expect(root.querySelectorAll(".qm-burn").length).toBe(0);
    expect(t.active).toBe(false);
    expect(root.innerHTML).toBe("<p>a bc <b>de</b></p>");
    expect(root.querySelector("p")!.childNodes.length).toBe(2);
  });

  it("leaves skipped subtrees and root-level whitespace alone", () => {
    const root = document.createElement("div");
    const t = new BurnTracker();
    t.update(root, 0, true);
    html(root, "<p>hi</p>\n<details><summary>thought</summary></details><span data-burn-skip>Loading</span>");
    t.update(root, 50, true);
    const burns = [...root.querySelectorAll(".qm-burn")].map((b) => b.textContent);
    expect(burns).toEqual(["hi"]);
  });
});
