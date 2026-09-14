import { get, writable } from "svelte/store";
import { scopedKey } from "../lib/tabScope";
import { syncSessions } from "../lib/sessionSync";

// One turn in a video thread. Deliberately the same shape as the image tab's
// Turn (stores/imageHistory.ts) with `videos` in place of `images`: a clip is
// stored exactly like a picture, as a data: URL that the server's extractMedia
// splits out into media/video/<hash>.webm on the first PUT, so the JSON blob
// stays small. jobId is kept so a reload can tell a finished turn from one that
// was abandoned mid-render.
export type Turn = {
  prompt: string;
  refs: string[];
  videos: string[];
  error?: string;
  secs?: number;
  model?: string;
  jobId?: string;
  frames?: number;
  fps?: number;
};

export interface VideoSession {
  id: string;
  title: string;
  turns: Turn[];
  updatedAt: number;
  titled?: boolean;
}

// All saved video threads + which one is open. Server-backed per playground
// user, stored exactly like image sessions (see stores/imageHistory.ts).
export const videoSessions = writable<VideoSession[]>([]);

const LAST_ACTIVE_KEY = scopedKey("playground-active-video-chat");
let lastActive = "";
try {
  lastActive = localStorage.getItem(LAST_ACTIVE_KEY) ?? "";
} catch {
  // ignore (private mode / storage disabled)
}
export const activeVideoChatId = writable<string>(lastActive);
activeVideoChatId.subscribe((id) => {
  try {
    localStorage.setItem(LAST_ACTIVE_KEY, id);
  } catch {
    // ignore
  }
});

// Id of the thread currently generating (null = idle). One at a time.
export const generatingVideoChatId = writable<string | null>(null);

let synced = false;

export async function loadVideoChats(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/videochats");
    const arr = r.ok ? await r.json() : [];
    videoSessions.set(Array.isArray(arr) ? arr : []);
  } catch {
    videoSessions.set([]);
  }
  synced = true;
}

export function clearVideoChats(): void {
  synced = false;
  videoSessions.set([]);
  activeVideoChatId.set("");
}

// Debounced push of the whole list to the server (client owns the list).
let timer: ReturnType<typeof setTimeout> | null = null;
videoSessions.subscribe((sessions) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/videochats", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sessions),
    }).catch(() => {});
  }, 800);
});

export function newVideoChatId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

export function deriveVideoTitle(turns: Turn[]): string {
  const first = turns.find((t) => t.prompt.trim());
  return first ? first.prompt.trim().slice(0, 48) : "New video";
}

// Same whole-list-owner convergence the other tabs need: see lib/sessionSync.ts.
syncSessions("videos", videoSessions, () => get(generatingVideoChatId));
