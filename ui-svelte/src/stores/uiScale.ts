import { get } from "svelte/store";
import { persistentStore } from "./persistent";

/**
 * UI scale — how large the whole interface is drawn.
 *
 * The app window (`quartermaster -app`) has no browser chrome, so it has no
 * zoom control of its own: WebView2's built-in Ctrl+Plus does not reach the
 * page, and go-webview2 does not expose the controller's ZoomFactor. This is
 * the replacement, and it works in a browser tab too.
 *
 * The value drives `--qm-scale`, which index.css feeds to `zoom` on :root.
 * Everything else follows from that one property — see the CSS for why `zoom`
 * and not a transform, and for the viewport-unit correction it forces.
 */
export const MIN_SCALE = 0.7;
export const MAX_SCALE = 2;
export const SCALE_STEP = 0.1;

export const uiScale = persistentStore<number>("uiScale", 1);

/** Rounds to one decimal so stepping never accumulates float dust (1.0999…). */
export function clampScale(v: number): number {
  if (!Number.isFinite(v)) return 1;
  return Math.round(Math.min(MAX_SCALE, Math.max(MIN_SCALE, v)) * 10) / 10;
}

export function setScale(v: number) {
  uiScale.set(clampScale(v));
}

export function nudgeScale(steps: number) {
  setScale(get(uiScale) + steps * SCALE_STEP);
}

export function resetScale() {
  setScale(1);
}

/**
 * Applies the scale and keeps the keyboard shortcuts live.
 *
 * Called from main.ts rather than a component so the very first paint is
 * already at the right size; a scale applied after mount makes the whole app
 * visibly jump. Returns a teardown for tests.
 */
export function initUIScale(): () => void {
  const stop = uiScale.subscribe((v) => {
    // Written even for 1 so a stale inline value from a previous session cannot
    // outlive a reset.
    document.documentElement.style.setProperty(
      "--qm-scale",
      String(clampScale(v)),
    );
  });

  const onKey = (e: KeyboardEvent) => {
    if (!e.ctrlKey || e.altKey) return;
    // Ctrl+Plus arrives as "=" on most layouts and "+" when shifted. Ctrl+0 is
    // the browser convention for "back to 100%", so it is the one people try.
    if (e.key === "0") {
      e.preventDefault();
      resetScale();
      return;
    }
    const dir = e.key === "-" ? -1 : e.key === "=" || e.key === "+" ? 1 : 0;
    if (dir === 0) return;
    e.preventDefault();
    nudgeScale(dir);
  };

  window.addEventListener("keydown", onKey);

  return () => {
    stop();
    window.removeEventListener("keydown", onKey);
  };
}
