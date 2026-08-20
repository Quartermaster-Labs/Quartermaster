import { derived, get, writable } from "svelte/store";
import { persistentStore } from "./persistent";
import { models } from "./api";
import { modelCategory } from "../lib/modelUtils";
import { userPref } from "./prefs";
import type { SystemPreset } from "../lib/systemPrompt";
import { DEFAULT_SEARCH_PROVIDERS, type SearchProviderCfg } from "../lib/webSearch";

// Shared singletons so other pages (e.g. the Models panel's "Chat" button) can
// drive the always-mounted playground. persistentStore returns a fresh writable
// per call, so these MUST be imported, not re-created, to stay in sync.
export type PlaygroundTab = "chat" | "images" | "speech" | "audio";

export const selectedTabStore = persistentStore<PlaygroundTab>("playground-selected-tab", "chat");
// A browser last parked on a tab that no longer exists (rerank / concurrency)
// would render no panel at all — snap it back to chat once at load.
if (!["chat", "images", "speech", "audio"].includes(get(selectedTabStore))) selectedTabStore.set("chat");
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
// Cross-conversation memory (lib/memoryTools.ts): the remembered facts are
// injected into the system prompt and the model can save/delete them. ON by
// default: an assistant that forgets everything between chats is the thing being
// fixed, and a feature nobody switches on is not a feature. The costs are real
// but bounded — an empty list injects nothing, and Settings → Memory is where a
// user prunes what the model kept.
export const memoryStore = userPref<boolean>("playground-memory", true);
// How hard the model should think. One string covering both worlds: "none" is
// thinking off, "on" is thinking with no level (a model whose template has no
// reasoning_effort ladder), anything else is a level off the ladder the server
// advertised. The pick follows the user across models — lib/effort.ts resolves
// it against what each one actually accepts, defaulting to medium.
export const reasoningEffortStore = userPref<string>("playground-reasoning-effort", "medium");
// Read-aloud: the TTS model the chat tab's speaker button uses. Empty = the
// button is inert (nothing picked yet). Separate from the Speech tab's model so
// reading a reply out doesn't hijack whatever that tab is set up for, but the
// VOICE is shared — it is the same person's voice either way.
export const chatTtsModelStore = userPref<string>("playground-chat-tts-model", "");
export const chatTtsVoiceStore = userPref<string>("playground-speech-voice", "");
// Playback shaping for read-aloud, live on the <audio> element rather than a
// synthesis parameter: neither engine takes a rate or a gain, and re-synthesising
// to change either would throw away the replay cache. Shared across messages and
// persisted, because "too loud" / "too slow" is a property of the listener, not
// of one reply. Rate is clamped to the dropdown's 0.25-2x range; browsers keep
// pitch constant over it (preservesPitch), so 2x is fast, not chipmunked.
export const chatTtsVolumeStore = userPref<number>("playground-speech-volume", 1);
export const chatTtsRateStore = userPref<number>("playground-speech-rate", 1);
// Installed TTS models, and the one read-aloud will actually use: the explicit
// pick when it still exists, else the first installed model. Auto-selecting
// rather than making the user choose keeps the speaker button working out of the
// box; it is only a playback voice, not a setting worth a setup step. Empty list
// = the box has no TTS model, and the button hides entirely.
export const ttsModels = derived(models, ($m) => $m.filter((x) => modelCategory(x) === "tts"));
export const effectiveTtsModel = derived([chatTtsModelStore, ttsModels], ([$pick, $tts]) =>
  $tts.some((m) => m.id === $pick) ? $pick : ($tts[0]?.id ?? ""),
);
// Rewrite mode: composer becomes a two-field (instructions + prose) rewriter
// whose output renders as a side-by-side diff. Toggle + last-used instruction.
// The TOGGLE is deliberately NOT persisted (plain writable): it changes what the
// composer does and what the model is told to do, so it must not survive a reload
// or follow the user into a new chat. Same for shopping below. The instruction
// text is only read while rewrite is on, so keeping it around is harmless.
export const rewriteStore = writable<boolean>(false);
export const rewriteInstructionStore = userPref<string>("playground-rewrite-instruction", "");
// Shopping assistant: staged buying helper (brief → research → report). Off by
// default and session-local — it changes how the model answers, so it is opt-in
// per conversation via the composer tool menu. The prefs line (country /
// currency / shops) IS standing: prices from the wrong country are worthless.
export const shoppingStore = writable<boolean>(false);
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
