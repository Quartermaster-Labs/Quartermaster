import { describe, it, expect } from "vitest";
import {
  resolvePromptVars,
  resolveSubPrompt,
  buildBasePrompt,
  DEFAULT_BUILTIN_PROMPT,
  DEFAULT_SEARCH_PROMPT,
  DEFAULT_CITE_PROMPT,
  type SystemPreset,
} from "./systemPrompt";

describe("resolvePromptVars", () => {
  it("substitutes {model} and leaves prose intact", () => {
    expect(resolvePromptVars("Model is {model}.", "qwen3")).toBe("Model is qwen3.");
  });

  it("replaces every occurrence of a variable", () => {
    expect(resolvePromptVars("{model} {model}", "x")).toBe("x x");
  });

  it("falls back when model is empty", () => {
    expect(resolvePromptVars("{model}", "")).toBe("the selected model");
  });

  it("expands {date} and {time} to non-empty, non-literal values", () => {
    const out = resolvePromptVars("d={date} t={time}", "m");
    expect(out).not.toContain("{date}");
    expect(out).not.toContain("{time}");
  });

  it("leaves unknown braces untouched", () => {
    expect(resolvePromptVars("keep {foo}", "m")).toBe("keep {foo}");
  });

  it("default prompt carries no template variables (KV-stable)", () => {
    expect(DEFAULT_BUILTIN_PROMPT).not.toMatch(/\{(date|time|model)\}/);
  });
});

describe("buildBasePrompt", () => {
  const preset = (over: Partial<SystemPreset>): SystemPreset => ({ id: "a", name: "A", content: "Be {model}.", search: null, wiki: null, cite: null, ...over });
  const off = { search: false, wiki: false, model: "m" };

  it("null → built-in default persona, no tools when tools off", () => {
    expect(buildBasePrompt(null, [], off)).toBe(DEFAULT_BUILTIN_PROMPT);
  });

  it('"" → no persona, and no tools when off', () => {
    expect(buildBasePrompt("", [], off)).toBe("");
  });

  it("preset persona resolves vars", () => {
    expect(buildBasePrompt("a", [preset({})], { ...off, model: "qwen" })).toBe("Be qwen.");
  });

  it("missing/deleted preset id → falls back to default persona", () => {
    expect(buildBasePrompt("gone", [], off)).toBe(DEFAULT_BUILTIN_PROMPT);
  });

  it("preset with null tool field uses the shipped default when tool on", () => {
    expect(buildBasePrompt("a", [preset({ content: "" })], { search: true, wiki: false, model: "m" })).toBe(
      DEFAULT_SEARCH_PROMPT + " " + DEFAULT_CITE_PROMPT,
    );
  });

  it("preset custom tool field overrides the default", () => {
    const out = buildBasePrompt("a", [preset({ content: "", search: "SEARCH-X", cite: "CITE-X" })], { search: true, wiki: false, model: "m" });
    expect(out).toBe("SEARCH-X CITE-X");
  });

  it("fixed default option still gets default tool prompts when tool on", () => {
    const out = buildBasePrompt(null, [], { search: true, wiki: false, model: "m" });
    expect(out).toBe(DEFAULT_BUILTIN_PROMPT + " " + DEFAULT_SEARCH_PROMPT + " " + DEFAULT_CITE_PROMPT);
  });
});

describe("resolveSubPrompt", () => {
  it("null → default verbatim", () => {
    expect(resolveSubPrompt(null, "DEF", "m")).toBe("DEF");
  });

  it("empty/whitespace → omitted", () => {
    expect(resolveSubPrompt("", "DEF", "m")).toBe("");
    expect(resolveSubPrompt("   ", "DEF", "m")).toBe("");
  });

  it("custom string → used with vars resolved", () => {
    expect(resolveSubPrompt("use {model}", "DEF", "qwen")).toBe("use qwen");
  });
});
