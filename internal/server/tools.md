# internal/server — chat tool fetch paths (fork)

The server-side half of the playground's tools. Most of these are **tool-loop only** — no route,
called from `turns.go`'s `runLoop` (see [`playground.md`](playground.md)); the few that also serve
the browser say so. The client-side contracts they mirror live in `ui-svelte/src/lib/` and are
documented in `ui-svelte/playground-tools.md`.

The web-search and YouTube **executors** now live in `internal/tools` (one implementation shared
by the turn loop and the `/v1/tools/*` API below); what stays in this package is the turn-layer
policy — per-turn caps, citation numbering, result eventing — and the arg parsers in `turns.go`.
See `../tools/CLAUDE.md` for the executor internals.

Two rules run through the whole set:

- **Every argument is model text.** URLs, currency codes, video ids, place names and expressions
  are interpolated into upstream requests or argv, so each file validates or rebuilds rather than
  escapes.
- **Per-turn caps, and a cache.** Each tool carries its own `max*` per-turn limit and a
  time-boxed result cache, because a weak model in a loop re-issues the same call.

## Web search

The executor moved to `internal/tools` (`search.go` there, plus `searxng.go`); the turn loop
reaches it through the aliases in `toolsbridge.go`. What follows describes the executor as it
exists in that package.

| File (in `internal/tools`) | Role |
|---|---|
| `search.go` | The **provider chain** — SearXNG is one hop, not the search. Ordered failover over `searxng` / `brave` / `tavily` / `duckduckgo` / `google` (Programmable Search). Each hop gets its own budget (`searchHopTimeout` 8s, `searxngHopTimeout` 12s) and an error, a timeout **or an empty result set** hands off to the next — empty counts as failure on purpose, since a rate-limited scraper answers 200 with nothing and treating that as success ends the search. `searchProviderCfg.ready()` skips an enabled-but-keyless row rather than spending a hop on it; `searchProviderIDs` is a whitelist because the id decides **which upstream the user's API key is sent to**. Results cached 10 min keyed `(provider identity, limit, query)` — **not** the API key, which is a credential rather than a scope, so a rotated key still hits. Providers arrive on the turn payload (`turnStart.SearchProviders`), in memory only, never written to `chats.json`. Total failure reports which providers were tried and why each failed. |
| `searxng.go` | `SearxngJSON` — the ONE choke point for every SearXNG query, from both the browser proxy and the turn loop. Public SearXNG engines are HTML scrapers that answer burst traffic with a CAPTCHA, after which SearXNG suspends the engine on exponential backoff — and an agent tool loop out-runs that threshold trivially. So: **one query in flight ever**, spaced by `minQueryGap` (1.5s), raw bodies cached per `(base, query)` for `cacheTTL` (10 min). The permit is a **1-slot channel, not a Mutex** — it is held across a multi-second HTTP call, and a `sync.Mutex` waiter cannot observe its own deadline, so one stalled query used to burn the budget of everything queued behind it. |
| `websearch.go` | `/api/websearch`. **POST** `{providers,q,limit}` runs the chain for the playground's per-provider Test button — POST, not GET, because the body carries API keys and a query string lands in the access log. **GET** `?url=&q=` is the original SearXNG-only same-origin proxy (`/search?format=json`), kept for older clients, dodging CORS. |

`formatSearchResults` (in `turnstools.go`) **stamps today's date into the result header** — never
into the tool description, which sits in the KV-stable prefix and would invalidate every
conversation at midnight. `searchDate` is a var so tests can pin it; mirrored in
`ui-svelte/src/lib/webSearch.ts`.

## Fetching pages

**`fetchpage.go`** — the `fetch_page` tool. GETs ONE page and reduces it to text + schema.org
JSON-LD (`extractHTML` drops script/style/nav/footer/form chrome; block elements become newlines
so table and spec rows stay separable).

The **SSRF guard is load-bearing** — the URL comes from the model, so a `net.Dialer.Control` hook
(`guardDial`/`isPublicIP`) rejects loopback/private/link-local/CGNAT/reserved destinations on the
already-resolved IP of *every* dial (covering redirects and DNS rebinding), and the transport sets
`Proxy: nil` so no proxy can dial past it.

Caps: 25s, 4 MiB read, 12k chars out, `maxFetches` (8) per turn, 15-min cache. No JavaScript — a
client-rendered page fails loudly. `formatPage` stamps the read time.

It also harvests up to `pageMaxImages` (3) **image URLs** (`pickImages`:
`og:image`/`twitter:image`/`link rel=image_src` first, then non-chrome `<img>` — `junkImg`/
`smallDim` drop sprites, logos and sub-200px thumbs; lazy `data-src`/`srcset` preferred over a
blank-gif `src`), resolved against `<base href>` or the response URL, so the shopping report can
show a picture the model was actually handed. `attrVal` lowercases — URLs must go through `attrRaw`.

