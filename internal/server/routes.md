# internal/server — the route table

Everything is registered in `routes` (`server.go`). Model-dispatched routes (`modelChain` →
`localPeerHandler`) carry the full middleware chain; ops/UI routes use `adminChain`; the rest
`apiChain` (auth only). See [`http-core.md`](http-core.md) for what each chain does and for the
**admin gating rule a new route must follow**.

## Model-dispatched (lists in `server.go`)

- **`POST` JSON** — `/v1/chat/completions`, `/v1/responses`, `/v1/completions`,
  `/v1/messages`(+`/count_tokens`), `/v1/embeddings`, `/rerank`(+variants), `/infill`,
  `/completion`, `/v1/audio/speech`, `/v1/images/generations`, `/sdapi/v1/{txt2img,img2img}`,
  and the `/v/...` versionless equivalents.
- **`POST` form** — `/v1/audio/transcriptions`, `/v1/images/edits`.
- **`GET`** — `/v1/audio/voices`, `/sdapi/v1/loras`.

Auth-gated but **not** model-dispatched (`discoveryChain`):

- `GET /v1/models` (`api.go`) — catalog, scoped per listener and per API key.
- `POST /v1/images/upscale` (`upscale.go` `handleUpscale`) — standalone ESRGAN upscale,
  exec-per-request, no scheduler entry and no VRAM swap. Distinct from `/v1/segment` (SAM),
  which IS a model-dispatched backend.
- `POST /v1/tools/search`, `/v1/tools/youtube/transcript`, `/v1/tools/youtube/search`,
  `/v1/tools/youtube/comments` (`toolsapi.go`) — tool **execution** for external AI projects:
  the executors from `internal/tools`, same API-key credential as the inference routes, model-ready
  JSON responses, OpenAI-shaped errors. Stateless per call (provider config in the body).
  See [`tools.md`](tools.md#v1toolss-—-tool-execution-api).

## Catalog, ops and UI

- `GET /logs`, `/logs/stream[/{logMonitorID...}]`; `GET /health`, `/wol-health`, `/{$}` redirect;
  `GET /ui/`, `/favicon.ico` (embedded SPA); `GET /metrics` (Prometheus); `GET /unload`,
  `/running`; `GET /upstream`, `/upstream/{upstreamPath...}`.
- `POST /api/models/unload[/{model...}]`; `GET /api/events`, `/api/metrics`, `/api/performance`,
  `/api/version`, `/api/captures/{id}`.
- `GET /api/catalog` — the whole local catalog as JSON (the `/api/events` `modelStatus` payload,
  pullable). Unlike `/v1/models` it **keeps unlisted variants and is NOT filtered by an API key's
  model scope**, which is why `quartermaster_inspect` reads it.
- Context / observe extras: `GET /api/canon`, `/api/backend-metrics`, `/api/websearch`,
  `/api/youtube/meta`, `/api/imgproxy`, `/api/fx`.
- Autostart: `GET`/`PUT /api/autostart` — **not** `-generate`-gated.
- Self-update: `POST /api/update` (Windows release builds only).

## Config editor — 501 without `-generate`

See [`configapi.md`](configapi.md).

`GET /api/models/{model}/config`; `PUT`/`DELETE /api/models/{model}/override`;
`PUT /api/models/{model}/variant`; `GET /api/models/{model}/estimate`;
`PUT /api/models/{model}/preview` (cmd preview); `PUT /api/models/{model}/adhoc-cmd` (one-off
flag-override cmd — no persistence, no reload); `PUT`/`DELETE /api/models/{model}/adhoc-load`
(inject that cmd into the LIVE router; in-memory only, DELETE or any file reload reverts);
`PUT`/`DELETE /api/models/{model}/display-name`; `GET`/`PUT`/`DELETE /api/settings`;
`PUT /api/settings/slotcache`; `PUT /api/settings/backends`; `PUT /api/default-variants`;
`POST /api/pick-folder` + `POST /api/settings/root/pick`; `POST /api/pick-file` (whitelisted
kinds only — `pickfile_spec.go`); `GET`/`POST /api/apikeys` + `DELETE /api/apikeys/{name}`.

## Managed backend installs (`backendsapi.go`, admin-gated)

`GET /api/backends/catalog` (local-only, never calls GitHub), `GET
/api/backends/{component}/releases` (`?refresh=1`), `POST /api/backends/install`,
`GET /api/backends/jobs` (progress polling), `POST /api/backends/activate` (which installed build
the row points at), `POST /api/backends/default` (★ for its class — a separate axis from
activate), `POST /api/backends/uninstall`.

Distinct from `GET /api/backends` + `PUT /api/settings/backends`, which are the **hand-entered**
registry.

### Tracked custom repos (`backendsources.go`, admin-gated)

`GET /api/backends/sources` (the tracked repos, in editable form), `GET
/api/backends/sources/assets?repo=&tag=&refresh=1` (one release's asset list, for an
**untracked** repo too — this is the picker that replaces typing a regex), `POST
/api/backends/sources` (create/update; the server derives the asset patterns from the picked
names), `POST /api/backends/sources/delete` (409 while builds are installed), and `GET
/api/backends/{component}/resolve?variant=&version=` (the file an install would download now).

A tracked repo is merged into the catalog, so it installs through the same
`install`/`activate`/`default`/`uninstall` routes as a built-in.

## Model hub browser (`hubapi.go`, admin-gated)

See [`hubapi.md`](hubapi.md).

- `GET /api/hub/sources`.
- `GET /api/hub/search` — `q` empty = the hub's own top-by-downloads listing, which is what the
  page opens on. `maxParams` caps by parameter count read from the repo name, `maxAgeDays` is the
  Trendy gate over the repo's **creation** date, and `skip` pages — the response carries
  `nextSkip`/`hasMore`, which page by the **hub's** row count rather than the filtered one (see
  `internal/hub/CLAUDE.md`).
- `GET /api/hub/model/{id...}` — the `{id...}` wildcard is required, a repo id carries its own slash.
- `GET /api/hub/avatar` — publisher avatar URL; a miss is **200 with an empty url, not 404**, since
  the caller draws a monogram either way.
- `GET /api/hub/estimate` (`hubapi_estimate.go`) — per-file context sizing from the Range-fetched
  GGUF header. 501 without `-generate`: there is no VRAM target to size against.
- `GET /api/hub/jobs` — progress polling, same shape as the backends installer.
- `POST /api/hub/download`, `/api/hub/pause`, `/api/hub/resume`, `/api/hub/cancel` — the three job
  verbs share one body shape and one handler body (`hubJobAction`); **cancel discards the bytes,
  pause does not** (see `internal/hub/CLAUDE.md`).
- `POST /api/hub/reveal` (`revealfolder.go`) — opens a folder inside the models root in the OS file
  manager; empty path = the root.

## Playground app (on `-playground-port`, `playground.go`)

See [`playground.md`](playground.md).

`GET /api/mode`; `POST /auth/login`, `/auth/logout`, `GET /auth/me`;
`GET`/`PUT /api/{chats,imagechats,speechchats,prefs}`; `GET /api/media/{file...}`;
assistant memory `GET`/`POST /api/memories` + `DELETE /api/memories/{id}` (`memories.go`);
server-run turns `POST /api/chats/turn` + `/stream`, `/state`, `/approve` + `DELETE` (`turns.go`).
