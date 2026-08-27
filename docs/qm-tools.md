<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Quartermaster tools (assistant self-service)

**Quartermaster tools** let the chat assistant look at - and, with your approval, tune - the quartermaster instance it's running in. Toggle them per chat with **QM Tools** in the Configs popover (on by default). They only act on this running instance, and they never load or unload models - that would evict the very model you're talking to.

**Inspect (read-only).** One slice per call, so the assistant pulls only what it needs:
- **status / loaded / vram** - what's running right now, with idle-TTL, live GPU/VRAM and system RAM.
- **models** - the whole installed catalog (not just loaded ones) with capabilities, context length, state and variant count.
- **settings** - the global memory knobs.
- **backends** - the registry: which executable and managed build each class runs, which is the ★ auto-pick, and whether any exe has gone missing from disk.
- **a model id** - that model's effective config plus its variants, each shown as the exact launch-flag deviations from the base command.
- **estimate** - a what-if load plan for a model: chosen context, GPU/CPU layer split, estimated VRAM against the budget and RAM against the cap. It can size a *proposed* tuning before suggesting it, and nothing is changed by asking.
- **logs** - the last lines of quartermaster's own lifecycle log, or the raw backend output (where a CUDA/Vulkan allocation failure actually shows), or both. This is what lets it diagnose a load failure or crash you just hit.
- **fields** - the complete list of settings it is able to change, with types.

So "what models do I have?", "how much VRAM is free?", "why did that model fail to load?" and "would a 64k context still fit?" are all answerable from the real state rather than a guess.

**Configure (approval-gated).** It can change:
- **Global memory settings** - VRAM target, headroom, max RAM, idle-unload TTL (the dashboard Settings).
- **A model's config** - context, KV quant, CPU offload and the rest - or **one named variant** of it.
- **Your playground preferences** - temperature, max tokens, thinking budget, web search on/off and the search knobs. These are your own per-user prefs and take effect on your next page reload.

**You approve every change.** When the assistant wants to configure something, the chat pauses and shows a **before -> after diff card** with **Accept** and **Deny**. Nothing is applied until you accept; deny - or just ignore it, it times out - and nothing happens. Accepting a global or model change hot-reloads the config in place: running models are **not** evicted, and a changed model's new launch args apply the next time it loads.

Editing needs the server running with config editing enabled (`-generate`); inspection works either way.
