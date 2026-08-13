import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { VOICES_CACHE_KEY, saveVoicesCache, fetchVoices, safeVoice, voiceLabel, hasCachedVoices, cachedVoices, voiceSubstitution } from "./voices";

// The suite runs in node (no DOM); voices.ts guards on `window`, so provide the
// two globals it actually touches.
const store = new Map<string, string>();
const fakeLocalStorage = {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, v),
};

describe("voices", () => {
  beforeEach(() => {
    store.clear();
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
    expect(JSON.parse(fakeLocalStorage.getItem(VOICES_CACHE_KEY)!)["kokoro-q8"]).toEqual(["af_heart"]);
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
