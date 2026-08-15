# autogen — non-LLM model classes

`emitModel` / `RenderSoloCmd` **dispatch by model class**, in order:
SAM (`.ggml`) → image → embedding → TTS → ASR → LLM (llama or vllm). This file covers
everything that is not the LLM path. Backend *selection* is in
[`backends.md`](backends.md); LLM sizing in [`sizing.md`](sizing.md).

| File | Class |
|---|---|
| `sam.go` | SAM segmentation (`sam3_server`), `*.ggml` |
| `image.go` | Diffusion / image generation (sd-server) |
| `embedding.go` | Text embedders |
| `audio.go` | TTS — **two engines** (qwentts.cpp, TTS.cpp) |
| `asr.go` | Speech-to-text (parakeet.cpp) |

TTS, ASR and SAM are **unsized** — small and fully resident, none of them touch the LLM
KV/offload math, and none emit `estVramGB` (see `sizing.md`, "Emitted footprint").

## SAM (`sam.go`)

`samCmdLines`/`emitSamModel` for `*.ggml` files, plus `samFallbackExe` — SAM has no legacy
`Settings` exe, so with no `segment`-class registry entry it derives as a sibling of
`ServerExe`. Tiny models, no sizing. `capabilities segmentation:true, in:[image] out:[image]`.

**Placement is always CPU**: `LiveOffloadArgs` appends `--no-gpu` to every `.ggml` because the
Vulkan SAM backend returns garbage on RX 7900 XTX (both PCS text and PVS box/point) while CPU
is correct.

## Image (`image.go`)

Emit + its own sizing path (not the LLM one): `--diffusion-model`, VAE / CLIP-L / CLIP-G / T5 /
text-encoder component paths, `--max-vram`, offload-to-cpu, VAE tiling,
`imageComputeOverheadGB`. `capabilities in:[text] out:[image]`.
Per-model image knobs live on `Override` (`VaePath`/`ClipLPath`/`ClipGPath`/`T5Path`/
`TextEncoderPath`/`OffloadToCpu`/`TeOnCpu`/`VaeTiling`/`DiffusionFa`/`DefaultSteps`/`DefaultCfg`/
`DefaultSampler`/`DefaultWidth`/`DefaultHeight`), merged via `mergeImageVariant`.

### Diffusion encoders/VAEs are dropped at discovery, not paired

A T5-XXL / UMT5 / CLIP-L/G / VAE gguf is a *component* of an image model —
`image.go`'s `resolveComponents` wires it in by explicit path from `settings.encoders`, so
discovery has nothing to pair it to. Left alone it parses as an ordinary gguf, gets emitted as
a llama-server row and shows up in the UI as an LLM ("T5 V1 1 Xxl Encoder") — an encoder-only
stack with no decoder and no chat template, which can't generate.

`encoderFileRe` (`discover.go`) drops it by name (`t5xxl`, `t5-v1_1`, `umt5`, `clip_l`/`clip_g`,
`text_encoder`, `ae`/`vae`/`taesd`, or any `-encoder` tail); `encoderArch` catches stragglers by
header arch. **Both rules are deliberately narrow on `t5`**: bare arch `t5` and a name like
`flan-t5-large` are a real seq2seq LLM llama.cpp serves, so only `t5encoder`/`umt5` and the
encoder-shaped names are excluded.

## Embedding (`embedding.go`)

`IsEmbeddingModel` is driven by the gguf `PoolingType` (the authoritative signal).
Emits `--embeddings` / `--pooling auto`, caps ctx via `embeddingCtx`, sets
`capabilities.embedding`.

## TTS (`audio.go`) — two engines in one class

`IsTTSModel` routes a model to speech instead of llama-server:

- **qwentts.cpp** — arch `qwen3-tts`/`qwentts`, or a `*talker*` filename (so a talker
  reporting a bare LM arch doesn't route to chat).
- **TTS.cpp** — `isTTSCppModel`: arch `kokoro`/`parler-tts`/`dia`/`orpheus`, or a
  `kokoro`/`parler_tts`/`orpheus` filename.

`ttsBackend` resolves the engine per model; `ttsCmdLines`/`emitTTSModel` branch on it:
qwentts gets talker `--model` + the discover-paired `--codec` + a per-model `voices/<stem>` dir
(cloned voices survive restarts); TTS.cpp gets a lone `--model-path` (vocoder is baked in, no
codec).

Shared: `checkEndpoint: /health`, `capabilities in:[text] out:[audio]` (what makes `/v1/models`
report `audio_speech`), and the OpenAI surface `/v1/audio/speech` + `/v1/audio/voices` — so the
playground Speech tab and read-aloud voice picker work against either.

### The engines are NOT interchangeable

llama and vllm both read any LLM gguf, so the `llm` class can auto-pick by ★default alone.
qwentts.cpp and TTS.cpp each read only their **own export format**, so the ★default of the `tts`
class is the wrong pick for a model of the other family. `resolveBackendPreferring` (`vllm.go`)
therefore ranks **kind-matches-the-model's-format above `Default`**, with an explicit
`Override.Backend` still beating both (the user overruling on purpose).

- Both projects ship a binary literally named `tts-server`, so a TTS.cpp model that finds no
  `ttscpp` registry entry emits a `# WARNING` rather than launching the qwentts exe against
  weights it cannot parse.
- **Cloning is declared, not inferred:** qwentts emits `capabilities.voiceClone` (rendered as
  `voice_clone` in `/v1/models`) because it registers new voices from a reference clip; TTS.cpp
  ships a fixed voice pack with no clone route, so it omits the flag and the playground hides
  the button.
- **TTS.cpp validates the request's `model` field** against its own map, keyed by gguf *file
  stem* (`server.cpp` builds it from `--model-path`), and 400s `Invalid Model: <our id>` on
  anything else — so its emit carries `useModelName: <stem>` and the request-filter middleware
  rewrites the field on the way through. qwentts ignores the field, so this is ttscpp-only.
- **TTS.cpp is CPU-only here:** `--use-metal` is macOS-only and upstream has no CUDA/ROCm path,
  so no GPU flag is emitted (Kokoro is 87M params — CPU is fine). Long flags only in that argv:
  its parser binds `-t` to both `--temperature` and `--timeout`.

## ASR (`asr.go`)

parakeet.cpp `parakeet-server`. `IsASRModel` = Parakeet/FastConformer/NeMo archs + `asrFileRe`
filename fallback — deliberately narrow on `nemotron`, which also names NVIDIA *text* LLMs.
`asrCmdLines`/`emitASRModel`. No KV/offload sizing (encoder-decoder transducer, no growing KV;
20–36× realtime on CPU, so GPU is opt-in via `ExtraArgs`).
`checkEndpoint: none` — parakeet-server documents no health route, so readiness = listen socket
open. `capabilities in:[audio] out:[text]`.
