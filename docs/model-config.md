<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Per-model configuration (the cogwheel editor)

On the Models page, the cogwheel opens the per-model config editor. It shows the exact launch command and lets you tune **every parameter the backend supports for that model** - the sizer's defaults are just a starting point, and anything you touch becomes a per-model override.

The knobs are grouped by how often they're needed. The ones you'll reach for first:

- **Context size (ctx)**: how many tokens the model can hold. Bigger ctx uses more KV-cache VRAM.
- **Target VRAM**: a budget; Quartermaster computes GPU offload (`-ngl` / `--n-cpu-moe`) to fit. Lower target = more layers on CPU = slower but fits smaller cards.
- **Backend**: which engine runs this model (llama.cpp, vLLM, ...). Leave on auto or pin one; see the Backends article. Config is stored per-backend.

Under **Launch parameters** and **Advanced** you get the rest, and which ones appear depends on what kind of model it is (text, image, speech, embedding...): KV cache quantisation and whether KV lives in RAM, flash attention, speculative decoding, reasoning on/off and its budget, context checkpoints, threads, parallel slots, batch/ubatch sizes, mmap / mlock, a custom chat template, and more.

- **Extra args**: a raw passthrough appended to the launch command, for any flag the editor doesn't model. Nothing is off-limits - if the backend accepts it, you can set it here.
- **Variants**: alternate profiles of the same model (e.g. different ctx tiers) selectable per request, each able to override any of the above.

**Save & reload** applies changes live - a running model keeps serving under its old settings and the new ones take effect on its next load. The staging card shows what a running model is *actually* loaded with, which can differ from the pending config until it reloads.