**`imgproxy.go`** — `GET /api/imgproxy?url=` re-serves ONE remote image for the shopping report
cards. Hotlinking a shop CDN from `<img>` mostly fails (foreign `Referer` refused, mixed content)
and a browser `<img>` can send no header, so the fix has to be server-side. Reuses `pageClient()`
= **the same SSRF guard**, since the URL came from the model. Refuses non-`image/*` **and
`image/svg+xml`** (a same-origin SVG can run script), caps at 8 MiB, drops the upstream
`Content-Length` (the copy is capped), 1-day `Cache-Control`.

**`feed.go`** — the `fetch_feed` tool: one RSS/Atom feed, newest first. Exists because search ranks
by relevance and hands back last year's article, while a feed is ordered by time. Reuses
`pageClient()` = **the same SSRF guard as fetch_page** (URL is model text) — do not swap in a plain
client. RSS 2.0 / RDF / Atom via `encoding/xml`; item HTML stripped, entity-decoded and truncated
(`cleanFeedText`), since feed summaries are routinely a whole article. The result closes by telling
the model these are headlines, not articles: `fetch_page` the link before quoting. 15-min cache,
`maxFeeds` (5) per turn.

## YouTube

The executors moved to `internal/tools` (`youtube.go` and `youtube_browse.go` there); the
per-turn caps (`maxYouTube`, `ytTurnTokens`, `ytMinTranscript`, `maxYtBrowse`, `maxYtComments`)
stay in `toolsbridge.go`. The arg parsers (`parseYouTubeArgs`, `parseYtSearchArgs`,
`parseYtCommentArgs`) stay in `turns.go` — they are turn-layer tolerance for the model's JSON,
not executor concerns.

**`youtube.go`** (in `internal/tools`) — the `media_transcript` fetch path. **Not YouTube-only**:
yt-dlp extracts from ~1800 sites and captions are captions, so the tool takes any page URL (Vimeo,
TED, Twitch VODs, PeerTube, podcast episodes…). `ParseMediaTarget` vets it — bare id / watch /
`youtu.be`/shorts/embed canonicalise to a watch URL, everything else must be `http(s)` on a public
address (`shared.IsPublicIP`, the same rule `fetch_page` dials under). Then **exec-per-request** `yt-dlp --skip-download --write-subs
--write-auto-subs --sub-format vtt` into a temp dir (no `--convert-subs` — that needs ffmpeg),
binary from PATH or beside the exe. `vttToParagraphs` strips cue timings, karaoke `<c>` markup and
the auto-caption rolling repeat into ~30s `[m:ss]` paragraphs (raw VTT is 2–3× the tokens).
`formatYouTubeTranscript` adds the citable header, truncating at `ytMaxTokens` with an INCOMPLETE
marker. 30-min cache keyed on the canonical URL + lang, `ytTimeout`. The turn loop still records
these as `kind:"youtube"` (stored chats and replayed history are full of it) and still answers to
the old `youtube_transcript` name, so a model copying its own earlier call is not told "unknown
tool".

Per turn the limiter is a **token budget** (`ytTurnTokens` 40k, each fetch capped at what remains,
refused below `ytMinTranscript`) rather than a call count — "watch all five of these" is legitimate
and five shorts cost less than one long talk. `maxYouTube` (8) survives only as a runaway-loop stop,
since each call is a yt-dlp process against an IP YouTube can 429. **The target is rebuilt or
URL-vetted, and lang regex-validated, before reaching argv** — but yt-dlp follows its own
redirects, so this is a front-door check, not the dial-time guard `fetch_page` gets.

**`youtube_browse.go`** (in `internal/tools`) — the *discovery* tools. `youtube_search` = free-text (`ytsearchN:` scheme,
no URL to build) or a channel/playlist listing, both `--flat-playlist --dump-json` = ONE metadata
page. `youtube_comments` = top-N via `--write-comments --dump-single-json`,
`max_comments=N,N,0,0`.

**`ytChannelURL` is the guard**: a handle / channel id / `/c/` / `/user/` / `list=` is *rebuilt*
from a regex match and the tab whitelisted, so no model text reaches argv verbatim. `ytCommentMax`
is 10 — comment extraction walks continuation tokens against the same IP as the transcript pull,
and a big dump risks 429ing the tool that matters. A flat listing has no upload date, so
`formatYouTubeVideos` states the ordering and disclaims the missing dates. 15-min cache,
`maxYtBrowse`/`maxYtComments` per turn.

**`youtube_meta.go`** — `GET /api/youtube/meta?id=`, link unfurl. **Not yt-dlp**: one
unauthenticated **oEmbed** GET (title + channel, no duration), since unfurling fires on every pasted
link while caption pulls are rate-limited. 24h/256-entry cache, id regex-validated (`ytVideoID`).
Thumbnails are **hotlinked** from `i.ytimg.com` by the browser, not proxied — a deliberate
outbound-to-Google tradeoff; route them through `extractMedia` for a locked-down deployment.

