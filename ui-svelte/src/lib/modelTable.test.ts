import { describe, it, expect } from "vitest";
import {
  baseKey,
  buildRows,
  familyOf,
  filterRows,
  fmtGB,
  groupFamilies,
  isLive,
  matches,
  nextSort,
  pickQuant,
  quantOf,
  sortRows,
  stripQuantCrumbs,
} from "./modelTable";
import type { Model } from "./types";

function mk(id: string, extra: Partial<Model> = {}): Model {
  return { id, state: "stopped", ...extra } as Model;
}

describe("quantOf", () => {
  it("prefers the server-parsed field", () => {
    expect(quantOf(mk("foo-q4_k_m", { quant: "Q4_K_M" }))).toBe("Q4_K_M");
  });

  it("derives from the id when the payload carries none", () => {
    expect(quantOf(mk("qwen3-32b-Q4_K_M"))).toBe("Q4_K_M");
    expect(quantOf(mk("gemma-3-27b-IQ4_XS"))).toBe("IQ4_XS");
    expect(quantOf(mk("kokoro-v1-fp16"))).toBe("FP16");
  });

  it("folds a UD / i1 recipe marker into the quant", () => {
    expect(quantOf(mk("qwen3.6-27b-ud-q4_k_xl"))).toBe("UD-Q4_K_XL");
    expect(quantOf(mk("some-model-70b-i1-Q4_K_M"))).toBe("I1-Q4_K_M");
  });

  it("takes the FIRST quant, not a trailing duplicate", () => {
    // autogen only strips the quant when it ends the filename, so a
    // "…-Q4_K_M-MTP.gguf" is served as "…-q4_k_m-mtp-q4_k_m".
    expect(quantOf(mk("thinkingcap-qwen3.6-27b-q4_k_m-mtp-q4_k_m"))).toBe("Q4_K_M");
  });

  it("is empty when the id carries no quant", () => {
    expect(quantOf(mk("sam3-large"))).toBe("");
  });

  it("reads an FP4 token sitting mid-id", () => {
    expect(quantOf(mk("qwen3.8-27b-nvfp4-mtp-mid-high"))).toBe("NVFP4");
    expect(quantOf(mk("gpt-oss-20b-mxfp4"))).toBe("MXFP4");
  });

  it("covers the whole quant families, not a hand-kept list", () => {
    // One per family the shared pattern recognises — see internal/quant.
    expect(quantOf(mk("gpt-oss-20b-mxfp4_moe"))).toBe("MXFP4_MOE");
    expect(quantOf(mk("bitnet-2b-tq1_0"))).toBe("TQ1_0");
    expect(quantOf(mk("qwen3.6-27b-iq4_ks"))).toBe("IQ4_KS"); // ik_llama.cpp
    expect(quantOf(mk("qwen3.8-27b-q1_0"))).toBe("Q1_0");
    expect(quantOf(mk("some-model-8b-bf16"))).toBe("BF16");
  });

  it("does not mistake a name part for a quant", () => {
    for (const id of ["qwen3.8-27b-mtp-mid-high", "gemma-4-12b-it", "sam3-large"]) {
      expect(quantOf(mk(id))).toBe("");
    }
  });
});

describe("baseKey", () => {
  it("collapses quants of one model onto one key", () => {
    expect(baseKey("qwen3-32b-Q4_K_M")).toBe("qwen3-32b");
    expect(baseKey("qwen3-32b-Q8_0")).toBe("qwen3-32b");
  });

  it("drops a trailing -gguf", () => {
    expect(baseKey("qwen3-32b-Q4_K_M-gguf")).toBe("qwen3-32b");
  });

  it("cuts the UD marker with the quant it belongs to", () => {
    expect(baseKey("qwen3.6-27b-ud-q4_k_xl")).toBe("qwen3.6-27b");
  });

  it("leaves no quant crumbs behind a build tag", () => {
    expect(baseKey("thinkingcap-qwen3.6-27b-q4_k_m-mtp-q4_k_m")).toBe("thinkingcap-qwen3.6-27b");
  });

  it("keeps a variant suffix out of the key", () => {
    expect(baseKey("qwen3-32b-Q4_K_M-32k")).toBe("qwen3-32b");
  });

  it("never returns empty, and never eats the whole id", () => {
    expect(baseKey("Q8_0")).toBe("Q8_0");
    expect(baseKey("q4-experiment")).toBe("q4-experiment"); // index 0 is not a quant
  });
});

