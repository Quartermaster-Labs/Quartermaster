import { describe, it, expect } from "vitest";
import { cleanEnhanced, parseEnhanced } from "./promptEnhance";

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

// Qwen's official PE system prompts mandate a JSON object, and these models
// deliberate in plain PROSE (no <think> tags), so the trailing object is the
// only reliable place the answer can be cut out of.
describe("parseEnhanced", () => {
  it("pulls the prompt out of a bare JSON envelope", () => {
    const r = parseEnhanced('{"rewritten_prompt": "a red fox in snow", "wh_ratio": "16:9", "ratio_follow": ""}');
    expect(r.prompt).toBe("a red fox in snow");
    expect(r.ratio).toBe("16:9");
    expect(r.ratioFollow).toBeUndefined();
    expect(r.structured).toBe(true);
  });

  it("finds the object after hundreds of tokens of deliberation", () => {
    const raw = `Let me think about what the user wants.
Pose: keep standing facing camera, arms relaxed.
\`\`\`json
{"rewritten_prompt": "a red fox in snow, 35mm"}
\`\`\``;
    expect(parseEnhanced(raw).prompt).toBe("a red fox in snow, 35mm");
  });

  it("takes the LAST object, not an echoed schema example", () => {
    const raw = `The format I must follow is {"rewritten_prompt": "<your prompt here>", "wh_ratio": "1:1"}.
So: {"rewritten_prompt": "a red fox in snow", "wh_ratio": "3:4"}`;
    const r = parseEnhanced(raw);
    expect(r.prompt).toBe("a red fox in snow");
    expect(r.ratio).toBe("3:4");
  });

  it("survives prose that happens to balance a brace", () => {
    const raw = `Consider {this} and {that}.
{"prompt": "a red fox in snow"}`;
    expect(parseEnhanced(raw).prompt).toBe("a red fox in snow");
  });

  it("falls back to the cleaner when no envelope is present", () => {
    const r = parseEnhanced("Enhanced prompt: a red fox in snow");
    expect(r.prompt).toBe("a red fox in snow");
    expect(r.structured).toBe(false);
    expect(r.ratio).toBeUndefined();
  });

  it("ignores an object whose prompt key is empty", () => {
    const r = parseEnhanced('{"rewritten_prompt": "   ", "wh_ratio": "16:9"}');
    expect(r.structured).toBe(false);
  });
});
