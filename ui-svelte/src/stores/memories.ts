import { writable } from "svelte/store";
import type { MemoryEntry } from "../lib/memoryTools";

// The logged-in user's assistant memories. NOT a userPref blob: the model writes
// this list too (memory_save / memory_delete, server-side), so the server owns it
// and every mutation is a per-entry request. A debounced whole-blob PUT would
// revert whatever the model saved while this tab was open.
export const memories = writable<MemoryEntry[]>([]);

export async function loadMemories(): Promise<void> {
  try {
    const r = await fetch("/api/memories");
    const arr = r.ok ? await r.json() : [];
    memories.set(Array.isArray(arr) ? arr : []);
  } catch {
    memories.set([]);
  }
}

export function clearMemories(): void {
  memories.set([]);
}

// saveMemory upserts one entry (omit id to create) and folds the server's copy
// back in — the server assigns the id and timestamps, so the response is the
// truth, not the object we sent.
export async function saveMemory(entry: { id?: string; text: string; tags?: string[] }): Promise<void> {
  const r = await fetch("/api/memories", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(entry),
  });
  if (!r.ok) throw new Error((await r.text()).trim() || "could not save memory");
  const saved: MemoryEntry = await r.json();
  memories.update((list) => {
    const i = list.findIndex((m) => m.id === saved.id);
    if (i < 0) return [saved, ...list];
    const next = [...list];
    next[i] = saved;
    return next;
  });
}

export async function deleteMemory(id: string): Promise<void> {
  const r = await fetch(`/api/memories/${encodeURIComponent(id)}`, { method: "DELETE" });
  // 404 = already gone, which is the state the caller wanted; drop it locally.
  if (!r.ok && r.status !== 404) throw new Error((await r.text()).trim() || "could not delete memory");
  memories.update((list) => list.filter((m) => m.id !== id));
}
