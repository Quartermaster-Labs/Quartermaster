<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Upscaling images

Quartermaster upscales any image with an ESRGAN/Real-ESRGAN model (e.g. 4x-UltraSharp) through the **realesrgan-ncnn-vulkan** runner.

**In the Images tab:** click the maximize button on any generated image's action row, or hover an attached image in the composer and click its button. The upscaled result posts as a new turn below. One upscale runs at a time.

**API:** `POST /v1/images/upscale` with `{ "image": "<data-url or base64>", "scale": 4, "model": "<optional model name>" }` -> `{ "image": "data:image/png;base64,...", "model", "scale" }`. Auth-gated like the inference API. Scale is 2-4; omit `model` and the first discovered one is used. A run is capped at two minutes.

**Scale vs the model:** an ncnn ESRGAN net has a fixed ratio baked into its weights (read off its file name - `x4plus`, `4x-UltraSharp`, `up2x`). The upscaler always runs at that native ratio and the result is resampled down if you asked for less, so a 2x request against a 4x model still comes back correct.

**Setup:** install **Real-ESRGAN (ncnn)** from the Backends tab - it ships its own ncnn model files - or point a backend of **kind `upscale`** at your own exe. Model files (`<name>.param` + `<name>.bin`) are discovered in a `models/` folder beside the exe, else next to the exe itself. The server must be running with config editing enabled (`-generate`).

**How it works:** the upscaler is exec-per-request - not a loaded/swapped model, so it holds no persistent VRAM and evicts nothing. A tile cap keeps peak VRAM low so an upscale coexists with a loaded generation model.
