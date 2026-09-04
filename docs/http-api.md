<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# HTTP API reference

Quartermaster speaks the **OpenAI API** (plus Anthropic's `/v1/messages`), so most clients work by pointing them at your server. This page lists every endpoint with a request you can paste into a terminal.

**Base URL** is your API port, `http://127.0.0.1:1250` by default. **Auth** is an API key if you have created one (`Authorization: Bearer qm-...`, or `x-api-key`, or the password half of Basic auth); when no keys exist the API is open and you can drop the header. See *API keys and access*.

**Every inference route takes a `model`**, and that is what selects the backend: ask for a model that is not loaded and it is loaded for you, evicting others if VRAM demands it. Use the id from `/v1/models`.

**Most routes are proxied**, not reimplemented, so any field the underlying llama.cpp / stable-diffusion.cpp / TTS server accepts passes straight through even if it is not listed here. Every `/v1/...` path below also answers on `/v/...`, for clients that insist on writing their own version prefix.

The `/api/...` routes the dashboard uses are deliberately **not** documented here: they are the UI's private surface and change without notice.

## Discovery and health

**`GET /v1/models`** - the catalog, OpenAI-shaped, plus a `quartermaster` metadata block per entry. Filtered to what your API key and listener are scoped to.

```
curl http://127.0.0.1:1250/v1/models -H "Authorization: Bearer qm-..."
```

**`GET /health`**, **`GET /wol-health`** - liveness. Answers `OK` without touching a model.

```
curl http://127.0.0.1:1250/health
```

**`GET /api/version`** - version, commit, and the self-update state.

```
curl http://127.0.0.1:1250/api/version
```

**`GET /metrics`** - Prometheus exposition for scraping.

```
curl http://127.0.0.1:1250/metrics
```

## Text generation

**`POST /v1/chat/completions`** - the one most clients use. `stream: true` gives SSE deltas.

```
curl http://127.0.0.1:1250/v1/chat/completions \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwen3","messages":[{"role":"user","content":"Hi"}],"stream":false}'
```

**`POST /v1/messages`** - the Anthropic shape, for clients that speak it. `max_tokens` is required.

```
curl http://127.0.0.1:1250/v1/messages \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwen3","max_tokens":256,"messages":[{"role":"user","content":"Hi"}]}'
```

**`POST /v1/messages/count_tokens`** - how many tokens that request would cost, without running it.

```
curl http://127.0.0.1:1250/v1/messages/count_tokens \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwen3","messages":[{"role":"user","content":"Hi"}]}'
```

**`POST /v1/responses`** - OpenAI's Responses API.

```
curl http://127.0.0.1:1250/v1/responses \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwen3","input":"Write a haiku about VRAM"}'
```

**`POST /v1/completions`** - legacy plain-prompt completion.

```
curl http://127.0.0.1:1250/v1/completions \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwen3","prompt":"The capital of France is","max_tokens":16}'
```

**`POST /completion`** - llama.cpp's own completion endpoint, forwarded verbatim. Use it for llama-server fields that have no OpenAI equivalent.

```
curl http://127.0.0.1:1250/completion \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwen3","prompt":"Once upon a time","n_predict":32}'
```

**`POST /infill`** - llama.cpp fill-in-the-middle, for code models that support it.

```
curl http://127.0.0.1:1250/infill \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwen-coder","input_prefix":"def add(a, b):\n    return ","input_suffix":"\n"}'
```

## Embeddings and reranking

**`POST /v1/embeddings`** - vectors for one string or an array of them.

```
curl http://127.0.0.1:1250/v1/embeddings \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed","input":["first passage","second passage"]}'
```

**`POST /v1/rerank`** - score documents against a query. Also answers on `/v1/reranking`, `/rerank` and `/reranking`, since clients disagree about the spelling.

```
curl http://127.0.0.1:1250/v1/rerank \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"bge-reranker","query":"how do I evict a model","documents":["unloading frees VRAM","cats are nice"],"top_n":2}'
```

## Images

**`POST /v1/images/generations`** - the OpenAI shape. Returns base64 images in `data`.

```
curl http://127.0.0.1:1250/v1/images/generations \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"sdxl","prompt":"a lighthouse in a storm","n":1,"size":"1024x1024"}'
```

**`POST /v1/images/edits`** - the OpenAI edit shape, `multipart/form-data` rather than JSON.

```
curl http://127.0.0.1:1250/v1/images/edits \
  -H "Authorization: Bearer qm-..." \
  -F model=qwen-image-edit -F prompt="make it snow" -F image=@photo.png
```

**`POST /sdapi/v1/txt2img`** - the AUTOMATIC1111-style route, and the one with the real knobs: `steps`, `cfg_scale`, `seed`, `sampler_name`, `scheduler`, `lora`, plus hires fix (`enable_hr`, `hr_scale`, `hr_upscaler`, `hr_steps`, `denoising_strength`) and `extra_images` for reference-conditioned models. Images come back base64 in `images`.

```
curl http://127.0.0.1:1250/sdapi/v1/txt2img \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"sdxl","prompt":"a lighthouse in a storm","negative_prompt":"blurry","width":1024,"height":1024,"steps":25,"cfg_scale":5,"seed":-1}'
```

**`POST /sdapi/v1/img2img`** - the same fields plus `init_images` (base64, no `data:` prefix), `denoising_strength`, and for inpainting a `mask` (white repaints, black keeps) with optional `inpainting_mask_invert`.

```
curl http://127.0.0.1:1250/sdapi/v1/img2img \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"sdxl","prompt":"add a red door","init_images":["<base64>"],"denoising_strength":0.55,"steps":25}'
```

**`GET /sdapi/v1/loras?model=<id>`** - the LoRAs that model can load, filtered to files that really are LoRAs.

```
curl "http://127.0.0.1:1250/sdapi/v1/loras?model=sdxl" -H "Authorization: Bearer qm-..."
```

**`POST /v1/images/upscale`** - standalone ESRGAN upscale. Not a loaded model: it runs the ncnn upscaler once per request. `image` is a data URL or bare base64, `scale` is 2 to 4 (default 4), `model` is optional. Returns `{"image": "data:image/png;base64,..."}`.

```
curl http://127.0.0.1:1250/v1/images/upscale \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"image":"data:image/png;base64,<...>","scale":4}'
```

**`POST /v1/segment`** - SAM segmentation. `image` is bare base64 with no `data:` prefix. Prompt with `text` (a concept, can match several instances), or with `box` as `[x0,y0,x1,y1]` and/or `points` as `[[x,y,label]]` where label 1 is foreground. Returns `{width, height, masks:[{instance_id, score, iou_score, box, png}]}`, each `png` a white-on-black mask.

```
curl http://127.0.0.1:1250/v1/segment \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"sam3","image":"<base64>","text":"the dog"}'
```

## Speech and audio

**`POST /v1/audio/speech`** - text to speech. Returns audio bytes, so redirect them to a file.

```
curl http://127.0.0.1:1250/v1/audio/speech \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"kokoro","input":"Hello there","voice":"af_heart","response_format":"wav"}' \
  -o out.wav
```

**`POST /v1/audio/transcriptions`** - speech to text, `multipart/form-data` with the audio as `file`.

```
curl http://127.0.0.1:1250/v1/audio/transcriptions \
  -H "Authorization: Bearer qm-..." \
  -F model=whisper-large -F file=@recording.wav
```

**`GET /v1/audio/voices?model=<id>`** - the voices that model can speak with. The model has to be loaded already.

```
curl "http://127.0.0.1:1250/v1/audio/voices?model=kokoro" -H "Authorization: Bearer qm-..."
```

**`POST /v1/audio/voices`** - register a cloned voice from a WAV, for engines that support cloning. `ref_text` is the transcript of the sample and is optional.

```
curl http://127.0.0.1:1250/v1/audio/voices \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"qwentts","name":"my-voice","wav_b64":"<base64 wav>","ref_text":"the words in the sample"}'
```

**`DELETE /v1/audio/voices/{name}?model=<id>`** - remove a cloned voice.

```
curl -X DELETE "http://127.0.0.1:1250/v1/audio/voices/my-voice?model=qwentts" \
  -H "Authorization: Bearer qm-..."
```

## Tools

Web search and YouTube executors, so your own agent does not have to wire up SearXNG or yt-dlp. Any valid key works and model scopes do not apply. Full field reference in *Tools API*.

**`POST /v1/tools/search`** - a web search, provider passed per call.

```
curl http://127.0.0.1:1250/v1/tools/search \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"query":"llama.cpp release","limit":5,"providers":[{"id":"duckduckgo"}]}'
```

**`POST /v1/tools/youtube/transcript`** - the full transcript with `[m:ss]` timestamps.

```
curl http://127.0.0.1:1250/v1/tools/youtube/transcript \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"url":"https://youtu.be/dQw4w9WgXcQ"}'
```

**`POST /v1/tools/youtube/search`** - search videos, or list a channel with `{"channel":"@handle"}`.

```
curl http://127.0.0.1:1250/v1/tools/youtube/search \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"query":"gguf quantization","limit":5}'
```

**`POST /v1/tools/youtube/comments`** - top-level comments for a video.

```
curl http://127.0.0.1:1250/v1/tools/youtube/comments \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"url":"https://youtu.be/dQw4w9WgXcQ","limit":20}'
```

## Operations

These are **not** API-key routes. They are part of the admin surface, so they answer to this host (and to anything `-admin-allow` names) rather than to a key.

**`GET /running`** - which models are loaded right now, with state and launch command.

```
curl http://127.0.0.1:1250/running
```

**`GET /unload`** - unload everything and free the VRAM.

```
curl http://127.0.0.1:1250/unload
```

**`POST /api/models/unload/{model}`** - unload one model, leaving the rest loaded.

```
curl -X POST http://127.0.0.1:1250/api/models/unload/qwen3
```

**`GET /upstream/{path}`** - proxy straight to the loaded backend's own HTTP server, bypassing the model chain. For reaching a llama-server endpoint Quartermaster does not route itself.

```
curl http://127.0.0.1:1250/upstream/qwen3/props
```

**`GET /logs`**, **`GET /logs/stream`** - the server log, as a snapshot or as a live stream.

```
curl http://127.0.0.1:1250/logs
```

## Errors

Errors are OpenAI-shaped, `{"error": {"message": "..."}}`. `400` is a bad request body, `401` a missing or wrong API key, `404` an unknown model, `501` a route that needs `-generate` mode, and `502` / `503` / `504` mean the backend for that model crashed, was evicted, or is still loading. A retry after a moment is the right response to the last group.
