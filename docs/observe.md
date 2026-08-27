<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Observe: activity, logs, performance, KV cache

The **Observe** page has four tabs, plus a shared time window (5 min / 15 min / 1 hr / All) that filters Activity rows and trims the Performance charts.

- **Activity**: per-request history. Summary tiles at the top (requests, cache hits, tokens processed and generated, median speed) with prompt-processing and token-generation histograms; below them a table you can filter by model / path / status, with a column picker you can reorder - id, time, model, path, status, content type, cached, prompt and generated tokens, prompt t/s, gen t/s, duration. Click a row's capture to open the full request/response body.
- **Logs**: live logs, shown as **Both**, **Proxy** only (Quartermaster's own lifecycle log) or **Upstream** only (the backend's raw stdout). Each panel has a regex line filter, text wrap, and copy.
- **Performance**: charts over time - GPU utilization, GPU memory, temperature (plus VRAM temperature and power draw where the card reports them), per-core CPU, memory and swap, load average, and network bandwidth. The refresh interval is selectable (off / 5s / 10s / 30s / 60s).
- **Context**: two panels. **KV Cache** shows the live slots, the saved snapshot files, preamble mints, restore success rate, disk usage and recent save/restore activity (see the KV-cache article). **Prompt Canonicalization** shows how many requests were rewritten to a stable prefix, how many bytes that trimmed, and the recent rewrites.

Live metrics stream over SSE, so the page updates in real time while a model generates.
