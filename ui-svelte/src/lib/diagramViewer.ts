// Sizing for rendered diagrams, and the full-screen pan/zoom viewer behind
// their Expand button.
//
// Inline, a diagram is shown at its NATURAL size (what mermaid laid it out at,
// with labels at the font size it measured) and only ever scaled DOWN: to the
// message width, and for a tall diagram towards a height budget so one answer
// is not a screen of arrows. Never up: stretching a narrow top-down flowchart to
// the full message width is exactly what made them comically large. Whatever
// the inline fit gives up in legibility, the viewer gives back.
import { cssZoom } from "./uiZoom";

/** Inline height budget, in local (pre-zoom) px. */
export const FIT_MAX_HEIGHT = 480;
/**
 * The height budget may shrink a diagram to this fraction of natural size and
 * no further. Below it, labels drop under ~10px, and a tall diagram that stays
 * readable is better than a short one nobody can read.
 */
export const FIT_MIN_SCALE = 0.6;

/** Natural size of an SVG from its viewBox, else its width/height attributes. */
export function svgNaturalSize(
  svg: SVGSVGElement,
): { w: number; h: number } | null {
  const vb = (svg.getAttribute("viewBox") ?? "")
    .trim()
    .split(/[\s,]+/)
    .map(Number);
  if (vb.length === 4 && vb[2] > 0 && vb[3] > 0) return { w: vb[2], h: vb[3] };
  const w = parseFloat(svg.getAttribute("width") ?? "");
  const h = parseFloat(svg.getAttribute("height") ?? "");
  if (w > 0 && h > 0) return { w, h };
  return null;
}

/**
 * The CSS width that fits a `w` x `h` diagram inline: the smallest of the
 * container, the natural width, and the width at which the height budget is
 * met (floored at FIT_MIN_SCALE). Height then follows from `aspect-ratio`.
 */
export function fitWidthCss(w: number, h: number): string {
  const byHeight = Math.max((FIT_MAX_HEIGHT * w) / h, FIT_MIN_SCALE * w);
  const cap = Math.min(w, byHeight);
  return `min(100%, ${Math.round(cap)}px)`;
}

/** Size a rendered mermaid SVG for inline display (see the file comment). */
export function fitDiagramSvg(svg: SVGSVGElement): void {
  const size = svgNaturalSize(svg);
  svg.removeAttribute("height");
  if (!size) {
    svg.style.maxWidth = "100%";
    return;
  }
  // Mermaid wrote `width="100%"` and its own max-width; both are replaced
  // wholesale so nothing it set can override the fit.
  svg.setAttribute("width", "100%");
  svg.style.maxWidth = "";
  svg.style.width = fitWidthCss(size.w, size.h);
  svg.style.height = "auto";
  svg.style.aspectRatio = `${size.w} / ${size.h}`;
}

const ICONS = {
  minus: `<path d="M5 12h14"/>`,
  plus: `<path d="M5 12h14"/><path d="M12 5v14"/>`,
  fit: `<path d="M8 3H5a2 2 0 0 0-2 2v3"/><path d="M21 8V5a2 2 0 0 0-2-2h-3"/><path d="M3 16v3a2 2 0 0 0 2 2h3"/><path d="M16 21h3a2 2 0 0 0 2-2v-3"/>`,
  close: `<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`,
  expand: `<path d="M15 3h6v6"/><path d="M9 21H3v-6"/><path d="M21 3l-7 7"/><path d="M3 21l7-7"/>`,
};

