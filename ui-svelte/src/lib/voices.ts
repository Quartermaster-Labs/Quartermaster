import { get } from "svelte/store";
import { inferenceHeaders } from "./inferenceAuth";
import { userPref } from "../stores/prefs";

// Voice lists for a TTS model, shared by the Speech tab and the read-aloud
// picker in Settings → General. Both need the same list for the same model, and
// a second copy of the normalization would drift the moment tts-server changes a
// field name.
//
// The list is CACHED per model on purpose: GET /v1/audio/voices proxies through
// to tts-server, so an uncached read forces a model load. Opening a settings
// panel must never swap what's on the GPU — callers fetch only when the model is
// already loaded, and render the cache otherwise.
//
// It lives in the SERVER-BACKED prefs blob rather than localStorage, because the
// cache is not a nicety: without it the picker has nothing to list and, worse,
// safeVoice() refuses to send any name at all, so the user's chosen voice
// silently stops being used. Keeping it next to the voice pref itself means both
// halves of "speak as af_bella" follow the person across browsers and survive a
// site-data clear, instead of the name outliving the list that validates it.

// v4: base models keep "" + cloned voices; kind-aware. Shared with the Speech
// tab, so the key must stay in step with it.
export const VOICES_CACHE_KEY = "playground-speech-voices-cache-v4";

// "" is the model's own default speaker — a base model has no named speakers.
export const DEFAULT_VOICES = [""];

const voicesCache = userPref<Record<string, string[]>>(VOICES_CACHE_KEY, {});

export function getVoicesCache(): Record<string, string[]> {
  const c = get(voicesCache);
  return c && typeof c === "object" && !Array.isArray(c) ? c : {};
}

export function saveVoicesCache(cache: Record<string, string[]>) {
  voicesCache.set(cache);
}

// migrateVoicesCache lifts a pre-existing localStorage cache into the prefs blob
// once, so moving the store doesn't make everyone re-load a TTS model just to
// repopulate a list they already had. Call it after loadPrefs() has hydrated;
// it never overwrites what the server already knows about a model.
export function migrateVoicesCache() {
  if (typeof window === "undefined") return;
  let old: Record<string, string[]>;
  try {
    const saved = localStorage.getItem(VOICES_CACHE_KEY);
    if (!saved) return;
    old = JSON.parse(saved);
    localStorage.removeItem(VOICES_CACHE_KEY);
  } catch {
    return;
  }
  if (!old || typeof old !== "object" || Array.isArray(old)) return;
  const cache = getVoicesCache();
  let added = false;
  for (const [model, list] of Object.entries(old)) {
    if (cache[model] || !Array.isArray(list) || !list.length) continue;
    cache[model] = list;
    added = true;
  }
  if (added) saveVoicesCache(cache);
}

export function cachedVoices(model: string): string[] {
  if (!model) return DEFAULT_VOICES;
  return getVoicesCache()[model] ?? DEFAULT_VOICES;
}

// hasCachedVoices distinguishes "this model really has one unnamed voice" from
// "we never asked". Callers that clamp a stored pick into the list MUST check
// it: DEFAULT_VOICES is a placeholder, and clamping against it silently rewrites
// the user's saved voice to "" the first time a model is selected before its
// list is known.
export function hasCachedVoices(model: string): boolean {
  return !!model && !!getVoicesCache()[model];
}

// fetchVoices asks the server for the model's real voice list and caches it.
// Only call it for a model that is already loaded.
export async function fetchVoices(model: string): Promise<string[]> {
  let voices: string[] | null = null;
  try {
    const response = await fetch(`/v1/audio/voices?model=${encodeURIComponent(model)}`, {
      headers: inferenceHeaders(),
    });
    if (response.ok) {
      const data = await response.json();
      // TTS.cpp answers with a map of model id → voice names ({"Kokoro_Q8":
      // ["af_heart", ...]}), not qwentts's {voices:[...]}. Its server holds one
      // model per process here, so flatten every list it reports.
      const ttscpp =
        data && !Array.isArray(data) && !data.voices
          ? Object.values(data as Record<string, unknown>).flatMap((v) => (Array.isArray(v) ? v : []))
          : null;
      const got: unknown[] = ttscpp ?? (Array.isArray(data) ? data : data.voices || []);
      // tts-server returns {voices:[{name,kind}]} (kind = "speaker" for built-in,
      // "registered" for a clone); also tolerate bare strings.
      const norm = got
        .map((v) => (typeof v === "string" ? { name: v, kind: "speaker" } : (v as { name?: string; kind?: string })))
        .filter((v): v is { name: string; kind?: string } => !!v?.name);
      const speakers = norm.filter((v) => v.kind !== "registered").map((v) => v.name);
      const registered = norm.filter((v) => v.kind === "registered").map((v) => v.name);
      // custom_voice model (named speakers) requires a named voice → no "".
      // base model (no speakers) → "" default plus any cloned voices.
      voices = speakers.length ? [...speakers, ...registered] : ["", ...registered];
    }
  } catch {
    return cachedVoices(model);
  }
  // A failed fetch must NOT reach the cache. The old code fell through and stored
  // DEFAULT_VOICES ([""]) for the model, which made hasCachedVoices() true with a
  // list holding nothing but the default — and the picker's clamp then rewrote the
  // user's saved voice to "". A voices call while the model is still loading (or
  // the 500 from tts.cpp's stale-response bug) was enough to lose the selection.
  if (!voices) return cachedVoices(model);
  const cache = getVoicesCache();
  cache[model] = voices;
  saveVoicesCache(cache);
  return voices;
}

// safeVoice keeps a voice name from reaching a model that doesn't have it.
// This is not cosmetic: TTS.cpp's Kokoro runner calls TTS_ABORT on an unknown
// voice, which kills the whole tts-server process (the request comes back as a
// 502 and the model has to be relaunched), while an empty voice is defined —
// both engines fall back to their own default speaker. The voice pref is one
// per user, shared across models, so a name picked for one engine WILL be sent
// to the other.
//
// An unknown model (nothing cached) yields "" rather than the requested name:
// we cannot tell a valid clone from a foreign engine's speaker, and the cost of
// guessing wrong is a downed backend.
export function safeVoice(model: string, voice: string): string {
  if (!voice) return "";
  const known = getVoicesCache()[model];
  if (!known) return "";
  return known.includes(voice) ? voice : (known[0] ?? "");
}

// voiceSubstitution explains, in one line, why the voice a caller asked for is
// not the voice the model will speak in — "" when the pick is sent through
// untouched. safeVoice swaps silently by design (a wrong name downs the
// backend), and a silent swap is indistinguishable from a mislabelled voice
// pack: the name on screen stays put while a different speaker talks. Any UI
// that shows the user a voice name should show this next to it.
export function voiceSubstitution(model: string, voice: string): string {
  const sent = safeVoice(model, voice);
  if (sent === voice) return "";
  if (!getVoicesCache()[model]) {
    return `${voice} isn't confirmed for this model yet - speaking in the model's default voice until its list loads.`;
  }
  return `${voice} isn't one of this model's voices - speaking as ${voiceLabel(sent)} instead.`;
}

// voiceLabel renders "" as the model's default speaker rather than a blank row.
export function voiceLabel(v: string): string {
  return v || "Default voice";
}
