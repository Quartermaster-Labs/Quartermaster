// Embedded mode: the playground running INSIDE an app-window tab.
//
// The app window is ONE WebView2 pointed at the dashboard's origin, and the
// playground is a separate app on a separate port -- so a tab is a cross-origin
// <iframe>, not a route.
//
// The frame was forced by state, not taste. Pointing the single webview at the
// playground instead would tear down its JS context, and a turn in flight is a
// streaming fetch owned by that context: switching tabs would abort whatever was
// generating. Frames stay mounted, so a background tab keeps streaming.
//
// Cross-origin then means the shell can read NOTHING out of the frame -- not
// document.title, not a store. So everything the tab strip shows (the label, the
// generating bolt) and everything the frame needs from the window (open this
// link in the real browser) crosses as a postMessage, and this module is both
// ends of that wire.

/** Tab id, unique per open tab. Also scopes the frame's localStorage. */
const TAB_PARAM = "qmtab";
/** The shell's origin, so the frame can address it without a wildcard. */
const SHELL_PARAM = "qmshell";

function param(name: string): string {
  if (typeof window === "undefined") return "";
  try {
    return new URLSearchParams(window.location.search).get(name) ?? "";
  } catch {
    return "";
  }
}

// Read once at module load, NOT on demand: PlaygroundApp's applyLaunchParams
// rewrites the URL through history.replaceState and drops the query string, so
// a later read comes back empty and the tab goes mute halfway through startup.
export const embedTabId = param(TAB_PARAM);
const shellOrigin = param(SHELL_PARAM);

/** True when this document is a tab inside the app window. */
export const isEmbedded = embedTabId !== "";

/** The URL the shell points a tab's frame at. */
export function embedURL(base: string, tabId: string, shell: string): string {
  const u = new URL(base);
  u.searchParams.set(TAB_PARAM, tabId);
  u.searchParams.set(SHELL_PARAM, shell);
  return u.href;
}

/** What the frame tells the shell. `tab` is the frame's own id. */
export type TabMessage =
  | { type: "qm-tab-state"; tab: string; label: string; busy: boolean }
  | { type: "qm-tab-external"; tab: string; url: string };

/**
 * Posts one message up to the shell.
 *
 * Addressed to the shell's exact origin rather than "*": the label is the user's
 * chat title, and a wildcard hands it to whatever page happens to be embedding
 * this one. No shell origin means no post -- a silent tab label is a papercut,
 * leaking chat titles to an unknown embedder is not.
 */
export function postToShell(msg: TabMessage): void {
  if (!isEmbedded || !shellOrigin) return;
  try {
    window.parent?.postMessage(msg, shellOrigin);
  } catch {
    // A dead or cross-process parent must never take the frame down with it.
  }
}
