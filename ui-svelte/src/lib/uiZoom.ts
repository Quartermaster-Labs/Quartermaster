// Interface-size (CSS `zoom`) correction for rect-anchored popups.
//
// index.css puts `zoom: var(--qm-scale)` on :root so the whole interface scales
// (see stores/uiScale.ts for why zoom and not a transform). That leaves two
// different pixel units in play:
//
//   - getBoundingClientRect(), window.innerWidth/innerHeight and MouseEvent
//     .clientX/Y report VISUAL pixels - they already include the zoom.
//   - A `left`/`top` you write into an element's style is in that element's own
//     LOCAL pixels, which the browser then multiplies by the inherited zoom.
//
// So `el.style.left = rect.left + "px"` lands at rect.left * zoom on screen:
// correct at 100%, and drifting further from the target the further it sits
// from the top-left corner at any other size. That is why tooltips looked
// untethered from what they described.
//
// Anything anchored to a rect must therefore divide by the zoom of the element
// it is positioning. Ratio math (clientX - r.left) / r.width is already
// zoom-safe - both operands are visual - and needs none of this.

/** Effective CSS zoom applied to `el`, i.e. visual pixels per local pixel. */
export function cssZoom(el: Element | null | undefined): number {
  if (!el) return 1;
  // Chromium exposes the resolved value directly; it is the product of every
  // `zoom` up the tree, which is what we want.
  const own = (el as Element & { currentCSSZoom?: number }).currentCSSZoom;
  if (typeof own === "number" && own > 0) return own;
  if (typeof getComputedStyle !== "function") return 1;
  let z = 1;
  for (let n: Element | null = el; n; n = n.parentElement) {
    const v = parseFloat(getComputedStyle(n).zoom as unknown as string);
    if (Number.isFinite(v) && v > 0) z *= v;
  }
  return z > 0 ? z : 1;
}

/** Viewport (visual) pixels -> local pixels for an element positioned inside `el`. */
export function toLocalPx(px: number, el: Element | null | undefined): number {
  return px / cssZoom(el);
}
