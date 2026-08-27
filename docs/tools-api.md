<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Tools API (search & YouTube for your own apps)

Besides chat, Quartermaster exposes the same tool **executors** the playground uses - web search and YouTube - as plain HTTP endpoints, so your own scripts, agents, and apps can call them instead of wiring up SearXNG, DuckDuckGo, or yt-dlp themselves.

They live on the `/v1` surface, so they are gated by the **same API keys** as inference (see *API keys and access*). Any valid key works; model scopes do not apply.

**Endpoints** (all `POST`, JSON in and out):

- `POST /v1/tools/search` - `{"query": "...", "limit": 5, "providers": [...]}` → `{"provider": "...", "results": [{title, url, content}]}`. **Providers are passed per call** - no config needed: `{"id":"duckduckgo"}` works keyless; also `{"id":"searxng","baseUrl":"http://localhost:8080"}`, `{"id":"brave","key":"…"}`, `{"id":"tavily","key":"…"}`, `{"id":"google","key":"…","cx":"…"}`. The chain fails over until one answers. `limit` is optional and clamped to a sane ceiling.
- `POST /v1/tools/youtube/transcript` - `{"url": "https://youtu.be/…"}` (or a bare 11-character video id), optional `lang` → `{id, title, uploader, duration, text}` with `[m:ss]` timestamps. Returns the **full** transcript - long videos come back long.
- `POST /v1/tools/youtube/search` - `{"query": "…"}` or `{"channel": "@handle"}` (optional `tab`, `limit`) → `{"videos": [{id, title, channel, duration, views, …}]}`.
- `POST /v1/tools/youtube/comments` - `{"url": "…"}` + `limit` → `{"video": {…}, "comments": [{author, text, likes, pinned, by_owner}]}`.

**Requirements** - the same as chat: the YouTube endpoints need **yt-dlp** (they answer `503` with an install hint when it is missing); transcripts need a video that actually has captions.

**Errors** are OpenAI-shaped `{"error": {"message": "…"}}`: `400` bad arguments, `502` upstream failure, `503` yt-dlp missing.

Typical use: your app keeps its own tool schemas for the model and executes each tool call here. Example:

```
curl http://127.0.0.1:1250/v1/tools/search \
  -H "Authorization: Bearer qm-..." \
  -H "Content-Type: application/json" \
  -d '{"query":"llama.cpp release","providers":[{"id":"duckduckgo"}]}'
```
