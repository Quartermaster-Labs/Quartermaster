// The bridge to the native window.
//
// internal/nativewin installs these functions on `window` before it navigates,
// so they are present from the first script the page runs. Both windows go
// through it -- the first-run wizard and the app window under `-app` -- which
// is why this lives in lib/ and not beside either app.
//
// They are absent in the browser, and that absence is the feature test: the
// page draws its own title bar and offers a folder picker only when there is a
// window underneath that can honour them. There is no build flag and no user
// agent sniffing; the same bundle serves both.
//
// Every call is fire-and-forget except pickFolder. A failed one must never take
// the page down with it: losing the ability to drag the window is a papercut,
// losing an install -- or a chat mid-generation -- is not.

import { isEmbedded, postToShell, embedTabId } from "./embed";

interface NativeWindow {
  qmDrag?: () => Promise<void>;
  qmMinimize?: () => Promise<void>;
  qmMaximize?: () => Promise<void>;
  qmClose?: () => Promise<void>;
  qmPickFolder?: (title: string, start: string) => Promise<string>;
  qmOpenExternal?: (url: string) => Promise<void>;
  qmCaptionColor?: (r: number, g: number, b: number) => Promise<void>;
  qmAppReady?: () => Promise<void>;
}

const w = (typeof window === "undefined" ? {} : window) as NativeWindow;

/**
 * True when this document IS the app window -- not merely inside it.
 *
 * The feature test alone is not enough any more. WebView2 runs its
 * document-creation scripts in every frame, so a tab's embedded playground sees
 * these bindings too, while `window.chrome.webview` (what they post through) is
 * exposed only to the top document -- so in a frame they exist and throw. The
 * embed check is therefore not a preference: without it the frame would draw a
 * second title bar whose every button silently fails.
 */
export const isNative = typeof w.qmDrag === "function" && !isEmbedded;

export function dragWindow(): void {
  void w.qmDrag?.().catch(() => {});
}

// A native drag eats the click that would have become a double-click, so the
// title bar has to count the presses itself.
//
// qmDrag ends in WM_NCLBUTTONDOWN/HTCAPTION, which puts Windows into its own
// modal move loop for the rest of the gesture: the webview never sees the
// mouseup, so it never synthesizes a `click`, so a plain `ondblclick` on the
// bar can never fire. Matching the OS rule here (two presses inside the
// double-click time, within a few pixels of each other on SCREEN -- client
// coordinates move with the window while it is being dragged) restores the
// verb without giving up drag-from-anywhere.
const DOUBLE_CLICK_MS = 500; // Windows' default GetDoubleClickTime
const DOUBLE_CLICK_SLOP = 4; // px, roughly SM_CXDOUBLECLK
let lastDownAt = 0;
let lastDownX = 0;
let lastDownY = 0;

/**
 * Title-bar mousedown: starts a window drag, or maximises on the second press.
 *
 * Use this INSTEAD of `dragWindow` + `ondblclick` -- a surviving `ondblclick`
 * would toggle a second time on the rare gesture where the click does land.
 */
export function titleBarMouseDown(e: MouseEvent): void {
  if (e.button !== 0) return;
  const near =
    Math.abs(e.screenX - lastDownX) <= DOUBLE_CLICK_SLOP &&
    Math.abs(e.screenY - lastDownY) <= DOUBLE_CLICK_SLOP;
  if (near && e.timeStamp - lastDownAt <= DOUBLE_CLICK_MS) {
    lastDownAt = 0; // a triple click must not toggle twice
    toggleMaximize();
    return;
  }
  lastDownAt = e.timeStamp;
  lastDownX = e.screenX;
  lastDownY = e.screenY;
  dragWindow();
}

export function minimizeWindow(): void {
  void w.qmMinimize?.().catch(() => {});
}

export function toggleMaximize(): void {
  void w.qmMaximize?.().catch(() => {});
}

export function closeWindow(): void {
  void w.qmClose?.().catch(() => {});
}

/**
 * Parses a computed CSS colour into [r,g,b], or null if it is not one this can
 * read. getComputedStyle always resolves backgrounds to `rgb()`/`rgba()`, so
 * that is the only form worth handling -- a null means "leave the frame alone",
 * never a guessed colour.
 */
export function parseRgb(css: string): [number, number, number] | null {
  const m = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/.exec(css.trim());
  if (!m) return null;
  const v = [m[1], m[2], m[3]].map((n) => Math.round(Number(n)));
  if (v.some((n) => !Number.isFinite(n))) return null;
  return [v[0], v[1], v[2]];
}

/**
 * Dyes the window frame to match the title bar. It shows in two pixels -- the
 * top corners, where Windows 11's rounded mask leaves a stub of frame the page
 * cannot cover -- so this is what stops them reading as white dots. See
 * SetCaptionColor in internal/nativewin.
 */
export function setCaptionColor(css: string): void {
  const rgb = parseRgb(css);
  if (!rgb) return;
  void w.qmCaptionColor?.(rgb[0], rgb[1], rgb[2]).catch(() => {});
}

