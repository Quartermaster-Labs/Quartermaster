# autogen — non-LLM model classes

`emitModel` / `RenderSoloCmd` **dispatch by model class**, in order:
SAM (`.ggml`) → TRELLIS → **video** → image → embedding → **audio.cpp** → TTS → ASR → LLM (llama or vllm). This file covers
everything that is not the LLM path. Backend *selection* is in
[`backends.md`](backends.md); LLM sizing in [`sizing.md`](sizing.md).

| File | Class |
|---|---|
| `sam.go` | SAM segmentation (`sam3_server`), `*.ggml` |
| `video.go` | Video generation (sd-server's job API) |
| `image.go` | Diffusion / image generation (sd-server) |
| `embedding.go` | Text embedders |
| `audiocpp.go` | audio.cpp — one engine serving **two classes** (tts + asr), 72 families |
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

LTX-2.x is the third family, and it is structural too: it declares `arch=ltxv`, and the class
still ignores the string. Its marker is `patchify_proj.weight`, a LINEAR patch embedding where
H3 has a conv stem and Wan a conv3d, so neither of their markers sees it.
`audio_patchify_proj.weight` sets `HasAudioOut`, because LTX's audio half is a second projection
in the same tower rather than a second model, and that is what makes `--audio-vae` required
rather than optional.

Families (`VideoFamilyMinimaxH3`, `VideoFamilyWan`, `VideoFamilyLtxAV`) carry the runtime
defaults, because the generic sd-server ones are wrong for all three: the built-in
`--video-frames` is 1, which renders a still, and the built-in `--cfg-scale` is 7.0, which no
family here wants. `videoDefaultsFor` takes the family and the model name, pinning
size/frames/fps plus whatever steps/cfg/sampler are load-bearing. H3: cfg 1.0, 640x384, 56
frames (its 17k+5 grid, not the 4n+1 grid the other families use) and steps left to sd-server,
because the 4-step figure belongs to per-request turbo LoRAs and pinning it made every LoRA-less
render mush. Wan: 832x480 / 81f / 16fps, steps and cfg left to sd-server since it is not
distilled. LTX: 1280x704 / 121f / 24fps, with the rest derived from the name, because its
distilled and dev checkpoints are TENSOR-IDENTICAL and nothing in the file separates them:
`isDistilledName` gets 8 steps at cfg 1.0, everything else cfg 3.0 + `euler` with steps
unpinned. 121 is on LTX's 8k+1 grid (sd.cpp rounds LTX DOWN, where the other families round up)
and under the 153-frame ceiling the checkpoint's own `positional_embedding_max_pos` imposes (20
latent frames). `Override.DefaultFrames`/`DefaultFps`/`DefaultCfg`/`AudioVaePath` still win over
the family.

`videoComponents` resolves the same encoder pool the image path uses, keyed by family. H3 and
LTX both have an audio branch, wired as `--audio-vae` with `out: [video, audio]`, so
`EncoderPool.AudioVae` now takes the family first (empty matches any): two soundtrack decoders on
one disk are unrelated networks over unrelated latents, and the family is what keeps a model from
being handed the other's. H3's video VAE is `VaeFamilyVideo3D` (its transformer autoencoder is
not interchangeable with an image VAE); LTX ships a video + audio pair under `VaeFamilyLtx` and
always requires both, picked by family rather than through the single-slot `enc.VideoVae`/
`enc.AudioVae` pins, which already mean H3's pair on a box that has one. `Override.VaePath`/
`.AudioVaePath` remain the per-model escape hatch. `CondHidden` 5120 auto-pairs Qwen3-VL-32B as
H3's text encoder; LTX conditions on a Gemma-4-12B republished with the caption projection
grafted on, which `pool.LlmHinted("ltx")` picks by PATH because a stock Gemma-3-12B is the same
3840 wide and would load clean and then condition on nothing. `--embeddings-connectors` is
deliberately never emitted: LTX-2.5 declares `use_embeddings_connector` and carries the connector
layers itself, so pointing the flag at an external file would load a second copy.
`vaeTemporalTiling` gates `--temporal-tiling` on the families whose shipped VAE actually
implements streaming decode, Wan and LTX (`LTXVideoVAE::decode_temporal_tiled_streaming`); H3's
autoencoder has no tiled path, and sd-server silently falls back rather than erroring, so an
ungated flag would claim a VRAM lever the decode never applies. A missing component is emitted
as a `WARNING` comment naming the role rather than a command that fails on the first request.

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

