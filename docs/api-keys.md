<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# API keys and access

Quartermaster serves an **OpenAI-compatible API**, so existing clients work by pointing at your server's URL: `/v1/chat/completions`, `/v1/responses`, `/v1/completions`, `/v1/messages`, `/v1/embeddings`, `/v1/rerank`, `/v1/audio/speech` and `/v1/audio/transcriptions`, `/v1/images/generations` and `/v1/images/edits`, the `/sdapi/v1/*` image routes, `/v1/segment`, `/v1/images/upscale`, and the `/v1/tools/*` executors.

**Keys.** The **API Keys** page (dashboard) creates a named key and shows it masked with reveal/copy, and you can delete one at any time. Managing keys needs the server running with config editing enabled (`-generate`). A key is accepted as `Authorization: Bearer <key>`, as the password half of Basic auth, or as an `x-api-key` header. When no keys are configured the API is open.

**Scoping.** A key with no model scope has full access; pick models and it can only see and call those - `/v1/models` is filtered to the scope, so a scoped client's catalog matches what it may actually use. Scope can be edited after the key exists. Keys gate **inference and discovery** only; the `/v1/tools/*` endpoints accept any valid key and ignore model scope.

**The dashboard has no key** - on purpose, so enabling keys can never lock you out of your own UI. Instead it is gated by **where the request comes from**: as soon as any API listener binds beyond loopback, the dashboard, ops and config-editor routes answer only to this host. Add trusted networks with `-admin-allow` (e.g. `100.64.0.0/10` for a tailnet), or `-admin-open` to restore the old wide-open behaviour. The inference API stays reachable either way - that's what the keys are for. The playground is separate again: its own port, its own per-user login (see Playground accounts).
