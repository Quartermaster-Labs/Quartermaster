import { describe, it, expect } from "vitest";
import { SDXL_ANIME_NEG, aspectDims, defaultsFor, fmtDur, parseSdProgress, settingsFor } from "./imageGen";

describe("aspectDims", () => {
  it("keeps squares square", () => {
    expect(aspectDims("1:1", 1024)).toEqual([1024, 1024]);
  });

  it("snaps the short edge to a multiple of 64", () => {
    const [w, h] = aspectDims("16:9", 1024);
    expect(w).toBe(1024);
    expect(h % 64).toBe(0);
    expect(h).toBe(576);
  });

  it("puts the long edge on the tall side for portrait", () => {
    expect(aspectDims("9:16", 1024)).toEqual([576, 1024]);
  });

  it("falls back to the first aspect for an unknown id", () => {
    expect(aspectDims("nope", 512)).toEqual([512, 512]);
  });
});

describe("defaultsFor", () => {
  it("matches on an id substring, case-insensitively", () => {
    expect(defaultsFor("SD/Flux-Kontext-Q8.gguf")?.match).toBe("kontext");
  });

  it("returns undefined for a model with no preset", () => {
    expect(defaultsFor("some-random-model")).toBeUndefined();
  });
});

describe("fmtDur", () => {
  it("stays in seconds under a minute", () => {
    expect(fmtDur(45)).toBe("45s");
  });

  it("splits minutes and seconds", () => {
    expect(fmtDur(90)).toBe("1m 30s");
  });
});

describe("parseSdProgress", () => {
  it("reports the preparing state with no markers", () => {
    expect(parseSdProgress("nothing here", 20)).toEqual({
      label: "Preparing…",
      phase: null,
      step: 0,
      totalSteps: 0,
      secPerIt: 0,
    });
  });

  it("takes the phase from the LAST marker in the tail", () => {
    const tail = "EDIT mode\nencode_first_stage completed\ngenerating image:\ndecoding\n";
    expect(parseSdProgress(tail, 20).phase).toBe("decode");
  });

  it("ignores a stale VAE bar while sampling (total must equal the step count)", () => {
    const tail = "EDIT mode\n4/4 - 1.0s/it\ngenerating image:\n7/20 - 2.5s/it\n";
    const p = parseSdProgress(tail, 20);
    expect(p.phase).toBe("sample");
    expect(p).toMatchObject({ step: 7, totalSteps: 20, secPerIt: 2.5 });
  });

  it("takes the VAE bar (total !== steps) while encoding", () => {
    const tail = "EDIT mode\n3/4 - 1.5s/it\n";
    const p = parseSdProgress(tail, 20);
    expect(p.phase).toBe("encode");
    expect(p).toMatchObject({ step: 3, totalSteps: 4, secPerIt: 1.5 });
  });

  it("stays indeterminate during prompt conditioning", () => {
    const tail = "EDIT mode\n3/4 - 1.5s/it\nencode_first_stage completed\n";
    const p = parseSdProgress(tail, 20);
    expect(p.phase).toBe("cond");
    expect(p).toMatchObject({ step: 0, totalSteps: 0, secPerIt: 0 });
  });
});

describe("settingsFor", () => {
  it("falls back to the generic settings for an unknown model", () => {
    expect(settingsFor("some-random-sd15")).toEqual(
      expect.objectContaining({ steps: 20, cfg: 7, sampler: "", scheduler: "", negative: "", denoise: 0.6 }),
    );
  });

  it("clears a preset-only field when the next model omits it", () => {
    expect(settingsFor("animagine-xl-3.1").negative).toBe(SDXL_ANIME_NEG);
    // z-image has no negative/denoise of its own — must not inherit the anime one.
    expect(settingsFor("z-image-turbo").negative).toBe("");
    expect(settingsFor("z-image-turbo").denoise).toBe(0.6);
  });

  it("carries a preset's size through", () => {
    expect(settingsFor("illustrious-xl").size).toBe("1024x1024");
    expect(settingsFor("z-image-turbo").size).toBeUndefined();
  });

  it("lets the model's own launch defaults win over the table", () => {
    // The whole point: a model configured with defaultCfg/defaultSteps must not
    // be reset to the generic 20/7 just because it has no preset row.
    const d = settingsFor("longcat-image-edit-turbo", { steps: 12, cfg: 1 });
    expect(d.steps).toBe(12);
    expect(d.cfg).toBe(1);
    // Even a model WITH a preset defers to what it is actually launched with.
    expect(settingsFor("animagine-xl-3.1", { cfg: 4.5 }).cfg).toBe(4.5);
  });

  it("keeps the preset for fields the launch line does not state", () => {
    const d = settingsFor("animagine-xl-3.1", { cfg: 4.5 });
    expect(d.steps).toBe(28);
    expect(d.sampler).toBe("euler_a");
    expect(d.negative).toBe(SDXL_ANIME_NEG);
    expect(d.size).toBe("1024x1024");
  });

  it("takes a size only when both edges are named", () => {
    expect(settingsFor("z-image-turbo", { width: 1024, height: 768 }).size).toBe("1024x768");
    expect(settingsFor("illustrious-xl", { width: 1024 }).size).toBe("1024x1024");
  });
});
