import { describe, it, expect } from "vitest";
import { parseCmdFields, genDefaultNum, cmdNum, specToggle, hoistCms } from "./modelCmdForm";
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

// The vision twin's projector flags are owned by the "Image projector" dropdown
// (--no-mmproj-offload) and by sidecar discovery (--mmproj). Neither may land in
// extraArgs: the emitter writes its own copy, so a leaked one double-emits from
// the first blur of the launch box onward.
describe("parseCmdFields mmproj flags", () => {
  it("keeps --mmproj and --no-mmproj-offload out of extraArgs", () => {
    const p = parseCmdFields(
      "llama-server -m x.gguf --mmproj C:/models/mmproj.gguf --no-mmproj-offload --foo bar",
    );
    expect(p.extraArgs).toBe("--foo bar");
  });
});

describe("parseCmdFields --ctx-checkpoints", () => {
  it("captures the value instead of swallowing it", () => {
    expect(parseCmdFields("llama-server -m x.gguf --ctx-checkpoints 2").ctxCheckpoints).toBe(2);
    expect(parseCmdFields("llama-server -m x.gguf --ctx-checkpoints 0").ctxCheckpoints).toBe(0);
  });
  it("reports null when the user deleted the flag", () => {
    expect(parseCmdFields("llama-server -m x.gguf -c 4096").ctxCheckpoints).toBeNull();
  });
  it("never bleeds into extraArgs", () => {
    expect(parseCmdFields("llama-server -m x.gguf --ctx-checkpoints 2").extraArgs).toBe("");
  });
});

// -cms is the flag that proved this whole class of bug: autogen emits it on
// every text model, the box did not parse it, so it landed in extraArgs and was
// re-appended after the generated copy - once more per round trip through the
// launch box.
describe("parseCmdFields -cms", () => {
  it("captures both spellings", () => {
    expect(parseCmdFields("llama-server -m x.gguf -cms 256").checkpointMinStep).toBe(256);
    expect(parseCmdFields("llama-server -m x.gguf --checkpoint-min-step 512").checkpointMinStep).toBe(512);
  });
  it("reports \"\" when the user deleted the flag", () => {
    expect(parseCmdFields("llama-server -m x.gguf -c 4096").checkpointMinStep).toBe("");
  });
  it("never bleeds into extraArgs", () => {
    expect(parseCmdFields("llama-server -m x.gguf -cms 256 --foo bar").extraArgs).toBe("--foo bar");
    expect(parseCmdFields("llama-server -m x.gguf --checkpoint-min-step 256").extraArgs).toBe("");
  });
});

// Installs saved before the parse existed carry the flag inside extraArgs.
// Hoisting it back into the field on load is what actually stops the duplicate
// the user already has on disk.
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
