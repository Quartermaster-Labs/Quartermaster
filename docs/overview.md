<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# What is Quartermaster

Quartermaster is an all-in-one **local inference engine** - a front-end over llama.cpp (text/vision models) and stable-diffusion.cpp (images) that runs entirely on your own machine. Nothing is sent to a cloud service; weights, prompts, and conversations stay local.

It discovers the model files you have on disk, works out sensible runtime settings for each one automatically (context length, GPU offload, KV cache), and **hot-swaps** models in and out of VRAM on demand - a request for a model that isn't loaded triggers a load (evicting others if needed).

Two web UIs: the **operator dashboard** (main port - catalog, loading, config, metrics) and the **playground** (its own port - chat, images, speech, transcription, rerank).
