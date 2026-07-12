import { writable } from "svelte/store";

// One turn in a speech thread: the text spoken, the voice used, and the
// generated audio (a base64 data URL so it survives a reload / server round-trip,
// unlike an ephemeral object URL). Mirrors the image tab's turn model.
// `voice` is the display label (speaker name, preset name, or "Default").
// `instructions` is the voice_design style description actually sent (empty for
// base/custom_voice); kept so regenerate reuses the same design.
export type Turn = { text: string; voice: string; instructions?: string; audio?: string; error?: string; secs?: number };

export interface SpeechSession {
  id: string;
  title: string;
  turns: Turn[];
  updatedAt: number;
  // Set once a title has been derived from the first line; until then it's the
  // first-text heuristic and is recomputed on every save.
  titled?: boolean;
}

// All saved speech threads + which one is open. Server-backed per playground user,
// stored exactly like chat/image sessions. Hydrated by loadSpeechChats() before the
// shell mounts; changes push back debounced.
export const speechSessions = writable<SpeechSession[]>([]);

const LAST_ACTIVE_KEY = "playground-active-speech-chat";
let lastActive = "";
try {
  lastActive = localStorage.getItem(LAST_ACTIVE_KEY) ?? "";
} catch {
  // ignore (private mode / storage disabled)
}
export const activeSpeechChatId = writable<string>(lastActive);
activeSpeechChatId.subscribe((id) => {
  try {
    localStorage.setItem(LAST_ACTIVE_KEY, id);
  } catch {
    // ignore
  }
});

// Id of the thread currently generating (null = idle). One at a time.
export const generatingSpeechChatId = writable<string | null>(null);

// synced gates the auto-save subscriber so it can't overwrite the server before
// the initial load finishes.
let synced = false;

export async function loadSpeechChats(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/speechchats");
    const arr = r.ok ? await r.json() : [];
    speechSessions.set(Array.isArray(arr) ? arr : []);
  } catch {
    speechSessions.set([]);
  }
  synced = true;
}

export function clearSpeechChats(): void {
  synced = false;
  speechSessions.set([]);
  activeSpeechChatId.set("");
}

// Debounced push of the whole list to the server (client owns the list).
let timer: ReturnType<typeof setTimeout> | null = null;
speechSessions.subscribe((sessions) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/speechchats", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sessions),
    }).catch(() => {});
  }, 800);
});

export function newSpeechChatId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

// First spoken line, trimmed — good enough as a title. "New speech" until then.
export function deriveSpeechTitle(turns: Turn[]): string {
  const first = turns.find((t) => t.text.trim());
  return first ? first.text.trim().slice(0, 48) : "New speech";
}
