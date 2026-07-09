# ui-svelte — web dashboard

## Purpose

`ui-svelte` is the single-page web app for llama-quartermaster, served by the Go server under `/ui/`. **One bundle, port-gated into two apps** (`App.svelte` `onMount` fetches `GET /api/mode`):

- **Operator dashboard** (main listen port): model catalog and loading, per-model config tuning, live activity/metrics/logs, GPU memory, API-key management.
- **Playground** (separate `-playground-port`): a login-gated, per-user interactive app (chat, images, speech, transcription, rerank, load testing) with **server-backed** chat history + prefs. Rendered by `PlaygroundApp`, NOT mounted inside the dashboard.

## Tech stack & build

- **Svelte 5** (runes mode), **TypeScript**, built with **Vite 8**.
- **Tailwind CSS v4** (via `@tailwindcss/vite`) plus a hand-written `src/index.css` for theme tokens.
- Client-side routing with `svelte-spa-router` (hash routing).
- Notable deps: `chart.js` (performance charts), `highlight.js` + the `unified`/`remark`/`rehype` + `katex` pipeline (markdown/math rendering in chat), `lucide-svelte` (icons).

Build output goes to `../internal/server/ui_dist` (see `vite.config.ts` `build.outDir`), which the **Go server embeds** and serves. The build also emits gzip and brotli pre-compressed assets via `vite-plugin-compression2`. The app is mounted under `base: "/ui/"`.

Key npm scripts (run from `ui-svelte/`):

| Script | Purpose |
|---|---|
| `npm start` | Vite dev server. Proxies `/api`, `/logs`, `/upstream`, `/unload`, `/v1`, `/sdapi` to the Go backend (`QUARTERMASTER_URL`, default `http://localhost:8080`). |
| `npm run build` | Production build into `ui_dist` (with `--emptyOutDir`). |
| `npm run check` | `svelte-check` type checking. |
| `npm test` / `npm run test:watch` | Vitest unit tests (e.g. `histogram.test.ts`, `markdown.test.ts`, `modelUtils.test.ts`). |

From the repo root, **`make test-ui`** runs `npm ci && npm run check && npm test`. Run it after changing anything under `ui-svelte/`.

## Directory layout

| Dir / file | Role |
|---|---|
| `src/main.ts`, `src/App.svelte` | Entry point + root. `App.svelte` fetches `/api/mode` and renders either the dashboard shell (Sidebar + StatusRail + Router) **or** the standalone `PlaygroundApp` when serving on the playground port. |
| `src/routes/PlaygroundApp.svelte` | Playground root: gates the whole app behind login (`playgroundAuth` `me`), hydrates server-backed chats/prefs, then mounts `PlaygroundShell`. |
| `src/routes/PlaygroundShell.svelte` | Playground shell: icon side-rail (Chat / Images / Speech / Transcription / Rerank / Load Test, hover-expand), chat-history flyout toggle, logout + username. |
| `src/routes/Login.svelte` | Playground username/password login screen (plaintext, registers unknown users). |
| `src/routes/` | Top-level pages mounted by the router. |
| `src/components/` | Reusable UI components (panels, modals, gauges, charts, tooltips). |
| `src/components/playground/` | The model playground's per-mode interfaces and shared playground widgets. |
| `src/stores/` | Svelte stores: backend state, SSE wiring, persisted prefs, theme, routing. |
| `src/lib/` | Framework-agnostic helpers: API client modules, shared `types.ts`, markdown/histogram utilities, the `scrollFade` action. |
| `index.css` | Theme tokens, dark/light variables, and shared component classes (e.g. `.card`, `.scroll-fade-y`). |
| `vite.config.ts`, `svelte.config.js`, `tsconfig.json` | Build / compiler / TS configuration. |

## Routes

Routing is hash-based (`svelte-spa-router`). The router table lives in `App.svelte`:

- **`/` — Dashboard** (`routes/Dashboard.svelte`): landing page; the shared live-models panel (`components/ActiveModelsPanel.svelte` — launch params + `InferenceFeedback` for whatever is loaded) plus the quick-load picker (ranked by per-**family** load tally, so loading any variant floats the family up). No GPU/activity duplication of the StatusRail/Observe; the config knobs live on `/settings`.
- **`/settings` — Settings** (`routes/Settings.svelte`): the global config knobs — memory budget (target VRAM / headroom / max RAM), **idle unload (ttl)**, and the experimental slot KV-cache disk-save section. All 501-gated on `-generate`; each save regenerates the config + hot-reloads.
- **`/models`, `/models/:category` — Models** (`routes/Models.svelte`): full model catalog, sectioned by swap group / listener; load/unload and the per-model config editor (cogwheel → `ModelConfigModal`). `:category` filters via `modelUtils` categories (drives the sidebar Models sub-menu).
- **`/observe`, `/logs`, `/activity`, `/performance` — Observe** (`routes/Observe.svelte`): a single tabbed page with **Activity**, **Logs**, **Performance**, and **Context** tabs. Context (`Context.svelte`) is itself sub-tabbed: **KV Cache** (`KvCache.svelte`) + **Prompt Canonicalization** (`Canon.svelte`). Legacy `/logs`, `/activity`, `/performance` deep-links preselect the matching tab.
- **`/api-keys` — API Keys** (`routes/ApiKeys.svelte`): create / scope / reveal / delete inference API keys (only when the server runs with `-generate`).
- **`/test` — Playground stub** (`routes/PlaygroundStub.svelte`): the dashboard no longer hosts the playground; this page links out to the standalone playground app on `playgroundPort`. The real playground is `PlaygroundApp`/`PlaygroundShell` (served on the playground port, see Directory layout).

