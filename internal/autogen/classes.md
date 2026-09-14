# autogen — non-LLM model classes

`emitModel` / `RenderSoloCmd` **dispatch by model class**, in order:
SAM (`.ggml`) → TRELLIS → **video** → image → embedding → TTS → ASR → LLM (llama or vllm). This file covers
everything that is not the LLM path. Backend *selection* is in
[`backends.md`](backends.md); LLM sizing in [`sizing.md`](sizing.md).

| File | Class |
|---|---|
| `sam.go` | SAM segmentation (`sam3_server`), `*.ggml` |
| `video.go` | Video generation (sd-server's job API) |
| `image.go` | Diffusion / image generation (sd-server) |
| `embedding.go` | Text embedders |
| `audio.go` | TTS — **two engines** (qwentts.cpp, TTS.cpp) |
| `asr.go` | Speech-to-text (parakeet.cpp) |

TTS, ASR and SAM are **unsized** — small and fully resident, none of them touch the LLM
KV/offload math, and none emit `estVramGB` (see `sizing.md`, "Emitted footprint").

Unsized is only half of it: a model that costs no VRAM must also be exempt from **eviction**,
or the group machinery undoes the exemption. SAM, CPU-only TTS.cpp and Parakeet ASR are
collected into `coexistSets` (`generate_emit.go`) and emitted as their own `sam` / `tts` / `asr`
groups — `exclusive:false`, `persistent:true`, `swap:false`, listed on every listener. GPU
qwentts stays in the exclusive group, where it belongs.

## SAM (`sam.go`)

`samCmdLines`/`emitSamModel` for `*.ggml` files, plus `samFallbackExe` — SAM has no legacy
`Settings` exe, so with no `segment`-class registry entry it derives as a sibling of
`ServerExe`. Tiny models, no sizing. `capabilities segmentation:true, in:[image] out:[image]`.

**Placement is always CPU**: `LiveOffloadArgs` appends `--no-gpu` to every `.ggml` because the
Vulkan SAM backend returns garbage on RX 7900 XTX (both PCS text and PVS box/point) while CPU
is correct.

## Video (`video.go`)

Same backend as image (sd-server, `--diffusion-model` + components), a different **class**
because the serving contract is different: a clip is rendered through the async job API, not
returned from the request that asked for it. See `internal/server/CLAUDE.md` for that half.

**Detection is structural, and it has to be** (`IsVideoModel`, fed by `Metadata.VideoKind` from
the tensor walk in `gguf.go`). Two traps, both real, both asserted in `video_test.go`:

- **MiniMax-H3 ships ZERO metadata KVs.** No arch, no name, nothing: every KV-based test
  classifies it as "unknown gguf" and it falls through to the LLM path, where llama-server
  rejects it. The tensor table is the only thing that identifies it.
- **`arch=wan` is NOT video here.** The one model on disk declaring it is ERNIE-Image-Turbo, an
  **image** model (see `ernie-image-turbo-runtime`). Classifying on that string alone would
  silently move a working image model onto the video path. `isImageArch("wan")` stays true and
  the video class never consults arch.

Families (`VideoFamilyMinimaxH3`, `VideoFamilyWan`) carry the runtime defaults, because the
generic sd-server ones are wrong for both: the built-in `--cfg-scale` is 7.0 and H3 is a 4-step
distill that **aborts** above 1.0, and the built-in `--video-frames` is 1, which renders a still.
`videoDefaultsFor` pins steps/cfg/size/frames/fps per family (H3: 4 / 1.0 / 640x384 / 25f / 24fps;
Wan: 832x480 / 81f / 16fps, steps and cfg left to sd-server since Wan is not distilled).
`Override.DefaultFrames`/`DefaultFps`/`DefaultCfg`/`AudioVaePath` still win over the family.

`videoComponents` resolves the same encoder pool the image path uses, plus two video-only roles:
`RoleAudioVae` (H3 has an audio branch, `--audio-vae`, and declares `out: [video, audio]`) and
`VaeFamilyVideo3D` (a 3D VAE is not interchangeable with an image VAE). `CondHidden` 5120
auto-pairs Qwen3-VL-32B as H3's text encoder. A missing component is emitted as a `WARNING`
comment naming the role rather than a command that fails on the first request.

Sizing reuses the image path with `videoComputeOverheadGB` (4.0) in place of the image figure.
Known under-charge: `estVramGB` has **no temporal term**, so a 81-frame Wan clip is priced like a
25-frame one. Accepted for v1 — the frame count is a request parameter, not a launch flag, so
there is no single number to size against.

## Image (`image.go`)

Emit + its own sizing path (not the LLM one): `--diffusion-model`, VAE / CLIP-L / CLIP-G / T5 /
text-encoder component paths, `--max-vram`, offload-to-cpu, VAE tiling,
`imageComputeOverheadGB`. `capabilities in:[text] out:[image]`.
Per-model image knobs live on `Override` (`VaePath`/`ClipLPath`/`ClipGPath`/`T5Path`/
`TextEncoderPath`/`OffloadToCpu`/`TeOnCpu`/`VaeTiling`/`DiffusionFa`/`DefaultSteps`/`DefaultCfg`/
`DefaultSampler`/`DefaultWidth`/`DefaultHeight`), merged via `mergeImageVariant`.

### Diffusion encoders/VAEs are dropped at discovery, not paired

A T5-XXL / UMT5 / CLIP-L/G / VAE gguf is a *component* of an image model:
`image.go`'s `resolveComponents` wires it in as a `--vae`/`--clip_l`/`--t5xxl`/`--llm` path, so
discovery has nothing to pair it to. Left alone it parses as an ordinary gguf, gets emitted as
a llama-server row and shows up in the UI as an LLM ("T5 V1 1 Xxl Encoder") — an encoder-only
stack with no decoder and no chat template, which can't generate.

`encoderFileRe` (`discover.go`) drops it by name (`t5xxl`, `t5-v1_1`, `umt5`, `clip_l`/`clip_g`,
`text_encoder`, `ae`/`vae`/`taesd`, or any `-encoder` tail); `encoderArch` catches stragglers by
header arch. **Both rules are deliberately narrow on `t5`**: bare arch `t5` and a name like
`flan-t5-large` are a real seq2seq LLM llama.cpp serves, so only `t5encoder`/`umt5` and the
encoder-shaped names are excluded.

### Components are DISCOVERED, not declared (`encoderpool.go`)

`settings.encoders` used to be the only source of those paths: one hand-written path per role,
per machine, which broke on every models-tree move and made a newly downloaded diffusion model
a config chore. `ScanEncoderPool` now classifies every component on disk from its header, and
`fillEncoderSet` fills only the roles the user left blank (**a declared path always wins**, so
existing configs are untouched).

Everything it keys on is structural, because filenames are not: three unrelated files on the
dev box are all named `ae.safetensors`, and two of those are byte-identical copies.

| Role | Signal | What it separates |
|---|---|---|
| VAE | `decoder.conv_in.weight` shape[1] | latent channels: 4 = SD/SDXL, 16 = flux.1 `ae`, 32 = flux.2/ERNIE |
| VAE (3D) | `conv1.weight` is 5-dim | the Wan-2.1 causal VAE (Wan / Krea / Qwen-Image) |
| CLIP | `text_model.embeddings.token_embedding.weight` shape[1] | 768 = CLIP-L, 1280 = CLIP-G |
| T5 | `encoder.block.0.layer.0.SelfAttention.q.weight` | widest wins (T5-XXL is 4096) |
| LLM | gguf `embedding_length` / `model.embed_tokens` shape[1] | matched against the DiT (below) |

`.safetensors` costs one header read (8-byte LE length + JSON table at offset 0), so the price
is independent of file size; ggufs come through `ReadGgufMetadataCached`. Scans are cached per
root set for 30s (`encoderPoolFor`), since a regen runs on every settings save and watcher tick.

**The DiT states the encoder it wants.** `Metadata.CondHidden` (`quantlabel.go`'s
`condTensorOrder`, filled by the tensor walk) is `ne[0]` of the caption projection —
`txt_in.weight`, `cap_embedder.1.weight`, `text_proj.weight`, `context_embedder.weight` or
LongCat's `txtfusion...prenorm.scale`, in that order. Matching it to an encoder's hidden width
picks the right file with no name table: 3584 = Qwen2.5-VL-7B (LongCat, Qwen-Image-Edit),
2560 = Qwen3-4B (Z-Image, Krea), 3072 = Ministral-3B (ERNIE), 4096 = T5-XXL (flux).

Width is **not** an identity on its own (five unrelated 2560-wide models are installed here), so
ties break on `encoderArchRank` first: Qwen > Mistral > Gemma/Llama > unknown. That table is
shipped knowledge about what DiTs are trained against, not per-machine tuning. Drafter sidecars
(`mtp-*`, dflash) and pooled embedders are excluded outright: their widths collide with real
encoders and neither can condition anything.

A declared `settings.encoders.qwenLlm` of the matching width wins outright (`EncoderPool.Llm`
takes it as `prefer`), and a per-model `textEncoderPath` wins over even that: the scan narrows the
field, the declaration names the file. Without one, two same-width Qwen candidates of the same
rounded size are separated only by the path tiebreak, which is arbitrary. Qwen3-4B and Qwen3-VL-4B
are both 2560 wide and their Q8 quants both round to 3.99 GiB, so that tiebreak handed Z-Image the
VL file Krea-2 wants until the pin was given precedence.

**`--llm_vision` pairs by directory.** `pairProjectors` attaches each encoder gguf to the
`mmproj-*` beside it, the same convention `inheritSidecars` uses for vision LLMs, so the
projector for a chosen `--llm` is simply its neighbour. Whether a model *wants* one is the one
thing that cannot be read off the weights: LongCat-Image-Edit and plain LongCat-Image have
identical `img_in`/`txt_in` shapes (the reference image enters as extra sequence tokens, not
extra input channels), so `editModelRe` name-detects it, with `llmVision: on|off` and
`llmVisionPath` as the escape hatches. Sampling knobs stay hand-wired: LongCat-Edit still wants
`extraArgs: "--flow-shift 3.16"`, which has no structural tell (the value is
exp(base_shift) from the model's own scheduler config, not something the gguf
states).

## Embedding (`embedding.go`)

`IsEmbeddingModel` is driven by the gguf `PoolingType` (the authoritative signal).
Emits `--embeddings` / `--pooling auto`, caps ctx via `embeddingCtx`, sets
`capabilities.embedding`. Embedders are class `llm`, so `embeddingExe` honors the model's
backend pin exactly like a chat model (only an auto model follows the ★ default; a `vllm`
pin keeps the class default, since there is no vllm embedding emitter).

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

**Grouping differs by engine.** qwentts runs on the GPU and gets an `estVramGB` + a normal
exclusive group. TTS.cpp is CPU-only here (no CUDA/ROCm path upstream, `--use-metal` is macOS),
costs no VRAM, and goes into the persistent `tts` coexistence group beside SAM — `emitModel`
collects those names into `coexistSets.TTS`. In the exclusive group, a playground read-aloud
click evicted the chat model that had just produced the reply, and the next turn cold-reloaded
it.

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
20–36× realtime on CPU, so GPU is opt-in via `ExtraArgs`). `asrExe` honors the model's backend
pin: only an auto model follows the class default the ★ sets. `checkEndpoint: none` —
parakeet-server documents no health route, so readiness = listen socket open.
`capabilities in:[audio] out:[text]`.

Placed in the persistent `asr` coexistence group (`coexistSets.ASR`) for the same reason it emits
no `estVramGB`: dictating must not evict the chat model the transcript is headed for. A GPU
opt-in through `ExtraArgs` keeps coexisting — the same accepted under-charge, and far cheaper
than a full swap on every dictation.
