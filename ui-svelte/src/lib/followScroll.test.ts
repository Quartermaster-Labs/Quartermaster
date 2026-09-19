import { describe, it, expect } from "vitest";
import { nextScrolledUp } from "./followScroll";

// A viewport 500 tall, pinned to the bottom of 2000 of content.
const pinned = { scrollTop: 1500, scrollHeight: 2000, clientHeight: 500 };

describe("nextScrolledUp", () => {
  it("keeps following when a diagram grows the content below a pinned view", () => {
    // The draw finished between the pin and the scroll event: same position,
    // 400px of new content underneath. This is the case that regressed.
    const grown = { ...pinned, scrollHeight: 2400 };
    expect(nextScrolledUp(false, pinned.scrollTop, grown)).toBe(false);
  });

  it("stops following when the user scrolls up", () => {
    const movedUp = { ...pinned, scrollTop: 1100 };
    expect(nextScrolledUp(false, pinned.scrollTop, movedUp)).toBe(true);
  });

  it("stays stopped while the user reads, even as content grows", () => {
    const readingWhileGrowing = {
      scrollTop: 1100,
      scrollHeight: 2400,
      clientHeight: 500,
    };
    expect(nextScrolledUp(true, 1100, readingWhileGrowing)).toBe(true);
  });

  it("follows again once the user returns to the bottom", () => {
    const backDown = { scrollTop: 1900, scrollHeight: 2400, clientHeight: 500 };
    expect(nextScrolledUp(true, 1100, backDown)).toBe(false);
  });

  it("forgives the clamp when content shrinks under a pinned view", () => {
    // A source block swapped for a shorter picture: the browser drops scrollTop
    // to the new bottom on its own. Downward-looking, but not the user.
    const shrunk = { scrollTop: 1200, scrollHeight: 1700, clientHeight: 500 };
    expect(nextScrolledUp(false, pinned.scrollTop, shrunk)).toBe(false);
  });

  it("treats sub-pixel rounding as the bottom", () => {
    expect(nextScrolledUp(false, 1500, { ...pinned, scrollTop: 1496 })).toBe(
      false,
    );
  });
});
