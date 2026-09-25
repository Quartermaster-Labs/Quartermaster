import { describe, it, expect } from "vitest";
import { cleanTitle, compactInPlacePrompt } from "./chatCompact";

describe("cleanTitle", () => {
  it("returns a plain title unchanged", () => {
    expect(cleanTitle("Quantum computing basics")).toBe("Quantum computing basics");
  });

  it("strips a closed reasoning block", () => {
    expect(cleanTitle("<think>hmm the user asks about cats</think>\nCat care tips")).toBe("Cat care tips");
  });

  it("strips an unclosed reasoning block (truncated output)", () => {
    expect(cleanTitle("<think>the user wants me to think a lot and i never finished")).toBe("");
  });

  it("strips wrapping quotes", () => {
    expect(cleanTitle('"React state bug"')).toBe("React state bug");
  });

  it("caps at 48 chars", () => {
    expect(cleanTitle("a".repeat(80)).length).toBe(48);
  });
});

describe("compactInPlacePrompt", () => {
  it("names the kept tail by a collapsed, capped snippet", () => {
    const p = compactInPlacePrompt(false, "  Now   write\nthe tests " + "x".repeat(300));
    expect(p).toContain('begins "Now write the tests ');
    expect(p).not.toContain("x".repeat(120));
    expect(p).not.toContain("Summary of earlier conversation");
  });

  it("folds in the prior summary only when there is one", () => {
    expect(compactInPlacePrompt(true, "hi")).toContain("Summary of earlier conversation");
  });

  it("drops the boundary clause for a text-less kept message", () => {
    expect(compactInPlacePrompt(false, "   ")).not.toContain("begins");
  });
});
