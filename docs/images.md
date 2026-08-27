<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Image generation

The Images tab drives **stable-diffusion.cpp** models as a thread: every prompt adds a turn, and you can keep editing the last image or start a **New thread** for a fresh subject.

**API mode** picks the route. **SDAPI** exposes the full control set below and can edit a previous image; **OpenAI** (`/v1/images/generations`) only ever generates fresh - it has no edit loop.

**Framing**: choose an **aspect** (1:1 through 9:16) and a **size** (the long edge; the short edge snaps to a multiple of 64). Sizes above what a model handles are greyed out.

**Controls** (SDAPI): steps, CFG, seed (-1 = random; a pinned seed reproduces the first image of a batch and increments from there), batch (up to 8, rendered one after another), tweak strength (how far a follow-up may stray from the previous image), sampler, scheduler, and an optional negative prompt. Switching models loads that model's known-good defaults, shown under the fields. Two toggles: **Tone anchor** pins reused-source brightness to the thread's first image so chained edits don't drift, and **Keep resolution** edits at the source image's native size.

**LoRAs**: adapters sitting next to the model file, listed on demand via **Load list** (which loads the model), each with its own strength.

**Editing an image**:

- **Reply** on any image reuses it as the source for the next prompt.
- **Reference images** (Kontext / Qwen-Image-Edit class): attach one or more images the model conditions on while the prompt drives the edit; the palette button does the same for **style transfer**.
- **Inpainting**: the brush button opens the mask editor - paint the area to change with a brush or lasso, and with a segmentation model configured you can also select by box, by point, or by text ("the sky"). Only the masked region is redrawn.

While a generation runs you get the live phase (encoding, prompt, sampling, decoding), step count, ETA and elapsed time. Each finished image has actions for regenerate, edit the prompt, copy, download, and **upscale x4** - see the Upscaling article.

Tips: **turbo/lightning/distilled** checkpoints usually need **CFG = 1.0** - a higher CFG makes them blurry or burnt. SDXL/SD models need a **full checkpoint** (they can't be split-loaded from separate UNet + text-encoder files).
