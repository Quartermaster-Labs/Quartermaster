import { describe, it, expect } from "vitest";
import { memoryBlock, MEMORY_BLOCK_LIMIT, type MemoryEntry } from "./memoryTools";

function mem(id: string, text: string, createdAt: number, updatedAt = createdAt): MemoryEntry {
  return { id, text, source: "assistant", createdAt, updatedAt };
}

describe("memoryBlock", () => {
  it("renders nothing for an empty memory", () => {
    expect(memoryBlock([])).toBe("");
    expect(memoryBlock([mem("a", "   ", 1)])).toBe("");
  });

  it("is append-only: a new save leaves every earlier line byte-identical", () => {
    const before = [mem("a", "Prefers metric units", 100), mem("b", "Runs an RX 7900 XTX", 200)];
    const after = [...before, mem("c", "Lives in Bucharest", 300)];
    const lines = memoryBlock(before).split("\n");
    const grown = memoryBlock(after).split("\n");
    expect(grown.slice(0, lines.length)).toEqual(lines);
    expect(grown[grown.length - 1]).toBe("- [c] Lives in Bucharest");
  });

  it("does not reshuffle when an old memory is touched", () => {
    const mems = [mem("a", "Prefers metric units", 100), mem("b", "Runs an RX 7900 XTX", 200)];
    const touched = [mem("a", "Prefers metric units", 100, 999), mems[1]];
    expect(memoryBlock(touched)).toBe(memoryBlock(mems));
  });

  it("orders by creation regardless of input order, breaking ties on id", () => {
    const out = memoryBlock([mem("z", "third", 5), mem("b", "first", 1), mem("a", "second", 5)]);
    expect(out.split("\n").slice(1)).toEqual(["- [b] first", "- [a] second", "- [z] third"]);
  });

  it("spends the budget newest-first but still renders oldest-first", () => {
    const big = "x".repeat(MEMORY_BLOCK_LIMIT - 10);
    const out = memoryBlock([mem("old", "stale fact", 1, 1), mem("new", big, 2, 2)]);
    expect(out).toContain(`- [new] ${big}`);
    expect(out).not.toContain("stale fact");
    expect(out).toContain("1 older memory is not shown here");
  });

  it("flattens newlines so one memory is one line", () => {
    const out = memoryBlock([mem("a", "two\n\nparagraphs", 1)]);
    expect(out.split("\n")).toHaveLength(2);
    expect(out).toContain("- [a] two paragraphs");
  });
});
