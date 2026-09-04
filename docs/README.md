<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Quartermaster user guide

The same help wiki the app ships with, reachable in-app from **Help** in the sidebar,
and searchable by the playground assistant itself via its `wiki_search` tool.

Looking for how quartermaster works *inside*? That lives beside the code it
describes: see the subsystem table in [`CLAUDE.md`](../CLAUDE.md).

## Getting started

- [What is Quartermaster](overview.md)
- [Updating Quartermaster](updating.md)

## Models & config

- [Loading and swapping models](loading-models.md)
- [Per-model configuration (the cogwheel editor)](model-config.md)
- [Config variants (normal vs fleet-wide)](config-variants.md)
- [Automatic config generation](autogen.md)
- [Backends (install, update, pick an engine)](backends.md)
- [Multiple ports & per-port model catalogs](multi-listener.md)

## Playground

- [Chat playground](playground-chat.md)
- [Playground accounts](playground-login.md)
- [Web search](web-search.md)
- [Video & audio transcripts (YouTube and beyond)](youtube.md)
- [Quartermaster tools (assistant self-service)](qm-tools.md)
- [Image generation](images.md)
- [Upscaling images](upscale.md)
- [Image segmentation (SAM)](segmentation.md)
- [Speech and transcription](speech-audio.md)
- [Rerank and embeddings](rerank-embed.md)
- [Playground settings](settings.md)

## Monitoring & VRAM

- [Observe: activity, logs, performance, KV cache](observe.md)
- [GPU memory / VRAM](gpu-memory.md)
- [KV-cache disk save (survive eviction)](slot-kv-cache.md)

## API & access

- [HTTP API reference](http-api.md)
- [API keys and access](api-keys.md)
- [Tools API (search & YouTube for your own apps)](tools-api.md)

## Troubleshooting

- [Troubleshooting common issues](troubleshooting.md)
- [Known issues & hardware limits](known-issues.md)