**The argv is COMPOSED, not concatenated.** `imageCmdLines` returns a `ComposedCmd`
(`SdFlags.ComposeCmd`, `flagtable_sd.go`), so a flag the user pins in `customArgs` suppresses the
generated one for that knob instead of being appended beside it, and `ClearOwnedFields` zeroes the
structured field behind it. The legacy `extraArgs` bucket still loads (the editor migrates it into
`customArgs` the first time a model is opened and saved) but nothing appends it raw any more: that
was the bug where every save added another copy of the same `--audio-vae/--video-frames/--fps`
run. `extraImageCmdLines` composes the same way for the hand-declared `extraImageModels`.

### Diffusion encoders/VAEs are dropped at discovery, not paired

A T5-XXL / UMT5 / CLIP-L/G / VAE gguf is a *component* of an image model:
`image.go`'s `resolveComponents` wires it in as a `--vae`/`--clip_l`/`--t5xxl`/`--llm` path, so
discovery has nothing to pair it to. Left alone it parses as an ordinary gguf, gets emitted as
a llama-server row and shows up in the UI as an LLM ("T5 V1 1 Xxl Encoder") — an encoder-only
stack with no decoder and no chat template, which can't generate.

`encoderFileRe` (`discover.go`) drops it by name (`t5xxl`, `t5-v1_1`, `umt5`, `clip_l`/`clip_g`,
`text_encoder`, `ae`/`vae`/`taesd`, or any `-encoder` / `with-proj` segment); `encoderArch`
catches stragglers by header arch. `with-proj` is the odd one: LTX republishes a Gemma-4-12B
with the DiT's caption projection grafted on, and the result parses as a perfectly good chat
model, so without the name match discovery serves it as one (a strictly worse Gemma than the
stock file beside it). **Both rules are deliberately narrow on `t5`**: bare arch `t5` and a
name like `flan-t5-large` are a real seq2seq LLM llama.cpp serves, so only `t5encoder`/`umt5`
and the encoder-shaped names are excluded.

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
| VAE (H3) | `encoder.conv_in.weight` is 5-dim | MiniMax-H3's transformer autoencoder: no `decoder.conv_in` and no bare `conv1`, so it shares no shape table with either arm above |
| VAE (LTX) | `decoder.conv_in.conv.weight` is 5-dim | the LTX-2.x video half: convs wrapped one level deeper (`LTXVideoCausalConv3d`), with shape[1] (128) kept as Width so a future second latent size is a family mismatch rather than a silent bad decode |
| Audio VAE | `dec_in_proj.weight` is 3-dim (H3, 1D stack) or `audio_vae.decoder.conv_in.conv.weight` is 4-dim (LTX, 2D over mel) | the soundtrack decoder; the family on each arm is what stops the two from cross-wiring |
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
2560 = Qwen3-4B (Z-Image, Krea), 3072 = Ministral-3B (ERNIE), 4096 = T5-XXL (flux). LTX is the
one family whose DiT states no width: its caption projection lives in the ENCODER file, not the
weights being scanned. `gguf.go` therefore also captures the diffusers `config` string KV and
reads `transformer.caption_channels` (3840) out of it via `captionChannelsFrom`, but only when
`scan.condHidden` is 0. A tensor shape is what the model actually runs with, so the config blob
never overrules it.

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
VL file Krea-2 wants until the pin was given precedence. LTX adds a path hint of its own:
`resolveComponents` passes `pool.LlmHinted("ltx")` as the `prefer` argument whenever the declared
`qwenLlm` is not itself a candidate at LTX's width, because LTX's encoder is a Gemma-4-12B
republished with the caption projection grafted on while a stock Gemma-3-12B is the same 3840 wide.
The test is `pool.llmCandidate`, i.e. "would `Llm` honour this pin", NOT "is a pin declared": one
global field serves every diffusion model, so a `qwenLlm` set for an image model fails the width
gate and decides nothing, and treating its presence as an answer would suppress the hint in every
install that has an `encoders:` block. A hint that misses returns `""` and falls through to the
ordinary width match, so it narrows the field and never guesses.

