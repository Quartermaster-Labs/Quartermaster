import { get, writable } from "svelte/store";
import { getTextContent, type ChatMessage } from "../lib/types";

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
  // Model this conversation last ran on. Recorded when a turn starts and when the
  // user picks a model while this chat is open; reopening the chat re-selects it,
  // so a thread stays on the model it was built with (its KV/context, its voice)
  // instead of inheriting whatever the previous chat used.
  model?: string;
  // Per-chat standing instructions (the composer's "Instructions" field). Layered
  // on top of the built-in prompt for this conversation only. Empty/undefined =
  // none. Persisted with the chat so it follows the conversation, not the user.
  instructions?: string;
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
//
// keepalive lets an in-flight PUT survive tab close/unload. A plain debounce
// starved during streaming: patchLast fires every token, resetting the timer, so
// the PUT only landed 800 ms AFTER generation ended — close mid-stream and the
// whole in-progress reply was never persisted. MAX_WAIT caps that: the timer
// still coalesces bursts, but can only be pushed out to MAX_WAIT from the first
// pending change, so a long stream flushes every ~5 s and a refresh restores
// what was generated so far. (Does NOT resume generation — that needs a
// server-side job; see below.)
const MAX_WAIT = 5000;
let timer: ReturnType<typeof setTimeout> | null = null;
let latest: ChatSession[] | null = null;
let deadline = 0;

// --- what actually reaches disk -------------------------------------------
//
// The store holds every session the tab has open, including throwaways: the
// blank chat the "New chat" button just made, and turns where the model never
// answered and the bubble carries nothing but "**Error:** …". Those used to be
// PUT along with the rest and came back on the next load as history the user
// never had. Persistence is therefore filtered, not the store — an open blank
// chat still works, it just does not survive a reload until it holds an answer.

// The test is written as "is this positively junk", NOT "is this good enough to
// keep": a PUT is destructive (the server also GCs the media of a session that
// disappears), so anything unrecognized — a shape from an older build, a session
// whose messages aren't even an array — falls through to KEEP.
//
// The failure tail both the client (catch/SSE error) and the server
// (turns.go flush) append to the assistant bubble. Stripped before deciding
// whether a bubble holds a real answer, so a turn that streamed prose and THEN
// died still counts as content worth keeping. Anchored at `\n\n` and greedy to
// the end, so an answer that merely *mentions* an error mid-prose keeps the
// prose in front of it and survives.
const ERROR_TAIL = /\n\n\*\*Error:\*\*[\s\S]*$/;

// Did this assistant turn produce anything at all? Text, reasoning, tool/search
// work or a tool call all count — only a bubble that is empty (or nothing but an
// error) counts as nothing.
function produced(m: ChatMessage): boolean {
  if (m.role !== "assistant") return false;
  return (
    getTextContent(m.content ?? "").replace(ERROR_TAIL, "").trim() !== "" ||
    (m.reasoning_content ?? "").trim() !== "" ||
    !!m.searches?.length ||
    !!m.tool_calls?.length
  );
}

// A session is disposable only when it is one of the two known throwaways: a
// chat with no messages at all, or one where no assistant turn ever produced
// anything. Per-chat `instructions` are a deliberate user act, so a chat set up
// with them is kept even before its first turn.
export function isDisposable(s: ChatSession): boolean {
  if (!s || !Array.isArray(s.messages)) return false;
  if (s.instructions?.trim()) return false;
  if (s.messages.length === 0) return true;
  return !s.messages.some(produced);
}

function keepable(sessions: ChatSession[], keepId?: string): ChatSession[] {
  // The live turn is exempt: at the moment it starts, its session is only a user
  // message plus an empty assistant bubble, and the server needs it on disk to
  // have somewhere to write the answer. keepId names it explicitly for the
  // pre-turn save (the generatingChatId mirror hasn't flushed yet at that point).
  const live = keepId ?? get(generatingChatId);
  if (!Array.isArray(sessions)) return sessions;
  return sessions.filter((s) => s.id === live || !isDisposable(s));
}

function pushChats(sessions: ChatSession[]): void {
  fetch("/api/chats", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(keepable(sessions)),
    keepalive: true,
  }).catch(() => {});
}

chatSessions.subscribe((sessions) => {
  if (!synced) return;
  latest = sessions;
  const now = Date.now();
  if (!timer) deadline = now + MAX_WAIT;
  if (timer) clearTimeout(timer);
  timer = setTimeout(
    () => {
      timer = null;
      pushChats(sessions);
    },
    Math.max(0, Math.min(800, deadline - now)),
  );
});

// Best-effort tail flush: capture whatever streamed since the last periodic
// push when the tab is hidden/closed. keepalive bodies are capped ~64 KB, so a
// very large history may not make it — the periodic flush above is the real
// guarantee; this just tightens the last few seconds.
if (typeof window !== "undefined") {
  const flush = () => {
    if (synced && latest) pushChats(latest);
  };
  window.addEventListener("pagehide", flush);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") flush();
  });
}

// Synchronous save used before starting a server-side turn: the server writes
// the in-flight assistant message straight into chats.json, so the session (with
// the new user message + empty assistant bubble) must be on disk FIRST. Awaited,
// unlike the debounced auto-save. Merge-guard is a no-op here (no turn running
// yet), so this writes the full array.
export async function saveChatsNow(keepId?: string): Promise<void> {
  try {
    await fetch("/api/chats", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      // keepId is the session the turn is about to run in: it holds only the user
      // message + an empty assistant bubble, so the junk filter would drop it and
      // the server would have nowhere to write the answer.
      body: JSON.stringify(keepable(get(chatSessions), keepId)),
    });
  } catch {
    // ignore — the turn still runs; periodic flush will reconcile
  }
}

export function newChatId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

// Open a fresh conversation, optionally pinned to a model (the dashboard's "Chat"
// button). An already-empty chat is reused instead of stacking blank sessions when
// the button is clicked repeatedly. Returns the active chat id.
export function startChat(model?: string): string {
  const sessions = get(chatSessions);
  const empty = sessions.find((s) => s.messages.length === 0);
  if (empty) {
    chatSessions.set(
      sessions.map((s) =>
        s.id === empty.id ? { ...s, ...(model ? { model } : {}), updatedAt: Date.now() } : s,
      ),
    );
    activeChatId.set(empty.id);
    return empty.id;
  }
  const s: ChatSession = {
    id: newChatId(),
    title: "New chat",
    messages: [],
    updatedAt: Date.now(),
    ...(model ? { model } : {}),
  };
  chatSessions.set([...sessions, s]);
  activeChatId.set(s.id);
  return s.id;
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
