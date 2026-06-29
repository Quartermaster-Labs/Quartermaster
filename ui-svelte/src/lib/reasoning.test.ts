import { describe, it, expect } from "vitest";
import { harmonyToThink } from "./reasoning";

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
