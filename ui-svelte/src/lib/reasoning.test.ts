import { describe, it, expect } from "vitest";
import { activityLabel, harmonyToThink, thinkSummary } from "./reasoning";

describe("thinkSummary", () => {
  it("strips stacked conversational openers", () => {
    expect(thinkSummary("Okay, so the user wants a weather forecast for Berlin.")).toBe(
      "The user wants a weather forecast for Berlin",
    );
    expect(thinkSummary("Hmm. Let me check the pricing table first.")).toBe("Check the pricing table first");
  });

  it("skips a stub opening sentence", () => {
    expect(thinkSummary("Right. They asked about VRAM headroom on a 24GB card.")).toBe(
      "They asked about VRAM headroom on a 24GB card",
    );
  });

  it("truncates on a word boundary", () => {
    const s = thinkSummary("The user is asking me to compare three different laptops on battery life and price.");
    expect(s.length).toBeLessThanOrEqual(53);
    expect(s.endsWith("…")).toBe(true);
    expect(s.startsWith("The user is asking me to compare")).toBe(true);
  });

  it("drops code fences and returns empty on nothing useful", () => {
    expect(thinkSummary("```js\nconst a = 1;\n```")).toBe("");
    expect(thinkSummary("")).toBe("");
    expect(thinkSummary("Okay.")).toBe("");
  });
});

describe("harmonyToThink", () => {
  it("leaves plain text untouched", () => {
    expect(harmonyToThink("just an answer")).toBe("just an answer");
    expect(harmonyToThink("uses <think>tags</think> already")).toBe("uses <think>tags</think> already");
  });

  it("converts a full analysis+final turn", () => {
    const raw =
      "<|channel|>analysis<|message|>I should add 2+2<|end|><|start|>assistant<|channel|>final<|message|>The answer is 4.";
    expect(harmonyToThink(raw)).toBe("<think>I should add 2+2</think>The answer is 4.");
  });

  it("treats a non-analysis channel name as reasoning", () => {
    const raw = "<|channel|>thought<|message|>Thinking Process: hmm<|end|><|channel|>final<|message|>Done.";
    expect(harmonyToThink(raw)).toBe("<think>Thinking Process: hmm</think>Done.");
  });

  it("leaves a trailing reasoning segment open (streaming)", () => {
    const raw = "<|channel|>analysis<|message|>still thinking";
    expect(harmonyToThink(raw)).toBe("<think>still thinking");
  });

  it("tolerates a missing closing pipe on control tokens", () => {
    const raw = "<|channel|>analysis<|message|>r<|end><|channel|>final<|message|>a";
    expect(harmonyToThink(raw)).toBe("<think>r</think>a");
  });

  it("keeps commentary as answer text, not reasoning", () => {
    const raw = "<|channel|>commentary<|message|>note to user";
    expect(harmonyToThink(raw)).toBe("note to user");
  });
});

describe("activityLabel", () => {
  it("returns nothing for a box that ran no tools", () => {
    expect(activityLabel([])).toBe("");
    // Instant local tools are not an activity worth naming.
    expect(activityLabel(["time", "calc", "units"])).toBe("");
  });

  it("names a read", () => {
    expect(activityLabel(["page"])).toBe("Read 1 page");
    expect(activityLabel(["page", "youtube"])).toBe("Read 2 pages");
  });

  it("names a search, counting repeats", () => {
    expect(activityLabel(["web"])).toBe("Searched the web");
    expect(activityLabel(["web", "web", "web"])).toBe("Searched the web 3×");
  });

  it("names a single-source search by its source", () => {
    expect(activityLabel(["wiki", "wiki"])).toBe("Searched the help wiki");
    expect(activityLabel(["quartermaster"])).toBe("Checked the server");
    expect(activityLabel(["youtube-search"])).toBe("Searched YouTube");
  });

  it("stays generic once the sources are mixed", () => {
    expect(activityLabel(["wiki", "web"])).toBe("Searched the web 2×");
  });

  it("combines a search and a read", () => {
    expect(activityLabel(["web", "page", "page"])).toBe("Searched and read 2 pages");
  });
});
