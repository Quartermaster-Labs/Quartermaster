# internal/tools

## Purpose

Server-side implementations of the chat tools that talk to the outside world: the **web-search provider chain** and the **YouTube fetchers** (transcript, free-text search, channel/playlist listing, comments).

They live here, not in `internal/server`, because they have two call sites: the playground's server-owned turn loop (`internal/server/turns.go`) and the **`/v1/tools/*` API** (`internal/server/toolsapi.go`) that external projects use to execute the same tools over the wire. Both get one implementation; the per-turn limits, citation numbering and result eventing stay in the turn layer.

## Key files

| File | Role |
|---|---|
| `search.go` | The provider chain. `SearchProvider`/`Result`, `Search` (ordered failover with per-hop budgets), `LegacyChain`, `SearxngSearch`, `FormatSearchResults`, the 10-minute result cache, the Brave/Tavily/Google-CSE/DDG adapters, and shared HTTP helpers (`searchDo`, `ReadLimited`, `CleanFeedText`). |
| `searxng.go` | The single serialized gate through which **every** SearXNG query passes (agent turns AND the browser proxy): a minimum 1.5s gap between queries and a body cache keyed on (normalized base URL, query). |
| `youtube.go` | Transcript fetching: `ParseVideoID`, `DlpPath` (managed yt-dlp), `GetTranscript` (exec, auto-caption VTT → ~30s paragraphs, token-budgeted truncation), `FormatTranscript`. |
| `youtube_browse.go` | Free-text video search (`yt-dlp ytsearchN:`), channel/playlist listing via tab URLs, comment fetching. `Video`/`Comment` types, `FormatVideos`/`FormatComments`. |
| `hidewindow_windows.go` / `hidewindow_other.go` | `hideConsole` build-tag pair — a `-H=windowsgui` exe has no console, so yt-dlp children must not pop their own window. |

## Important types & functions

