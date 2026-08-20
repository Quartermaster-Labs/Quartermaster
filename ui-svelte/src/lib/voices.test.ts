import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { VOICES_CACHE_KEY, saveVoicesCache, getVoicesCache, migrateVoicesCache, fetchVoices, safeVoice, voiceLabel, hasCachedVoices, cachedVoices, voiceSubstitution } from "./voices";
import { clearPrefs } from "../stores/prefs";

// The cache lives in the server-backed prefs blob now; clearPrefs() is the reset.
// migrateVoicesCache still reads the retired localStorage key, and the suite runs
// in node (no DOM), so stub the two globals it touches.
const store = new Map<string, string>();
const fakeLocalStorage = {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, v),
  removeItem: (k: string) => void store.delete(k),
};

describe("voices", () => {
  beforeEach(() => {
    store.clear();
    clearPrefs();
    vi.stubGlobal("window", {});
    vi.stubGlobal("localStorage", fakeLocalStorage);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function stubVoicesResponse(body: unknown) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => body }) as unknown as Response),
    );
  }

  it("normalizes the qwentts {voices:[{name,kind}]} shape, clones last", async () => {
    stubVoicesResponse({
      voices: [
        { name: "serena", kind: "speaker" },
        { name: "my-clone", kind: "registered" },
      ],
    });
    expect(await fetchVoices("qwen-talker")).toEqual(["serena", "my-clone"]);
  });

  it("normalizes the TTS.cpp model→names map", async () => {
    stubVoicesResponse({ Kokoro_no_espeak_Q8: ["af_heart", "am_michael"] });
    expect(await fetchVoices("kokoro-q8")).toEqual(["af_heart", "am_michael"]);
  });

  it("keeps a base model's default speaker when it reports only clones", async () => {
    stubVoicesResponse({ voices: [{ name: "my-clone", kind: "registered" }] });
    expect(await fetchVoices("base")).toEqual(["", "my-clone"]);
  });

  // An unknown voice is not a 400 on TTS.cpp — the Kokoro runner TTS_ABORTs and
  // takes the whole server process down with it.
  it("never sends a voice the model does not have", () => {
    saveVoicesCache({ kokoro: ["af_heart", "am_michael"], qwen: ["serena"] });
    expect(safeVoice("kokoro", "serena")).toBe("af_heart");
    expect(safeVoice("kokoro", "am_michael")).toBe("am_michael");
    expect(safeVoice("qwen", "serena")).toBe("serena");
  });

  it("falls back to the server default for a model whose voices are unknown", () => {
    expect(safeVoice("never-loaded", "serena")).toBe("");
    expect(safeVoice("never-loaded", "")).toBe("");
  });

  it("caches under the shared key so the Speech tab and read-aloud agree", async () => {
    stubVoicesResponse({ Kokoro_Q8: ["af_heart"] });
    await fetchVoices("kokoro-q8");
    expect(getVoicesCache()["kokoro-q8"]).toEqual(["af_heart"]);
  });

  // Storing [""] for a model whose fetch failed made hasCachedVoices() true with
  // a list holding only the default, and the picker's clamp then rewrote the
  // user's saved voice to "". A refresh while the model was still loading was
  // enough to lose the selection.
  it("keeps the known list when a refresh fails", async () => {
    saveVoicesCache({ kokoro: ["af_heart", "am_michael"] });
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 503 }) as unknown as Response));
    expect(await fetchVoices("kokoro")).toEqual(["af_heart", "am_michael"]);
    expect(getVoicesCache()["kokoro"]).toEqual(["af_heart", "am_michael"]);
    expect(safeVoice("kokoro", "am_michael")).toBe("am_michael");
  });

  it("does not invent a cache entry when a first fetch fails", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("offline"); }));
    expect(await fetchVoices("kokoro")).toEqual([""]);
    expect(hasCachedVoices("kokoro")).toBe(false);
  });

  it("lifts a legacy localStorage cache into prefs exactly once", () => {
    store.set(VOICES_CACHE_KEY, JSON.stringify({ kokoro: ["af_heart"], stale: [] }));
    saveVoicesCache({ qwen: ["serena"] });
    migrateVoicesCache();
    expect(getVoicesCache()).toEqual({ qwen: ["serena"], kokoro: ["af_heart"] });
    // Consumed: a later call finds nothing left to lift.
    expect(fakeLocalStorage.getItem(VOICES_CACHE_KEY)).toBeNull();
    migrateVoicesCache();
    expect(getVoicesCache()).toEqual({ qwen: ["serena"], kokoro: ["af_heart"] });
  });

  // A model with a single unnamed voice and a model nobody asked about both
  // render as [""] — only the cache tells them apart, and the read-aloud picker
  // clamps the saved voice pref on that distinction.
  it("separates a known one-voice model from an unasked one", () => {
    saveVoicesCache({ base: [""] });
    expect(hasCachedVoices("base")).toBe(true);
    expect(hasCachedVoices("never-loaded")).toBe(false);
    expect(hasCachedVoices("")).toBe(false);
    expect(cachedVoices("never-loaded")).toEqual([""]);
  });

  // A swapped voice is indistinguishable from a mislabelled voice pack unless
  // the UI says which one it sent.
  it("explains a substituted voice and stays quiet otherwise", () => {
    saveVoicesCache({ kokoro: ["af_heart", "am_michael"] });
    expect(voiceSubstitution("kokoro", "am_michael")).toBe("");
    expect(voiceSubstitution("kokoro", "serena")).toContain("af_heart");
    expect(voiceSubstitution("never-loaded", "am_michael")).toContain("default voice");
  });

  it("labels the empty voice", () => {
    expect(voiceLabel("")).toBe("Default voice");
    expect(voiceLabel("af_heart")).toBe("af_heart");
  });
});
