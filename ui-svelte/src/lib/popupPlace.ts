import { cssZoom } from "./uiZoom";

/**
 * Where a popup list should sit relative to the control that opened it.
 *
 * All values are LOCAL (post-zoom) pixels, except `viewportH`, which rides
 * along in the same units so a list that flipped upward can anchor with
 * `bottom` without the caller having to convert anything itself.
 */
export interface PopupPos {
  left: number;
  top: number;
  width: number;
  maxHeight: number;
  above: boolean;
  viewportH: number;
}

const GAP = 4;
const EDGE = 8;

/**
 * Measure `anchor` and decide where its popup goes.
 *
 * Shared by Select and Combobox so the two cannot drift apart: they are the
 * same popup with a different trigger, and a list that flips at a different
 * threshold or clamps to a different screen edge reads as a different widget.
 *
 * The popup is position:fixed rather than absolute so it escapes a modal body's
 * overflow clipping, but it still lives inside the component's own DOM: a
 * showModal() <dialog> paints in the browser's top layer, so a popup portalled
 * to <body> would be painted UNDER the dialog it belongs to.
 */
export function placePopup(anchor: HTMLElement): PopupPos {
  const r = anchor.getBoundingClientRect();
  // Rect and viewport are visual pixels; the list's own left/top/width are
  // local ones. Divide by the interface zoom or the list drifts off its
  // anchor - see lib/uiZoom.ts.
  const z = cssZoom(anchor);
  const below = window.innerHeight - r.bottom - GAP - EDGE;
  const above = r.top - GAP - EDGE;
  // Flip up only when the gap below is genuinely too small AND above is
  // roomier - a list that jumps sides on every few pixels of scroll is worse
  // than one that scrolls internally.
  const flip = below < 160 && above > below;
  return {
    left: Math.max(EDGE, Math.min(r.left, window.innerWidth - r.width - EDGE)) / z,
    top: (flip ? r.top - GAP : r.bottom + GAP) / z,
    width: r.width / z,
    maxHeight: Math.max(120, Math.min(280, flip ? above : below)) / z,
    above: flip,
    viewportH: window.innerHeight / z,
  };
}

/** The inline style string that puts a popup list where `placePopup` said. */
export function popupStyle(pos: PopupPos): string {
  const vertical = pos.above ? `bottom:${pos.viewportH - pos.top}px` : `top:${pos.top}px`;
  return `left:${pos.left}px; width:${pos.width}px; max-height:${pos.maxHeight}px; ${vertical}`;
}
