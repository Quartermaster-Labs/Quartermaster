// Keeps the history lists identical across every frame/tab on this origin.
//
// Why this has to exist: each of the three history stores holds the WHOLE list
// and PUTs the WHOLE list (see stores/chatHistory.ts). Two documents open at
// once are therefore two owners of one blob, and the last one to flush wins --
// create a chat in tab A and tab B's next debounced PUT deletes it, taking its
// images with it, because the server GCs the media of a session that vanished.
// That was already true of two browser tabs; app-window tabs make it routine.
//
// The fix is convergence rather than locking: every document broadcasts its list
// on change, and every document merges what it receives, so the racing PUTs all
// carry the same content and it stops mattering who lands last.
//
// Merge rules, in order:
//   - the locally generating session always wins, whatever arrived. It is being
//     written a token at a time and a remote copy of it is stale by definition.
//   - otherwise the higher `updatedAt` wins, so an edit beats an older copy.
//   - ids the sender REMOVED are dropped, and are then REMEMBERED. A union alone
//     would resurrect every deleted chat the moment another tab spoke; naming
//     the deletion is still not enough on its own, because the other document's
//     next broadcast was composed before it heard and carries the corpse. So a
//     deletion leaves a short-lived tombstone, and no incoming list may bring
//     back an id that has one. Both sides converge within one broadcast.
import { get, type Writable } from "svelte/store";

interface Session {
  id: string;
  updatedAt: number;
}

interface Wire<T> {
  list: T[];
  removed: string[];
}

// Long enough to coalesce a stream (patchLast rewrites the list on every token,
// and structured-cloning the whole history per token would be absurd), short
// enough that switching tabs after sending a message finds it already there.
const BROADCAST_MS = 400;

// How long a deleted id stays un-resurrectable. It only has to outlive the
// in-flight broadcast of a document that had not heard yet -- a couple of
// rounds. Long enough to be certain, short enough that it cannot matter to a
// document that reloads (which refetches the list from the server anyway).
const TOMBSTONE_MS = 60_000;

/**
 * Wires one store to its counterparts in the other documents.
 *
 * `liveId` names the session this document is currently generating into, or
 * null -- passed as a getter because it is a store the caller owns and this
 * must read it at merge time, not at wiring time.
 *
 * No-op where BroadcastChannel is missing; a single-document setup then behaves
 * exactly as it did before.
 */
export function syncSessions<T extends Session>(
  name: string,
  store: Writable<T[]>,
  liveId: () => string | null,
): void {
  if (typeof BroadcastChannel === "undefined") return;

  let ch: BroadcastChannel;
  try {
    ch = new BroadcastChannel(`qm-sessions-${name}`);
  } catch {
    return;
  }

  // Guards the echo: applying a received list re-enters our own subscriber, and
  // rebroadcasting it there would make two documents talk forever.
  let applying = false;
  let prevIds = new Set((get(store) ?? []).map((s) => s.id));
  let timer: ReturnType<typeof setTimeout> | null = null;
  let pendingRemoved = new Set<string>();
  const tombs = new Map<string, number>();

  const bury = (id: string) => tombs.set(id, Date.now());
  const prune = () => {
    const cutoff = Date.now() - TOMBSTONE_MS;
    for (const [id, at] of tombs) if (at < cutoff) tombs.delete(id);
  };

  store.subscribe((list) => {
    if (applying || !Array.isArray(list)) return;
    const ids = new Set(list.map((s) => s.id));
    for (const id of prevIds) {
      if (ids.has(id)) continue;
      pendingRemoved.add(id);
      bury(id);
    }
    // An id that came back locally (an undo) is no longer a deletion -- send it
    // as present rather than as both at once, and lift its tombstone.
    for (const id of ids) {
      pendingRemoved.delete(id);
      tombs.delete(id);
    }
    prevIds = ids;

    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      const removed = [...pendingRemoved];
      pendingRemoved.clear();
      prune();
      try {
        // Read at send time, minus anything buried: between the change and this
        // flush another document may have broadcast a list still holding a
        // session we deleted, and merging it back in would make us re-announce
        // the corpse alongside its own death notice.
        const list = (get(store) ?? []).filter((s) => !tombs.has(s.id));
        ch.postMessage({ list, removed } satisfies Wire<T>);
      } catch {
        // A list holding something unclonable is a dropped sync, not a crash.
      }
    }, BROADCAST_MS);
  });

  ch.onmessage = (e: MessageEvent<Wire<T>>) => {
    const msg = e.data;
    if (!msg || !Array.isArray(msg.list)) return;
    const live = liveId();
    prune();
    // Their deletions become ours, so our own next broadcast cannot re-announce
    // what they just dropped.
    for (const id of msg.removed ?? []) if (id !== live) bury(id);

    const merged = new Map<string, T>();
    for (const s of get(store) ?? []) {
      if (tombs.has(s.id) && s.id !== live) continue;
      merged.set(s.id, s);
    }
    for (const s of msg.list ?? []) {
      if (!s?.id || s.id === live || tombs.has(s.id)) continue;
      const mine = merged.get(s.id);
      if (!mine || (s.updatedAt ?? 0) > (mine.updatedAt ?? 0)) merged.set(s.id, s);
    }

    const next = [...merged.values()];
    applying = true;
    try {
      store.set(next);
      prevIds = new Set(next.map((s) => s.id));
    } finally {
      applying = false;
    }
  };
}
