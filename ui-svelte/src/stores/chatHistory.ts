import { writable } from "svelte/store";
import type { ChatMessage } from "../lib/types";

export interface ChatSession {
  id: string;
  title: string;
  messages: ChatMessage[];
  updatedAt: number;
  // Auto-compact bookkeeping. The full message list is always kept on disk and
  // shown in the UI; these only change what is SENT to the model. `summary` is a
  // running recap of the first `compactedCount` messages, prepended as system
  // context, and those messages are not resent — only summary + the messages
  // from `compactedCount` onward go to the model, so it never overflows.
  summary?: string;
  compactedCount?: number;
  // Set once the model has named the chat (see generateTitle). Until then the
  // title is the first-message heuristic and is recomputed on every save.
  titled?: boolean;
}

// All saved conversations + which one is currently open. Server-backed, keyed by
// the logged-in playground user. Hydrated by loadChats() before the chat UI
// mounts; changes are pushed back to the server (debounced).
export const chatSessions = writable<ChatSession[]>([]);

// Which chat reopens with the tab. Persisted to localStorage (per browser) so a
// reload/reopen returns to the chat you were on instead of an arbitrary one.
const LAST_ACTIVE_KEY = "playground-active-chat";
let lastActive = "";
try {
  lastActive = localStorage.getItem(LAST_ACTIVE_KEY) ?? "";
} catch {
  // ignore (private mode / storage disabled)
}
export const activeChatId = writable<string>(lastActive);
activeChatId.subscribe((id) => {
  try {
    localStorage.setItem(LAST_ACTIVE_KEY, id);
  } catch {
    // ignore
  }
});

// Id of the session whose turn is currently streaming (null = idle). One turn at
// a time; set by ChatInterface so the rail can flag the generating row.
export const generatingChatId = writable<string | null>(null);

// synced gates the auto-save subscriber: it must not fire (and overwrite the
// server) during/before the initial load.
let synced = false;

export async function loadChats(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/chats");
    const arr = r.ok ? await r.json() : [];
    chatSessions.set(Array.isArray(arr) ? arr : []);
  } catch {
    chatSessions.set([]);
  }
  synced = true;
}

export function clearChats(): void {
  synced = false;
  chatSessions.set([]);
  activeChatId.set("");
}

// Debounced push of the whole list to the server. The client owns the list
// (add/rename/delete happen client-side); the server just persists it per user.
let timer: ReturnType<typeof setTimeout> | null = null;
chatSessions.subscribe((sessions) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/chats", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sessions),
    }).catch(() => {});
  }, 800);
});

export function newChatId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

// First user message, trimmed — good enough as a title. "New chat" until then.
export function deriveTitle(messages: ChatMessage[]): string {
  const first = messages.find((m) => m.role === "user");
  if (!first) return "New chat";
  const text =
    typeof first.content === "string"
      ? first.content
      : first.content.map((p) => (p.type === "text" ? p.text : "")).join(" ");
  return text.trim().slice(0, 48) || "New chat";
}
