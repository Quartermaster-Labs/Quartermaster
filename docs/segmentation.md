<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Image segmentation (SAM)

**SAM (Segment Anything)** models run through the `sam3_server` backend and turn an image plus a prompt into object masks.

**Where you use it:** the inpaint mask editor in the Images tab. With a segmentation model configured, the mask tools grow three AI options beside the freehand brush and lasso - **box select** (drag a rectangle around the object), **point select** (click foreground/background points), and **text select** (type what to mask, e.g. "the sky"). The resulting mask feeds straight into the inpaint generation, so you can retouch an object without hand-painting its outline. A text prompt can match several instances; they are merged into one mask.

**API:** `POST /v1/segment` with the model id, the image, and a `text`, `box`, or `points` prompt. The response carries each instance's score, bounding box, and a grayscale mask PNG (white = object).

**Setup and placement:** the model is discovered from a `.ggml` file and served like any other backend - the first request loads it. It runs in a **coexisting persistent group**, so a text or image model never evicts it, and it is pinned to the **CPU** (the Vulkan path returns numerically broken masks), which costs no VRAM budget but makes a segment take a few seconds. Configure the backend as **kind `sam`** (the `sam3_server` exe path); see the Backends article.