describe("familyOf", () => {
  it("reduces a finetune to the base model + parameter count", () => {
    expect(familyOf("qwen3.6-27b")).toBe("qwen3.6-27b");
    expect(familyOf("thinkingcap-qwen3.6-27b")).toBe("qwen3.6-27b");
    expect(familyOf("qwen3.6-27b-uncensored-heretic-v2-native-mtp-preserved")).toBe("qwen3.6-27b");
  });

  it("keeps a bare version number with the name", () => {
    expect(familyOf("gemma-4-12b-it")).toBe("gemma-4-12b");
    expect(familyOf("gemma-4-e2b-it-qat")).toBe("gemma-4-e2b");
  });

  it("keeps the MoE active-parameter tail", () => {
    expect(familyOf("qwen3.6-35b-a3b")).toBe("qwen3.6-35b-a3b");
  });

  it("falls back to the whole key when there is no size token", () => {
    expect(familyOf("z-image-turbo")).toBe("z-image-turbo");
  });
});

describe("buildRows", () => {
  const models = [
    mk("qwen3-32b-Q4_K_M", { sizeGB: 18, estVramGB: 20 }),
    mk("qwen3-32b-Q8_0", { sizeGB: 34, estVramGB: 36 }),
    mk("qwen3-32b-Q4_K_M-32k", { sizeGB: 18 }),
    mk("gemma-3-27b-Q4_K_M", { sizeGB: 15 }),
  ];

  it("groups by model, then quant, largest quant first", () => {
    const rows = buildRows(models);
    const qwen = rows.find((r) => r.key === "qwen3-32b")!;
    expect(qwen.quants.map((q) => q.quant)).toEqual(["Q8_0", "Q4_K_M"]);
    expect(rows.map((r) => r.key).sort()).toEqual(["gemma-3-27b", "qwen3-32b"]);
  });

  it("keeps ctx variants as pills under their own quant", () => {
    const q4 = buildRows(models).find((r) => r.key === "qwen3-32b")!.quants.find((q) => q.quant === "Q4_K_M")!;
    expect(q4.base.id).toBe("qwen3-32b-Q4_K_M");
    expect(q4.variants.map((v) => v.label)).toEqual(["32k"]);
  });

  it("folds an NVFP4 model's ctx tiers and vision twin into ONE row", () => {
    // The quant sits mid-id here, so before NVFP4 was a recognised token every
    // one of these cut nowhere and stood alone as its own "model".
    const rows = buildRows([
      mk("qwen3.8-27b-nvfp4-mtp-mid-high", { sizeGB: 15.8 }),
      mk("qwen3.8-27b-nvfp4-mtp-mid-high-32k", { sizeGB: 15.8 }),
      mk("qwen3.8-27b-nvfp4-mtp-mid-high-vision", { sizeGB: 15.8 }),
      mk("qwen3.8-27b-q4_k_m", { sizeGB: 16.5 }),
    ]);
    expect(rows.map((r) => r.key)).toEqual(["qwen3.8-27b"]);
    const nvfp4 = rows[0].quants.find((q) => q.quant === "NVFP4")!;
    expect(nvfp4.base.id).toBe("qwen3.8-27b-nvfp4-mtp-mid-high");
    expect(nvfp4.variants.map((v) => v.label)).toEqual(["32k", "vision"]);
  });

  it("folds a CUSTOM-named quant's variants on the gguf, not on the id", () => {
    // "mix-q-k" is not a shape the quant pattern knows, so baseKey cuts nowhere
    // and every tier used to stand alone as a model of its own. The -m path the
    // server ships says otherwise: one file, one row, four pills.
    const gguf = "D:/LLM/Models/mixq/Qwen3.8-27B-mix-q-k.gguf";
    const rows = buildRows([
      mk("qwen3.8-27b-mix-q-k", { family: gguf, sizeGB: 16 }),
      mk("qwen3.8-27b-mix-q-k-32k", { family: gguf, sizeGB: 16 }),
      mk("qwen3.8-27b-mix-q-k-64k", { family: gguf, sizeGB: 16 }),
      mk("qwen3.8-27b-mix-q-k-game", { family: gguf, sizeGB: 16 }),
      mk("qwen3.8-27b-mix-q-k-vision", { family: gguf, sizeGB: 16 }),
    ]);
    expect(rows.map((r) => r.key)).toEqual(["qwen3.8-27b-mix-q-k"]);
    expect(rows[0].quants).toHaveLength(1);
    expect(rows[0].quants[0].variants.map((v) => v.label)).toEqual(["32k", "64k", "game", "vision"]);
  });

  it("keeps a custom quant's separate rebuild apart - it is a different gguf", () => {
    // …-mtp is its own file, so it stays its own entry rather than being fused
    // with the plain build just because neither id parses to a quant.
    const rows = buildRows([
      mk("qwen3.8-27b-mix-q-k", { family: "D:/m/mix-q-k.gguf" }),
      mk("qwen3.8-27b-mix-q-k-32k", { family: "D:/m/mix-q-k.gguf" }),
      mk("qwen3.8-27b-mix-q-k-mtp", { family: "D:/m/mix-q-k-mtp.gguf" }),
      mk("qwen3.8-27b-mix-q-k-mtp-32k", { family: "D:/m/mix-q-k-mtp.gguf" }),
    ]);
    expect(rows.map((r) => r.key).sort()).toEqual(["qwen3.8-27b-mix-q-k", "qwen3.8-27b-mix-q-k-mtp"]);
    for (const r of rows) expect(r.quants[0].variants.map((v) => v.label)).toEqual(["32k"]);
    // Both rows land under one heading, so they still read as one model.
    expect(new Set(rows.map((r) => r.family))).toEqual(new Set(["qwen3.8-27b"]));
  });

  it("still merges two copies of the SAME recognised quant across folders", () => {
    const rows = buildRows([
      mk("qwen3-32b-q8_0", { family: "D:/a/qwen3-32b-q8_0.gguf" }),
      mk("qwen3-32b-q8_0-32k", { family: "D:/a/qwen3-32b-q8_0.gguf" }),
      mk("qwen3-32b-q8_0-copy", { family: "D:/b/qwen3-32b-q8_0.gguf" }),
    ]);
    expect(rows).toHaveLength(1);
    expect(rows[0].quants).toHaveLength(1);
    expect(rows[0].quants[0].variants.map((v) => v.label)).toEqual(["32k", "copy"]);
  });

  it("gives every quant entry a key unique within its row", () => {
    const rows = buildRows([
      mk("qwen3.8-27b-q4_k_m", { family: "D:/a.gguf" }),
      mk("qwen3.8-27b-bf16", { family: "D:/b.gguf" }),
    ]);
    const keys = rows[0].quants.map((q) => q.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("marks a row live when any member is loaded, and unlisted only when all are", () => {
    const rows = buildRows([mk("m-Q4_K_M", { unlisted: true }), mk("m-Q8_0", { state: "ready" })]);
    expect(rows[0].live).toBe(true);
    expect(rows[0].unlisted).toBe(false);
    expect(pickQuant(rows[0]).quant).toBe("Q8_0"); // the loaded one, not the largest
  });
});

describe("isLive", () => {
  it("counts starting and stopping as live", () => {
    expect(isLive(mk("a", { state: "starting" }))).toBe(true);
    expect(isLive(mk("a", { state: "ready" }))).toBe(true);
    expect(isLive(mk("a", { state: "stopped" }))).toBe(false);
  });
});

describe("matches / filterRows", () => {
  const rows = buildRows([
    mk("qwen3-32b-Q4_K_M", { name: "Qwen3 32B" }),
    mk("gemma-3-27b-Q8_0", { unlisted: true, state: "ready" }),
  ]);

  it("searches id, display name and quant of any member", () => {
    const qwen = rows.find((r) => r.key === "qwen3-32b")!;
    expect(matches(qwen, "32B")).toBe(true); // name, case-insensitive
    expect(matches(qwen, "q4_k")).toBe(true); // quant
    expect(matches(qwen, "gemma")).toBe(false);
    expect(matches(qwen, "")).toBe(true);
  });

  it("searches the family too, so a base name finds its finetunes", () => {
    const tune = buildRows([mk("thinkingcap-qwen3.6-27b-q4_k_m")])[0];
    expect(matches(tune, "qwen3.6-27b")).toBe(true);
  });

  it("filters by state and the unlisted toggle", () => {
    const opts = { search: "", state: "all" as const, showUnlisted: true };
    expect(filterRows(rows, { ...opts, showUnlisted: false }).map((r) => r.key)).toEqual(["qwen3-32b"]);
    expect(filterRows(rows, { ...opts, state: "loaded" }).map((r) => r.key)).toEqual(["gemma-3-27b"]);
    expect(filterRows(rows, { ...opts, state: "idle" }).map((r) => r.key)).toEqual(["qwen3-32b"]);
  });
});

describe("nextSort", () => {
  it("cycles ascending → descending → off on the same column", () => {
    expect(nextSort("name", "asc", "name")).toEqual({ key: "name", dir: "desc" });
    expect(nextSort("name", "desc", "name")).toEqual({ key: "none", dir: "asc" });
  });

  it("starts a different column over at ascending", () => {
    expect(nextSort("name", "desc", "size")).toEqual({ key: "size", dir: "asc" });
    expect(nextSort("none", "asc", "size")).toEqual({ key: "size", dir: "asc" });
  });
});

describe("sortRows", () => {
  const rows = buildRows([
    mk("alpha-Q4_K_M", { sizeGB: 30, estVramGB: 32, estRamGB: 4 }),
    mk("beta-Q4_K_M", { sizeGB: 10, estVramGB: 12 }),
    mk("zeta-Q8_0", { sizeGB: 20, estVramGB: 22, state: "ready" }),
  ]);

  it("floats loaded models above the sort, in both directions", () => {
    expect(sortRows(rows, "name", "asc")[0].key).toBe("zeta");
    expect(sortRows(rows, "name", "desc")[0].key).toBe("zeta");
  });

  it("pins favorites above even the loaded model", () => {
    const favorites = new Set(["beta"]);
    expect(sortRows(rows, "name", "asc", { favorites }).map((r) => r.key)).toEqual(["beta", "zeta", "alpha"]);
  });

  it("sorts by the numeric columns", () => {
    expect(sortRows(rows, "size", "asc").map((r) => r.key)).toEqual(["zeta", "beta", "alpha"]);
    expect(sortRows(rows, "vram", "desc").map((r) => r.key)).toEqual(["zeta", "alpha", "beta"]);
  });

  it("treats a missing figure as lowest, not as zero-equal", () => {
    // beta has no estRamGB: it must not tie with alpha's 4.
    expect(sortRows(rows, "ram", "desc").map((r) => r.key)).toEqual(["zeta", "alpha", "beta"]);
  });

  it("keeps the catalog order under 'none', with the pins still applied", () => {
    expect(sortRows(rows, "none", "asc").map((r) => r.key)).toEqual(["zeta", "alpha", "beta"]);
    expect(sortRows(rows, "none", "asc", { favorites: new Set(["beta"]) }).map((r) => r.key)).toEqual(["beta", "zeta", "alpha"]);
  });

  it("does not mutate the input", () => {
    const before = rows.map((r) => r.key);
    sortRows(rows, "size", "desc");
    expect(rows.map((r) => r.key)).toEqual(before);
  });
});

describe("groupFamilies", () => {
  const rows = buildRows([
    mk("thinkingcap-qwen3.6-27b-q4_k_m-mtp-q4_k_m"),
    mk("gemma-4-12b-it-UD-Q8_K_XL"),
    mk("qwen3.6-27b-ud-q4_k_xl", { state: "ready" }),
  ]);

  it("clusters finetunes of one base model without reordering them", () => {
    const sorted = sortRows(rows, "none", "asc");
    const groups = groupFamilies(sorted);
    expect(groups.map((g) => g.key)).toEqual(["qwen3.6-27b", "gemma-4-12b"]); // loaded family first
    expect(groups[0].rows.map((r) => r.key)).toEqual(["qwen3.6-27b", "thinkingcap-qwen3.6-27b"]);
    expect(groups[0].label).toBe("Qwen3.6 27b");
  });

  it("leaves a lone model as a group of one", () => {
    expect(groupFamilies(rows).find((g) => g.key === "gemma-4-12b")!.rows).toHaveLength(1);
  });
});

describe("stripQuantCrumbs", () => {
  it("drops quant fragments the id scrub left in the display name", () => {
    expect(stripQuantCrumbs("Thinkingcap Qwen3.6 27b K M")).toBe("Thinkingcap Qwen3.6 27b");
    expect(stripQuantCrumbs("Qwen3.6 27b UD Q4 K XL")).toBe("Qwen3.6 27b");
  });

  it("leaves real name tails alone", () => {
    expect(stripQuantCrumbs("Qwen3.6 27b Uncensored Heretic V2 Native Mtp Preserved")).toBe("Qwen3.6 27b Uncensored Heretic V2 Native Mtp Preserved");
    expect(stripQuantCrumbs("Gemma 4 12b It")).toBe("Gemma 4 12b It");
  });

  it("never strips a name down to nothing", () => {
    expect(stripQuantCrumbs("Q8")).toBe("Q8");
  });
});

describe("fmtGB", () => {
  it("renders one decimal, an em dash for absent", () => {
    expect(fmtGB(18.64)).toBe("18.6");
    expect(fmtGB(undefined)).toBe("-");
    expect(fmtGB(0)).toBe("-");
    expect(fmtGB(128)).toBe("128");
  });
});
