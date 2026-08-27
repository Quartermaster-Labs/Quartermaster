<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Loading and swapping models

You don't manually start models most of the time - sending a request to a model (from the playground or an API client) **loads it on demand**. If the GPU can't fit it alongside what's already running, Quartermaster stops enough of the resident models to make room (eviction), then loads yours.

- **Models page** (dashboard): browse the catalog, click a model to load or unload it explicitly.
- **VRAM budget decides who coexists**: models stay resident together as long as their estimated VRAM fits the budget set in Settings. Only the least-recently-used ones are evicted, and only until the new model fits - a small model rarely costs you a big one.
- **Requests queue, they don't fail.** A request for a model that isn't ready waits its turn; the connection is held open while the swap runs, and the playground shows *Loading…* or *Waiting its turn* (with your place in the queue) until it's granted.
- **Turn holding**: when a model finishes a request it stays un-evictable for about 10 seconds. That gap is what an agent loop spends running a tool call, and without the hold two agent loops on different models would swap the GPU back and forth every round instead of generating. A waiting request that has been patient long enough preempts the hold anyway - the playground gives up waiting after 60s, an unattended API client after 5 minutes.
- **Helpers coexist**: speech, transcription and segmentation models are kept out of the swap set, so reading a reply aloud doesn't evict the chat model you're talking to.
- **Idle unload**: a model with an unload timeout stops itself after being idle, freeing VRAM.
- First load of a large model is slow (reading weights from disk); subsequent swaps are faster while the file is warm in OS cache.

Advanced: `config.yaml` can also express coexistence and eviction as named groups. The generated config uses one exclusive group plus the helper groups above; there is no dashboard editor for groups, and with a VRAM budget set you rarely need to touch them.
