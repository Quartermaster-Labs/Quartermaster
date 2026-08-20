import { describe, it, expect } from "vitest";
import { splitForSpeech } from "./speechApi";

describe("splitForSpeech", () => {
  it("keeps short text as one chunk", () => {
    expect(splitForSpeech("Hello there. How are you?")).toEqual(["Hello there. How are you?"]);
  });

  it("emits a small first chunk so audio starts fast", () => {
    const text = Array.from({ length: 20 }, (_, i) => `Sentence number ${i} goes here.`).join(" ");
    const chunks = splitForSpeech(text, 60, 200);
    expect(chunks.length).toBeGreaterThan(2);
    expect(chunks[0].length).toBeLessThanOrEqual(60);
    for (const c of chunks) expect(c.length).toBeLessThanOrEqual(200);
  });

  it("splits on sentence boundaries, not mid-word", () => {
    const chunks = splitForSpeech("One two three. Four five six. Seven eight nine.", 20, 20);
    expect(chunks).toEqual(["One two three.", "Four five six.", "Seven eight nine."]);
  });

  it("breaks an unpunctuated wall of text on whitespace", () => {
    const text = "word ".repeat(80).trim();
    const chunks = splitForSpeech(text, 50, 100);
    expect(chunks.length).toBeGreaterThan(1);
    for (const c of chunks) expect(c).not.toMatch(/^\s|\s$/);
    expect(chunks.join(" ")).toBe(text);
  });

  it("loses no text", () => {
    const text = "Alpha beta. Gamma delta epsilon!\n\nZeta eta theta? Iota kappa.";
    expect(splitForSpeech(text, 15, 25).join(" ").replace(/\s+/g, " "))
      .toBe(text.replace(/\s+/g, " "));
  });

  it("returns nothing for empty input", () => {
    expect(splitForSpeech("   ")).toEqual([]);
  });
});