/**
 * Opens the native folder browser and resolves to the chosen path, or "" if the
 * user cancelled or there is no native window. Callers must treat "" as "leave
 * the current value alone" rather than as a new value.
 */
export async function pickFolder(
  title: string,
  start: string,
): Promise<string> {
  if (!w.qmPickFolder) return "";
  try {
    return await w.qmPickFolder(title, start);
  } catch {
    return "";
  }
}

/**
 * Opens a URL in the user's real browser instead of inside the app window.
 *
 * The window has no address bar, no back button and no tabs; a Hugging Face
 * page loaded into it is a dead end the user cannot navigate out of. Non-http
 * URLs are dropped on the Go side -- the binding is a shell execution path.
 */
export function openExternal(url: string): void {
  // A tab frame cannot reach the binding (chrome.webview is top-document only),
  // so it asks the shell to make the call on its behalf.
  if (isEmbedded) {
    postToShell({ type: "qm-tab-external", tab: embedTabId, url });
    return;
  }
  void w.qmOpenExternal?.(url).catch(() => {});
}

/**
 * Routes every outbound link to the real browser, and returns the teardown.
 *
 * Two escapes have to be closed, because WebView2 answers both by opening a
 * bare popup window with no chrome at all -- worse than a browser tab in every
 * way:
 *
 *   1. `<a target="_blank">`, caught in the CAPTURE phase so a component's own
 *      click handler cannot consume the event first. `data-qm-inapp` on the
 *      anchor opts a link back out -- see the check below.
 *   2. `window.open(...)`, monkey-patched, because a script call never produces
 *      a click to intercept.
 *
 * Same-document links (`#/models`) are left alone: those are the hash router,
 * and handing them to a browser would open a second copy of the app.
 *
 * No-op outside the native window, so the browser keeps its ordinary tabs.
 */
export function installExternalLinkHandler(): () => void {
  // A tab frame needs this every bit as much as the window does: it has no
  // chrome either, and a Hugging Face page loaded into a 32px-capped frame is a
  // dead end with no way back. openExternal routes it up to the shell.
  if (!isNative && !isEmbedded) return () => {};

  const onClick = (e: MouseEvent) => {
    // Let modified clicks through untouched: ctrl/shift/middle-click mean
    // something specific to the user, and the browser we hand off to will
    // honour them better than we can.
    if (
      e.defaultPrevented ||
      e.button !== 0 ||
      e.ctrlKey ||
      e.shiftKey ||
      e.altKey ||
      e.metaKey
    )
      return;
    const a = (e.target as Element | null)?.closest?.("a");
    if (!a) return;

    const href = a.getAttribute("href") ?? "";
    if (!href || href.startsWith("#")) return;
    // A download attribute means "save this", not "leave the app", even on an
    // http(s) URL to another origin. Handing it to the system browser would ask
    // the user to download it twice, in the wrong application. WebView2 saves it
    // to the Downloads folder with its own progress flyout, which is what the
    // playground's image/audio/transcript save buttons already rely on.
    if (a.hasAttribute("download")) return;
    // The shell's own escape hatch. This handler runs in the capture phase
    // precisely so a component cannot consume the click first -- which also
    // means a component can never opt OUT by calling preventDefault in its own
    // onclick, because that runs later. A link the app window means to handle
    // itself (the sidebar's Playground entry, which opens an in-app tab rather
    // than leaving) says so declaratively instead. The href stays real so the
    // same markup is an ordinary link in a browser.
    if (a.hasAttribute("data-qm-inapp")) return;

    // Resolve against the document so a relative href is compared as the
    // absolute URL it will actually navigate to.
    let url: URL;
    try {
      url = new URL(href, location.href);
    } catch {
      return;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") return;
    // Leaving is the exception, not the rule. Anything on another origin is
    // somebody else's site; anything on ours is the app navigating itself and
    // stays put -- unless it explicitly asks for a new window AND points at a
    // different page, which is how the playground port and the raw log views
    // are linked.
    const leaves =
      url.origin !== location.origin ||
      (a.target === "_blank" && url.pathname !== location.pathname);
    if (!leaves) return;

    e.preventDefault();
    openExternal(url.href);
  };

  document.addEventListener("click", onClick, true);

  const realOpen = window.open;
  window.open = ((url?: string | URL) => {
    if (url) openExternal(String(url));
    return null;
  }) as typeof window.open;

  return () => {
    document.removeEventListener("click", onClick, true);
    window.open = realOpen;
  };
}

/**
 * Tells the app window that the bundle got as far as mounting.
 *
 * The window watches for this and reloads itself if it never arrives, because
 * WebView2 offers no reload button and no retry: a first navigation that fails
 * -- or a page that loads and then throws before it renders -- is a white
 * window until the process is restarted. That is a real bug users hit on the
 * first launch after an install, and this one call is the whole detector.
 *
 * Sent from every document that has the binding, frames included: a frame
 * reporting that it mounted is still true, and the window only ever asks
 * whether SOMETHING came up. Fire and forget, like every other call here.
 */
export function signalAppReady(): void {
  try {
    void w.qmAppReady?.();
  } catch {
    /* no window underneath, or the binding is gone -- nothing to do */
  }
}
