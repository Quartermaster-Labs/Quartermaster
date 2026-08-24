// Per-tab localStorage keys.
//
// Every playground tab is a frame on the SAME origin, so they share one
// localStorage. The three "which thread is open" pointers are the keys that
// cannot be shared: two tabs would each write their own choice and yank the
// other onto it, which is the opposite of what a tab means. Suffixing them with
// the tab id gives each tab its own pointer while everything else -- prefs,
// caches -- stays deliberately shared.
//
// Ids repeat across window sessions (the shell counts up from zero each time),
// which is a feature: reopening the first tab returns to the thread it had.
import { embedTabId } from "./embed";

export function scopedKey(base: string): string {
  return embedTabId ? `${base}::${embedTabId}` : base;
}
