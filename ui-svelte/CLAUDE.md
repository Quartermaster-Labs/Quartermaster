# ui-svelte — web dashboard

## Purpose

`ui-svelte` is the single-page web dashboard for llama-quartermaster. It is served by the Go server under the `/ui/` path and gives an operator live visibility and control over the model swapper: model catalog and loading, per-model config tuning, live activity/metrics/logs, GPU memory, and an interactive model playground (chat, images, speech, transcription, rerank, load testing).

## Tech stack & build

- **Svelte 5** (runes mode), **TypeScript**, built with **Vite 8**.
- **Tailwind CSS v4** (via `@tailwindcss/vite`) plus a hand-written `src/index.css` for theme tokens.
- Client-side routing with `svelte-spa-router` (hash routing).
- Notable deps: `chart.js` (performance charts), `highlight.js` + the `unified`/`remark`/`rehype` + `katex` pipeline (markdown/math rendering in chat), `lucide-svelte` (icons).

Build output goes to `../internal/server/ui_dist` (see `vite.config.ts` `build.outDir`), which the **Go server embeds** and serves. The build also emits gzip and brotli pre-compressed assets via `vite-plugin-compression2`. The app is mounted under `base: "/ui/"`.

Key npm scripts (run from `ui-svelte/`):

| Script | Purpose |
|---|---|
| `npm start` | Vite dev server. Proxies `/api`, `/logs`, `/upstream`, `/unload`, `/v1`, `/sdapi` to the Go backend (`LLAMA_SWAP_URL`, default `http://localhost:8080`). |
| `npm run build` | Production build into `ui_dist` (with `--emptyOutDir`). |
| `npm run check` | `svelte-check` type checking. |
| `npm test` / `npm run test:watch` | Vitest unit tests (e.g. `histogram.test.ts`, `markdown.test.ts`, `modelUtils.test.ts`). |

From the repo root, **`make test-ui`** runs `npm ci && npm run check && npm test`. Run it after changing anything under `ui-svelte/`.

## Directory layout

| Dir / file | Role |
|---|---|
| `src/main.ts`, `src/App.svelte` | Entry point and root layout (Sidebar + StatusRail + Router + always-mounted Playground). |
| `src/routes/` | Top-level pages mounted by the router. |
| `src/components/` | Reusable UI components (panels, modals, gauges, charts, tooltips). |
| `src/components/playground/` | The model playground's per-mode interfaces and shared playground widgets. |
| `src/stores/` | Svelte stores: backend state, SSE wiring, persisted prefs, theme, routing. |
| `src/lib/` | Framework-agnostic helpers: API client modules, shared `types.ts`, markdown/histogram utilities, the `scrollFade` action. |
| `index.css` | Theme tokens, dark/light variables, and shared component classes (e.g. `.card`, `.scroll-fade-y`). |
| `vite.config.ts`, `svelte.config.js`, `tsconfig.json` | Build / compiler / TS configuration. |

## Routes

Routing is hash-based (`svelte-spa-router`). The router table lives in `App.svelte`:

- **`/` — Dashboard** (`routes/Dashboard.svelte`): landing page; model overview, quick-load picker, GPU-memory / settings card.
- **`/models` — Models** (`routes/Models.svelte`): full model catalog, sectioned by swap group / listener; load/unload and the per-model config editor (cogwheel → `ModelConfigModal`).
- **`/observe`, `/logs`, `/activity`, `/performance` — Observe** (`routes/Observe.svelte`): a single tabbed page with **Activity**, **Logs**, and **Performance** tabs (rendered from `Activity.svelte`, `LogViewer.svelte`, `Performance.svelte`). The legacy `/logs`, `/activity`, `/performance` deep-links just preselect the matching tab.
- **`/test` — Playground** (`routes/Playground.svelte`): the interactive model playground. It is **always mounted** (kept alive via CSS `hidden` toggling, not the router) so streaming state and attachments survive navigation. Tabs: Chat, Images, Speech, Transcription (audio), Rerank, Load Test (concurrency).

## State / API

Backend communication is centralized in `src/stores/api.ts`, with shared types in `src/lib/types.ts`.

- **Live updates use SSE.** `enableAPIEvents()` opens an `EventSource` on `/api/events` with auto-reconnect (exponential backoff). The envelope `type` field is dispatched to writable stores: `modelStatus` → `models`, `logData` → `proxyLogs`/`upstreamLogs`, `metrics` → `metrics`, `inflight` → `inFlightRequests`, `liveTokens` → `liveTokens` (live generation progress). Connection state is tracked in `connectionState` (drives the title-bar status dot).
- **Polling** is used for performance history: `startPerfPolling()` (`stores/perf.ts`) periodically calls `fetchPerformance()` against `/api/performance`.
- **Request/response (REST):** model load/unload go to `/upstream/<model>/` and `/api/models/unload[...]`; the config editor uses `/api/models/<id>/config`, `/override`, `/variant`, and a live `/estimate` load-plan preview; global tuning uses `/api/settings`. Playground mode interfaces call the OpenAI-compatible / SD endpoints through their own `lib/*Api.ts` modules (`chatApi`, `imageApi`, `audioApi`, `speechApi`, `rerankApi`, `sdApi`).
- **Persistence:** `stores/persistent.ts` provides `persistentStore` (localStorage-backed) used for UI prefs and tallies (e.g. per-model `loadCounts`, selected playground tab).

## Conventions

- **Svelte 5 runes** throughout — `$state`, `$derived`, `$effect`, `$props`; stores are read with the `$store` auto-subscription. Components are mounted with `mount()` (`main.ts`), not the legacy constructor API.
- **Component organization:** generic widgets live in `src/components/`; the playground's mode-specific interfaces and helpers are grouped under `src/components/playground/`. Pages live in `src/routes/` and stay thin, delegating to components and stores.
- **Fork-specific UI** (added on top of upstream llama-quartermaster): the per-model config editor (`ModelConfigModal.svelte`, dynamic ctx / VRAM-target / variant tuning), live metrics & activity (`ActivityStats.svelte`, `StatusRail.svelte`, `InferenceFeedback.svelte`, live-token readout), VRAM gauge, and the multi-mode model playground.
- **Styling:** Tailwind utility classes plus theme tokens and shared classes defined in `src/index.css` (dark/light via the `data-theme` attribute set in `App.svelte`). The `scrollFade` action (`lib/scrollFade.ts`) backs `.scroll-fade-y` edge-fade containers.
- **Tests:** pure-logic helpers in `src/lib/` carry colocated `*.test.ts` Vitest specs; keep new utilities testable and add specs there.
