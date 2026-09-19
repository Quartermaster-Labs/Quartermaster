// Should a scrolling list still follow its newest content?
//
// Pulled out of ChatInterface because the rule is subtler than it looks and got
// it wrong once: the obvious version, "following means the view is at the
// bottom", recomputed from distance alone, silently stopped following whenever
// a chat answer drew a diagram.

/** The slice of a scroller this decision needs. */
export interface ScrollMetrics {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
}

/**
 * How far from the bottom still counts as the bottom.
 *
 * Absorbs sub-pixel rounding and nothing else. A wider band (this was 40px)
 * read as a rubber band: the user scrolled up a little, was still inside the
 * band, and the next streamed token snapped them hard to the end.
 */
export const AT_BOTTOM_SLOP = 8;

/**
 * The new "the user has scrolled away" state, given the previous one and where
 * the list sat at the previous scroll event.
 *
 * Distance to the bottom can only ever RESUME following, never stop it. A
 * scroll event is delivered a frame after the scroll, so content that lands in
 * that gap is already counted in `scrollHeight`: a diagram finishing its draw
 * below a pinned view leaves the position untouched but puts a diagram's height
 * between it and the bottom. Reading that as "the user scrolled up" is what
 * abandoned autoscroll for the rest of an answer once a picture appeared.
 *
 * Only a move UP stops the follow, which content changes cannot fake. Growth
 * below cannot lower `scrollTop`, and a shrink can only clamp it to the new
 * bottom, which the at-bottom check forgives first.
 */
export function nextScrolledUp(
  prev: boolean,
  lastScrollTop: number,
  m: ScrollMetrics,
): boolean {
  if (m.scrollHeight - m.scrollTop - m.clientHeight <= AT_BOTTOM_SLOP)
    return false;
  if (m.scrollTop < lastScrollTop) return true;
  return prev;
}
