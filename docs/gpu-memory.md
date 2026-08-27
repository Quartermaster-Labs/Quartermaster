<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# GPU memory / VRAM

Quartermaster tracks GPU memory live; the gauge in the status rail breaks the used VRAM into labelled segments, and the VRAM budget already accounts for what the system and other apps hold, so its estimates target *free* VRAM.

**The breakdown.** With a single model loaded and a fresh load-plan estimate for it you get the full split - **System** (OS and other apps), **Weights**, **Vision projector**, **Draft** (speculative/MTP model), **Compute buffer** (logits + activations), **KV cache**, **Checkpoints**, **Headroom** (the reserved safety margin), and **Overhead** (measured usage beyond the estimate). Otherwise - no estimate yet, or more than one model resident - the model side collapses to a single **Model(s)** segment. The estimated parts are fitted to the measured slice, so the bar always adds up to what the driver reports.

- **System** is measured from an idle floor. If no idle sample has been seen yet (a model was already resident when the page loaded) the segment says *estimated*.
- **Foreign**: VRAM held by llama-server/sd-server processes Quartermaster didn't spawn (a stray llama.cpp), listed by process name. It counts these so it won't overcommit.
- If a model won't load or crashes on load, the usual cause is **not enough free VRAM**. Lower that model's **Target VRAM** (pushing more layers to CPU) or its **context size** in the cogwheel editor, or unload other models. The server-wide **Target VRAM** and **Headroom** live in dashboard Settings - raise Headroom if loads succeed but OOM moments later.
