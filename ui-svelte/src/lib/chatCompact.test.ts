import { describe, it, expect } from "vitest";
import { cleanTitle } from "./chatCompact";

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
