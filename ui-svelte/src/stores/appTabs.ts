// The app window's tab strip.
//
// Browser-shaped, with one difference: the DASHBOARD IS NOT A TAB. It is what
// the window is, and the wordmark on the title bar is always the way back to it
// (activeTabId "" means the dashboard is showing). Tabs are the things opened on
// top of it -- today the playground, on its own port, in a frame.
//
// Shell side only. The frames' half of the wire lives in lib/embed.ts.
import { get, writable } from "svelte/store";
import { embedURL } from "../lib/embed";

export interface AppTab {
  id: string;
  /** Frame src, already carrying the tab id and the shell origin. */
  url: string;
  /** Where "open in browser" sends it: the same page, minus the embed params. */
  externalURL: string;
  /** What the strip shows. The frame renames it as the open thread changes. */
  label: string;
  /** Frame is generating -- the strip's bolt. */
  busy: boolean;
}

export const appTabs = writable<AppTab[]>([]);

/** "" = the dashboard. Any other value is a tab id in `appTabs`. */
export const activeTabId = writable<string>("");

// Monotonic within a window session. Deliberately not random: lib/tabScope keys
// each tab's open-thread pointers off this id, so a stable counter means the
// first tab of the next session reopens on the thread the first tab had.
let seq = 0;

export function openTab(baseURL: string, label = "Playground"): string {
  const id = `t${++seq}`;
  const tab: AppTab = {
    id,
    url: embedURL(baseURL, id, window.location.origin),
    externalURL: baseURL,
    label,
    busy: false,
  };
  appTabs.update((t) => [...t, tab]);
  activeTabId.set(id);
  return id;
}

export function focusTab(id: string): void {
  activeTabId.set(id);
}

/** Back to the dashboard without closing anything -- the wordmark's action. */
export function showDashboard(): void {
  activeTabId.set("");
}

export function closeTab(id: string): void {
  const list = get(appTabs);
  const i = list.findIndex((t) => t.id === id);
  if (i < 0) return;
  const next = list.filter((t) => t.id !== id);
  appTabs.set(next);
  // Closing the tab you are looking at falls to its right-hand neighbour, then
  // its left, then the dashboard -- the order every browser uses.
  if (get(activeTabId) === id) {
    activeTabId.set(next[i]?.id ?? next[i - 1]?.id ?? "");
  }
}

/** Applied from the frame's postMessage; ignored for a tab that just closed. */
export function setTabState(id: string, label: string, busy: boolean): void {
  appTabs.update((list) => list.map((t) => (t.id === id ? { ...t, label: label || t.label, busy } : t)));
}
