import { get } from "svelte/store";
import { chatSessions, activeChatId, newChatId, type ChatSession } from "../stores/chatHistory";
import { imageSessions, activeImageChatId, newImageChatId, type ImageSession } from "../stores/imageHistory";
import { videoSessions, activeVideoChatId, newVideoChatId, type VideoSession } from "../stores/videoHistory";
import { threeDSessions, activeThreeDChatId, newThreeDChatId, type ThreeDSession } from "../stores/threeDHistory";
import { speechSessions, activeSpeechChatId, newSpeechChatId, type SpeechSession } from "../stores/speechHistory";

// "New thread" for every playground tab. Pure store ops: each interface reacts
// to its active id, loading/persisting the working state itself. Shared here so
// the history drawer (in the shell) and each pane's own header button start a
// thread the same way. A blank active thread is reused, never stacked.

export function newChat() {
  const cur = get(chatSessions).find((s) => s.id === get(activeChatId));
  if (cur && cur.messages.length === 0) {
    activeChatId.set(cur.id);
    return;
  }
  const s: ChatSession = { id: newChatId(), title: "New chat", messages: [], updatedAt: Date.now() };
  chatSessions.update((ss) => [s, ...ss]);
  activeChatId.set(s.id);
}

export function newImageChat() {
  const cur = get(imageSessions).find((s) => s.id === get(activeImageChatId));
  if (cur && cur.turns.length === 0) {
    activeImageChatId.set(cur.id);
    return;
  }
  const s: ImageSession = { id: newImageChatId(), title: "New image", turns: [], updatedAt: Date.now() };
  imageSessions.update((ss) => [s, ...ss]);
  activeImageChatId.set(s.id);
}

export function newVideoChat() {
  const cur = get(videoSessions).find((s) => s.id === get(activeVideoChatId));
  if (cur && cur.turns.length === 0) {
    activeVideoChatId.set(cur.id);
    return;
  }
  const s: VideoSession = { id: newVideoChatId(), title: "New video", turns: [], updatedAt: Date.now() };
  videoSessions.update((ss) => [s, ...ss]);
  activeVideoChatId.set(s.id);
}

export function newThreeDChat() {
  const cur = get(threeDSessions).find((s) => s.id === get(activeThreeDChatId));
  if (cur && cur.turns.length === 0) {
    activeThreeDChatId.set(cur.id);
    return;
  }
  const s: ThreeDSession = { id: newThreeDChatId(), title: "New mesh", turns: [], updatedAt: Date.now() };
  threeDSessions.update((ss) => [s, ...ss]);
  activeThreeDChatId.set(s.id);
}

export function newSpeechChat() {
  const cur = get(speechSessions).find((s) => s.id === get(activeSpeechChatId));
  if (cur && cur.turns.length === 0) {
    activeSpeechChatId.set(cur.id);
    return;
  }
  const s: SpeechSession = { id: newSpeechChatId(), title: "New speech", turns: [], updatedAt: Date.now() };
  speechSessions.update((ss) => [s, ...ss]);
  activeSpeechChatId.set(s.id);
}

// "3m ago" / "2h ago" / "Mon" / "Sep 3": compact enough for a meta line.
export function relTime(ts: number, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - ts) / 1000));
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 7) return `${d}d ago`;
  return new Date(ts).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

// Drawer bucket for a timestamp, by calendar day rather than rolling hours.
export type DayBucket = "Today" | "Yesterday" | "Last 7 days" | "Older";
export function dayBucket(ts: number, now = Date.now()): DayBucket {
  const start = new Date(now);
  start.setHours(0, 0, 0, 0);
  const day = 86_400_000;
  if (ts >= start.getTime()) return "Today";
  if (ts >= start.getTime() - day) return "Yesterday";
  if (ts >= start.getTime() - 6 * day) return "Last 7 days";
  return "Older";
}
