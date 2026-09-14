import { get, writable } from "svelte/store";
import { scopedKey } from "../lib/tabScope";
import { syncSessions } from "../lib/sessionSync";

// One turn in a 3D thread: the source image the tab was given plus every mesh
// it produced from it. Same shape as the video tab's Turn
// (stores/videoHistory.ts) with a single `image` (a data: URL or /api/media/
// path) and `meshes` in place of `videos`: a GLB is stored exactly like a
// picture, as a data: URL that the server's extractMedia splits out into
// media/model/<hash>.glb on the first PUT, so the JSON blob stays small.
export type Turn = {
  image: string;
  meshes: string[];
  error?: string;
  secs?: number;
  model?: string;
  bytes?: number;
};

export interface ThreeDSession {
  id: string;
  title: string;
  turns: Turn[];
  updatedAt: number;
  titled?: boolean;
}

// All saved 3D threads + which one is open. Server-backed per playground
// user, stored exactly like image sessions (see stores/imageHistory.ts).
export const threeDSessions = writable<ThreeDSession[]>([]);

const LAST_ACTIVE_KEY = scopedKey("playground-active-3d-chat");
let lastActive = "";
try {
  lastActive = localStorage.getItem(LAST_ACTIVE_KEY) ?? "";
} catch {
  // ignore (private mode / storage disabled)
}
export const activeThreeDChatId = writable<string>(lastActive);
activeThreeDChatId.subscribe((id) => {
  try {
    localStorage.setItem(LAST_ACTIVE_KEY, id);
  } catch {
    // ignore
  }
});

// Id of the thread currently generating (null = idle). One at a time.
export const generatingThreeDChatId = writable<string | null>(null);

let synced = false;

export async function loadThreeDChats(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/3dchats");
    const arr = r.ok ? await r.json() : [];
    threeDSessions.set(Array.isArray(arr) ? arr : []);
  } catch {
    threeDSessions.set([]);
  }
  synced = true;
}

export function clearThreeDChats(): void {
  synced = false;
  threeDSessions.set([]);
  activeThreeDChatId.set("");
}

// Debounced push of the whole list to the server (client owns the list).
let timer: ReturnType<typeof setTimeout> | null = null;
threeDSessions.subscribe((sessions) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/3dchats", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sessions),
    }).catch(() => {});
  }, 800);
});

export function newThreeDChatId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

export function deriveThreeDTitle(turns: Turn[]): string {
  void turns;
  // The 3D tab has no prompt, so a thread title cannot be derived from the
  // turn: it is either the user's own or this default.
  return "New mesh";
}

// Same whole-list-owner convergence the other tabs need: see lib/sessionSync.ts.
syncSessions("threed", threeDSessions, () => get(generatingThreeDChatId));
