import { persistentStore } from "./persistent";
import type { ChatMessage } from "../lib/types";

export interface ChatSession {
  id: string;
  title: string;
  messages: ChatMessage[];
  updatedAt: number;
}

// All saved conversations + which one is currently open. Persisted across
// route navigation and reloads (localStorage via persistentStore).
export const chatSessions = persistentStore<ChatSession[]>("playground-chats", []);
export const activeChatId = persistentStore<string>("playground-active-chat", "");

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
