import { inferenceHeaders } from "./inferenceAuth";

// Voice lists for a TTS model, shared by the Speech tab and the read-aloud
// picker in Settings → General. Both need the same list for the same model, and
// a second copy of the normalization would drift the moment tts-server changes a
// field name.
//
// The list is CACHED in localStorage per model on purpose: GET /v1/audio/voices
// proxies through to tts-server, so an uncached read forces a model load. Opening
// a settings panel must never swap what's on the GPU — callers fetch only when
// the model is already loaded, and render the cache otherwise.

// v4: base models keep "" + cloned voices; kind-aware. Shared with the Speech
// tab, so the key must stay in step with it.
export const VOICES_CACHE_KEY = "playground-speech-voices-cache-v4";

// "" is the model's own default speaker — a base model has no named speakers.
export const DEFAULT_VOICES = [""];

export function getVoicesCache(): Record<string, string[]> {
  if (typeof window === "undefined") return {};
  try {
    const saved = localStorage.getItem(VOICES_CACHE_KEY);
    return saved ? JSON.parse(saved) : {};
  } catch {
    return {};
  }
}

export function saveVoicesCache(cache: Record<string, string[]>) {
  if (typeof window === "undefined") return;
  try {
    localStorage.setItem(VOICES_CACHE_KEY, JSON.stringify(cache));
  } catch (e) {
    console.error("Error saving voices cache", e);
  }
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
  let voices = DEFAULT_VOICES;
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
    return DEFAULT_VOICES;
  }
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
