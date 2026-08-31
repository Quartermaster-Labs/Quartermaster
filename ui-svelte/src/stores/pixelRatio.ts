import { readable } from "svelte/store";

/**
 * `window.devicePixelRatio`, kept live.
 *
 * Anything that sizes a canvas backing store has to multiply by this, and a
 * value read once at mount goes stale: the ratio moves when the browser's own
 * zoom changes (Ctrl+scroll), when the display scale changes under an open
 * window, and when the window is dragged to a monitor with a different one.
 * The DOM re-rasterizes itself for all three; a canvas keeps the bitmap it was
 * given, so it is the one thing on the page that goes soft until something
 * re-allocates it. This store is that something.
 *
 * There is no `devicepixelratiochange` event. The standard trick is a media
 * query pinned to the CURRENT ratio: it stops matching the moment the ratio
 * moves, which is the notification. The query has to be rebuilt around the new
 * value each time, so this re-subscribes on every change rather than listening
 * once.
 */
function currentRatio(): number {
  if (typeof window === "undefined") return 1;
  return window.devicePixelRatio || 1;
}

export const pixelRatio = readable<number>(currentRatio(), (set) => {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return;
  }

  let mq: MediaQueryList | null = null;

  const listen = () => {
    const dpr = currentRatio();
    set(dpr);
    mq?.removeEventListener("change", listen);
    mq = window.matchMedia(`(resolution: ${dpr}dppx)`);
    mq.addEventListener("change", listen);
  };

  listen();

  return () => {
    mq?.removeEventListener("change", listen);
    mq = null;
  };
});