**`--llm_vision` pairs by directory.** `pairProjectors` attaches each encoder gguf to the
`mmproj-*` beside it, the same convention `inheritSidecars` uses for vision LLMs, so the
projector for a chosen `--llm` is simply its neighbour. Whether a model *wants* one is the one
thing that cannot be read off the weights: LongCat-Image-Edit and plain LongCat-Image have
identical `img_in`/`txt_in` shapes (the reference image enters as extra sequence tokens, not
extra input channels), so `editModelRe` name-detects it, with `llmVision: on|off` and
`llmVisionPath` as the escape hatches. Sampling knobs stay hand-wired: LongCat-Edit still wants
`customArgs: "--flow-shift 3.16"`, which has no structural tell (the value is
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

## audio.cpp (`audiocpp.go`) - one engine, two classes

`audiocpp_server` (upstream `0xShug0/audio.cpp`, catalogued as the `audiocpp-server` component)
serves 72 model families behind one binary. It is dispatched **before** both legacy speech
engines, because the routing question is already answered in the file: an audio.cpp conversion
writes `general.architecture = audiocpp` and `audiocpp.model_spec.family = <family>` into the
GGUF header, so `IsAudioCppModel` is an exact test and the family needs no filename heuristic
and no runtime read of upstream's `model_specs/`. `gguf.go` parses the family KV (and skips the
`...model_spec.json` beside it, which is the whole spec inlined and orders of magnitude larger;
the server reads it out of the same file for itself).

`audioCppFamilies` maps every upstream family to the classes it serves. Three ways a model is
**deliberately dropped with a comment instead of an entry**, since no other engine here could
serve it either: no family id in the header (a third-party `-orig` conversion that dropped the
spec - we warn rather than guess), a known family we do not serve yet (music, separation, voice
conversion, codec, align, diar, midi, s2s), and an unknown family (upstream added one; the table
needs a row).

### It must never become a second scheduler

audio.cpp ships its own model manager: lazy loading, a residency cap, idle unloads and a
free-memory guard. All of it is a scheduler, and a second scheduler behind the router means
residency decisions the VRAM budget never sees. It is contained rather than used:

- **one model per generated config**, plus `--max-loaded-models 1` - a cap over a single model
  has nothing to choose between;
- `lazy_load: false`, so the listen socket opening means the weights are resident, which is what
  makes `checkEndpoint: /health` a real readiness gate rather than a port check;
- `idle_unload_ms: 0`, `min_free_memory_mb: 0` - TTL and eviction are the router's, exclusively;
- `concurrencyLimit: 1` in the emitted YAML;
- `/v1/models/load`, `/v1/models/unload` and `/v1/tasks/unload_models` are **never proxied**.

### There is no `--model` flag (the `audiocpp:` block)

A model can only be named inside the JSON file passed as `--config`; every other knob is an argv
flag, and argv wins over the file. Writing one JSON per model at generate time would mean N
generated files duplicating facts the config already holds, left behind on every rename or
delete. Instead the emitted entry carries a typed `audiocpp:` block (`family`/`path`/`task`, see
`config.AudioCppConfig`) and `internal/server/audiocppconfig.go` materializes it at **spawn**
time into `<CacheDir>/audiocpp/<model>-<hash>.json` (atomic temp+rename; the hash is of the
original model id, so two ids that sanitize alike cannot share one file). A failed write refuses
the spawn - an empty `models` array would 400 every request and read as a broken model rather
than a broken disk. A user-written `--config` in the launch box is left alone.

