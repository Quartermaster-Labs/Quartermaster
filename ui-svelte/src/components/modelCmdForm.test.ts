import { describe, it, expect } from "vitest";
import { parseCmdFields, genDefaultNum, specToggle } from "./modelCmdForm";
import type { ModelConfig } from "../stores/api";

// The sampler defaults are the one flag group where 0 is a real value, so the
// parse path has to keep "absent" and "pinned to 0" apart. Everything else in
// the form collapses 0 to "inherit", and getting this wrong either loses a
// deliberate --min-p 0 or invents one.
describe("parseCmdFields sampler defaults", () => {
  it("keeps a pinned 0 distinct from an absent flag", () => {
    const p = parseCmdFields("llama-server -m x.gguf --top-k 20 --min-p 0 --temp 1");
    expect(p.topK).toBe(20);
    expect(p.minP).toBe(0);
    expect(p.temp).toBe(1);
    expect(p.topP).toBe("");
    expect(p.presencePenalty).toBe("");
  });

  it("does not leak sampler flags into extraArgs", () => {
    const p = parseCmdFields("llama-server -m x.gguf --top-k 20 --min-p 0 --presence-penalty 1.5 --foo bar");
    expect(p.extraArgs).toBe("--foo bar");
  });

  it("accepts llama's --temperature alias", () => {
    expect(parseCmdFields("llama-server --temperature 0.7").temp).toBe(0.7);
  });
});

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