## State / API

Backend communication is centralized in `src/stores/api.ts`, with shared types in `src/lib/types.ts`.

- **Live updates use SSE.** `enableAPIEvents()` opens an `EventSource` on `/api/events` with auto-reconnect (exponential backoff). The envelope `type` field is dispatched to writable stores: `modelStatus` → `models`, `logData` → `proxyLogs`/`upstreamLogs`, `metrics` → `metrics`, `inflight` → `inFlightRequests`, `liveTokens` → `liveTokens`, `backendMetrics` → backend KV-fill/throughput gauges. Connection state is tracked in `connectionState` (drives the title-bar status dot).
- **Polling** is used for performance history: `startPerfPolling()` (`stores/perf.ts`) periodically calls `fetchPerformance()` against `/api/performance`; `stores/vram.ts` derives a `vramBreakdown` (System / Weights / Draft / KV / Checkpoints / CUDA / Foreign) via `estimateSegments`.
- **Request/response (REST):** model load/unload go to `/upstream/<model>/` and `/api/models/unload[...]`; the config editor uses `/api/models/<id>/config`, `/override`, `/variant`, `/preview`, and a live `/estimate` load-plan preview; global tuning uses `/api/settings`. Observe fetches `/api/kvcache` (`fetchKvCache`) + `/api/canon` (`fetchCanon`). API-key page uses `listApiKeys`/`upsertApiKey`/`deleteApiKey`. Sidebar self-update uses `versionInfo` + `/api/update` (`runUpdate`). Playground mode interfaces call the OpenAI-compatible / SD endpoints through their own `lib/*Api.ts` modules (`chatApi`, `imageApi`, `audioApi`, `speechApi`, `rerankApi`, `sdApi`).
- **Persistence:** `stores/persistent.ts` (`persistentStore`, localStorage) for dashboard prefs/tallies. Playground state is **server-backed**: `stores/chatHistory.ts` (`chatSessions`/`activeChatId`/`generatingChatId`, `loadChats` + debounced PUT `/api/chats`), `stores/prefs.ts` (`userPref()` bound to `/api/prefs`), `stores/playgroundAuth.ts` (`me`/`login`/`logout`/`checkMe` via `/auth/*`, `playgroundPort`). `stores/observe.ts` holds Observe tab/window state; `stores/playgroundActivity.ts` drives the sidebar activity dot.

## Conventions

- **Svelte 5 runes** throughout — `$state`, `$derived`, `$effect`, `$props`; stores are read with the `$store` auto-subscription. Components are mounted with `mount()` (`main.ts`), not the legacy constructor API.
- **Component organization:** generic widgets live in `src/components/`; the playground's mode-specific interfaces and helpers are grouped under `src/components/playground/`. Pages live in `src/routes/` and stay thin, delegating to components and stores.
- **Fork-specific UI** (added on top of upstream llama-quartermaster): the per-model config editor (`ModelConfigModal.svelte`, dynamic ctx / VRAM-target / variant tuning), the shared live-models panel (`ActiveModelsPanel.svelte`, used by both `Dashboard` and `ModelsPanel`), the global Settings page (`Settings.svelte`), live metrics & activity (`ActivityStats.svelte`, `StatusRail.svelte`, `InferenceFeedback.svelte`, live-token readout), VRAM gauge, request/response inspector (`CaptureDialog.svelte`), API-key page, and the standalone multi-mode playground.
- **Playground feature components** (`src/components/playground/`):
  - `ChatInterface.svelte` — chat with **vision** (paperclip image attach → `ContentPart`/`getImageUrls`), **Rewrite mode** (`sendRewrite` + side-by-side `RewriteDiff.svelte` via `lib/wordDiff.ts`), **web search** (SearXNG tool-calling, `lib/webSearch.ts` → `/api/websearch`), a live KV context-usage bar, and **auto-compaction** (`lib/chatCompact.ts`: `summarizeConversation`/`generateTitle`, `COMPACT_AT`/`KEEP_RECENT`).
  - `ImageInterface.svelte` — full SD image-gen UI: txt2img/img2img (`ImageGenMode`), denoise/upscale, hires (`enable_hr`), reference images (`extra_images`, Kontext), per-model defaults, style presets, seed modes.
  - `MaskEditor.svelte` — canvas brush painter producing a PNG mask data URL for sd-server inpainting.
- **Help wiki** (`src/lib/wiki.ts` + `components/WikiModal.svelte`): one array of help articles is the single source for both the **Help** button (dashboard `Sidebar` + playground rail → `WikiModal`) and the `wiki_search` tool the chat models call. The tool is always advertised in chat (local, no network), dispatched in `ChatInterface`'s tool loop alongside `web_search`.
- **Playground libs** (`src/lib/`): `webSearch.ts` (SearXNG tool), `chatCompact.ts` (auto-compaction + title gen), `wordDiff.ts` (rewrite diff), `reasoning.ts` (Harmony/reasoning parsing), `inferenceAuth.ts` (`refreshInferenceKey`/`inferenceHeaders` — auto-attach API key to playground inference), `modelUtils.ts` (`modelCategory`/`MODEL_CATEGORIES`, drives Models sub-menu + key scoping).
- **Styling:** Tailwind utility classes plus theme tokens and shared classes defined in `src/index.css` (dark/light via the `data-theme` attribute set in `App.svelte`). The `scrollFade` action (`lib/scrollFade.ts`) backs `.scroll-fade-y` edge-fade containers.
- **Tests:** pure-logic helpers in `src/lib/` carry colocated `*.test.ts` Vitest specs; keep new utilities testable and add specs there.
