# playground — the 3D tab

`components/playground/ThreeDInterface.svelte`, `GlbViewer.svelte`, `threeDGen.ts`,
`lib/threeDApi.ts`, `stores/threeDHistory.ts`.

Image in, orbitable mesh out, via TRELLIS.2 (`trellis2-server`). Deliberately the same tab shape as
the Images and Video studios — a thread of turns, a server-backed history flyout, a settings popover
on the composer — so the only things worth documenting here are where it *has* to differ.

## The route decides the design

`POST /v1/3d/generations?model=<id>` (`internal/server/server.go`, `modelPostRawRoutes`):

- **The image is the raw request body.** There is no JSON document, so the model id and every knob
  ride as query parameters and `Content-Type` is the image's own mime, which is how the backend
  decides how to decode it. One image per request, which is why the composer holds one pending
  picture rather than the image tab's chip row.
- **It is synchronous.** Unlike video (a job id in milliseconds, polled for minutes) the request
  stays open for the whole generation: roughly 90s at the 512 profile, plus the first request's
  weight load. There is **no job API, no queue position and no progress field**, so an in-flight turn
  shows an elapsed counter against `estimateLabel()` and nothing finer. Any percentage on this tab
  would be invented.
- **The backend is single-threaded.** While a mesh generates that process serves no other request,
  so the tab allows one generation at a time. That is not a UI nicety: a second request would sit
  inside the backend with no queue document to show for it.
- **There is no cancel route.** Stop aborts the `fetch`, which abandons the *response*; the render
  runs to completion and the backend stays busy until it does. The stop tooltip says that outright
  rather than implying the GPU came back, and the abandoned turn's source image is put back into the
  composer so an abort costs nothing.

## No prompt

The whole request is one image, so `Composer.svelte` grows a `hideTextarea` prop and this is its only
user. Consequences that are easy to trip over:

- **There is no Enter to send on**, so the tab adds an explicit send button to
  `extraRightButtons`. Without it the composer has no way to start a generation.
- **Paste is bound to the window**, not to a field, because with no textarea there is nothing for a
  paste to land in.
- **A title is never derived.** `deriveThreeDTitle()` always returns `"New mesh"`; a thread is named
  by renaming it, and its history row is identified by a thumbnail of the *source* image
  (`threeDThumbs` in `PlaygroundShell`). A result thumbnail is not an option: a GLB cannot be drawn
  in an `<img>`, and a WebGL preview per history row would spend a real context on each.

## Storage

A mesh is stored exactly like a picture: `threeDApi.ts` converts the GLB response to a
`data:model/gltf-binary;base64,…` URL, and the server's `extractMedia` splits it out to
`media/model/<hash>.glb` on the first PUT, so the session JSON stays small. The mime is **forced**
rather than trusted — a proxy that rewrote it to `octet-stream` would leave a `.bin` on disk.

A `blob:` URL would be cheaper but is scoped to the document that made it, so every mesh would be a
dead link after a reload.

Two server-side registrations are load-bearing and easy to forget when adding any sidecar:

- `3dchats.json` is in the **`gcMedia` scan list** (`internal/server/playground.go`). A tab JSON that
  is not scanned there does not count as a reference, so every mesh and source image would be
  silently deleted by the next PUT from any *other* tab.
- `model/gltf-binary` is in `mediaKind` / `mimeExt` / `extMime`, so meshes land under
  `media/model/` with a `.glb` extension and are served back with the right type.

## The viewer

`GlbViewer.svelte`. three.js is loaded with a dynamic `import()`, the way `lib/diagrams.ts` loads
mermaid: it is the heaviest dependency in the bundle and only this tab needs it.

- **Disposal is load-bearing, not hygiene.** A default mesh is around a million triangles with a
  1024 texture atlas, and those buffers live in GPU memory, which the JS garbage collector does not
  manage: dropping the last reference to a `Mesh` frees the JS object and leaks the VBO. Every
  geometry, material and texture slot is disposed explicitly, and so is the context —
  `forceContextLoss()`, because browsers cap live WebGL contexts (typically 16) and silently kill the
  *oldest* when a new viewer asks past the limit, which would blank a mesh further up the thread.
- **`RoomEnvironment` IBL, not a light rig.** TRELLIS.2 bakes a real PBR material with
  metalness/roughness, and those are lit almost entirely by the environment map: under two
  directional lights a metallic surface renders black. `RoomEnvironment` is procedural, so it ships
  no asset and cannot 404 in an offline install.
- **The camera is framed from the bounding sphere**, and the model is re-centred at the origin. A GLB
  arrives at whatever scale and offset it was authored in, so a fixed camera position shows either
  the inside of the model or an empty world.
- **Pan is off.** With damping and zoom already on the two gestures people try, a third that slides
  the subject out of frame is the one triggered by accident, and it leaves a blank viewport.
- The frame loop is dirty-flagged: an idle mesh is not redrawn sixty times a second on a box that is
  mid-generation.

## Settings

`threeDGen.ts` — much smaller than `imageGen`/`videoGen`, because TRELLIS.2 has no size, no sampler,
no scheduler and no CFG. Steps, texture size, pipeline profile, shape-only, seed; the defaults are
the backend's own documented CLI defaults (`docs/three-d-generation.md`), not per-checkpoint tuning.

- The **1024 pipeline** is marked `warn` rather than hidden: it is the higher-quality profile and the
  docs call it unsafe on some GPUs. That is a warning, not a block.
- **Seed `-1`** means random, and the backend's random is what you get by *omitting* the parameter.
  Sending `-1` would be a literal seed.
- `estimateSeconds()` is anchored on the one figure on record (~90s at 12 steps, 512 profile,
  discrete GPU) and is deliberately coarse. It exists so the composer can say "about a minute and a
  half" before a user commits the whole backend, not to be accurate to the second.