export function icon(name: keyof typeof ICONS, size = 16): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${ICONS[name]}</svg>`;
}

function button(name: keyof typeof ICONS, title: string): HTMLButtonElement {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "qm-dv-btn";
  b.title = title;
  b.setAttribute("aria-label", title);
  b.innerHTML = icon(name);
  return b;
}

const MIN_SCALE = 0.1;
const MAX_SCALE = 8;

/**
 * Open `svg` full screen with wheel/button zoom and drag pan.
 *
 * The SVG is MOVED into the viewer, not cloned, and a same-size placeholder
 * holds its spot in the message: mermaid's arrowheads are `url(#id)` markers
 * and its styles are `#id`-scoped, so a clone would put every id in the
 * document twice, and a marker resolves to whichever copy comes first. Close
 * puts the very same element back.
 */
export function openDiagramViewer(svg: SVGSVGElement): void {
  const size = svgNaturalSize(svg) ?? (() => {
    const r = svg.getBoundingClientRect();
    const z = cssZoom(svg);
    return { w: r.width / z || 400, h: r.height / z || 300 };
  })();

  const home = svg.parentNode;
  const next = svg.nextSibling;
  const placeholder = document.createElement("div");
  {
    const r = svg.getBoundingClientRect();
    const z = cssZoom(svg);
    placeholder.style.cssText = `width:${r.width / z}px;height:${r.height / z}px;max-width:100%`;
  }
  const savedStyle = svg.getAttribute("style");
  const savedWidth = svg.getAttribute("width");
  const savedHeight = svg.getAttribute("height");
  const opener = document.activeElement as HTMLElement | null;

  const root = document.createElement("div");
  root.className = "qm-dv";
  root.setAttribute("role", "dialog");
  root.setAttribute("aria-modal", "true");
  root.setAttribute("aria-label", "Diagram viewer");

  const bar = document.createElement("div");
  bar.className = "qm-dv-bar";
  const out = button("minus", "Zoom out (-)");
  const pct = document.createElement("button");
  pct.type = "button";
  pct.className = "qm-dv-pct";
  pct.title = "Actual size (1)";
  const inn = button("plus", "Zoom in (+)");
  const fit = button("fit", "Fit to screen (0)");
  const close = button("close", "Close (Esc)");
  const spacer = document.createElement("div");
  spacer.style.flex = "1";
  const hint = document.createElement("span");
  hint.className = "qm-dv-hint";
  hint.textContent = "Scroll to zoom, drag to pan";
  bar.append(hint, spacer, out, pct, inn, fit, close);

  const stage = document.createElement("div");
  stage.className = "qm-dv-stage";
  const layer = document.createElement("div");
  layer.className = "qm-dv-layer";
  stage.appendChild(layer);
  const panel = document.createElement("div");
  panel.className = "qm-dv-panel";
  panel.append(bar, stage);
  root.appendChild(panel);

  home?.insertBefore(placeholder, svg);
  svg.removeAttribute("style");
  svg.setAttribute("width", String(size.w));
  svg.setAttribute("height", String(size.h));
  svg.style.display = "block";
  svg.style.maxWidth = "none";
  layer.appendChild(svg);
  document.body.appendChild(root);

  // All transform math is in the stage's LOCAL px: pointer and rect values are
  // visual px under the interface zoom, so each is divided by it (lib/uiZoom.ts).
  let s = 1;
  let tx = 0;
  let ty = 0;
  const zoomOf = () => cssZoom(stage);
  const stageSize = () => {
    const r = stage.getBoundingClientRect();
    const z = zoomOf();
    return { w: r.width / z, h: r.height / z, left: r.left, top: r.top, z };
  };
  const apply = () => {
    layer.style.transform = `translate(${tx}px, ${ty}px) scale(${s})`;
    pct.textContent = `${Math.round(s * 100)}%`;
  };
  const fitView = () => {
    const st = stageSize();
    const pad = 32;
    s = Math.min(
      (st.w - pad * 2) / size.w,
      (st.h - pad * 2) / size.h,
      // Small diagrams open bigger than inline, but not absurdly so.
      3,
    );
    s = Math.max(MIN_SCALE, s);
    tx = (st.w - size.w * s) / 2;
    ty = (st.h - size.h * s) / 2;
    apply();
  };
  // Zoom keeping the local stage point (px, py) fixed under the cursor.
  const zoomAt = (factor: number, px: number, py: number) => {
    const ns = Math.min(MAX_SCALE, Math.max(MIN_SCALE, s * factor));
    tx = px - (px - tx) * (ns / s);
    ty = py - (py - ty) * (ns / s);
    s = ns;
    apply();
  };
  const zoomCenter = (factor: number) => {
    const st = stageSize();
    zoomAt(factor, st.w / 2, st.h / 2);
  };
  const actualSize = () => {
    const st = stageSize();
    zoomAt(1 / s, st.w / 2, st.h / 2);
  };

  stage.addEventListener(
    "wheel",
    (e) => {
      e.preventDefault();
      const st = stageSize();
      // Trackpad pinch arrives as ctrl+wheel with small deltas; a mouse wheel
      // notch is ~100. exp() makes both feel proportional.
      const factor = Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015));
      zoomAt(factor, (e.clientX - st.left) / st.z, (e.clientY - st.top) / st.z);
    },
    { passive: false },
  );

  let drag: { x: number; y: number; id: number } | null = null;
  stage.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    drag = { x: e.clientX, y: e.clientY, id: e.pointerId };
    stage.setPointerCapture(e.pointerId);
    stage.classList.add("dragging");
  });
  stage.addEventListener("pointermove", (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    const z = zoomOf();
    tx += (e.clientX - drag.x) / z;
    ty += (e.clientY - drag.y) / z;
    drag.x = e.clientX;
    drag.y = e.clientY;
    apply();
  });
  const endDrag = () => {
    drag = null;
    stage.classList.remove("dragging");
  };
  stage.addEventListener("pointerup", endDrag);
  stage.addEventListener("pointercancel", endDrag);
  stage.addEventListener("dblclick", (e) => {
    const st = stageSize();
    zoomAt(2, (e.clientX - st.left) / st.z, (e.clientY - st.top) / st.z);
  });

  out.addEventListener("click", () => zoomCenter(1 / 1.25));
  inn.addEventListener("click", () => zoomCenter(1.25));
  fit.addEventListener("click", fitView);
  pct.addEventListener("click", actualSize);

  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      shut();
    } else if (e.key === "+" || e.key === "=") zoomCenter(1.25);
    else if (e.key === "-" || e.key === "_") zoomCenter(1 / 1.25);
    else if (e.key === "0") fitView();
    else if (e.key === "1") actualSize();
  };
  // Capture phase, so Esc closes the viewer and not whatever modal or composer
  // handler would otherwise see it first.
  document.addEventListener("keydown", onKey, true);
  const onResize = () => fitView();
  window.addEventListener("resize", onResize);

  function shut() {
    document.removeEventListener("keydown", onKey, true);
    window.removeEventListener("resize", onResize);
    // Back where it came from, with exactly the attributes it left with.
    if (savedStyle === null) svg.removeAttribute("style");
    else svg.setAttribute("style", savedStyle);
    if (savedWidth === null) svg.removeAttribute("width");
    else svg.setAttribute("width", savedWidth);
    if (savedHeight === null) svg.removeAttribute("height");
    else svg.setAttribute("height", savedHeight);
    if (placeholder.isConnected) placeholder.replaceWith(svg);
    else if (home) home.insertBefore(svg, next);
    root.remove();
    opener?.focus?.();
  }
  close.addEventListener("click", shut);
  // A click on the dimmed margin around the panel closes, like any lightbox.
  root.addEventListener("click", (e) => {
    if (e.target === root) shut();
  });

  fitView();
  close.focus();
}