- `SearchProvider` — one row of a caller's chain: `ID` (whitelist: `searxng | brave | tavily | duckduckgo | google`), `Enabled`, `BaseURL` (SearXNG only), `Key`, `CX` (Google CSE engine id). `ready()` skips half-configured rows silently — a hop that cannot succeed is latency spent for nothing. `cacheIdentity()` is what makes two configs distinct, and **the API key is not part of it**: the key is a credential, not a scope, so rotating it must not evict the cache.
- `Search(ctx, providers, query, limit) ([]Result, string, error)` — tries providers in order; the first non-empty set wins. Each hop gets its own context (8s; 12s for SearXNG, which fans out to N engines). Errors are **accumulated**, so a total failure says which providers were tried and why each failed. `limit` clamps to `DefaultResults` (5) / `MaxResults` (10) — results are prefill on every following turn, so "as many as you like" is a context bill.
- `ErrNoProviders` — no configured provider is ready. Callers map it to a 4xx (the request asked for a search with nothing to run it on), not a 5xx upstream failure.
- `LegacyChain(baseURL)` — builds a SearXNG-only chain from a single base URL, so callers predating provider config (turn payload, stored client) keep working unchanged.
- `SearxngSearch` / `SearxngJSON` — SearXNG's JSON API directly, server-side. Both go through the gate in `searxng.go`, which they share with the browser proxy (`/api/websearch` in `internal/server/websearch.go`): SearXNG public engines throttle per-client-IP, so an agent loop and the UI probing must be **one** serialized client or they starve each other.
- `FormatSearchResults(query, results, numbers)` — the model-facing text: a header carrying **today's date** (volatile tool results may carry today's date; tool *descriptions* may not — they sit in the KV-stable prefix), then numbered results. `numbers[i]` is the citation number the caller assigned to result i.
- `searchDate` — a **var**, not a call, so tests can pin the date the header stamps. `FormatSearchResults` and `FormatVideos` read it.
- `ParseVideoID` — accepts watch / `youtu.be` / shorts / embed URLs (any subdomain) or a bare id, and **rebuilds the id from a regex match** — never interpolates the input, so the value is guaranteed well-formed before it reaches argv.
- `VideoID` — the bare-id regex (11 base64url chars), exported so `internal/server/youtube_meta.go` can share it via alias.
- `DlpPath` — resolves the **managed** yt-dlp from `internal/backends` (first installed, `yt-dlp` or `yt-dlp.exe`) rather than trusting PATH: a hostile PATH entry would otherwise run with our argv. Returns `ErrDlpMissing` (a sentinel, so callers match it with `errors.Is`) when absent — the API maps it to a 503, and the message doubles as the install hint.
- `GetTranscript(ctx, id, lang)` — `yt-dlp --skip-download --write-auto-subs`, then the subtitle file is parsed: auto-caption VTT is a rolling window (every cue repeats the previous cue's last line, words carry inline `<time><c>` markup), so cues are folded into ~30s paragraphs with one timestamp each — that is the whole point of the tool being affordable in context. `lang` is validated against a strict code regex **before** it reaches argv. The result is token-budgeted (`ytMaxTokens`, 12k by default); truncation keeps whole paragraphs and reports the timestamp it cut at, so the model can say what it is missing.
- `FormatTranscript(tr, citation, maxTokens)` — `citation` 0 means "no number" (the API path); `maxTokens` 0 means the default ceiling, and an over-large ask is **clamped to the ceiling, not honoured**.
- `SearchVideos(ctx, query, limit)` / `ChannelVideos(ctx, channel, tab, limit)` — yt-dlp flat JSON (`--flat-playlist`, cheap: no per-video fetch). `ChannelVideos` rebuilds the target URL from validated parts (`@handle` / channel id / c- or user- or playlist URL, tab from a whitelist) — never splices the model's text.
- `GetComments(ctx, id, limit)` — `--extractor-args "youtube:max_comments=top,N"`, capped at 50. Returns the comment rows plus minimal video meta (title) so the caller can say what they are opinions about.
- `Video` / `Comment` — the wire types for the browse results (also the JSON cache and API response shapes; `Views == -1` means unknown, never zero — a fresh upload is not a failed one).
- `FormatVideos` / `FormatComments` — model-facing listings; the comment header **must** say the block is audience opinions, because a comment block that reads like the video's own content is the failure mode the model most easily falls for.
- `Count` — human-readable counts (`1.5K`, `2.4M`) for view counts.

## Gotchas / conventions

- **Every argument is model text.** URLs, video ids and language codes are validated or rebuilt from a regex match before they reach a URL or an argv. Nothing is interpolated verbatim.
- **Each tool carries its own time-boxed result cache**, because a weak model in a loop re-issues the same call (same query, same URL). The search cache is keyed by provider identity + limit + query, never by API key.
- **Do not bypass the SearXNG gate** with a second client — it exists precisely because this package and the browser proxy would otherwise hammer one per-IP throttle from two unsynchronized goroutines.
- **`hideConsole` on every exec** — Windows `-H=windowsgui` builds have no console; an un-hidden yt-dlp pops a window per call.
- **The server keeps thin aliases, not copies.** `internal/server/toolsbridge.go` re-exports the names `turns.go` already uses (`searchChain`, `formatYouTubeTranscript`, `ytSearch`, …) and re-declares the per-turn limits (`maxYouTube`, `ytTurnTokens`, `maxYtBrowse`, …) — those caps are **turn-layer policy and stay in the server**, not here.
- **No dependencies beyond `internal/backends`** (for `DlpPath`) and the stdlib. This package must stay testable without a config, a models tree or a running router.

## Connections

Depends on: `internal/backends` (managed yt-dlp resolution) and the standard library.

Called by: `internal/server/turns.go` (the playground turn loop — per-turn caps, dedup, citations, events), `internal/server/toolsapi.go` (`/v1/tools/*` — the external execution API), `internal/server/websearch.go` (browser SearXNG proxy, via `SearxngJSON`), `internal/server/youtube_meta.go` (link unfurling, via the `VideoID` / `ParseVideoID` aliases).
