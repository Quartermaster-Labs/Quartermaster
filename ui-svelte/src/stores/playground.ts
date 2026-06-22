import { persistentStore } from "./persistent";

// Shared singletons so other pages (e.g. the Models panel's "Chat" button) can
// drive the always-mounted playground. persistentStore returns a fresh writable
// per call, so these MUST be imported, not re-created, to stay in sync.
export type PlaygroundTab = "chat" | "images" | "speech" | "audio" | "rerank" | "concurrency";

export const selectedTabStore = persistentStore<PlaygroundTab>("playground-selected-tab", "chat");
export const selectedModelStore = persistentStore<string>("playground-selected-model", "");