## Local (no network) tools

**`datetime.go`** — `get_datetime`. A model has no clock: today's date only reaches it stamped into
`formatSearchResults`, so a turn with no search answers date questions from the training cutoff.
Returns local/zoned date+time, ISO week, day-of-year, and whole-**calendar**-day distance to an
optional `until` (midnight-to-midnight, so an afternoon call doesn't shorten "3 days until Friday").
Imports `_ "time/tzdata"` — **Windows ships no zoneinfo**, so a named timezone would otherwise fail
on the one platform this targets. An unknown zone is an error, never a silent fall back to
server-local. `dtNow` is a var so tests can pin the clock.

**`calc.go`** — `calculate`. **A hand-written recursive-descent parser over a closed grammar —
never an evaluator.** Numbers, `+ - * / ^`, parens, postfix `%` (percent, not modulo — this tool's
job is prices), and a whitelist of functions; no variables, no assignment, no way out of the file.
The expression is model text, so keep it that way: a general evaluator here is RCE on the user's
box. Digit-grouping commas are stripped **only** when the expression holds no function call
(`max(100,250)` and `1,299.50` can't both be honoured, and silently reading the first as `100250`
is a wrong answer, not an error). `fmtCalcNum` kills float noise (`0.30000000000000004`).

**`units.go`** — `convert_units`: a fixed factor table over 11 dimensions. Cross-dimension requests
(kg→cm) are an **error, not a number** — they mean the model misread a spec, and answering hides
that. Temperature is separate (affine, not a pure scale). Decimal vs binary data units are distinct
rows on purpose (TB vs TiB = the 10% complaint). Aliases are listed, never derived — including each
unit's own canonical display name, so a model can pass a previous result straight back in.

## Keyless upstreams

**`weather.go`** — `get_weather` over **Open-Meteo, keyless** (geocode then forecast), picked
because no API key means no secret to store or leak. WMO codes are mapped to words (`wmoText`) so
the model isn't left to invent what "code 63" means. The place name goes through `url.Values`, never
concatenated into a path. 30-min cache, `maxWeather` (4) per turn.

**`currency.go`** — `convert_currency`. Exists because shopping asks which currency the user buys in
and then finds the best option priced in another, and a model converting from memory quotes a
training-cutoff rate with total confidence. Two keyless upstreams: **Frankfurter** (ECB daily
reference, ~30 currencies) then **open.er-api.com** (~160) for pairs ECB doesn't publish; 6 h cache,
`maxConverts` (8) per turn. `normCurrency` **refuses** anything not exactly three letters rather
than escaping it — the code is model text interpolated into an upstream URL. `parseConvertArgs`
accepts `value`/`source`/`target` aliases and strips symbols/grouping from a string amount.
`formatFxRate` states the rate, its as-of date and that it is a reference rate, so the answer can be
attributed.

The same file also serves **`GET /api/fx?from=&to=`** — the same `fetchFxRate` + 6h cache, exposed
to the browser for the ask-wizard, which rewrites budget brackets the model wrote before it knew the
user's currency. Codes go through `normCurrency` before touching a URL.

## /v1/tools/* — tool execution API

**`toolsapi.go`** — POST endpoints that let an external (usually local) AI project execute the
same tools the playground runs, instead of re-implementing them. The caller keeps its own
model-visible tool schemas and local wrappers; only execution is delegated. Responses are the
same compact, model-consumable shapes the turn loop feeds a model, so a result drops into a
context as-is.

| Route | Body | Response |
|---|---|---|
| `POST /v1/tools/search` | `{query|q, limit|count?, providers: []SearchProvider}` | `{provider, results: []Result}` |
| `POST /v1/tools/youtube/transcript` | `{url|video|id|link, lang?}` | the `Transcript` object |
| `POST /v1/tools/youtube/search` | `{query|q?, channel|handle?, tab?, limit|count?}` — one of query/channel required | `{videos: []Video}` |
| `POST /v1/tools/youtube/comments` | `{url|video|id|link, limit|count?}` | `{video, comments: []Comment}` |

Conventions that matter:

- **Same credential as the inference API**: the routes sit on `discoveryChain` (API-key auth),
  not `adminChain` — these are data-plane calls, and an external key must reach them. They are
  still **not** on the admin surface, so the admin-gating rule above is unaffected.
- **Stateless per call.** Provider config arrives in the request body (exactly like the turn
  payload) and is never persisted. Per-API-key provider storage is a possible later addition,
  deliberately not built yet.
- **Errors are OpenAI-shaped** `{"error":{"message":...}}`: 400 for missing/invalid args and
  `tools.ErrNoProviders` (the *request* asked for a search with nothing to run it), 502 for
  upstream failures, 503 for `tools.ErrDlpMissing` (the message doubles as the install hint).
- **The executors already clamp** — `tools.Search` to `MaxResults`, the YouTube calls to their
  per-call ceilings; the handlers pass limits through rather than re-capping.
