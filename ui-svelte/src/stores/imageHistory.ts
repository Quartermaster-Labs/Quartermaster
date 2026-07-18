import { writable } from "svelte/store";

// One turn in an image thread: a prompt, the source/reference images fed into it,
// and the generated result. Mirrors the chat tab's message model but for images.
export type Turn = { prompt: string; refs: string[]; images: string[]; maskPreview?: string; error?: string; secs?: number; model?: string };

export interface ImageSession {
  id: string;
  title: string;
  turns: Turn[];
  updatedAt: number;
  // Set once a title has been derived from the first prompt; until then it's the
  // first-prompt heuristic and is recomputed on every save.
  titled?: boolean;
}

// All saved image threads + which one is open. Server-backed per playground user,
// stored exactly like chat sessions (see stores/chatHistory.ts). Hydrated by
// loadImageChats() before the shell mounts; changes push back debounced.
export const imageSessions = writable<ImageSession[]>([]);

const LAST_ACTIVE_KEY = "playground-active-image-chat";
let lastActive = "";
try {
  lastActive = localStorage.getItem(LAST_ACTIVE_KEY) ?? "";
} catch {
  // ignore (private mode / storage disabled)
}
export const activeImageChatId = writable<string>(lastActive);
activeImageChatId.subscribe((id) => {
  try {
    localStorage.setItem(LAST_ACTIVE_KEY, id);
  } catch {
    // ignore
  }
});

// Id of the thread currently generating (null = idle). One at a time.
export const generatingImageChatId = writable<string | null>(null);

// synced gates the auto-save subscriber so it can't overwrite the server before
// the initial load finishes.
let synced = false;

export async function loadImageChats(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/imagechats");
    const arr = r.ok ? await r.json() : [];
    imageSessions.set(Array.isArray(arr) ? arr : []);
  } catch {
    imageSessions.set([]);
  }
  synced = true;
}

export function clearImageChats(): void {
  synced = false;
  imageSessions.set([]);
  activeImageChatId.set("");
}

// Debounced push of the whole list to the server (client owns the list).
let timer: ReturnType<typeof setTimeout> | null = null;
imageSessions.subscribe((sessions) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/imagechats", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sessions),
    }).catch(() => {});
  }, 800);
});

export function newImageChatId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

// First prompt, trimmed — good enough as a title. "New image" until then.
export function deriveImageTitle(turns: Turn[]): string {
  const first = turns.find((t) => t.prompt.trim());
  return first ? first.prompt.trim().slice(0, 48) : "New image";
}