### `--backend` is load-bearing

audio.cpp's config defaults to **CUDA**, so a Vulkan or CPU build launched bare fails on a
non-NVIDIA box. `audioCppFlavour` derives the flag from the installed build's variant; a
hand-entered registry row records no variant, so the emit carries a `# NOTE` naming the cuda
default instead of guessing.

### `--device` too, and the index is probed (`audiocppdev.go`)

`--backend` says which runtime; it does not say which adapter. Left alone audio.cpp takes device
**0** of that backend, and on any box whose integrated GPU enumerates first that is a model
running out of shared system memory, silently: nothing in the log distinguishes it from a card.
There is no ROCm build to escape to either (v0.8.0 publishes `vulkan`, `cuda`, `cpu`,
`cpu-portable` only), so on AMD the Vulkan listing is the whole device list.

The index comes from the binary's own `--list-devices`, which prints
`Vulkan:0 "AMD Radeon RX 7900 XTX" [GPU]` / `Vulkan:1 "AMD Radeon(TM) Graphics" [IGPU]` and tags
each row `[GPU]` / `[IGPU]` / `[CPU]` - so "discrete" is read off upstream's own judgement rather
than guessed from a marketing string. Neither shape `parseBackendDevices` reads matches it, hence
a parser of its own; the listing is GLOBAL (every backend in one run, measured at 0.154s), so it
is memoized per exe on the same `exe|size|mtime` key `ListBackendDevices` uses and filtered by
flavour afterwards. It draws on the same shared `backendProbeBudget`, so an audio backend that
hangs cannot spend the whole generate's allowance.

`backenddev.go`'s refusal rule carries over: no flag is emitted for flavour `""` or `cpu`, for a
backend listing a single device (nothing to choose), or when no row is tagged `[GPU]`. A wrong
`--device` is a hard launch failure where a missing one is just the old behaviour.
`Override.AudioDevice` (`*int`, the model editor's "GPU device" knob) wins over the probe and
skips it; **negative means emit nothing**, which is how the knob is turned back off without
having to know what the probe would have said.

### First kind to serve two classes

`kindClasses("audiocpp")` is `{tts, asr}`, which the registry's "an install never steals a
populated class" rule has to reason about as a SET: an existing Parakeet install is enough to
stop audio.cpp claiming the tts star it travels with (`ClassTaken`, used by the interactive
install and by `backendsadopt.go`). In the other direction, an audio.cpp row holding a class star
must not hand its binary to the legacy emitters - it reads neither engine's weights.
`withoutAudioCpp` hides those rows from `ttsBackend`/`asrExe`, which is stronger than checking
the answer afterwards: the star degrading to audio.cpp then falls through to the NEXT installed
engine of that class, not past the registry to the legacy derived exe.

`onlyAudioCpp` is the mirror image, and it exists because the per-model backend picker can now
name any engine of the class. A cross-engine pin has to be a NO-OP, not a broken launch:
`audioCppBackend` resolves within the audio.cpp rows alone, so an `Override.Backend` pointing at
a TTS.cpp row simply finds nothing and falls through to the installed audio.cpp build, while a
pin at a specific audio.cpp BUILD (vulkan vs cuda) still wins. Without it the pin resolved to a
zero row and the emitter fell to its "no audio.cpp backend registered" path: a bare
`audiocpp_server` name that is not on PATH, with no `--backend` flavour. The editor stops
offering the mismatch in the first place (`modelConfigResp.IsAudioCpp`, filled from the presence
of the generated `audiocpp:` block), but the resolver does not rely on the client for that.

Music (`ace_step`, `songbloom`, ...) is a follow-up: it needs a class of its own, a
`/v1/tasks/run` base64-WAV translation handler and a UI tab.
