import { persistentStore } from "./persistent";
import { userPref } from "./prefs";

// Shared singletons so other pages (e.g. the Models panel's "Chat" button) can
// drive the always-mounted playground. persistentStore returns a fresh writable
// per call, so these MUST be imported, not re-created, to stay in sync.
export type PlaygroundTab = "chat" | "images" | "speech" | "audio" | "rerank" | "concurrency";

export const selectedTabStore = persistentStore<PlaygroundTab>("playground-selected-tab", "chat");
// Per-user (server-backed) so the chosen model follows the user, not the browser.
export const selectedModelStore = userPref<string>("playground-selected-model", "");

// Per-user chat settings, shared between the chat-input gear (model + temperature)
// and the user-level settings modal in the nav rail (system prompt, web search,
// max tokens). Server-backed via userPref so they follow the logged-in user.
export const systemPromptStore = userPref<string>("playground-system-prompt", "");
export const temperatureStore = userPref<number>("playground-temperature", 0.7);
export const maxTokensStore = userPref<number>("playground-max-tokens", 8192);
// Cap on how long a model may "think" before being forced to answer (approx
// tokens of reasoning). 0 = unlimited. Stops models stuck in reasoning loops.
export const reasoningBudgetStore = userPref<number>("playground-reasoning-budget", 2500);
export const webSearchStore = userPref<boolean>("playground-websearch", true);
export const reasoningStore = userPref<boolean>("playground-reasoning", true);
// Rewrite mode: composer becomes a two-field (instructions + prose) rewriter
// whose output renders as a side-by-side diff. Toggle + last-used instruction.
export const rewriteStore = userPref<boolean>("playground-rewrite", false);
export const rewriteInstructionStore = userPref<string>("playground-rewrite-instruction", "");
export const searxngUrlStore = userPref<string>("playground-searxng-url", "http://localhost:8888");

// Chat history sidebar collapsed state. Shared so the side-rail Chat icon can
// toggle the ChatInterface's own history panel when chat is already active.
export const chatSidebarCollapsed = persistentStore<boolean>("playground-chat-sidebar-collapsed", false);
