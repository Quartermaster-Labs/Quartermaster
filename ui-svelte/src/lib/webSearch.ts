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
        count: {
          type: "integer",
          description:
            "How many results to return (1-10, default 5). Ask for more when you are comparing options or gathering a shortlist, fewer when you need a single fact.",
        },
      },
      required: ["query"],
    },
  },
};

const MAX_RESULTS = 5;
const SNIPPET_MAX = 400;

// --- providers -------------------------------------------------------------
//
// Web search is an ordered failover chain, not one endpoint: SearXNG's public
// engines are scrapers that answer burst traffic with a CAPTCHA, and an agent
// loop out-runs that threshold, so the user sees timeouts. The chain keeps
// SearXNG first (free, local) and hands off to a keyed API only when the free
// path failed. Dispatch lives server-side in internal/server/search.go — this
// module owns the config shape and the Test button.

export type SearchProviderId = "searxng" | "brave" | "tavily" | "duckduckgo" | "google";

export interface SearchProviderCfg {
  id: SearchProviderId;
  enabled: boolean;
  baseUrl?: string; // searxng
  key?: string; // brave / tavily / google
  cx?: string; // google programmable-search engine id
}

export interface SearchProviderMeta {
  id: SearchProviderId;
  label: string;
  needs: "url" | "key" | "key+cx" | "none";
  hint: string;
  signupUrl?: string;
}

// Order here is the DEFAULT chain order: free/local first, hard-capped last.
export const SEARCH_PROVIDER_META: SearchProviderMeta[] = [
  {
    id: "searxng",
    label: "SearXNG",
    needs: "url",
    hint: "Self-hosted, keyless. Needs JSON format enabled. Fast when its engines aren't rate-limited.",
  },
  {
    id: "brave",
    label: "Brave Search",
    needs: "key",
    hint: "Real JSON API, 2,000 queries/month free. The most reliable failover under an agent loop.",
    signupUrl: "https://brave.com/search/api/",
  },
  {
    id: "tavily",
    label: "Tavily",
    needs: "key",
    hint: "Built for LLMs - returns extracted page text, not just a snippet. ~1,000 credits/month free.",
    signupUrl: "https://tavily.com/",
  },
  {
    id: "duckduckgo",
    label: "DuckDuckGo",
    needs: "none",
    hint: "Keyless HTML scrape. No quota, but the same bot-challenge exposure as SearXNG - last resort only.",
  },
  {
    id: "google",
    label: "Google Programmable Search",
    needs: "key+cx",
    hint: "Best results, hardest cap: 100 queries/day free. Needs an API key and a search-engine id (cx).",
    signupUrl: "https://programmablesearch.google.com/",
  },
];

export const DEFAULT_SEARCH_PROVIDERS: SearchProviderCfg[] = SEARCH_PROVIDER_META.map((m) => ({
  id: m.id,
  enabled: m.id === "searxng",
}));

// normalizeProviders repairs a stored list: unknown ids dropped, missing rows
// appended (so a provider added in a later version shows up without the user
// resetting), and the SearXNG row seeded from the legacy standalone URL pref.
// Stored ORDER wins — it is the failover order the user arranged.
export function normalizeProviders(stored: unknown, legacySearxngUrl = ""): SearchProviderCfg[] {
  const known = new Map(SEARCH_PROVIDER_META.map((m) => [m.id, m]));
  const rows: SearchProviderCfg[] = [];
  const seen = new Set<SearchProviderId>();
  for (const raw of Array.isArray(stored) ? stored : []) {
    const o = raw as Record<string, unknown>;
    const id = String(o?.id ?? "") as SearchProviderId;
    if (!known.has(id) || seen.has(id)) continue;
    seen.add(id);
    rows.push({
      id,
      enabled: !!o.enabled,
      baseUrl: typeof o.baseUrl === "string" ? o.baseUrl : undefined,
      key: typeof o.key === "string" ? o.key : undefined,
      cx: typeof o.cx === "string" ? o.cx : undefined,
    });
  }
  for (const m of SEARCH_PROVIDER_META) {
    if (!seen.has(m.id)) rows.push({ id: m.id, enabled: rows.length === 0 && m.id === "searxng" });
  }
  const searx = rows.find((r) => r.id === "searxng");
  if (searx && !searx.baseUrl?.trim() && legacySearxngUrl.trim()) searx.baseUrl = legacySearxngUrl.trim();
  return rows;
}

// providerReady mirrors searchProviderCfg.ready() in internal/server/search.go:
// an enabled row missing its credentials is not a chain hop, it is a no-op.
export function providerReady(c: SearchProviderCfg): boolean {
  const meta = SEARCH_PROVIDER_META.find((m) => m.id === c.id);
  if (!meta || !c.enabled) return false;
  switch (meta.needs) {
    case "url":
      return !!c.baseUrl?.trim();
    case "key":
      return !!c.key?.trim();
    case "key+cx":
      return !!c.key?.trim() && !!c.cx?.trim();
    default:
      return true;
  }
}

// searchViaChain runs one provider (or a whole chain) through the server.
// POST, not GET: the body carries API keys, and a query string lands in the
// access log.
export async function searchViaChain(
  providers: SearchProviderCfg[],
  query: string,
  limit = MAX_RESULTS,
  signal?: AbortSignal
): Promise<{ provider: string; results: SearchResult[] }> {
  const resp = await fetch("/api/websearch", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ providers, q: query, limit }),
    signal,
  });
  if (!resp.ok) {
    const detail = await resp.text();
    throw new Error(detail.trim() || `web search ${resp.status}`);
  }
  const json = await resp.json();
  const results: unknown[] = Array.isArray(json?.results) ? json.results : [];
  return {
    provider: String(json?.provider ?? ""),
    results: results.slice(0, limit).map((r): SearchResult => {
      const o = r as Record<string, unknown>;
      return {
        title: String(o.title ?? ""),
        url: String(o.url ?? ""),
        content: String(o.content ?? "").slice(0, SNIPPET_MAX),
      };
    }),
  };
}

// Render results as the plain-text tool message fed back to the model.
// `numbers[i]` is the citation number for `results[i]` — resolved by the
// caller so a URL repeated across searches in the same turn reuses its
// earlier number instead of minting a duplicate.
//
// The header stamps today's date. The system prompt states it too, but that line
// is at the end of a long prefix and models still reach for their training-year;
// on the result it sits right where freshness is being judged. It deliberately
// does NOT go in the tool description — that lives in the stable prompt prefix
// and would invalidate every conversation's KV cache at midnight.
// Mirrored server-side in internal/server/turnstools.go (formatSearchResults).
export function formatSearchResults(query: string, results: SearchResult[], numbers: number[]): string {
  const date = new Date().toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric" });
  if (results.length === 0) return `No results found for "${query}". (Searched ${date}.)`;
  const lines = results.map(
    (r, i) => `[${numbers[i]}] ${r.title}\n${r.url}\n${r.content}`
  );
  return `Search results for "${query}" (searched ${date} - today's date, use it, not a year from memory, when a query is time-sensitive):\n\n${lines.join("\n\n")}`;
}
