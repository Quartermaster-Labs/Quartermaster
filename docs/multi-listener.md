<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Multiple ports & per-port model catalogs

Quartermaster can expose **several listen ports** from one process, each showing a different subset of models - e.g. an 'assistant' port and a separate 'tools/judge' port.

- **Per-port catalogs**: a request to a listener only sees (and can load) the models assigned to that port; `/v1/models` is filtered per port.
- **One shared GPU budget**: all listeners share ONE scheduler and VRAM accounting, so loading a model on one port can still evict a model loaded via another - there is a single GPU, not one per port. That shared accounting is the whole reason to use ports instead of running separate instances.
- **Config**: the `listeners:` block maps a bind address to swap groups (groups are auto-derived from model-name globs at autogen time). Requires the group router.
- **Access**: an API key can be scoped to specific models, which intersects with a port's catalog - see the api-keys article.

Changing a listener's bind address needs a restart; its model scoping updates live on config save.
