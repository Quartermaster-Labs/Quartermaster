// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { cssZoom, toLocalPx } from "./uiZoom";

describe("cssZoom", () => {
  it("reads the browser-resolved zoom when it is exposed", () => {
    const el = { currentCSSZoom: 1.25 } as unknown as Element;
    expect(cssZoom(el)).toBe(1.25);
  });

  it("falls back to 1 rather than 0 for a missing or nonsensical value", () => {
    // A zero would turn every division into Infinity and park popups off-screen.
    const zeroed = Object.assign(document.createElement("div"), { currentCSSZoom: 0 });
    expect(cssZoom(zeroed)).toBe(1);
    expect(cssZoom(null)).toBe(1);
    expect(cssZoom(document.createElement("div"))).toBe(1);
  });

  it("converts visual pixels to the local ones a style property is read in", () => {
    const el = { currentCSSZoom: 2 } as unknown as Element;
    expect(toLocalPx(400, el)).toBe(200);
    expect(toLocalPx(400, null)).toBe(400);
  });
});
