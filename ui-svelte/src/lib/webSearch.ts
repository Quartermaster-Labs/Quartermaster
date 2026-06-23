import type { ToolDef } from "./types";

export interface SearchResult {
  title: string;
  url: string;
  content: string;
}

// Advertised to the model via the chat-completions `tools` param.
export const WEB_SEARCH_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "web_search",
    description:
      "Search the web for current or factual information. Use when the answer may be recent, niche, or beyond your training data. Returns the top results with title, URL and a snippet.",
    parameters: {
      type: "object",
      properties: {
        query: { type: "string", description: "The search query" },
      },
      required: ["query"],
    },
  },
};

const MAX_RESULTS = 5;
const SNIPPET_MAX = 400;

// Query SearXNG via the same-origin Go proxy (/api/websearch), which forwards to
// <baseUrl>/search?format=json. The proxy avoids browser CORS — SearXNG ships no
// CORS headers, so a direct browser fetch is blocked. SearXNG still needs its
// JSON format enabled (`formats: [html, json]`).
export async function searxngSearch(
  baseUrl: string,
  query: string,
  signal?: AbortSignal
): Promise<SearchResult[]> {
  const trimmed = baseUrl.trim().replace(/\/+$/, "");
  if (!trimmed) throw new Error("SearXNG URL not set");
  const u = new URL("/api/websearch", window.location.origin);
  u.searchParams.set("url", trimmed);
  u.searchParams.set("q", query);

  const resp = await fetch(u.toString(), { signal });
  if (!resp.ok) {
    const detail = await resp.text();
    throw new Error(detail.trim() || `web search ${resp.status}`);
  }
  const json = await resp.json();
  const results: unknown[] = Array.isArray(json?.results) ? json.results : [];
  return results.slice(0, MAX_RESULTS).map((r): SearchResult => {
    const o = r as Record<string, unknown>;
    return {
      title: String(o.title ?? ""),
      url: String(o.url ?? ""),
      content: String(o.content ?? "").slice(0, SNIPPET_MAX),
    };
  });
}

// Render results as the plain-text tool message fed back to the model.
export function formatSearchResults(query: string, results: SearchResult[]): string {
  if (results.length === 0) return `No results found for "${query}".`;
  const lines = results.map(
    (r, i) => `[${i + 1}] ${r.title}\n${r.url}\n${r.content}`
  );
  return `Search results for "${query}":\n\n${lines.join("\n\n")}`;
}
