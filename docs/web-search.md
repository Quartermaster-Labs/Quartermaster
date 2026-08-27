<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Web search

Chat can let the model search the web for fresh or niche facts. Search is an **ordered failover chain**, not a single endpoint: providers are tried top-first, and if one times out, gets rate-limited or returns nothing, the next one runs. That matters because the free, keyless options are scrapers - an agent loop out-runs their bot thresholds and starts collecting CAPTCHAs.

Configure it in playground **Settings -> Web Search**:
1. Toggle **Web Search** on (the model must support **tool calling**).
2. Enable the providers you want, fill in what each needs, and **Test** each row.
3. Order them with the **arrows** - that's the failover order. Keyed providers only spend quota when everything above them failed, so put free ones first.

Providers:
- **SearXNG** - self-hosted, keyless. Give it your base URL (e.g. `http://localhost:8888`); it must have **JSON format enabled** (`formats: [html, json]`). Fast when its engines aren't rate-limited.
- **Brave Search** - real JSON API, 2,000 queries/month free (sign up at https://brave.com/search/api/). The most reliable failover under an agent loop.
- **Tavily** - built for LLMs, returns extracted page text rather than a snippet. ~1,000 credits/month free (sign up at https://tavily.com/).
- **DuckDuckGo** - keyless HTML scrape, no quota, but the same bot-challenge exposure as SearXNG. Last resort.
- **Google Programmable Search** - best results, hardest cap (100 queries/day free). Needs an API key *and* a search-engine id (cx), from https://programmablesearch.google.com/.

Knobs:
- **Max / Turn** caps searches per message - once hit, the model has to answer with what it found.
- **Throttle ms** spaces requests so a rate limiter doesn't trip.
- **Dedupe Queries** reuses the result when the model repeats a query within a turn.

**Reading pages.** Turning web search on also gives the model *fetch page*, so it can open a result and read the real thing instead of answering from a snippet. Results are numbered and come back as citation chips under the answer.

Troubleshooting: a bare "Failed to fetch" when testing SearXNG is almost always a wrong host/port or CORS. If every provider is off or half-configured the panel warns you - a search with nothing to run it on fails the tool call rather than silently returning nothing.

**Standing up SearXNG.** It is a Python app plus a Redis/Valkey cache, so the easy path is Docker:
```
docker run -d --name valkey valkey/valkey:alpine
docker run -d --name searxng -p 8888:8080 --link valkey -v ./searxng:/etc/searxng searxng/searxng
```
Then set `formats: [html, json]` under `search:` in `searxng/settings.yml` (JSON is off by default and quartermaster needs it), restart, and check by hand with `curl "http://localhost:8888/search?q=test&format=json"` - an HTML body back means the setting did not take. A bare Python install in a VM or WSL works the same way; only the URL matters. Queries are throttled to one every 1.5s process-wide with a 10-minute cache, so an agent loop cannot hammer your instance.

**Keys are per playground user**, sent on the turn payload and never written into a chat. The browser never talks to a provider directly - every query goes out through quartermaster, so there is no CORS to fight and no key sitting in client-side JS. An enabled row missing its credentials is skipped rather than attempted, so a half-configured provider costs nothing.

More symptoms: `all search providers failed (...)` lists each provider's own error in the parenthesis - read it. Brave `422` / "subscription token invalid" is a wrong or unsubscribed key, passed through verbatim. Google "Daily Limit Exceeded" is the 100/day free cap; move it lower in the chain. DuckDuckGo `rate-limited (bot challenge)` has no fix - it is a fallback, not a primary.
