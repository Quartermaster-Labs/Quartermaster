import { describe, it, expect } from "vitest";
import { speechWords, alignChunks } from "./speechHighlight";

// The DOM half needs a browser (TreeWalker, Range, CSS.highlights); the aligner
// is the part that can be wrong in interesting ways, and it is pure.
const align = (dom: string, chunks: string[]) =>
  alignChunks(speechWords(dom), chunks.map(speechWords));

describe("speechHighlight", () => {
  it("reduces both sides to comparable words", () => {
    expect(speechWords("**Note:** it's 42% — see [1].")).toEqual(["note", "it", "s", "42", "see", "1"]);
  });

  it("walks chunks forward through the document", () => {
    const dom = "One two three. Four five six. Seven eight nine.";
    expect(align(dom, ["One two three.", "Four five six.", "Seven eight nine."])).toEqual([
      { start: 0, end: 2 },
      { start: 3, end: 5 },
      { start: 6, end: 8 },
    ]);
  });

  // The rendered reply carries text the speech never gets: citation chips, code
  // spans, table cells. A chunk has to survive words appearing mid-span.
  it("tolerates on-screen words the speech dropped", () => {
    const spans = align("Kokoro is 1 small and 2 fast.", ["Kokoro is small and fast."]);
    expect(spans[0]).toEqual({ start: 0, end: 6 });
  });

  // Without a monotonic cursor a repeated sentence highlights the FIRST copy
  // every time and the marker jumps backwards mid-read.
  it("never matches behind the reader", () => {
    const dom = "Same line here. Different bit. Same line here.";
    const spans = align(dom, ["Same line here.", "Different bit.", "Same line here."]);
    expect(spans[0]).toEqual({ start: 0, end: 2 });
    expect(spans[2]).toEqual({ start: 5, end: 7 });
  });

  // A chunk that came from something not rendered as prose (a dropped code
  // block) must not drag the cursor along, or every later chunk misses too.
  it("gives up on a chunk it cannot place, then recovers", () => {
    const spans = align("Alpha beta gamma. Delta epsilon zeta.", [
      "Alpha beta gamma.",
      "Nothing like this on screen at all.",
      "Delta epsilon zeta.",
    ]);
    expect(spans[0]).toEqual({ start: 0, end: 2 });
    expect(spans[1]).toBeNull();
    expect(spans[2]).toEqual({ start: 3, end: 5 });
  });

  it("returns one span per chunk, empty chunks included", () => {
    expect(align("Alpha beta.", ["Alpha beta.", "", "…"])).toHaveLength(3);
  });
});
