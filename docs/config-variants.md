<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Config variants (normal vs fleet-wide)

A **variant** is a named alternate profile of a model - most often a **high- or low-context** version. Each variant surfaces as its own selectable entry (the variant name becomes an id suffix, e.g. `mymodel-32k` or `mymodel-128k`) and launches with its own settings - context size, VRAM target, KV type, speculative decoding, sampler, and the rest. Pick a variant in the playground's model selector; each loads and swaps independently, like any other model.

Why bother: a bigger context window holds more conversation/documents but costs more VRAM, so a low-context variant fits on a smaller card or leaves room for other models, while a high-context variant is there when you need the extra room.

**Inheritance**: a variant only overrides the fields you set on it; anything left blank inherits the model-wide config. So a high-context variant can set just `ctx` and inherit everything else.

Two kinds, both edited in the cogwheel config editor:

- **Normal (per-model) variants** - belong to one model and appear only on it. Use them for that model's context tiers (e.g. 32k / 64k / 128k) or a specially tuned profile of that one model.
- **Fleet-wide (default) variants** - shared by **every** model, saved globally (not on any single model). Use them for a profile you want available everywhere, like a standard low- and high-context pair. They're edited in the same editor but saved separately, so a change applies across the whole fleet at once.

Autogen can also emit **context-tier variants automatically** from a model's ctx tiers, so you often get 32k/64k/128k options without defining them by hand.
