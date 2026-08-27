<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Automatic config generation

Rather than hand-writing a config per model, Quartermaster **auto-generates** one at startup: it scans your model folders for GGUF/checkpoint files, estimates each model's VRAM footprint (weights + KV + compute buffers), and derives runtime flags (context, GPU offload, KV type) that fit your hardware.

This means adding a model is usually just dropping the file in the watched folder - no manual YAML. You can still override any of it per-model in the cogwheel editor, and those overrides survive regeneration.
