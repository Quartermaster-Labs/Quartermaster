// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { get } from "svelte/store";
import {
  uiScale,
  clampScale,
  setScale,
  nudgeScale,
  resetScale,
  initUIScale,
  MIN_SCALE,
  MAX_SCALE,
} from "./uiScale";

const scaleVar = () =>
  document.documentElement.style.getPropertyValue("--qm-scale");

describe("clampScale", () => {
  it("holds the range at both ends", () => {
    expect(clampScale(99)).toBe(MAX_SCALE);
    expect(clampScale(0.01)).toBe(MIN_SCALE);
  });

  it("rounds to one decimal so steps do not accumulate float dust", () => {
    // 1 + 0.1 + 0.1 in binary floating point is 1.2000000000000002.
    expect(clampScale(1.2000000000000002)).toBe(1.2);
  });

  it("falls back to 1 for any non-finite value, not to a maximally zoomed UI", () => {
    // A corrupt localStorage entry should leave the app usable. Clamping
    // Infinity to MAX_SCALE would technically be in range but hands the user a
    // 200% interface they did not ask for and may struggle to navigate back.
    expect(clampScale(NaN)).toBe(1);
    expect(clampScale(Infinity)).toBe(1);
    expect(clampScale(-Infinity)).toBe(1);
  });
});

describe("initUIScale", () => {
  let teardown = () => {};

  beforeEach(() => {
    resetScale();
    teardown = initUIScale();
  });
  afterEach(() => {
    teardown();
    resetScale();
  });

  it("writes the custom property immediately, before any change", () => {
    expect(scaleVar()).toBe("1");
  });

  it("tracks the store", () => {
    setScale(1.3);
    expect(scaleVar()).toBe("1.3");
  });

  it("steps with Ctrl+= and Ctrl+-", () => {
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "=", ctrlKey: true }),
    );
    expect(get(uiScale)).toBe(1.1);
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "-", ctrlKey: true }),
    );
    expect(get(uiScale)).toBe(1);
  });

  it("treats Ctrl+0 as back to 100%", () => {
    setScale(1.5);
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "0", ctrlKey: true }),
    );
    expect(get(uiScale)).toBe(1);
  });

  it("ignores the same keys without Ctrl, so typing a minus is safe", () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "-" }));
    expect(get(uiScale)).toBe(1);
  });

  it("ignores Ctrl+Alt combinations, which belong to the OS", () => {
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "=", ctrlKey: true, altKey: true }),
    );
    expect(get(uiScale)).toBe(1);
  });

  it("stops stepping at the ends instead of running away", () => {
    setScale(MAX_SCALE);
    nudgeScale(1);
    expect(get(uiScale)).toBe(MAX_SCALE);
    setScale(MIN_SCALE);
    nudgeScale(-1);
    expect(get(uiScale)).toBe(MIN_SCALE);
  });

  it("detaches its listener on teardown", () => {
    teardown();
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "=", ctrlKey: true }),
    );
    expect(get(uiScale)).toBe(1);
    teardown = () => {};
  });
});
