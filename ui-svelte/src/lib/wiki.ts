import type { ToolDef } from "./types";
import articles from "../../../internal/server/wiki_articles.json";
import categories from "./wiki-categories.json";

// The quartermaster help wiki. ONE source of truth for both the in-app Help
// button (WikiModal renders these articles) and the `wiki_search` tool the
// playground models call to answer "how do I…" questions about the platform.
// Keep bodies short and factual — they're fed verbatim into a model's context.
export interface WikiArticle {
  id: string;
  title: string;
  // Extra search terms beyond the title/body words (synonyms, symptoms).
  keywords: string[];
  // Markdown. Rendered in the Help modal; sent as plain text to the model.
  body: string;
}

// Corpus data lives in internal/server/wiki_articles.json — the ONE copy. It
// sits in the Go package because //go:embed cannot reach outside its own
// directory (the server-side wiki_search tool embeds it); Vite imports it
// from there, so there is no copy step to forget.
export const WIKI_ARTICLES: WikiArticle[] = articles as WikiArticle[];

// Sidebar grouping for the Help modal — one place, keyed by article id, so the
// articles themselves stay a flat list (search/tool don't care about groups).
// Order there is the display order; any id not listed falls under "More".
//
// JSON rather than a const here because scripts/wiki-docs.mjs renders the same
// grouping into docs/ and is plain node — it cannot import a .ts module, and a
// second hand-kept copy of the ordering is exactly the drift this file exists
// to avoid.
export const WIKI_CATEGORIES: { title: string; ids: string[] }[] = categories;

// Group articles into their display categories, keeping only groups with a
// member in `list` (so it works for both the full list and search results).
export function groupWikiArticles(list: WikiArticle[]): { title: string; items: WikiArticle[] }[] {
  const known = new Set(WIKI_CATEGORIES.flatMap((c) => c.ids));
  const groups = WIKI_CATEGORIES.map((c) => ({
    title: c.title,
    items: c.ids.map((id) => list.find((a) => a.id === id)).filter((a): a is WikiArticle => !!a),
  }));
  const orphans = list.filter((a) => !known.has(a.id));
  if (orphans.length) groups.push({ title: "More", items: orphans });
  return groups.filter((g) => g.items.length > 0);
}

// Advertised to playground models so they can answer "how does X work" about
// quartermaster instead of guessing. Local + free — no network, no rate limit.
export const WIKI_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "wiki_search",
    description:
      "Search the quartermaster help wiki for how the platform works - loading/swapping models, per-model config, the playground (chat/images/speech), web search setup, API keys, the /v1/tools API for external apps, GPU/VRAM, and troubleshooting. Use this whenever the user asks how to do something in quartermaster or hits a problem with it, so your answer matches the actual app.",
    parameters: {
      type: "object",
      properties: {
        query: {
          type: "string",
          description:
            "What to look up, e.g. 'why won't my model load' or 'set up web search'",
        },
      },
      required: ["query"],
    },
  },
};

const WIKI_MAX_RESULTS = 3;

// Score articles by term overlap (title > keywords > body) and return the best
// few in full. ponytail: naive substring scan — the wiki is ~15 short articles,
// swap for a real index only if it grows into the hundreds.
export function searchWiki(query: string): WikiArticle[] {
  const terms = query.toLowerCase().match(/[a-z0-9]+/g) ?? [];
  if (terms.length === 0) return [];
  const scored = WIKI_ARTICLES.map((a) => {
    const title = a.title.toLowerCase();
    const keys = a.keywords.join(" ").toLowerCase();
    const body = a.body.toLowerCase();
    let score = 0;
    for (const t of terms) {
      if (title.includes(t)) score += 3;
      if (keys.includes(t)) score += 2;
      if (body.includes(t)) score += 1;
    }
    return { a, score };
  }).filter((s) => s.score > 0);
  scored.sort((x, y) => y.score - x.score);
  return scored.slice(0, WIKI_MAX_RESULTS).map((s) => s.a);
}

// Plain-text tool message fed back to the model. On a miss, list the topics so
// the model can steer the user or retry with a better query. `numbers[i]` is
// the citation number for `results[i]` — resolved by the caller so an article
// repeated across searches in the same turn reuses its earlier number instead
// of minting a duplicate (a wiki "[n]" opens the Help modal to that article).
export function formatWikiResults(
  query: string,
  results: WikiArticle[],
  numbers: number[],
): string {
  if (results.length === 0) {
    const topics = WIKI_ARTICLES.map((a) => `- ${a.title}`).join("\n");
    return `No wiki article matched "${query}". Available topics:\n${topics}`;
  }
  return results
    .map((a, i) => `## [${numbers[i]}] ${a.title}\n${a.body}`)
    .join("\n\n---\n\n");
}
