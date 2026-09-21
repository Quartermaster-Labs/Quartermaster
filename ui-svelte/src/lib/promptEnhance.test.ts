import { describe, it, expect } from "vitest";
import { cleanEnhanced } from "./promptEnhance";

describe("cleanEnhanced", () => {
  it("passes a plain prompt through untouched", () => {
    expect(cleanEnhanced("a red fox in snow, 35mm")).toBe("a red fox in snow, 35mm");
  });

  it("drops a leaked reasoning block", () => {
    expect(cleanEnhanced("<think>the user wants a fox</think>\na red fox in snow")).toBe("a red fox in snow");
  });

  it("unwraps a fenced block", () => {
    expect(cleanEnhanced("```\na red fox in snow\n```")).toBe("a red fox in snow");
    expect(cleanEnhanced("```text\na red fox in snow\n```")).toBe("a red fox in snow");
  });

  it("drops a leading label line", () => {
    expect(cleanEnhanced("Enhanced prompt: a red fox in snow")).toBe("a red fox in snow");
    expect(cleanEnhanced("Rewritten prompt:\na red fox in snow")).toBe("a red fox in snow");
  });

  it("unwraps quotes only when they wrap the whole thing", () => {
    expect(cleanEnhanced('"a red fox in snow"')).toBe("a red fox in snow");
    // The quotes here are CONTENT: stripping them would change what gets
    // rendered on the sign, which is the whole point of the prompt.
    const sign = '"OPEN" on a shop sign, neon, "24h" below it';
    expect(cleanEnhanced(sign)).toBe(sign);
  });

  it("keeps a colon that is part of the prompt", () => {
    const s = "a diptych: left a fox, right a wolf";
    expect(cleanEnhanced(s)).toBe(s);
  });

  it("returns empty for a response with no content left", () => {
    expect(cleanEnhanced("<think>hmm</think>")).toBe("");
    expect(cleanEnhanced("   ")).toBe("");
  });
});
