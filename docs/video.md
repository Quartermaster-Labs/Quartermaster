<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Video generation

The Video tab renders **short clips** from a text prompt, optionally starting from an image. It runs on the same **stable-diffusion.cpp** `sd-server` backend the Images tab uses, so if image generation already works there is no second engine to install - only the checkpoint and its components.

## It is asynchronous, and that changes what you can do

A picture is one request. A clip is a **job**: `POST /sdcpp/v1/vid_gen` validates the parameters and answers in milliseconds with a job id, while the sampler runs for minutes behind it. Quartermaster tracks the job for you, and two things follow from that:

- **The model is pinned for the life of the render.** The router holds a lease on it, so nothing evicts it mid-sample and its TTL cannot unload it underneath a clip that is still rendering.
- **Closing the tab does not kill the render.** The server polls the job itself, so the clip finishes and stays readable for **10 minutes** after it lands - long enough to come back for it, bounded because the whole video is held in memory as base64.

**There is no percentage.** The job reports a status (`queued`, `generating`, `completed`, `failed`, `cancelled`) and nothing finer, so what you get is elapsed time, not a progress bar. Per-step time varies by more than an order of magnitude between the sampler and the 3D VAE decode, so any bar would be a lie. **Cancel** stops a running job and releases the model.

## Models and what each one needs

Video checkpoints are detected **structurally**, from the tensor table rather than from metadata: MiniMax-H3's gguf declares no architecture at all, and the one architecture string that reads `wan` in circulation belongs to an image model. Three families are supported, each with its own component set:

| Family | Default framing | Needs alongside the checkpoint |
|---|---|---|
| **MiniMax-H3** | 640x384, 56 frames at 24 fps | a video (3D transformer) VAE + a Qwen3-VL text encoder |
| **Wan 2.x** | 832x480, 81 frames at 16 fps | the Wan 3D causal VAE + umT5-XXL |
| **LTX 2.x** | 1280x704, 121 frames at 24 fps | **both** LTX autoencoders (video + audio) + the projected Gemma-4 encoder |

**LTX is the reason the canvas defaults are so different.** Its autoencoder compresses 32x on each spatial axis and 8x along time, where the other two compress 8x spatially (then patch-embed 2x2 on top) and 4x temporally. That is four times fewer tokens per pixel and half as many per frame, so 1280x704 at 121 frames costs the sampler *less* than MiniMax-H3 at 640x384 does. A 22B checkpoint that renders a wider clip than a 14B one is not a mistake in the table.

Two LTX quirks are worth knowing before you pick a file:

- **Its text encoder is not a normal chat model.** LTX ships a Gemma-4-12B republished with the DiT's caption projection grafted on, named `*-with-proj-*`. A stock Gemma of the same width loads without complaint and then conditions on nothing, so Quartermaster matches the path, not just the width, and keeps the projected file out of the served-model list where it would otherwise appear as a strictly worse Gemma.
- **Distilled and dev are the same tensors.** The two checkpoints are identical in shape and differ only in the schedule they were trained for: distilled is genuinely 8-step and guidance-free, dev wants around 20 euler steps at cfg 3.0. Nothing in the file says which one it is, so the **filename** decides. Keep `distilled` in the name of a distilled checkpoint, or it will be sampled as dev (and 8 steps at cfg 3.0 on the wrong one is either noise or a scorched clip).

A checkpoint that also denoises a soundtrack needs an **audio VAE** as well, and the audio VAEs are **not interchangeable between families**: MiniMax-H3's and LTX's are unrelated networks over unrelated latents, so each is matched to its own family rather than to whichever one happens to be on disk. If any required piece is missing, config generation still emits the model but writes a `WARNING:` line above it naming what it could not find - a half-wired model is visible rather than silently broken.

Find them under **Browse -> Video**, which lists safetensors video repos as well as GGUF ones.

**On steps and turbo LoRAs**: neither family is distilled, so both sample at the backend's default step count. The 4-step figure quoted for video models belongs to the **turbo LoRAs** (lightx2v and friends), which apply *per request*, not at launch. Pick one in the LoRA picker (or write `<lora:name:1.0>` in the prompt) and then drop the steps; without one, 4 steps renders mush.

## Generating

Each prompt renders a fresh clip into the thread, the same way the Images tab works.

**Framing and length**: pick an **aspect** and a **size**, then a **length in seconds**. The rungs are not round numbers on purpose - the backend's grid is defined in *frames*, there are three of them (17k+5 for MiniMax-H3, 8k+1 for LTX, 4n+1 for everything else), and an off-grid number is not rejected, it is silently changed. Only exact values are offered so a clip cannot quietly become a different length than you asked for. LTX is also the one family that rounds **down** rather than up, and it stops at **153 frames**: that is a hard ceiling in the checkpoint's own positional embedding table, not a VRAM judgement, so no card makes a longer single clip possible.

A setting that probably will not fit turns **orange** with the arithmetic behind it: the latent-token count for that size and length against what your card is estimated to hold. It stays selectable, because the estimate ignores VAE tiling and backend offload. If a render does fail, cut length before resolution.

**Controls**: steps, seed, sampler, scheduler, an optional negative prompt, and **LoRAs** from the model's own folder (**Load list** loads the model to enumerate them), each with its own strength. Keeping video LoRAs in a tree of their own (a ComfyUI layout always does) is Settings -> Advanced -> **LoRA folder -> Video models**, which applies to video models only and leaves image models on their own folder.

## Starting from an image, and going past the VRAM ceiling

Models trained for it accept frame conditioning, offered only where it applies:

- **Start frame** - the image the clip animates from.
- **End frame** - the clip travels from the start frame to this one. It needs a start frame first; on its own there is nothing to travel from.

**Continue from here**, on any finished clip, loads that clip's last frame as the next render's start frame. This is the one way past the length ceiling: a clip's VRAM cost is fixed by its own size and frame count, so chaining clips end to end costs the same per clip however long the finished video gets. Render in segments and stitch them, rather than asking for one long clip that will not fit.

## Per-model settings

The cogwheel on a video model carries two knobs the other classes do not:

- **Temporal tiling** decodes the VAE in windows along time, which is the main lever on decode-time VRAM. It is emitted **only for Wan and LTX**, whose VAEs actually implement it; MiniMax-H3's transformer autoencoder has no tiled decode path, and the backend would accept the flag and silently ignore it.
- **Stream layers** streams the diffusion weights against the VRAM cap instead of pinning them resident, which hands that headroom to the sampler. It only does anything when the weights are offloaded to RAM in the first place: sd.cpp ignores the flag outright when they are resident on the card, so it is emitted alongside offload rather than for every clip.

Both default to on, and both are emitted for video models only. Turning temporal tiling explicitly **on** forces the flag through even on a family that does not implement it, which is the escape hatch if a future backend build adds support before Quartermaster knows about it.

## API

Three routes, all behind the same API key as the inference routes:

```
POST /sdcpp/v1/vid_gen            -> {"id":"job_...","status":"queued", ...}
GET  /sdcpp/v1/jobs/{id}          -> the same document, until status is terminal
POST /sdcpp/v1/jobs/{id}/cancel
```

The first names a model like any other generate route. The two follow-ups carry a job id and nothing else, so they are routable only through Quartermaster's own job registry - which is also why they work after the model has been evicted. The finished document carries **one encoded clip** as base64 (`result.b64_json` with `result.mime_type`), not a list of frames; frame conditioning goes in as `init_image` / `end_image`, as full `data:` URLs.
