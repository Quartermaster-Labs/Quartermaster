import { describe, it, expect } from "vitest";
import { genDefaultNum, cmdNum, specToggle, hoistCms } from "./modelCmdForm";
import type { ModelConfig } from "../stores/api";

describe("genDefaultNum", () => {
  const cfg = (cmd: string) => ({ cmd }) as ModelConfig;

  it("reads the value the generator emits, including 0", () => {
    expect(genDefaultNum(cfg("llama-server --min-p 0 --top-k 20"), "--min-p")).toBe(0);
    expect(genDefaultNum(cfg("llama-server --min-p 0 --top-k 20"), "--top-k")).toBe(20);
  });

  it("returns '' when the flag is absent, so the box falls back to llama's default", () => {
    expect(genDefaultNum(cfg("llama-server -m x.gguf"), "--min-p")).toBe("");
    expect(genDefaultNum(null, "--min-p")).toBe("");
  });

  it("does not match a longer flag that merely starts the same", () => {
    expect(genDefaultNum(cfg("llama-server --top-k 20"), "--top-p")).toBe("");
  });
});

describe("specToggle", () => {
  it("chains compatible backends and clears none", () => {
    expect(specToggle("none", "ngram-mod", true)).toBe("ngram-mod");
    expect(specToggle("ngram-mod", "ngram-map-k4v", true)).toBe("ngram-mod+ngram-map-k4v");
  });

  // The two draft backends share the single -md slot, so picking one must drop
  // the other rather than emitting both --spec-type flags over one draft file.
  it("keeps draft-mtp and draft-dflash exclusive", () => {
    expect(specToggle("draft-mtp+ngram-mod", "draft-dflash", true)).toBe("ngram-mod+draft-dflash");
    expect(specToggle("draft-dflash", "draft-mtp", true)).toBe("draft-mtp");
  });

  it("stores an explicit none when the last backend is cleared", () => {
    expect(specToggle("ngram-mod", "ngram-mod", false)).toBe("none");
  });
});

// Installs saved before the parse existed carry the flag inside extraArgs; the
// image/audio/SAM forms still hoist it back into their structured field on load.
describe("hoistCms", () => {
  it("pulls the flag out and returns the rest", () => {
    expect(hoistCms("-cms 256")).toEqual({ extra: "", step: 256 });
    expect(hoistCms("--foo bar -cms 256 --baz")).toEqual({ extra: "--foo bar --baz", step: 256 });
    expect(hoistCms("--checkpoint-min-step 512 --foo")).toEqual({ extra: "--foo", step: 512 });
  });
  it("leaves an extraArgs without it untouched", () => {
    expect(hoistCms("--foo bar")).toEqual({ extra: "--foo bar", step: "" });
    expect(hoistCms("")).toEqual({ extra: "", step: "" });
  });
  it("does not match a longer flag that merely ends in -cms", () => {
    expect(hoistCms("--not-cms 256").step).toBe("");
  });
});

describe("cmdNum", () => {
  it("reads a flag off any command text, not just the model baseline", () => {
    expect(cmdNum("llama-server --ctx-checkpoints 3 -c 8192", "--ctx-checkpoints")).toBe(3);
    expect(cmdNum("llama-server -c 8192", "--ctx-checkpoints")).toBe("");
  });
});
