# ui-svelte — playground image, video, speech & music studios

`src/components/playground/`. The chat surface is in
[`playground-chat.md`](playground-chat.md).

## `ImageInterface.svelte`

Full SD image-gen UI: txt2img/img2img (`ImageGenMode`), denoise/hires (`enable_hr`), reference
images (`extra_images`, Kontext), per-model defaults, seed modes.

**Layout.** A params panel on the left (prompt box with Enhance / inpaint / alpha and the model
picker, `+ Negative` / `+ Reference` chips, aspect + size segments, steps/CFG/seed/batch, an
Advanced fold for the API, denoise, sampler, tone anchor, keep-res and LoRAs, then Generate) and a
canvas on the right showing ONE turn big, with a thumbnail strip of the thread underneath.
`selTurn` pins an older turn from the strip; `null` follows the newest, and it resets to `null`
whenever the turn count or the thread changes, so a new render or a regenerate always lands in view.

**Batch** (Settings → Batch, `sdapi batch_size` → sd.cpp `batch_count`, capped at `MAX_BATCH`):
N images per prompt, rendered sequentially with the seed incrementing per image — the step bar
therefore restarts per image (the in-flight row shows `×N`). Each turn keeps a **picked** image
index (numbered segment in the canvas toolbar, batch>1 only) that the reply/copy/download/upscale
actions act on.

**Prompt enhance** (`runEnhance` -> `lib/promptEnhance.enhancePrompt` -> `POST
/v1/chat/completions`): Enhance button in the prompt box's bottom bar, rendered only when the
selected model carries an enhancer (server-resolved, see `../CLAUDE.md`). A model may name one per
direction, so the pick follows the mode the render itself will use: `promptEnhancerEdit` when a
`baseImage` is attached, `promptEnhancer` otherwise, each half standing in for a missing other.
The tooltip names which one is about to run, since the button silently changes meaning once an
image is attached. Rewrites the
prompt box IN PLACE, with an undo button next to it that survives until the user edits the box
themselves. A vision enhancer is also handed `baseImage` plus the other attachments, capped at
`MAX_REF_IMAGES`, images first because these models are trained on image-then-instruction order
and flipping it makes them describe the picture instead of rewriting the request. Failures go to
their own dismissible banner, not `dropError`: that one self-clears on a 4 second timer.

