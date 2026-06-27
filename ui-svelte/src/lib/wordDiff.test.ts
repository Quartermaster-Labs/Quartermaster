import { describe, it, expect } from "vitest";
import { diffWords } from "./wordDiff";

// Reconstruct each side from the ops to prove the diff is lossless.
const left = (ops: ReturnType<typeof diffWords>) =>
  ops.filter((o) => o.type !== "insert").map((o) => o.value).join("");
const right = (ops: ReturnType<typeof diffWords>) =>
  ops.filter((o) => o.type !== "delete").map((o) => o.value).join("");

describe("diffWords", () => {
  it("returns all-equal for identical text", () => {
    const ops = diffWords("the cat sat", "the cat sat");
    expect(ops.every((o) => o.type === "equal")).toBe(true);
    expect(left(ops)).toBe("the cat sat");
  });

  it("is lossless: left reconstructs original, right reconstructs rewritten", () => {
    const a = "The quick brown fox jumps over the lazy dog.";
    const b = "A quick brown fox leaps over a sleepy dog!";
    const ops = diffWords(a, b);
    expect(left(ops)).toBe(a);
    expect(right(ops)).toBe(b);
  });

  it("marks a replaced word as delete+insert, keeps surrounding equal", () => {
    const ops = diffWords("make it formal", "make it casual");
    expect(ops.some((o) => o.type === "delete" && o.value.includes("formal"))).toBe(true);
    expect(ops.some((o) => o.type === "insert" && o.value.includes("casual"))).toBe(true);
    expect(ops.some((o) => o.type === "equal" && o.value.includes("make"))).toBe(true);
  });

  it("handles empty original (pure insert)", () => {
    const ops = diffWords("", "hello world");
    expect(left(ops)).toBe("");
    expect(right(ops)).toBe("hello world");
    expect(ops.every((o) => o.type === "insert")).toBe(true);
  });

  it("merges adjacent same-type runs", () => {
    const ops = diffWords("one two three", "");
    expect(ops.length).toBe(1);
    expect(ops[0].type).toBe("delete");
  });
});
