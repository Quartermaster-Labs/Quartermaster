<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Rerank and embeddings

Rerankers and embedding models have no playground UI - they are API-only, but they load, swap and share the VRAM budget like any other model.

**Rerank**: served on `/v1/rerank` (`/rerank`, `/reranking` and `/v1/reranking` are accepted too). Give it a query and a list of documents and it scores each document's relevance, so an external app (a RAG pipeline) can order results.

**Embeddings**: served on the OpenAI-compatible `/v1/embeddings` endpoint for vector search and semantic similarity.

Both take the model id in the JSON body like every other route, and both are gated by API keys and their model scopes.
