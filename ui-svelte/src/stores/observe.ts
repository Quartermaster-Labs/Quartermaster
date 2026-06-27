import { persistentStore } from "./persistent";

// Unified Observe screen: Activity + Logs + Performance share one route, one tab
// selection, and one time window. The window is consumed by Activity (row
// filtering) and Performance (chart cutoff); Logs ignore it.

export type ObserveTab = "activity" | "logs" | "performance" | "kvcache";

export const observeTab = persistentStore<ObserveTab>("observe-tab", "activity");

// ms = 0 means "all" (no cutoff).
export const OBSERVE_WINDOWS = [
  { label: "5 min", ms: 5 * 60 * 1000 },
  { label: "15 min", ms: 15 * 60 * 1000 },
  { label: "1 hr", ms: 60 * 60 * 1000 },
  { label: "All", ms: 0 },
] as const;

export const observeWindowIdx = persistentStore<number>("observe-window", 0);
