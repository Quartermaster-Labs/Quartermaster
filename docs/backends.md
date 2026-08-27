<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Backends (install, update, pick an engine)

**Settings -> Backends** is where the inference engines live. It has two layers: the **managed catalog** on top (install and update engines from their upstream GitHub releases, LM-Studio style) and the plain **registry** below it (a row per binary: kind, name, path to the executable).

**Installing an engine.** The catalog is tabbed by what a backend is *for* - Text, Image, Upscale, Speech, Transcription, Segmentation, Tools. Pick a component and **Install latest**, or choose an older release from its version list. Quartermaster matches the release asset to your GPU (CUDA / ROCm / Vulkan / CPU) and unpacks it into its own versioned folder, so several builds of the same engine can sit side by side and you can switch which one is **active** - or roll back - without redownloading. **Update** appears when upstream ships a newer release. **Track a repo** points the same machinery at any GitHub repo you follow, including your own builds.

**Not everything is installable.** Engines with no upstream release - a locally built qwentts `tts-server`, `sam3_server`, a hand-patched sd-server - are added the old way: **+ Add backend**, pick the kind, browse to the exe. Managed and manual rows coexist in one registry and behave identically once registered. `yt-dlp` installs from the same catalog but is a helper tool, not a backend, so it never gets registered or starred.

**Auto-pick (★).** Each backend can be starred as the default for its class (text / image / speech / transcription / segmentation / upscale). When a model loads, the model's explicit choice wins, else the ★-starred backend, else the only one registered for that class. With one llama build everything just works; add a second (say Vulkan *and* ROCm) and the star decides.

**Per-model override.** In the cogwheel editor each model has a **Backend** dropdown, filtered to the backends valid for its class. Config is *keyed to the backend*: a model's llama tuning (ctx / target VRAM / variants) and its vLLM knobs are stored separately, so switching engines never wipes the other set.

**vLLM.** Selecting a vLLM backend launches `vllm serve <gguf> --quantization gguf --gpu-memory-utilization <util> --max-model-len <ctx> [--tensor-parallel-size N]`, reusing the same discovered GGUF. vLLM self-manages VRAM, so the llama estimate panel and offload/KV planner are hidden - you get **GPU utilization** (fraction of VRAM, default 0.90) and **tensor-parallel size** (GPUs to shard across). It is slow to start and not every GGUF architecture runs on it.
