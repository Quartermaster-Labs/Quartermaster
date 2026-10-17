import { get, writable } from "svelte/store";
import { scopedKey } from "../lib/tabScope";
import { syncSessions } from "../lib/sessionSync";

// What one take in a music thread was asked to do. "generate" is text to music;
// the three edits rework an existing clip (ACE-Step only); "separate" splits a
// clip into stems (HTDemucs).
export type MusicTask = "generate" | "repaint" | "complete" | "cover" | "separate";

export type Stem = { name: string; audio: string };

// One take. Every audio field is a /api/media ref, never an inline data: URL:
// the client uploads each result once (POST /api/media) and the thread only ever
// carries refs, so the debounced whole-list PUT stays small no matter how many
// songs a thread holds. See lib/musicApi.ts uploadMedia.
//
// `seed` is the one the server ECHOED, not the one sent, so a random-seed take
// can still be replayed exactly. `source` is the clip an edit or separation
// read; `sourceLabel` names it for the card ("Take 3", or the uploaded file).
export type Turn = {
  task: MusicTask;
  model: string;
  prompt: string;
  lyrics?: string;
  score?: string;
  duration?: number;
  seed?: number;
  steps?: number;
  guidance?: number;
  options?: Record<string, string | number>;
  source?: string;
  sourceLabel?: string;
  // Repaint window in seconds; end -1 = to the end of the source.
  start?: number;
  end?: number;
  repaintMode?: string;
  // Cover: how much of the source survives (audio_cover_strength).
  strength?: number;
  audio?: string;
  stems?: Stem[];
  error?: string;
  secs?: number;
};

export interface MusicSession {
  id: string;
  title: string;
  turns: Turn[];
  updatedAt: number;
  titled?: boolean;
}

// All saved music threads + which one is open. Server-backed per playground
// user (GET/PUT /api/musicchats), hydrated by loadMusicChats() before the shell
// mounts; changes push back debounced. Same shape as stores/speechHistory.ts.
export const musicSessions = writable<MusicSession[]>([]);

// Scoped per tab, see speechHistory.ts.
const LAST_ACTIVE_KEY = scopedKey("playground-active-music-chat");
let lastActive = "";
try {
  lastActive = localStorage.getItem(LAST_ACTIVE_KEY) ?? "";
} catch {
  // ignore (private mode / storage disabled)
}
export const activeMusicChatId = writable<string>(lastActive);
activeMusicChatId.subscribe((id) => {
  try {
    localStorage.setItem(LAST_ACTIVE_KEY, id);
  } catch {
    // ignore
  }
});

// Id of the thread currently generating (null = idle). One at a time.
export const generatingMusicChatId = writable<string | null>(null);

let synced = false;

export async function loadMusicChats(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/musicchats");
    const arr = r.ok ? await r.json() : [];
    musicSessions.set(Array.isArray(arr) ? arr : []);
  } catch {
    musicSessions.set([]);
  }
  synced = true;
}

export function clearMusicChats(): void {
  synced = false;
  musicSessions.set([]);
  activeMusicChatId.set("");
}

let timer: ReturnType<typeof setTimeout> | null = null;
musicSessions.subscribe((sessions) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/musicchats", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sessions),
    }).catch(() => {});
  }, 800);
});

export function newMusicChatId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

// First prompt, trimmed. A separation has no prompt, so it falls back to what
// it split.
export function deriveMusicTitle(turns: Turn[]): string {
  const first = turns.find((t) => t.prompt.trim());
  if (first) return first.prompt.trim().slice(0, 48);
  const sep = turns.find((t) => t.task === "separate");
  return sep ? `Stems: ${sep.sourceLabel || "clip"}`.slice(0, 48) : "New music";
}

syncSessions("music", musicSessions, () => get(generatingMusicChatId));
