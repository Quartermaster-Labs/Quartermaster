import { describe, it, expect } from "vitest";
import { searchWiki, formatWikiResults, groupWikiArticles, WIKI_ARTICLES } from "./wiki";

describe("searchWiki", () => {
  it("ranks the most relevant article first", () => {
    expect(searchWiki("how do I set up web search")[0].id).toBe("web-search");
    expect(searchWiki("my model won't load, out of memory")[0].id).toMatch(/gpu-memory|troubleshooting|loading-models/);
    expect(searchWiki("blurry images cfg")[0].id).toBe("images");
  });

  it("returns nothing for a query with no overlapping terms", () => {
    expect(searchWiki("xyzzy qwerty")).toEqual([]);
    expect(searchWiki("")).toEqual([]);
  });

  it("caps results", () => {
    // 'model' appears in many articles; still bounded.
    expect(searchWiki("model config vram context").length).toBeLessThanOrEqual(3);
  });
});

describe("groupWikiArticles", () => {
  it("places every article in exactly one group - no orphaned 'More'", () => {
    const groups = groupWikiArticles(WIKI_ARTICLES);
    expect(groups.some((g) => g.title === "More")).toBe(false);
    const grouped = groups.flatMap((g) => g.items.map((a) => a.id)).sort();
    expect(grouped).toEqual(WIKI_ARTICLES.map((a) => a.id).sort());
  });

  it("drops empty groups when given a filtered subset", () => {
    const groups = groupWikiArticles(searchWiki("web search searxng"));
    expect(groups.length).toBeGreaterThan(0);
    expect(groups.every((g) => g.items.length > 0)).toBe(true);
  });
});

describe("formatWikiResults", () => {
  it("lists all topics on a miss so the model can steer", () => {
    const out = formatWikiResults("xyzzy", [], []);
    expect(out).toContain("No wiki article matched");
    for (const a of WIKI_ARTICLES) expect(out).toContain(a.title);
  });

  it("emits each hit's title and body", () => {
    const hits = searchWiki("web search searxng");
    const out = formatWikiResults("web search", hits, hits.map((_, i) => i + 1));
    expect(out).toContain(hits[0].title);
    expect(out).toContain("SearXNG");
  });
});
