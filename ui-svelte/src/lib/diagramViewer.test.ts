// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import {
  FIT_MAX_HEIGHT,
  FIT_MIN_SCALE,
  fitDiagramSvg,
  fitWidthCss,
  openDiagramViewer,
  svgNaturalSize,
} from "./diagramViewer";

function svgEl(attrs: Record<string, string>): SVGSVGElement {
  const el = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  return el;
}

describe("svgNaturalSize", () => {
  it("reads the viewBox first", () => {
    expect(
      svgNaturalSize(svgEl({ viewBox: "-8 -8 250 600", width: "100%" })),
    ).toEqual({ w: 250, h: 600 });
  });
  it("falls back to numeric width/height", () => {
    expect(svgNaturalSize(svgEl({ width: "120", height: "80" }))).toEqual({
      w: 120,
      h: 80,
    });
  });
  it("gives up on a percentage-only size", () => {
    expect(svgNaturalSize(svgEl({ width: "100%" }))).toBeNull();
  });
});

describe("fitWidthCss", () => {
  it("never scales a small diagram up past its natural width", () => {
    expect(fitWidthCss(250, 200)).toBe("min(100%, 250px)");
  });
  it("shrinks a tall diagram toward the height budget", () => {
    // 300x600 natural -> 480 tall at 240 wide (scale 0.8, above the floor).
    expect(fitWidthCss(300, 600)).toBe(
      `min(100%, ${(FIT_MAX_HEIGHT * 300) / 600}px)`,
    );
  });
  it("stops shrinking at the legibility floor", () => {
    // 300x2000 would need scale 0.24 to fit; the floor holds it at 0.6.
    expect(fitWidthCss(300, 2000)).toBe(
      `min(100%, ${FIT_MIN_SCALE * 300}px)`,
    );
  });
});

describe("fitDiagramSvg", () => {
  it("replaces mermaid's stretch-to-container sizing", () => {
    const el = svgEl({
      viewBox: "0 0 250 400",
      width: "100%",
      height: "400",
      style: "max-width: 250px;",
    });
    fitDiagramSvg(el);
    expect(el.hasAttribute("height")).toBe(false);
    expect(el.style.maxWidth).toBe("");
    expect(el.style.width).toBe("min(100%, 250px)");
    expect(el.style.height).toBe("auto");
  });
});

describe("openDiagramViewer", () => {
  it("moves the SVG out and puts the same node back, attributes intact", () => {
    const host = document.createElement("div");
    const before = document.createElement("span");
    const after = document.createElement("span");
    const el = svgEl({ viewBox: "0 0 250 400", width: "100%" });
    el.style.width = "min(100%, 250px)";
    host.append(before, el, after);
    document.body.appendChild(host);
    const style = el.getAttribute("style");

    openDiagramViewer(el);
    const dlg = document.querySelector(".qm-dv");
    expect(dlg).not.toBeNull();
    expect(dlg!.contains(el)).toBe(true);
    // A placeholder holds its spot, so the message does not reflow under it.
    expect(host.children).toHaveLength(3);
    expect(host.contains(el)).toBe(false);

    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    expect(document.querySelector(".qm-dv")).toBeNull();
    expect(host.children[1]).toBe(el);
    expect(el.getAttribute("style")).toBe(style);
    expect(el.getAttribute("width")).toBe("100%");
    expect(el.hasAttribute("height")).toBe(false);
    host.remove();
  });
});
