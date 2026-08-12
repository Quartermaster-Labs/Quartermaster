import { persistentStore } from "./persistent";
import { userPref } from "./prefs";
import type { SystemPreset } from "../lib/systemPrompt";
import { DEFAULT_SEARCH_PROVIDERS, type SearchProviderCfg } from "../lib/webSearch";

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
// Quartermaster tools: let the chat model inspect + tune this running instance
// (see lib/qmTools.ts). Default on, like web search; a toggle in the chat menu.
export const qmToolsStore = userPref<boolean>("playground-qmtools", true);
// Weather + RSS/Atom feeds (lib/assistantTools.ts EXTRA_TOOLS). Separate from
// the always-on local trio (clock / calculator / units) because these two hit
// the network and only matter to some conversations — and every advertised
// tool is prefix the KV cache carries on every turn.
export const extraToolsStore = userPref<boolean>("playground-extratools", true);
export const reasoningStore = userPref<boolean>("playground-reasoning", true);
// Rewrite mode: composer becomes a two-field (instructions + prose) rewriter
// whose output renders as a side-by-side diff. Toggle + last-used instruction.
export const rewriteStore = userPref<boolean>("playground-rewrite", false);
export const rewriteInstructionStore = userPref<string>("playground-rewrite-instruction", "");
// Shopping assistant: staged buying helper (brief → research → report). Off by
// default — it changes how the model answers, so it is opt-in per conversation
// via the composer tool menu. The prefs line (country / currency / shops) is
// standing, not per-chat: prices from the wrong country are worthless.
export const shoppingStore = userPref<boolean>("playground-shopping", false);
export const shoppingPrefsStore = userPref<string>("playground-shopping-prefs", "");
export const searxngUrlStore = userPref<string>("playground-searxng-url", "http://localhost:8888");
// Ordered web-search failover chain (SearXNG first, keyed APIs behind it). One
// provider means one timeout ends the tool call; see lib/webSearch.ts and
// internal/server/search.go. Stored raw — normalizeProviders() repairs it on
// read, so a provider added later needs no migration here.
export const searchProvidersStore = userPref<SearchProviderCfg[]>("playground-search-providers", DEFAULT_SEARCH_PROVIDERS);
// Web-search rate controls (protect self-hosted SearXNG from runaway agents).
export const searchMaxPerTurnStore = userPref<number>("playground-search-max-per-turn", 5);
export const searchThrottleMsStore = userPref<number>("playground-search-throttle-ms", 500);
export const searchDedupeStore = userPref<boolean>("playground-search-dedupe", true);

// Chat history sidebar collapsed state. Shared so the side-rail Chat icon can
// toggle the ChatInterface's own history panel when chat is already active.
export const chatSidebarCollapsed = persistentStore<boolean>("playground-chat-sidebar-collapsed", false);
