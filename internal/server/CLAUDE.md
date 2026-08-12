# internal/server

## Purpose

Owns the HTTP layer: it builds the mux, applies cross-cutting middleware (auth, CORS, request context, request-body filters, in-flight counting, metrics/captures), and dispatches model-routed requests between the local router and remote peers. It also serves the OpenAI-compatible catalog, the operations/UI JSON API, the embedded Svelte UI, and (fork additions) per-listener catalog scoping, live token streaming, and the `-generate` config-editor endpoints.

## Key files

| File | Role |
|---|---|
| `server.go` | `Server` struct, `New`, route table (`routes`), local/peer dispatch (`localPeerHandler`), version-prefix stripping, preload kickoff, graceful shutdown. |
| `api.go` | `/v1/models` listing + capability rendering, `/running`, `/unload`, `/metrics` (Prometheus), `/upstream/...` passthrough, model-in-path resolution, preload machinery. |
| `apigroup.go` | UI-facing `/api/*` JSON endpoints: SSE event stream, unload, metrics/performance/version/capture, and the `modelStatus` payload (fork: tags each model with `family`, `group`, `listeners`). |
| `configapi.go` | Fork: `-generate` config-editor endpoints — per-model override editor, named variants, display-name rename, cmd preview. All 501 when `s.autogen == nil`. Shared helpers live here: `resolveModelGguf`, `findSidecarOverride`, `regenAndReload`, `writeJSON`. |
| `configapi_dto.go` | Wire types for that editor (`variantDTO`, `overrideDTO`, `modelConfigResp`) + the conversions to/from `autogen.Override`/`VariantSpec` (`applyOverrideDTO`, `applyVariantPatch`). Sparse by design: a zero field means "keep auto-computing". |
| `configapi_settings.go` | Global settings editor: GPU-memory card (VRAM target/headroom/max-RAM + `ttlSec` idle-eviction, 0 = never), backend registry, slot-cache knobs, native folder/file pickers. |
| `backendsapi.go` | Fork: managed backend installs — the `/api/backends/*` surface over `internal/backends` (catalog, upstream releases, install job start + poll, activate/rollback, uninstall) plus the registry write-back `registerManagedBackend`. Sits **alongside** the hand-entered backend rows in `configapi_settings.go`, never replacing them: several backends have no installable upstream release. |
| `configapi_estimate.go` | Load-plan estimate: rebuilds an `autogen.EstimateInput` from a rendered launch command (`estimateInputFromCmd`, `forcedOffloadFromCmd`) so the UI's VRAM/KV breakdown reflects what's actually configured. |
| `configapi_adhoc.go` | Ad-hoc launch commands: `renderAdhocCmd` (sparse patch over the effective override → fully sized cmd with `${PORT}`) and the load/unload endpoints that inject one into the live router without persisting anything. Also used by `ensureCtxVariant`. |
| `auth.go` | Auth, request-context, and CORS middleware; `Access-Control-Request-Headers` sanitization. |
| `admin.go` | Fork: remote-address gate for the unauthenticated admin surface (`adminChain`). `SetAdminAccess(localOnly, allow)` + `ParseAdminAllow`; loopback (and `-admin-allow` CIDRs) only, playground requests exempt. Enabled by `main` whenever an API listener binds beyond loopback. |
| `captures.go` | Request/response capture storage: per-route field masks, zstd+CBOR (de)compression, header redaction. |
| `family.go` | `modelFamily` — derives a stable grouping key (the gguf `-m`/`--model` path, via `config.ParseCmd`) so the UI groups variants of one model. |
| `variant.go` | Fork: **synthetic per-request model variants**. `ensureCtxVariant(realID, ctx)` mints `realID@ctx<N>` on first use — a base-model copy whose cmd is re-rendered by `renderAdhocCmd` for the requested context (`-ngl`/`--n-cpu-moe`/KV types re-derived, not inherited), reusing the base's port and joining all its groups. `requestedCtx` reads the size off the `?ctx=` suffix or `X-QM-Ctx`. Also `ensureBackendVariant` (`X-QM-Backend` exe swap) and the shared COW `addVariantToConfig`. |
| `filters.go` | Request-body filter middleware (model-name rewrite, strip/set params) for JSON and multipart form requests. |
| `inflight.go` | Atomic in-flight request counter + middleware emitting `InFlightRequestsEvent`. |
| `listener.go` | Fork: per-listener catalog scoping. `ServeListener` stores a listen address's allowed model set in request context; `listenerModelSet` reads it back. |
| `livemetrics.go` | Fork: `liveTokenCounter` — scans streaming SSE chunks and emits throttled `LiveTokensEvent`s for a live tokens/sec readout. |
| `log.go` | Logger construction (`NewLoggers`), `/logs` + `/logs/stream` handlers, access-log middleware, `statusRecorder` (status/size capture + Flush/Hijack passthrough). |
| `metrics.go` | `metricsMonitor` (token-usage parsing, bounded ring buffer, capture storage) and `responseBodyCopier` (tees upstream response for metrics while streaming to client). |
| `metrics_middleware.go` | Middleware that resolves the model, buffers request body/headers for capture, restricts `Accept-Encoding`, tees the response, and records metrics after dispatch. |
| `ui.go` | Embedded SPA serving from `ui_dist` (`//go:embed`), pre-compressed (br/gzip) file selection, SPA index.html fallback, favicon. |
| `slotcache.go` | Fork: slot KV-cache persistence — saves a llama-server slot's KV to disk so an expensive conversation survives eviction, and seeds cold/warm loads from a per-agent system+tools **preamble cache**. State machine (`middleware`, `onSwitch`, `restoreOnLoad`, `saveOnEvict`, `ensurePreambleSeed`, `synthPrefill`) + the `/slots` HTTP calls. See "Slot KV cache" below. |
| `slotcache_anchor.go` | How a request becomes a cache key: `sessionAnchor` (conversation id + stable system+tools preamble), `normalizeTimestamps`, `preambleHash`/`preambleKey`. |
| `slotcache_disk.go` | Snapshot-directory layout and the pruning passes: `fileName`/`splitFileName`, `enforceCaps` (LRU by mtime), `prunePreambleFiles`, `dropStalePreambles`, `bestSeed`. Guarded by `diskMu`. |
| `slotcache_stats.go` | Observability: `kvCounters`, the `kvEvent` ring, the pending-confirm queue (`pushAwait`/`confirmReuse`) that pairs a restore with llama-server's reported reuse, and the `stats()` snapshot behind `/api/kvcache`. Own `statsMu`. |
| `kvcacheapi.go` | `GET /api/kvcache` — the monitoring snapshot (counters, recent events, on-disk files) for the Observe → KV Cache tab. |
| `promptcanon.go` | Fork: **always-on** prompt-canonicalization middleware (`promptCanon.middleware`) — strips sub-day timestamps from every chat request's system prompt so the stable prefix stays byte-identical turn-to-turn (KV reuse for ANY client/model, not just slotcache participants). Date granularity kept, idempotent. `GET /api/canon` snapshot for Observe → Context. Distinct from the slotcache's own normalization. |
| `backendmetrics.go` | Fork: `backendMetricsMonitor` — polls each running llama-server's `/metrics`+`/slots` on a 2s ticker for KV-fill / slot-saturation / throughput gauges, **skipped while busy** (both share llama-server's inference task queue; `RequestsProcessing` comes from quartermaster's own in-flight counter instead). `/props` is fetched once per process lifetime (static). Caches per-model, emits `BackendMetricsEvent` over SSE; `GET /api/backend-metrics` snapshot. |
| `websearch.go` | Fork: `/api/websearch` — **POST** `{providers,q,limit}` runs the `search.go` chain for the playground's per-provider Test button (POST, not GET: the body carries API keys and a query string lands in the access log); **GET** `?url=&q=` is the original SearXNG-only same-origin proxy (`/search?format=json`), kept for older clients, dodging CORS. `formatSearchResults` (`turnstools.go`) **stamps today's date into the result header** — never into the tool description, which sits in the KV-stable prefix and would invalidate every conversation at midnight. `searchDate` is a var so tests can pin it; mirrored in `ui-svelte/src/lib/webSearch.ts`. |
| `youtube.go` | Fork: the `youtube_transcript` tool's fetch path (tool-loop only, no route). `parseYouTubeID` accepts watch/`youtu.be`/shorts/embed/bare-id; **exec-per-request** `yt-dlp --skip-download --write-subs --write-auto-subs --sub-format vtt` into a temp dir (no `--convert-subs` — needs ffmpeg), binary from PATH or beside the exe. `vttToParagraphs` strips cue timings, karaoke `<c>` markup and the auto-caption rolling repeat into ~30s `[m:ss]` paragraphs (raw VTT is 2–3× the tokens). `formatYouTubeTranscript` adds the citable header, truncating at `ytMaxTokens` with an INCOMPLETE marker. 30-min cache, `ytTimeout`; per turn the limiter is a **token budget** (`ytTurnTokens` 40k, each fetch capped at what remains, refused below `ytMinTranscript`) rather than a call count — "watch all five of these" is legitimate and five shorts cost less than one long talk; `maxYouTube` (8) survives only as a runaway-loop stop, since each call is a yt-dlp process and a request YouTube can 429; **video id + lang regex-validated before reaching argv**. |
| `youtube_browse.go` | Fork: the **discovery** YouTube tools (tool-loop only). `youtube_search` = free-text (`ytsearchN:` scheme, no URL to build) or a channel/playlist listing, both `--flat-playlist --dump-json` = ONE metadata page. `youtube_comments` = top-N via `--write-comments --dump-single-json`, `max_comments=N,N,0,0`. **`ytChannelURL` is the guard**: a handle / channel id / `/c/` / `/user/` / `list=` is *rebuilt* from a regex match and the tab whitelisted, so no model text reaches argv verbatim. `ytCommentMax` is 10 — comment extraction walks continuation tokens against the same IP as the transcript pull, and a big dump risks 429ing the tool that matters. A flat listing has no upload date, so `formatYouTubeVideos` states the ordering and disclaims missing dates. 15-min cache, `maxYtBrowse`/`maxYtComments` per turn. |
| `fetchpage.go` | Fork: the `fetch_page` tool's fetch path (tool-loop only). GETs ONE page, reduces it to text + schema.org JSON-LD (`extractHTML` drops script/style/nav/footer/form chrome; block elements become newlines so table/spec rows stay separable). **SSRF guard is load-bearing** — the URL comes from the model, so a `net.Dialer.Control` hook (`guardDial`/`isPublicIP`) rejects loopback/private/link-local/CGNAT/reserved destinations on the already-resolved IP of *every* dial (covering redirects and DNS rebinding), and the transport sets `Proxy: nil` so no proxy can dial past it. Caps: 25s, 4 MiB read, 12k chars out, `maxFetches` (8) per turn, 15-min cache. No JavaScript — a client-rendered page fails loudly. `formatPage` stamps the read time. Also harvests up to `pageMaxImages` (3) **image URLs** (`pickImages`: `og:image`/`twitter:image`/`link rel=image_src` first, then non-chrome `<img>` — `junkImg`/`smallDim` drop sprites, logos and sub-200px thumbs; lazy `data-src`/`srcset` preferred over a blank-gif `src`), resolved against `<base href>` or the response URL, so the shopping report can show a picture the model was actually handed. `attrVal` lowercases — URLs must go through `attrRaw`. |
| `imgproxy.go` | Fork: `GET /api/imgproxy?url=` — re-serves ONE remote image for the shopping report cards. Hotlinking a shop CDN from `<img>` mostly fails (foreign `Referer` refused, mixed content) and a browser `<img>` can send no header, so the fix is server-side. Reuses `pageClient()` = the **same SSRF guard** (the URL came from the model). Refuses non-`image/*` **and `image/svg+xml`** (a same-origin SVG can run script), caps at 8 MiB, drops upstream `Content-Length` (the copy is capped), 1-day `Cache-Control`. |
| `currency.go` | Fork: the `convert_currency` tool's fetch path (tool-loop only, no route). Exists because shopping asks which currency the user buys in and then finds the best option priced in another — and a model converting from memory quotes a training-cutoff rate with total confidence. Two keyless upstreams: **Frankfurter** (ECB daily reference, ~30 currencies) then **open.er-api.com** (~160) for pairs ECB doesn't publish; 6 h cache, `maxConverts` (8) per turn. `normCurrency` refuses anything not exactly three letters rather than escaping it — the code is model text interpolated into an upstream URL. `parseConvertArgs` accepts `value`/`source`/`target` aliases and strips symbols/grouping from a string amount. `formatFxRate` states the rate, its as-of date and that it is a reference rate, so the answer can be attributed. |
| `datetime.go` | Fork: the `get_datetime` tool (tool-loop only, no route). A model has no clock — today's date only reaches it stamped into `formatSearchResults`, so a turn with no search answers date questions from the training cutoff. Returns local/zoned date+time, ISO week, day-of-year, and whole-**calendar**-day distance to an optional `until` (midnight-to-midnight, so an afternoon call doesn't shorten "3 days until Friday"). Imports `_ "time/tzdata"` — **Windows ships no zoneinfo**, so a named timezone would otherwise fail on the one platform this targets. An unknown zone is an error, never a silent fall back to server-local. `dtNow` is a var so tests can pin the clock. |
| `calc.go` | Fork: the `calculate` tool. **A hand-written recursive-descent parser over a closed grammar — never an evaluator.** Numbers, `+ - * / ^`, parens, postfix `%` (percent, not modulo — this tool's job is prices), and a whitelist of functions; no variables, no assignment, no way out of the file. The expression is model text, so keep it that way: a general evaluator here is RCE on the user's box. Digit-grouping commas are only stripped when the expression holds no function call (`max(100,250)` and `1,299.50` can't both be honoured, and silently reading the first as `100250` is a wrong answer, not an error). `fmtCalcNum` kills float noise (`0.30000000000000004`). |
| `units.go` | Fork: the `convert_units` tool — a fixed factor table over 11 dimensions, no network. Cross-dimension requests (kg→cm) are an **error, not a number**: they mean the model misread a spec, and answering hides that. Temperature is separate (affine, not a pure scale). Decimal vs binary data units are distinct rows on purpose (TB vs TiB = the 10% complaint). Aliases are listed, never derived — including each unit's own canonical display name, so a model can pass a previous result straight back in. |
| `weather.go` | Fork: the `get_weather` tool — **Open-Meteo, keyless** (geocode then forecast), picked because no API key means no secret to store or leak. WMO codes are mapped to words (`wmoText`) so the model isn't left to invent what "code 63" means. Place name goes through `url.Values`, never concatenated into a path. 30-min cache, `maxWeather` (4) per turn. |
| `feed.go` | Fork: the `fetch_feed` tool — one RSS/Atom feed, newest first. Exists because search ranks by relevance and hands back last year's article, while a feed is ordered by time. Reuses `pageClient()` = **the same SSRF guard as fetch_page** (URL is model text) — do not swap in a plain client. RSS 2.0 / RDF / Atom via `encoding/xml`, item HTML stripped + entity-decoded and truncated (`cleanFeedText`) since feed summaries are routinely a whole article. Result closes by telling the model these are headlines, not articles: `fetch_page` the link before quoting. 15-min cache, `maxFeeds` (5) per turn. |
| `currency.go` (route) | Also serves **`GET /api/fx?from=&to=`** — the same `fetchFxRate` + 6h cache the tool uses, exposed to the browser for the ask-wizard, which rewrites budget brackets the model wrote before it knew the user's currency. Codes go through `normCurrency` before touching a URL. |
| `youtube_meta.go` | Fork: `GET /api/youtube/meta?id=` — link unfurl. **Not yt-dlp**: one unauthenticated **oEmbed** GET (title + channel, no duration), since unfurling fires on every pasted link while caption pulls are rate-limited. 24h/256-entry cache, id regex-validated (`ytVideoID`). Thumbnails are **hotlinked** from `i.ytimg.com` by the browser, not proxied — deliberate outbound-to-Google tradeoff; proxy via `extractMedia` for a locked-down deployment. |
| `upscale.go` | Fork: `POST /v1/images/upscale` — standalone ESRGAN upscale. **Exec-per-request**, NOT a loaded/swapped model: shells out to `realesrgan-ncnn-vulkan` (`kind:upscale` registry entry) per call, mutex-serialized, tile-capped VRAM. `{image,model?,scale?}`→`{image,model,scale}`; model files (`<name>.param`/`.bin`) discovered in `<exeDir>/models`. `hidewindow_{windows,other}.go` = `hideConsole(cmd)` so no console window pops. |
| `autostart{,_windows,_other}.go` | Fork: `GET`/`PUT /api/autostart` over the per-user Windows Run key (`HKCU\…\CurrentVersion\Run`); works without `-generate`. Every install writes the SAME value name `Quartermaster`, so at most one can autostart: a `PUT` whose stored entry points at a *different* exe returns **409 + the status** instead of clobbering it (dashboard offers Take over → re-`PUT` with `takeover:true`). Written command = this process's own argv, relative paths absolutised, `-tray` forced on. Non-Windows: `supported:false`. |
| `playground.go` | Fork: standalone playground app on the `-playground-port` address. `Playground` struct + `SetPlayground`/`markPlayground`; plaintext per-user login (`pg_user` cookie, `users.json`), server-backed chat history + prefs, and `/api/mode` so one bundle serves dashboard or playground per port. **Storage:** per-user `DataDir/users/<user>/{chats,imagechats,speechchats,prefs}.json`; generated media (inline `data:` base64) is split out on write into `media/<hash>.<ext>` (`extractMedia`, regex over raw bytes — structure-agnostic, byte-preserves numbers/timestamps, dedup by content hash), served via `GET /api/media/{file...}` (per-user, Range-capable). Boot-time `Migrate()` folds the old flat inline-base64 layout into this. |
| `turns.go` | Fork: **server-owned turn runner** for the playground chat. A turn is a server goroutine (`turnManager`, one `activeTurn` per user) streaming ONE completion plus the whole tool loop (web/wiki search, youtube, `fetch_page`), reasoning-budget finalize, and qm-tools approval gate. Repeat tool calls are **deduped per turn** (`doneCalls`, keyed name+args): the second identical call gets the first result back with a note, re-executes nothing and paints no second card — weak models re-issue the same channel listing round after round. Search-card `kind` distinguishes the three YouTube tools (`youtube` = transcript, `youtube-search`, `youtube-comments`) because one shared kind labelled a metadata listing as if the video had been watched — straight into `chats.json` (single source of truth, merge-guarded via `guardedChatsPut`) and to any attached SSE viewer, so a closed/refreshed tab doesn't lose or stop the answer. Endpoints: `POST /api/chats/turn`, `GET .../stream` (SSE snapshot+tail), `/state`, `DELETE` (stop — the client MUST call it: aborting the SSE fetch only detaches the viewer, and `handleTurnStop` blocks until the runner is actually done so an immediate re-send can't race into a 409), `POST .../approve`. The self-call loops back through the normal proxy with the configured API key injected. **`at.lens()` returns UTF-16 code-unit lengths, not bytes** (`utf16Len`): `turnSearch.At`/`ReasoningAt` are split points the UI applies with JS string indices, so a byte offset drifted right by one unit per emoji and dropped tool cards inside a word. A `busy` delta carries the live activity label (`busyLabel`: "Searching for …", "Reading example.com") that the UI shimmers next to the source counter — set before each tool runs, cleared after, and **replayed in `subscribe()`'s snapshot** so a reattaching tab does not sit on a stale or missing status; instant local tools (time, math, units) get "" because a label that flickers for 3ms is noise. See `turns_design.md`. |
| `turnstools.go` | Fork: server-side ports of the playground's tool + reasoning helpers so the turn runner drives the model→tool→model loop headlessly; behaviourally identical to the client originals (`wiki.ts`, `webSearch.ts`, `reasoning.ts`). Also the **anti-fabrication guards** — models invent YouTube videos (plausible title, plausible 11-char id, no tool call). Two layers, wired in `runLoop`: `pastedNewVideo` — the last user message links a video id seen nowhere earlier — → `tool_choice:"required"` on round 1 only; and `unverifiedYtIDs` vs ids seen in the conversation or any tool result → `unverifiedVideoMarker` **appended**, since deltas are append-only and a streamed answer cannot be retracted. Both triggers are structural on purpose. A forced call removes the model's option to answer *or* to ask which video is meant, so it fires only where a lookup is provably the only correct move — naming YouTube ("why does it compress so hard?", a follow-up on an already-read transcript) is a topic, not a task. A third layer — a regex spotting "let me fetch those now." with no call, nudging the model to follow through — was **deleted**: matching English prose false-fired on finished answers and burned a round on the model disputing the nudge. The marker layer is a check on output, not a guess at intent, and catches the only failure that actually misleads. Holds `searchWiki` over the **embedded** wiki corpus (`//go:embed wiki_articles.json`, copied from `ui-svelte/src/lib/` by the Makefile `ui` target — one source, two consumers) and `formatSearchResults`. `parseSearchCount` lets the model pick how many results it gets (`count`/`num_results`/`limit`, clamped to 1-10, default 5) — a fixed 5 is one or two real candidates once duplicates and listicles are dropped, which is too thin for a shortlist; the count is part of the result cache key, so a wider re-ask is not served the narrow cached answer. |
| `turnsreplay.go` | Fork: **tool-call replay into history**. The client sends prior turns as role+content only — the calls they made and the results they got live in the UI's `searches` metadata and never reach the model, so its own history shows nothing but prose and no evidence a tool was ever called or ever worked. Models copy that: in one thread the model spent three turns saying "let me grab the remaining three now" with zero calls. `replayToolCalls` rebuilds each such turn as assistant-with-`tool_calls` → real stored results as `tool` messages → the answer it wrote, giving back both the evidence and the example. Only runs when tools are on (a `tool_calls` message with no tools is unanswerable). Truncation is **per-result and fixed** (`replayResultMax`), never a whole-history budget: a shared budget would re-trim older results as newer turns spent it, changing the prompt prefix and voiding the conversation's KV cache on every message. URL-taking tools replay with `Sources[0].URL`, not `Query` — `Query` holds the resolved *title* after a successful fetch. `quartermaster` searches are skipped: a config action is not reference data and the kind doesn't say which of the two QM tools ran. |
| `turns_qm.go` | Fork: **the "quartermaster MCP"** — dispatch for the `quartermaster_inspect` / `quartermaster_configure` chat tools (advertised in `ui-svelte/src/lib/qmTools.ts`). Both call quartermaster's OWN loopback API (`pg.SelfBase`) with the turn's injected key, reusing existing handler validation + regen/reload (and the 501-without-`-generate` gate). Deliberately **no load/unload** — swapping would evict the model answering the chat. A `configure` builds a fully-resolved `configPlan` **before** the approval gate, streams the before→after `qmDiffRow` card (`kind:"approval"`, re-sent on reconnect), applies only on accept; unanswered calls drop after `approvalTimeout` (5 min) so a closed tab can't wedge the single turn slot. Responses capped at `qmBodyLimit` (24 KB). |
| `turns_qm_fields.go` | Fork: the editable-field catalog those tools advertise, **derived from the DTOs by reflection** (`qmSpecsOf` over `overrideDTO`/`variantDTO`) so the tool surface is exactly the cogwheel's surface — no hand-kept list to drift (an earlier hand-written one made a model write `--chat-template-file` into `extraArgs`). Pointer fields render as `T|null` (= inherit/auto). |
| `searxng.go` | Fork: `searxngJSON` — the ONE choke point for every SearXNG query, from both the browser proxy (`websearch.go`) and the turn loop (`search.go`). Public SearXNG engines are HTML scrapers that answer burst traffic with a CAPTCHA, after which SearXNG suspends the engine on exponential backoff — and an agent tool loop out-runs that threshold trivially. So: one query in flight ever, spaced by `minQueryGap` (1.5s), raw bodies cached per `(base, query)` for `cacheTTL` (10 min). The permit is a **1-slot channel, not a Mutex** — it is held across a multi-second HTTP call, and a `sync.Mutex` waiter cannot observe its own deadline, so one stalled query used to burn the budget of every query queued behind it. |
| `search.go` | Fork: the **web-search provider chain** — SearXNG is one hop, not the search. Ordered failover over `searxng` / `brave` / `tavily` / `duckduckgo` / `google` (Programmable Search): each hop gets its own budget (`searchHopTimeout` 8s, `searxngHopTimeout` 12s) and an error, a timeout **or an empty result set** hands off to the next. Empty counts as failure on purpose — a rate-limited scraper answers 200 with nothing, and treating that as success ends the search. `searchProviderCfg.ready()` skips an enabled-but-keyless row rather than spending a hop on it; `searchProviderIDs` is a whitelist because the id decides which upstream the user's API key is sent to. Results cached 10 min keyed `(provider identity, limit, query)` — **not** the API key (a credential, not a scope) — so a rotated key still hits and a wider re-ask isn't served the narrow answer. Providers arrive on the turn payload (`turnStart.SearchProviders`), which is in-memory only and never written to `chats.json`. Total failure returns which providers were tried and why each failed. |
| `loras.go` | Fork: `filterLorasResponse` — buffers the `/sdapi/v1/loras` response and drops non-LoRA rows. sd-server lists every weight file under `--lora-model-dir`, which autogen points at the model gguf's own folder (zero-config drop-in), so checkpoints and encoders showed up in the picker as guaranteed failures. Filtered **by file identity, not guesswork**: any row resolving to a path some model launches with (`-m`, `--diffusion-model`, `--vae`, `--clip_l`, …) is removed; a real LoRA is never a launch argument. `bufferedResponse` is for this one-shot JSON only — never reuse it on a streaming route. |
| `apikeyscope.go` | Fork: per-key model scoping plumbing — `buildKeyScopes` (`cfg.APIKeyModels` → key ⇒ allowed-model set, dropping empty lists as unrestricted), `withKeyScope`, `apiKeyModelSet` (`ok=false` = unrestricted). Read by the auth middleware, `handleListModels`, and `localPeerHandler`. |
| `configapi_apikeys.go` | Fork: the local admin API-key manager — `GET`/`POST /api/apikeys`, `DELETE /api/apikeys/{name}` over the sidecar's managed keys (`autogen.{Load,Upsert,Delete}SidecarAPIKey`). `-generate` only. |
| `update.go` | Fork: `POST /api/update` — downloads + launches the release installer then graceful shutdown (Windows release builds only); `updater` field + `SetShutdownHook`, status surfaced in `handleAPIVersion`. |
| `pickfolder_{windows,linux,other}.go` | Fork: native folder-picker dialog (`pickFolder()` — WinForms / zenity / unsupported) backing `POST /api/pick-folder` and `POST /api/settings/root/pick` in the `-generate` config editor. |
| `pickfile_spec.go` | Fork: `pickSpecs`, the **server-side whitelist** of open-file dialog kinds (`backend`, `template`) with their Windows/zenity filter strings. The platform `pickFile` implementations interpolate these into a shell/PowerShell command line, so `/api/pick-file` rejects anything not in this map — a spec must never be built from request data. |

## Important types & functions

- `Server` (`server.go`) — owns mux/handler, local `router.LocalRouter` + peer `router.Router`, `metricsMonitor`, `inflightCounter`, `perf.Monitor`, `listenerModels`, optional `autogen` admin.
- `New(...)` — constructs routers (group or matrix per `cfg.Routing.Router.Use`) + peer, builds routes, fires preload.
- `localPeerHandler` — resolves the model once via `shared.FetchContext`, enforces per-listener scoping, routes local or peer.
- `routes` — assembles the middleware chains and registers every route; wraps the mux with request-log + CORS.
- `Shutdown` / `CloseStreams` — idempotent parallel router teardown; `CloseStreams` cancels SSE so HTTP drain doesn't block.
- `handleListModels` / `renderCapabilities` (`api.go`) — `/v1/models` with capability rendering + per-listener filtering.
- `modelStatus` / `groupIndex` / `handleAPIEvents` (`apigroup.go`) — per-model UI payload + the SSE multiplexer (modelStatus, logData, metrics, inflight, liveTokens, backendMetrics).
- `AutogenAdmin` / `SetAutogenAdmin` (`configapi.go`) — gate + wiring for the `-generate` editor endpoints.
- Middleware constructors: `CreateAuthMiddleware`, `CreateRequestContextMiddleware`, `CreateCORSMiddleware` (`auth.go`), `CreateFilterMiddleware`/`CreateFormFilterMiddleware` (`filters.go`), `CreateInflightMiddleware` (`inflight.go`), `CreateMetricsMiddleware` (`metrics_middleware.go`), `CreateRequestLogMiddleware` (`log.go`).
- `ServeListener` (`listener.go`) — per-listener entry point for the multi-listener startup.

## HTTP routes

Registered in `routes` (`server.go`). Model-dispatched routes (`modelChain` → `localPeerHandler`) carry the full middleware chain; everything else uses `apiChain` (auth only).

Model-dispatched (lists in `server.go`):
- `POST` JSON routes — `/v1/chat/completions`, `/v1/responses`, `/v1/completions`, `/v1/messages`(+`/count_tokens`), `/v1/embeddings`, `/rerank`(+variants), `/infill`, `/completion`, `/v1/audio/speech`, `/v1/images/generations`, `/sdapi/v1/{txt2img,img2img}`, and `/v/...` versionless equivalents.
- `POST` form routes — `/v1/audio/transcriptions`, `/v1/images/edits`.
- `GET` routes — `/v1/audio/voices`, `/sdapi/v1/loras`.

Not model-dispatched but auth-gated (discoveryChain):
- `POST /v1/images/upscale` (`upscale.go` `handleUpscale`) — standalone ESRGAN upscale, exec-per-request (no scheduler/VRAM-swap). Distinct from `/v1/segment` (SAM), which IS a model-dispatched backend.

API / operations / UI (all registered in `server.go`; handler file named only where it isn't obvious):
- `GET /v1/models` (→ `api.go`) — catalog, scoped per listener.
- `GET /logs`, `/logs/stream[/{logMonitorID...}]`; `GET /health`, `/wol-health`, `/{$}` redirect; `GET /ui/`, `/favicon.ico` (embedded SPA); `GET /metrics` (Prometheus); `GET /unload`, `/running`; `GET /upstream`, `/upstream/{upstreamPath...}`.
- `POST /api/models/unload[/{model...}]`, `GET /api/events`, `/api/metrics`, `/api/performance`, `/api/version`, `/api/captures/{id}`.
- Config editor (501 without `-generate`): `GET /api/models/{model}/config`, `PUT`/`DELETE /api/models/{model}/override`, `PUT /api/models/{model}/variant`, `GET /api/models/{model}/estimate`, `PUT /api/models/{model}/preview` (cmd preview), `PUT /api/models/{model}/adhoc-cmd` (one-off flag-override cmd — no persistence, no reload), `PUT`/`DELETE /api/models/{model}/adhoc-load` (inject that cmd into the LIVE router; in-memory only, DELETE or any file reload reverts), `PUT`/`DELETE /api/models/{model}/display-name`, `GET`/`PUT`/`DELETE /api/settings`, `PUT /api/settings/slotcache`, `PUT /api/settings/backends`, `PUT /api/default-variants`, `POST /api/pick-folder` + `POST /api/settings/root/pick`, `POST /api/pick-file` (whitelisted kinds only — `pickfile_spec.go`), `GET`/`POST /api/apikeys` + `DELETE /api/apikeys/{name}`.
- Autostart: `GET`/`PUT /api/autostart` — NOT `-generate`-gated.
- Context / observe extras: `GET /api/canon`, `/api/backend-metrics`, `/api/websearch`, `/api/youtube/meta`, `/api/imgproxy`, `/api/fx`.
- Managed backend installs (`backendsapi.go`, admin-gated): `GET /api/backends/catalog` (local-only, never calls GitHub), `GET /api/backends/{component}/releases` (`?refresh=1`), `POST /api/backends/install`, `GET /api/backends/jobs` (progress polling), `POST /api/backends/activate` (which installed build the row points at), `POST /api/backends/default` (★ for its class — separate axis from activate), `POST /api/backends/uninstall`. Distinct from `GET /api/backends` + `PUT /api/settings/backends` = the hand-entered registry.
- Self-update: `POST /api/update` (Windows release builds only).
- Playground app (on `-playground-port`, `playground.go`): `GET /api/mode`, `POST /auth/login`, `/auth/logout`, `GET /auth/me`, `GET`/`PUT /api/{chats,imagechats,speechchats,prefs}`, `GET /api/media/{file...}`; server-run turns `POST /api/chats/turn` + `/stream`,`/state`,`/approve` + `DELETE` (`turns.go`).

## Gotchas / conventions

- **Auth scope (fork).** API keys gate the **external inference API only** — model-dispatch routes (`modelChain`) plus discovery (`/v1/models`, `discoveryChain`). Dashboard / admin / ops / SSE / `/ui/` (`apiChain`) are **open**, so enabling keys never locks the operator out of their own UI. `CreateAuthMiddleware` is a pass-through with no configured keys; keys accepted via `Authorization: Bearer`, `Authorization: Basic` (password field), or `x-api-key`. A key may be **model-scoped** via `cfg.APIKeyModels`: the middleware attaches the allowed set to the request context (`apikeyscope.go`), and `handleListModels` + `localPeerHandler` intersect it with the listener scope. Empty/absent scope = full access. Keys + scopes are emitted into the generated config by autogen, managed via `/api/apikeys` (`configapi_apikeys.go`).
- **Admin surface is gated by REMOTE address, not by listener (fork).** API keys never cover `apiChain`, so publishing the API to the LAN/tailnet (`-listen 0.0.0.0:1250`) would otherwise expose the dashboard, `/upstream/*`, captures, unload, and the config editor to every host that can reach the port — and API + dashboard share one port, so the split can't be per-listener. `adminChain` (`s.requireAdmin`, `admin.go`) 403s any admin route whose `r.RemoteAddr` isn't loopback or inside an `-admin-allow` CIDR (e.g. `100.64.0.0/10` for a tailnet). `main` enables it automatically when a non-playground listen address is non-loopback; `-admin-open` restores the wide-open behaviour. Ungated: `/v1/*` (keys' job), `/health`, `/api/version`, `/metrics`, `/favicon.ico`, and every playground route (own port + login; `isPlaygroundRequest` exempt). **Adding a new `/api/*` ops or editor route means wiring it to `adminChain`, not `apiChain`.**
- **Per-listener catalog filtering.** Restriction lives in the request context, not the handler. `ServeListener` injects the address's allowed model set; `handleListModels` and `localPeerHandler` read it via `listenerModelSet`. `ok=false` = unrestricted (legacy single `--listen`). Peer models are omitted from restricted listeners. All listeners share one `Server` (one router/scheduler) — the invariant making cross-listener VRAM accounting/eviction correct.
- **Metrics teeing.** `CreateMetricsMiddleware` resolves the model up front (priming `shared.FetchContext`'s fast path), restricts `Accept-Encoding` to gzip/deflate so the buffered body stays parseable, and wraps the writer in `responseBodyCopier`. Both `responseBodyCopier` and `statusRecorder` forward `Flush`/`Hijack` so SSE and websocket upgrades keep working. Captures off unless `CaptureBuffer > 0`.
- **Config editor is `-generate`-only.** Every `configapi.go` handler 501s when `s.autogen` is nil. A successful edit upserts the sidecar override/settings, calls `autogen.EnsureConfig`, then hot-reloads (SIGHUP path) — slow (re-reads gguf metadata), acceptable for a settings save.
- **Managed and manual backends share ONE registry.** `registerManagedBackend` (`backendsapi.go`) upserts a normal `autogen.BackendEntry` row (`id: managed-<component>`) flagged `Managed` with `Component`/`Version`/`Variant`, so per-model pinning, the ★ class default, and `deriveBackendExes` are untouched. Two consequences: (1) the row's **id is kept** across updates, so a per-model `Override.Backend` survives a version bump; (2) `PUT /api/settings/backends` (manual editor, sends the whole list) **restores managed provenance from the stored row by id** rather than trusting the client — a manual save can't strip `Managed` or repoint the path away from the active build. Managed rows render read-only there. A managed row becomes the ★ default only when its class is empty.
- **Reload is in-place (`Server.ApplyConfig`).** The ONE long-lived `Server` (SSE streams, metrics history, slotCache saved-KV, goroutines, running processes) survives a config change — only the config pointer and cfg-derived handler swap. `s.cfg`/`s.listenerModels` are `atomic.Pointer`s (read via `s.config()`); `s.handler` is `atomic.Pointer[http.Handler]`. `ApplyConfig(newCfg)` validates + applies to the shared router first (`s.local.ApplyConfig` — add/remove/retune in place, no eviction; new launch args apply on the model's NEXT load), then swaps `s.cfg`, refreshes `s.listenerModels`, and calls `s.routes()` to rebuild + atomically swap the handler without dropping in-flight requests or SSE. Invalid config = nothing touched. `main.reload` just calls `activeSrv.ApplyConfig`. Every save path (cogwheel, SIGHUP, `-watch-config`, `-watch-models`) flows through this, then emits one `ConfigFileChangedEvent`. NOT live-swapped: bound listen sockets (main warns via `listenerAddrsChanged`; per-listener *scoping* does refresh) and peer targets — both need a restart.
- **`RunningCmd` in `modelStatus`.** `runningCmd` = the actual argv the process spawned with (`s.local.LaunchedCmd(id)`), set only while running. Differs from the config command after a live settings edit or spawn-time offload rewrite, so the UI staging card shows what's REALLY loaded.
- **Embedded UI.** `ui_dist` is `//go:embed`-ed; the Makefile `ui` target copies the Svelte build in, a placeholder keeps the embed valid before any build. Pre-compressed `.br`/`.gz` siblings preferred; extensionless misses fall back to `index.html` for SPA routing.
- **Access-log skips.** `/wol-health`, `/api/performance`, `/metrics` excluded (poll traffic). Path is captured before `next` runs because `/upstream` rewrites the URL in place.
- **Versionless routes.** `/v/...` is rewritten to `/...` by `stripVersionPrefix` before forwarding (issue #728).
- **Family / group tagging (fork).** `modelFamily` keys models by gguf path so the UI collapses ctx/game/judge variants; `modelStatus` also tags each model with its swap group and exposing listeners. Thin memoized wrapper over `config.ParseCmd(cmd).ModelPath` (runs per SSE status build and per slot-cache request).
- **Ask a command questions via `config.ParseCmd`, not `strings.Contains`.** Every consumer needing a fact out of a rendered launch command (`modelFamily`, `slotParticipates`/`slotRecurrent` in `server.go`, `cmdArgv` in `configapi_estimate.go`, `portFromCmd` in `configapi_adhoc.go`, the image/audio/MTP sniffs in `configapi.go`, `loras.go`) goes through the shared memoized `CmdInfo`. Substring tests break on line-wrapped flags and match prefixes of longer flags.
- **Per-request variants are synthetic sibling models, not a re-key (fork).** `X-QM-Backend: <id>` and `?ctx=<N>`/`X-QM-Ctx` do NOT parameterize the running process — they mint a new model id (`realID@<backend>`, `realID@ctx<N>`) via `ensureBackendVariant`/`ensureCtxVariant`. The variant is `Unlisted`, reuses the base's allocated `${PORT}`, and joins **every group the base is in** — that shared exclusive group is what makes port reuse and VRAM accounting correct (base and variant can never be resident together). Minting is a read-modify-`ApplyConfig` cycle over the whole config: serialized on `Server.variantMu`, registered through the COW `addVariantToConfig`. Never mutate the live config's maps or a group's `Members` slice in place. Ctx variants are capped per base (`maxCtxVariants`) so a client sweeping `?ctx=` can't grow the config unbounded; they last until the next file reload.
- **The `?ctx=` suffix resolves to the REAL model id.** `config.RealModelName` strips it (`config.SplitCtxRequest`), so listener scoping, key scoping, filters and metrics labels see the base model. `localPeerHandler` reads the requested ctx **after** both scope checks — moving it earlier would let a client reach an unscoped model by appending a suffix. Malformed/out-of-range values are not clamped; they fail to resolve.
- **OOM / VRAM protection (fork).** Three pieces surfaced through `/api/performance`:
  - **Foreign VRAM** — `foreignGPU`/`foreignVram`/`isInferenceProc` (`apigroup.go`) tally GPU memory held by `llama-server`/`sd-server` processes this instance did NOT spawn (`perf.QueryComputeApps` minus `router.RunningPIDs()`), returned as `"foreign"` so the sizer knows VRAM it can't reclaim.
  - **Idle floor** — `trackSystemVram` (`server.go`) keeps min idle used-VRAM on the largest GPU while no model runs (`"system_mb"`), the baseline the budget must reserve.
  - **Dynamic offload guard** — `WireDynamicOffload`/`freeVramGB` (`server.go`), the spawn-time argv rewriter wired into `autogen.LiveOffloadArgs`: re-derives GPU/CPU layer placement from live free VRAM and **refuses** a spawn that can't fit. See `internal/autogen/CLAUDE.md`.
- **Prompt canonicalization is a separate, always-on middleware (fork).** `promptCanon.middleware` runs *before* slotcache/upstream for every chat request, regardless of slot-cache participation. Not the same as the slotcache's `normalizeTimestamps` (scoped to participating models' anchoring). Stats via `GET /api/canon`.

## Slot KV cache (fork — `slotcache*.go`)

Persists a llama-server **slot's KV** to disk so an expensive prefill isn't thrown
away when the single live slot is reused. llama-server has **one slot (`/slots/0`)**;
any new request can evict the resident conversation. This subsystem snapshots the KV
before it's lost and restores it (instead of reprefilling) when that conversation —
or one sharing its preamble — returns.

**When it's active.** Two gates: `cfg.SlotCache.Enable` (global; `dir`/`minSaveTokens`/
`maxDiskGB`/`maxSessions`) **and** per-model `participates(model)` — true only when the
model's cmd carries `--slot-save-path`. Non-participating models are left alone; disabled
cache = branchless no-op middleware.

**Wiring.** `slotCache.middleware` sits in the model-dispatch chain. Cross-swap
persistence also needs two router process hooks: **pre-stop → `saveOnEvict`** and
**post-start → `restoreOnLoad`** (after Ready, before the triggering request is served).
Without those hooks the cold path is dead — if restore never fires, check they're called.

### Two file categories

1. **Conversation snapshots** — `model__<key>.bin` (+ `.meta` preamble sidecar), one per
   chat. Keyed by `sessionAnchor`: the `X-Conversation-Id` header if sent (preferred —
   survives compaction, no opening collisions), else `sha256(firstSystem + firstUser)`.
   LRU-bounded by `enforceCaps`.
2. **Preamble caches** — `model__preamble_<hash>.bin`: one system+tools-only KV per
   `(model, preamble)`, i.e. per agent/environment. Seeds *every* cold/warm load sharing
   that preamble. `hash = sha256("preamble\x00"+preamble)[:16]`;
   `preamble = system + "\x00tools\x00" + toolsJSON`. Differentiation is **purely
   content** — identical bytes share one file, whatever harness sent them.

### Save path (conversation snapshots)

- **WARM** (`onSwitch`, model loaded): new conversation arrives → save the outgoing one
  if worth it, restore the incoming one if on disk.
- **COLD** (`saveOnEvict`): evicting A to load B kills A's process with no A request to
  trigger a save — the pre-stop hook snapshots it.

"Worth saving" = live KV ≥ `minSaveTokens`. **Cost is the only gate** — no turn-count
gate: a single-turn chat with a long answer is still expensive to reprefill.

### Restore / seed path (preamble caches + Tier-1)

On a load with no exact conversation file, warm (`onSwitch`) and cold (`restoreOnLoad`)
try, in order:

1. **`ensurePreambleSeed`** — restore this agent's `preamble_<hash>.bin` (`preamble-hit`),
   else **mint** it: `synthPrefill` POSTs a system+tools-only `max_tokens:1` chat (llama-server
   can only save the *whole* live slot, so a clean preamble-only KV needs a synthetic prefill
   while the slot is safe to clobber), then saves the resident KV (`preamble-mint`). Gated on
   a non-empty system prompt **and** `len(preamble) ≥ seedMinPrefixBytes` (2 KB).
2. **`bestSeed`** (Tier-1 fallback) — a prior session sharing a ≥2 KB leading preamble
   prefix, chosen to **minimize over-restore**: tail-free preamble caches first, then
   longest shared prefix, then smallest `.bin`. Over-restore (a sibling conversation whose
   tail diverges) is wasted I/O on plain attention and harmful on hybrid/recurrent —
   un-rewindable layers emit `non-consecutive token position N after M` + full reprocess.
   - Recurrent/hybrid models skip the slot cache entirely — see `recurrentSkip` below.

After any restore, `awaitConfirm[model]` is set; the **next** request's upstream
`cached_tokens` (`confirmReuse`, from the metrics monitor) is the proof the KV was
actually reused (`confirm` / `confirm-miss`), not just loaded.

### Pruning (three mechanisms)

- **`enforceCaps`** — LRU by mtime within `maxDiskGB` / `maxSessions`. Preamble caches
  **exempt** (sticky shared seeds).
- **`prunePreambleFiles`** — backstop: keep newest `maxPreambleGenerations` (3) per model.
- **`dropStalePreambles`** — on mint, delete a prior preamble that is the **same agent apart
  from a small dynamic span** (`supersedesPreamble`: shared prefix + suffix, non-matching
  middle ≤ `preambleDynDeltaMax` 512 B). Catches a daily date bump without nuking a different
  agent sharing identical tools. Needs the full preamble in `.meta`, so preamble sidecars are
  stored **uncapped** (conversation `.meta` stays capped at `metaMaxBytes`).

### Gotchas

- **We mutate the forwarded prompt (timestamp normalization).** `sessionAnchor` strips
  time-of-day from ISO datetimes in the **system prompt** (`normalizeTimestamps`/
  `isoTimeOfDay`) and rewrites the re-attached body, so upstream sees the date-only form.
  Otherwise an agent stamping the wall clock into its preamble re-mints a multi-hundred-MB
  preamble KV every run. **System prompt only** — user messages keep their timestamps.
  Bare dates untouched. Always on when the slot cache participates.
- **Single slot, three locks.** All save/restore hit `/slots/0` (one per model — each model
  is its own llama-server). `stateMu` guards bookkeeping maps (never held across I/O);
  `slotMu` is **per model id** (`lockModel`) and serializes that model's long work;
  `diskMu` serializes directory-wide prune passes. Lock order: `slotMu` → `stateMu`,
  `slotMu` → `diskMu`; `statsMu` nests into none. A single global lock here made a multi-GB
  save for model A block every other model (regression test
  `TestSlotCache_SaveDoesNotBlockOtherModels`).
- **Cold mint template mismatch.** `synthPrefill` always mints via OpenAI
  `/v1/chat/completions`; a harness on a different upstream template (Anthropic `/v1/messages`)
  may tokenize differently → `confirm-miss`, no correctness harm. Upgrade path: mint via the
  request's own endpoint.
- **Anthropic system.** `sessionAnchor` falls back to the top-level `"system"` field when
  there's no system-role message.
- **Stats lock.** `record()` uses `statsMu` so it's callable inside any `stateMu`/`slotMu`
  section without reentrancy.
- **Recurrent / hybrid: `recurrentSkip`** — save, exact restore AND partial-prefix seeding
  are all a **no-op**. Whole-slot restore reuses **0 tokens** on GatedDeltaNet/SSM (state
  restorable only at its exact saved length → full reprocess; llama.cpp #21831), so
  `middleware` bails before reading the body. Detection: `newSlotCache`'s `recurrent`
  predicate reads the gguf (`autogen.ReadGgufMetadataCached`) and treats `FullAttnInterval > 0`
  as recurrent. **SWA (Gemma) and plain attention are NOT gated** — their restore does reuse.
  Ground truth is the KV Cache tab's confirm-hit/miss ratio: if a new arch shows
  `confirm-miss` waste, extend `recurrent`/`recurrentSkip`. Repro:
  `scripts/kvcache_probe.py switch` (warm) / `swap` (cross-process). Drop the guards in
  `middleware`/`saveOnEvict`/`restoreOnLoad` only if #21831 lands. The *in-RAM same-process*
  checkpoint path is separate and already on (`internal/autogen/CLAUDE.md`, `--ctx-checkpoints 2`).
- **Warm-slot skip (`preamble-warm`).** `onSwitch` does NOT restore the disk preamble when the
  slot already holds that exact preamble live — that would clobber valid live state; skipping
  lets upstream reuse the prefix natively. The disk preamble earns its keep on a genuinely
  cold load, **plain-attention models only**.

## Connections

Depends on:
- `internal/router` — local (`NewGroup`/`NewMatrix`) and peer routers; the scheduler/state owner this package dispatches into.
- `internal/config` — `Config`, model/capability/filter config, `RealModelName`, `SanitizeCommand`, `ListenerModelSets`.
- `internal/shared` — `FetchContext`/`SetContext`/`ReadContext`, error/response helpers, event payload types (`LiveTokensEvent`, `InFlightRequestsEvent`, `ProcessStateChangeEvent`, etc.).
- `internal/event` — pub/sub bus used by the SSE stream and inflight/metrics/live-token emitters.
- `internal/chain` — middleware composition.
- `internal/logmon` — proxy/upstream/mux log monitors backing `/logs`.
- `internal/perf` — system/GPU stats for `/api/performance` and `/metrics`.
- `internal/autogen` — sidecar override/settings I/O, gguf metadata, load-plan estimation (config editor only).
- `internal/cache`, `internal/ring` — capture cache and metrics ring buffer.

Called by: `quartermaster.go` (root entry) constructs the `Server` via `New`, wires `NewLoggers` and `SetAutogenAdmin`, and drives each listen address through `ServeListener`.
