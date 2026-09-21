# ui-svelte — playground image, video & speech studios

`src/components/playground/`. The chat surface is in
[`playground-chat.md`](playground-chat.md).

## `ImageInterface.svelte`

Full SD image-gen UI: txt2img/img2img (`ImageGenMode`), denoise/hires (`enable_hr`), reference
images (`extra_images`, Kontext), per-model defaults, style presets, seed modes.

**Batch** (Settings → Batch, `sdapi batch_size` → sd.cpp `batch_count`, capped at `MAX_BATCH`):
N images per prompt, rendered sequentially with the seed incrementing per image — the step bar
therefore restarts per image (the in-flight row shows `×N`). Each turn keeps a **picked** image
index (numbered badge on the thumbnails, batch>1 only) that the reply/copy/download/upscale
actions act on.

**ESRGAN upscale** (`runUpscale` → `lib/imageApi.upscaleImage` → `POST /v1/images/upscale`):
⤢ button on any result-image action row AND on each composer attachment (hover); posts the 4×
result as a new turn. `toB64(img)` first — a saved image is a `/api/media/<hash>` URL, not a data
URL. Busy key `m<turn>`/`a<idx>` serializes runs to one at a time.

**Prompt queue + live settings.** Every control except the per-turn actions (edit prompt,
regenerate, upscale, new thread) stays enabled while a render runs, so a send during generation
enqueues a `QueuedJob` instead of being refused. The job carries a **snapshot** of every setting
(`captureParams()` → `GenParams`: model, size, steps, cfg, seed, sampler, scheduler, LoRAs,
denoise, negative, batch, the edit-mode flags), so changing the panel afterwards only affects the
*next* job you queue. `runningParams` is the snapshot of the in-flight job and is what the progress
parser, the `×N` batch badge and `cancelGeneration()`'s unload read - never the live panel, which
the user may have already retargeted at another model.

A queued job whose source image is implicit (no attachment, no mask, no skip-base) stores
`refs: null` = "whatever the running render produces", resolved at dispatch, so a queued follow-up
edits the image it was written about. **Stop** returns the aborted job to the composer (or to the
head of the queue if the composer is busy) and does not drain; `drainQueue()` skips jobs whose
thread was deleted rather than stalling on them.

Pure helpers live beside it in `imageGen.ts`: `ASPECTS`/`SIZE_TIERS`/`aspectDims`,
`SAMPLER_OPTIONS`/`SCHEDULER_OPTIONS`, the `IMAGE_DEFAULTS` per-model preset table + `defaultsFor`,
`fmtDur`, and `parseSdProgress` — the sd-server stdout phase/step parser, spec'd in
`imageGen.test.ts`.

## `VideoInterface.svelte`

The Video studio, deliberately a close sibling of `ImageInterface` (same thread/turn model, same
bubbles, same composer chrome, a `<video controls loop>` where the `<img>` was). Store in
`stores/videoHistory.ts`, API client in `lib/videoApi.ts`, pure helpers in `videoGen.ts` (which
re-exports the aspect/sampler/`fmtDur` tables from `imageGen.ts` rather than forking them).

Three things are genuinely different, and each is forced by the backend:

- **The request is ASYNCHRONOUS.** `startVideoJob` (`POST /sdcpp/v1/vid_gen`) returns a job id in
  milliseconds; `awaitVideoJob` polls `/sdcpp/v1/jobs/{id}` every `VIDEO_POLL_MS` (2s) until a
  terminal status. A turn therefore carries a `jobId`, and the server pins the model for the life
  of the job (`internal/server/videojobs.go`).
- **Stop is a REAL cancel.** The Images tab has to `unloadSingleModel` to interrupt a render;
  here `cancelVideoJob` posts the job API's cancel route and the poll is aborted, so the model
  stays warm. sd.cpp may refuse once sampling has begun — the poll is dropped either way and the
  server's watcher releases the lease when the orphaned render finishes.
- **There is NO progress field** anywhere in the job document, only a status. The bar is therefore
  `parseSdProgress` over `$upstreamLogs` (the same sd-server stdout parser the Images tab uses),
  and `videoStatusLabel(status, queuePosition)` fills the gap before the backend has printed
  anything — it distinguishes "Queued (#2)" from "Rendering…", which a spinner cannot.

**Frames snap to 4n+1** (`snapFrames`, hence `FRAME_OPTIONS`): the backend rounds anyway, so
offering 30 would silently render 33. The FPS picker shows the resulting clip length next to it,
because neither knob means anything about duration on its own.

**Saved clips are files, not data URLs.** `serveUserBlob` runs `extractMedia` on every PUT, so a
`data:video/webm;base64,…` in a turn is rewritten to `/api/media/video/<hash>.webm` server-side
and the thread JSON stays small. That needed three server-side tweaks (`playground.go`): bucket
`video/*` into its own folder, know the mp4/webm extensions, and name the Content-Type on read —
`extMime("webm")` returns **`audio/webm`**, and a clip served with an audio type never paints.

## `SpeechInterface.svelte`

The Speech studio.

**Voice cloning is gated on the server-declared `capabilities.voice_clone`**, not on "the voice
list contains `''`": a TTS.cpp model's cached list starts at `[""]` too, and its engine has no
clone route at all, so the old inference offered a Clone button that could only fail.

The voice list auto-fetches when the selected model **becomes** ready, not only when the selection
changes — picking an idle model used to seed `[""]` and never look again, leaving a fixed-voice-pack
engine looking voiceless until the user found ⟳.

Voice-list normalization (`lib/voices.ts`) is shared with chat read-aloud — see
[`playground-chat.md`](playground-chat.md) for the `safeVoice` / substitution-warning rules, which
apply to both surfaces.

## `MaskEditor.svelte`

Canvas brush painter producing a PNG mask data URL for sd-server inpainting.
