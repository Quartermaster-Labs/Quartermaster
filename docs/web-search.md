# Enable web search

The playground can hand the model a `web_search` tool it calls mid-conversation, plus
`fetch_page` to read one of the results in full. Search itself is **not** a single endpoint —
it is an ordered **provider chain** with failover, configured per playground user.

Nothing is enabled by default: with no provider configured the tool reports
`no web search provider configured` instead of guessing.

## Why a chain

SearXNG is the right default — local, keyless, no quota — but its public engines are HTML
scrapers. They answer burst traffic with a CAPTCHA, SearXNG then suspends the engine on
exponential backoff, and the remaining engines are what the query waits on. An agent tool
loop out-runs that threshold trivially, so the failure you actually see is a *timeout*, not
an error page — and with one provider that timeout ends the tool call.

So: providers are tried in order, each with its own budget. One that errors, times out, or
returns nothing hands off to the next. Keep the free/local one first; keyed APIs sit behind
it as backup quota that is only spent when the free path failed.

Implementation: `internal/server/search.go` (dispatch, cache), `internal/server/websearch.go`
(the `/api/websearch` proxy), `ui-svelte/src/lib/webSearch.ts` (config shape + tool def).

## Providers

| Provider | Needs | Free tier | Notes |
|---|---|---|---|
| **SearXNG** | Instance URL | unlimited (self-hosted) | Keyless, local. Needs JSON format enabled. Fast when its engines aren't rate-limited. |
| **Brave Search** | API key | ~2,000 queries/month | Real JSON API. The most reliable failover under an agent loop. [Sign up](https://brave.com/search/api/) |
| **Tavily** | API key | ~1,000 credits/month | Built for LLMs — returns extracted page text, not just a snippet, so it often saves a `fetch_page` round trip. [Sign up](https://tavily.com/) |
| **DuckDuckGo** | nothing | unlimited-ish | Keyless HTML scrape. Same bot-challenge exposure as SearXNG — last resort only. |
| **Google Programmable Search** | API key + engine id (`cx`) | 100 queries/day | Best results, hardest cap — belongs late in a chain, never first. [Sign up](https://programmablesearch.google.com/) |

An **enabled row missing its credentials is skipped**, not attempted: a half-configured
provider should cost nothing, not a hop.

Keys are stored per playground user and sent on the turn payload. They are **never written
into a chat**, and the browser never talks to a provider directly — every query goes through
quartermaster (no CORS, no key in client-side JS).

## Configure it

1. Open the playground (its own port, `-playground-port`) and log in.
2. Side rail → **Settings** → **Web Search**.
3. Tick the providers you want, fill in URL/key/`cx`.
4. Order the rows with the ▲/▼ arrows — **row order is the failover order**.
5. Hit **Test** on a row. It probes *that row alone*, so a chain test can't silently pass on
   the provider below the one you're configuring.
6. Turn the per-message **web search** toggle on in the chat composer.

Rate controls live on the same pane — max searches per turn (default 5), throttle between
queries (default 500 ms), and dedupe of repeat queries. These exist to keep a runaway agent
loop from hammering a self-hosted SearXNG.

## Standing up SearXNG

SearXNG is a Python app + a Redis/Valkey cache — there is no single-binary bundle, so run it
in Docker:

```bash
docker run -d --name valkey valkey/valkey:alpine
docker run -d --name searxng -p 8888:8080 \
  --link valkey \
  -v ./searxng:/etc/searxng \
  searxng/searxng
```

Then edit `searxng/settings.yml` — **quartermaster needs the JSON format**, which SearXNG
disables by default:

```yaml
search:
  formats:
    - html
    - json
```

Restart the container, and paste `http://localhost:8888` into the SearXNG row.

Verify by hand:

```bash
curl 'http://localhost:8888/search?q=test&format=json'
```

An HTML body back means `formats: [html, json]` didn't take.

Non-Docker installs work the same way — bare Python in a VM/WSL is fine, only the URL matters.

### Rate limiting

Queries to SearXNG go through one choke point (`internal/server/searxng.go`): min 1.5 s
between queries process-wide, plus a 10-minute result cache. The chain's own cache is keyed
by provider + query + result count, so a cached SearXNG answer is never served in place of
the paid provider you just switched to.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `no web search provider configured` | No row enabled, or every enabled row is missing its key/URL. |
| `all search providers failed (…)` | Each provider's own error is listed — read the parenthesis. |
| SearXNG: results in the browser, empty via quartermaster | JSON format not enabled (`formats: [html, json]`). |
| SearXNG: timeouts under an agent loop | Its engines are CAPTCHA-suspended. Expected — add Brave or Tavily as the next hop. |
| DuckDuckGo: `rate-limited (bot challenge)` | Same thing, no fix. It is a fallback, not a primary. |
| Brave: `422` / `subscription token invalid` | Wrong or unsubscribed key — the upstream message is passed through verbatim. |
| Google: `Daily Limit Exceeded` | 100/day free cap. Move it lower in the chain. |

## Related tools

- **`fetch_page`** (`internal/server/fetchpage.go`) — reads ONE page server-side (text +
  JSON-LD, chrome stripped). Advertised alongside web search. SSRF-guarded at dial on the
  resolved IP, so redirects and DNS rebinding are covered. Caps: 25 s, 4 MiB, 12k chars,
  8 fetches/turn, 15-min cache. JS-rendered pages return a clear error rather than a guess.
- **`media_transcript`** (`internal/tools/youtube.go`) — captions for a video or audio page.
  Works on YouTube *and* the other ~1800 sites yt-dlp extracts from (Vimeo, TED, Dailymotion,
  Twitch VODs, Rumble, PeerTube, SoundCloud, most podcast episode pages) — whatever publishes
  subtitles. Needs `yt-dlp` on `PATH` or beside the exe; the Windows installer offers it as an
  optional task. `youtube_search` and `youtube_comments` stay YouTube-only: the `ytsearch:`
  scheme and comment extraction exist for barely anything else.
