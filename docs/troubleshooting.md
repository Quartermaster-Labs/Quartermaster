<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Troubleshooting common issues

Common issues and fixes:

- **Model won't load / crashes on load** -> usually not enough free VRAM. Lower its Target VRAM or context in the cogwheel editor, or unload other models. Check Observe -> Logs (Upstream) for what the backend actually said.
- **"Exited prematurely" right after launch, nothing in the log** -> the backend exe couldn't load its DLLs. Quartermaster names the missing library in the proxy log (e.g. a ROCm/HIP runtime); install that runtime or switch to a build that ships it.
- **Generation is slow** -> too many layers on CPU (low VRAM target), or a very large context. Raise Target VRAM if you have headroom, and check the model's split in the cogwheel editor's load plan.
- **A long chat re-processes its whole prompt after a swap** -> that's a cold prefill. Turn on KV-cache disk save for that model so the cache survives eviction (see its article).
- **Your request sits at "Waiting its turn"** -> another request holds the model, or one is queued ahead of you (the position is shown). It starts as soon as the model frees up; nothing is lost. If it happens constantly, the two models in play don't fit in VRAM together.
- **Model swapped out while you weren't looking** -> a request for another model needed the VRAM, so the scheduler evicted the least recently used one. Give the model you care about more of the budget, or keep the other one unloaded.
- **Web search fails** -> open Settings -> Web Search and hit **Test** on the provider row. A provider marked "enabled but not configured" is skipped, not tried; the chain falls through to the next one, so a working fallback (DuckDuckGo) hides a broken primary.
- **YouTube tools answer 503** -> yt-dlp isn't installed or isn't on PATH. Install it from the Backends tab (Tools) or re-run the installer with the yt-dlp helper ticked. Transcripts also need a video that actually has captions.
- **Blurry or burnt images** -> turbo/lightning/distilled checkpoints need CFG = 1.0. SDXL/SD need a full checkpoint file. See also Known issues for the AMD Vulkan allocation cap.
- **A client gets 401** -> its API key is missing, wrong, or sent in a header the server doesn't read (use `Authorization: Bearer`, Basic password, or `x-api-key`).
- **A model is missing from a client's `/v1/models`** -> the key is scoped to a subset of models, or the port it connects to has its own catalog. See API keys and access.
- **The dashboard is unreachable from another machine** -> that's deliberate: binding beyond loopback restricts the admin surface to this host. Add the network with `-admin-allow`.
- **Config change didn't take effect** -> most settings apply live, but a running model keeps the arguments it launched with until it reloads; the staging card shows what's actually running. Changing a listener's bind address needs a restart.
