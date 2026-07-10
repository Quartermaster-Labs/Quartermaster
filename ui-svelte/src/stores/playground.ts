import { persistentStore } from "./persistent";
import { userPref } from "./prefs";
import type { SystemPreset } from "../lib/systemPrompt";

// Shared singletons so other pages (e.g. the Models panel's "Chat" button) can
// drive the always-mounted playground. persistentStore returns a fresh writable
// per call, so these MUST be imported, not re-created, to stay in sync.
export type PlaygroundTab = "chat" | "images" | "speech" | "audio" | "rerank" | "concurrency";

export const selectedTabStore = persistentStore<PlaygroundTab>("playground-selected-tab", "chat");
// Per-user (server-backed) so the chosen model follows the user, not the browser.
export const selectedModelStore = userPref<string>("playground-selected-model", "");

// Per-user chat settings, shared between the chat-input gear (model + temperature)
// and the user-level settings modal in the nav rail (web search, max tokens).
// Server-backed via userPref so they follow the logged-in user.
// (Per-chat "instructions" live on the ChatSession, not here — they're per chat.)
// System-prompt presets: named personas the user creates instead of editing the
// default. The active selection points at one (or the fixed default / none).
// active: null = built-in default, "" = none, else = a preset id. Preset content
// supports {date}/{time}/{model}. See lib/systemPrompt.
// A preset bundles its tool sub-prompts (search/wiki/cite) alongside the persona,
// so there are no separate global tool-prompt stores.
export const systemPresetsStore = userPref<SystemPreset[]>("playground-system-presets", []);
export const activeSystemPresetStore = userPref<string | null>("playground-active-system-prompt", null);
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
// Web-search rate controls (protect self-hosted SearXNG from runaway agents).
export const searchMaxPerTurnStore = userPref<number>("playground-search-max-per-turn", 5);
export const searchThrottleMsStore = userPref<number>("playground-search-throttle-ms", 500);
export const searchDedupeStore = userPref<boolean>("playground-search-dedupe", true);

// Chat history sidebar collapsed state. Shared so the side-rail Chat icon can
// toggle the ChatInterface's own history panel when chat is already active.
export const chatSidebarCollapsed = persistentStore<boolean>("playground-chat-sidebar-collapsed", false);