**ESRGAN upscale** (`runUpscale` → `lib/imageApi.upscaleImage` → `POST /v1/images/upscale`):
⤢ button in the canvas toolbar AND on each attached reference (hover); posts the 4×
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
params panel + canvas + thread strip layout, a `<video controls loop>` where the `<img>` was; the
Start/End frame chips sit where Image has `+ Reference`, and the strip's tiles are `<video
preload="metadata">` nudged to 0.1s so they paint a frame). Store in
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

**Playing a take lifts it into the spotlight** (`TakeSpotlight.svelte`), above the grid: whole
text, voice + model chips, its own player, and the sentence being spoken painted like chat
read-aloud (`--color-speak`, spoken text dimmed). A card's play button never plays in place: its
`AudioPlayer` gets `onplayrequest` and hands its position to the spotlight. `focus` pins the take
by index AND audio string, so a delete, regenerate, edit or thread switch drops the spotlight
instead of showing one take's text over another's clip. Each new take records `model`; older takes
have none and show no chip.

**The highlight timing is estimated, not reported.** Chat knows the playing sentence because it
synthesises one chunk per request; a take is ONE wav and no engine returns timing. Chunking the
take instead was rejected (it costs cross-sentence prosody and voice consistency, and the download
would be stitched), and a forced-aligner model would evict the TTS model on one GPU.
`lib/takeTiming.ts` splits sentences (long ones at commas), weights them by letters plus a
punctuation pause, spreads them over the voiced part of the decoded clip, and snaps each boundary
to the nearest real pause, re-estimating the rest from where it landed so drift does not
accumulate. Sentence-level only: a length model is not good enough for words.

## `MusicInterface.svelte`

The Speech studio's layout (settings rail left, takes grid right, composer under it) with four
modes on a segmented control, three of them one per route family in `lib/musicApi.ts`:

- **Create**: text to music (`/v1/music/generations`, ACE-Step, MiniMax Music 3 and YuE2).
- **Edit**: repaint a time window, complete, or cover an existing clip (`/v1/music/edits`,
  ACE-Step only). The repaint window is dragged across a bar or typed in seconds.
- **Stems** (mode key `stems`): a source clip, uploaded or pulled from a pasted link
  (`fetchLinkAudio` -> `/v1/tools/media/audio`, yt-dlp, decoded by `toWav` like a file), split
  into four stems (`/v1/audio/separations`, HTDemucs). No prompt.
- **Scores** (mode key `scores`): the score library, below. Needs no model, so it is always
  offered, and the tab renders with no music model at all once the library has a score.

**A reference song is not a mode, it is an attachment.** While the picked generator advertises
`music_score` (YuE2), the composer has a **+** menu: upload a song, paste a link, or load a
`.abc` file. A song is transcribed in the background with the first `music_transcription` model
(SheetSage2, `/v1/music/transcriptions`), no Run press, straight into the score
(`playground-music-score`) and credited to its recording (`playground-music-score-origin`, kept
with a copy of the score so an edited score stops claiming the origin). It is not a take; the
score and its origin are copied into the song that uses it. The score floats above the composer
as a chip: click to view and edit it, the link icon opens the original, the info icon carries the
non-commercial disclaimer, and X drops it (aborting a read in flight). Generate waits while a read
runs, and a finished read turns Planning `off` into `melody`, since YuE2 400s a score without one.
The + exists **only for a score-taking generator**: for any other model a transcription would
swap SheetSage into VRAM and the generator out for a result nothing reads. A take's menu offers
"Use as reference" through the same path. Older transcription takes still render, with
"Use this score".

Each mode keeps its **own** model pick (`playground-music-model` / `-edit-model` /
`-stems-model`), filtered by capability (`music_generation`, `music_edit`, `audio_separation`),
so switching modes can never leave a model the route would refuse. A mode with no model is
hidden. `playground-music-model` is also what the dashboard's launch link writes, so an effect
moves a separator that lands there into the stems pick and switches mode.

**Every audio field on a take is a `/api/media` ref.** Results are uploaded as they land
(`uploadMedia`) and the thread holds refs only; see `internal/server/playground.md` for why and
for the GC grace that covers the gap. A picked source file is uploaded at RUN time, not pick
time, so a user who takes longer than the grace before pressing play does not lose it. A source
taken from a take's menu ("Edit this take", "Split into stems") is already a ref.

The source is converted to WAV in the browser (`toWav`): a WAV passes through byte for byte,
anything else is decoded and re-encoded as stereo PCM16 (`lib/wav.ts` is mono, which suits speech
and flattens a song). MiniMax is recognised by model id (`/minimax/i`) to require lyrics and hide
ACE-Step's options, YuE2 (`/yue/i`) to hide them and the Length field (its song is as long as
its lyrics, and the route 400s a duration) and to offer its own Planning option (`cot`,
`playground-music-cot`); the server's family check is the backstop. Edits over ~24 s fail only on AMD
Vulkan (the VAE encoder's 2 GB buffer), so the client does not block them and shows the server's
message instead.

Every generated take has a **References** button (scroll icon) opening a popup of what went
into it: style/instruction, source clip and its page, lyrics, score and where it was extracted
from, stems, and the settings (`settingsRows`). Escape or a backdrop click closes it.

A take that starts (send, or "New take, new seed") pauses every `<audio>` under the tab's root
(`stopPlayback`), so the run is not talked over by the take it is replacing.

### The score library

`stores/scoreLibrary.ts` (`GET`/`PUT /api/musicscores`, a per-user blob kept apart from the
threads, so deleting a thread never takes its scores). `addScore` is called wherever a score
appears: a SheetSage2 read through the composer's +, an older transcription take, a `.abc` loaded
into the composer, and the Scores mode's own Upload (several files at once, 64 KiB each, the
server's score cap). It dedupes on the normalised text, so re-reading the same song bumps the
entry instead of adding a twin. `kind` records how it arrived; `origin` credits the recording.

Scores mode is `ScoreList.svelte` in the rail and `ScoreViewer.svelte` on the right. The same
`ScoreList` is the composer's **+ -> From the library…** picker (offered while the generator's
`music_score` is `abc`), whose pick goes through `useLibraryScore`, the same path as a fresh read.
The viewer's "Use in Create" does the same.

`ScoreViewer` imports `lib/abcScore.ts` lazily (abcjs is ~400 KB) and:

- engraves with `abcjs.renderAbc` (`foregroundColor: currentColor`, so it follows the theme);
- plays through `lib/scorePlayer.ts`, a WebAudio lookahead scheduler over `noteEvents`, NOT
  abcjs's synth, which fetches its soundfont from a public CDN. The sounding notes are lit via
  `getElementFromChar(startChar).abselem.elemset`. "Chords" adds a pad under the chord symbols;
- edits the ABC in place (saved debounced) and renames on blur;
- exports `.abc`, `.mid` (`abcjs.synth.getMidiFile`) and `.musicxml` (`toMusicXML`).

`lib/abcScore.ts` walks abcjs's parse, never the text. Every `V:` voice is a MusicXML part named
from its `name=`; `Z<n>` becomes N measure rests; `% name` comment lines become rehearsal marks on
the first part (domina-music's `abc_to_musicxml.py` did the same); alterations come from a written
accidental, then the measure's memory, then the key; triplets carry `time-modification`, and a
length no single note spells is split into tied notes. Parts are padded to one length, since
MuseScore wants that. abcjs prints `#` in a chord name as `♯`; `parseChordName` reads both.
`abcScore.test.ts` pins it on the SheetSage2 shape, including "every measure sums to the meter".

Takes are a library, not a conversation: a new take, "New take, new seed" included, is always
appended and nothing after it is dropped. `execute` reads the stored take rather than the form, so
a regenerate replays exactly that take's settings. The seed shown on a card is the one the server
**echoed** (`X-Seed`), so a random-seed take can be pinned and reproduced; clicking it pins it.

## `MaskEditor.svelte`

Canvas brush painter producing a PNG mask data URL for sd-server inpainting.
