import { describe, it, expect } from "vitest";
import { genDefaultNum, cmdNum, specToggle, hoistCms, knobTokens, lockedBool, type KnobToken } from "./modelCmdForm";
import type { CmdToken, ModelConfig } from "../stores/api";

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

// The form controls read the composed command's provenance so a custom flag can
// disable the control it overrides and name itself in the badge.
describe("knobTokens", () => {
  const tok = (text: string, knob?: string, source: "generated" | "custom" = "custom"): CmdToken => ({ text, source, knob, suppressed: false });

  it("pairs a flag with its following value token", () => {
    expect(knobTokens([tok("-c", "ctx"), tok("32768")], "ctx")).toEqual([{ text: "-c 32768", flag: "-c", value: "32768" }]);
  });

  it("splits an inline --flag=value", () => {
    expect(knobTokens([tok("--cache-ram=2048", "cacheRam")], "cacheRam")).toEqual([{ text: "--cache-ram 2048", flag: "--cache-ram", value: "2048" }]);
  });

  it("keeps a bare flag and ignores other knobs and generated tokens", () => {
    const toks = [tok("-c", "ctx", "generated"), tok("-c", "ctx", "generated"), tok("8192"), tok("--no-mmap", "loadMode"), tok("--metrics", "metrics")];
    expect(knobTokens(toks, "loadMode")).toEqual([{ text: "--no-mmap", flag: "--no-mmap", value: "" }]);
    expect(knobTokens(toks, "parallel")).toEqual([]);
  });

  it("matches a group of knobs and keeps command order", () => {
    const toks = [tok("--dry-base", "dryBase"), tok("1.1"), tok("--dry-multiplier", "dryMultiplier"), tok("0.8")];
    expect(knobTokens(toks, ["dryMultiplier", "dryBase"]).map((k) => k.text)).toEqual(["--dry-base 1.1", "--dry-multiplier 0.8"]);
    expect(knobTokens(undefined, "ctx")).toEqual([]);
  });
});

describe("lockedBool", () => {
  const lock = (flag: string, value = ""): KnobToken => ({ text: value ? `${flag} ${value}` : flag, flag, value });

  it("reads on/off literals", () => {
    expect(lockedBool(lock("-fa", "off"))).toBe(false);
    expect(lockedBool(lock("-fa", "on"))).toBe(true);
    expect(lockedBool(lock("--reasoning-format", "none"))).toBe(false);
  });

  it("takes extra spellings for bare flags", () => {
    expect(lockedBool(lock("--no-mmap"), ["--mlock"], ["--no-mmap"])).toBe(false);
    expect(lockedBool(lock("--no-kv-offload"), ["--no-kv-offload"])).toBe(true);
  });

  it("treats a bare flag as on, and says nothing otherwise", () => {
    expect(lockedBool(lock("--spec-default"))).toBe(true);
    expect(lockedBool(lock("--rope-scaling", "yarn"))).toBeNull();
    expect(lockedBool(undefined)).toBeNull();
  });
});
