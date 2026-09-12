<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# 3D generation (TRELLIS.2)

Quartermaster can turn **one image into a 3D mesh** - a textured GLB file - with [TRELLIS.2](https://huggingface.co/microsoft/TRELLIS.2-4B), run by the `trellis2-server` backend and routed like any other model: `POST /v1/3d/generations`.

It is the one model that is **not a single file**. A TRELLIS.2 install is a folder plus two sidecars living beside it, and autogen only picks it up when all of the pieces that it needs are there.

## 1. Install the backend

**Settings -> Backends -> 3D generation**, then *Install latest* on **trellis2-server**. Match the build to your GPU the same way you would for any other engine:

- **vulkan** for AMD and Intel, and for Linux (the only build there).
- **cuda** for NVIDIA on Windows. It still needs a working Vulkan driver: the texture baker and the mesh postprocessor are Vulkan in every build, CUDA or not.

## 2. Download the weights

Three downloads, arranged as one folder with three children:

```
<models root>/3D/TRELLIS.2/
  TRELLIS.2-4B/                      the package     --model     (~16 GB)
    pipeline.json
    texturing_pipeline.json
    ckpts/*.safetensors
  dinov3-vitl16-pretrain-lvd1689m/   the encoder     --dino      (~1.2 GB, REQUIRED)
    model.safetensors
    config.json
  BiRefNet/BiRefNet-F16.gguf         background cut  --birefnet  (~0.4 GB, optional)
```

The names of the three folders do not matter; being in the same tree does. Autogen treats any directory holding a `pipeline.json` as a package, then looks for the encoder and the remover in that directory, beside it, and anywhere under your models root - the last one so the browser's own layout works, where the encoder arrives under `facebook/` while the package lands under `microsoft/`. Nothing has to be moved after downloading.

**The package.** Browse, switch to the **3D** category, open `microsoft/TRELLIS.2-4B`. Because the repo has no GGUF, the browser offers its loadable files as **one row** (the safetensors plus the manifests that name them) and downloads the set in one go. It is not gated, but it is 16 GB, so start it and let it run.

**The encoder.** `facebook/dinov3-vitl16-pretrain-lvd1689m`, which *is* gated: accept its licence on the model page, then paste a Hugging Face read token into **Settings -> System -> Hugging Face token** (the same field the browser uses for Llama and friends; `HF_TOKEN` in the environment works too). Without this folder nothing can be generated: the shape stage starts from DINOv3 features, and a package with no encoder next to it is skipped at config generation with `no DINOv3 encoder` as the reason.

**The remover.** Any BiRefNet GGUF (search the browser for `BiRefNet`). It cuts the subject out of the image before the shape stage. Optional, but for a photo with a background it is the difference between a solid object and a thin shell - see *Known issues*. Save it as `<...>/TRELLIS.2/BiRefNet/BiRefNet-F16.gguf` and it is used automatically.

## 3. It is a model like any other

Once the folders are in place, the package shows up in `GET /v1/models` (the next config generation scans for it) as a **3D** model with an image-in / 3d-out capability line. It is loaded on first request and holds roughly 11 GB of VRAM while it is resident, so it swaps with your text and image models exactly like they do with each other.

The 3D config form (Models -> **3D** -> the model) is deliberately short: backend, unlisted/skip, and an **Extra args** box. There is no context size, offload or KV-cache knob to set, because none of it applies - the checkpoints are streamed through a bounded stage cache and the image arrives with each request. The flags you are most likely to want live in that box, since they have no equivalent anywhere else in the UI:

```
--pipeline 512 --steps 12 --texture-size 1024
```

Those are the defaults; leave them alone unless you are chasing quality or speed.

## 4. Generate

The **image is the request body**, so the model id moves into the query string:

```
curl -X POST "http://127.0.0.1:1250/v1/3d/generations?model=trellis-2-4b" \
  -H "Authorization: Bearer qm-..." \
  -H "Content-Type: image/png" \
  --data-binary @subject.png \
  -o subject.glb
```

The response **is** the GLB (content type `model/gltf-binary`). Add `keep=1` (or `format=json`) and the mesh stays on disk instead, the body is `{"file":...,"path":...,"bytes":...,"seconds":...}`, and you fetch the file through the backend proxy at `/upstream/<model>/output/<name>` - useful when the caller would rather have the file path first, and for picking up a mesh an earlier run left behind:

```
curl -X POST "http://127.0.0.1:1250/v1/3d/generations?model=trellis-2-4b&keep=1" \
  -H "Authorization: Bearer qm-..." -H "Content-Type: image/jpeg" \
  --data-binary @photo.jpg

# {"file":"trellis2-19992-3.glb","path":"...","bytes":41834520,"seconds":88.4}
curl -o photo.glb http://127.0.0.1:1250/upstream/trellis-2-4b/output/trellis2-19992-3.glb
```

Per-request options, all query parameters (the same names the backend's own CLI uses):

| Parameter | Default | What it does |
|---|---|---|
| `steps` | 12 | Sampling steps, both stages. |
| `texture_size` | 1024 | Texture atlas resolution. |
| `pipeline` | 512 | Coordinates the model works at. `1024` is the higher-quality profile and is not safe on every GPU - see *Known issues*. |
| `shape_only` | 0 | Geometry only, no texture. Much faster. |
| `seed`, `noise_seed` | random | Fix the seed to reproduce a mesh. |
| `keep`, `format=json` | off | Answer with JSON and leave the GLB in the backend's output folder, to fetch from `/upstream/<model>/output/<file>`. |

Expect roughly a minute and a half per 512-profile image on a discrete GPU, plus a few seconds for the first request, which loads the weights.

The mesh is **simplified by default** (about 1 million triangles, a 40 MB GLB) because a served model is normally viewed or sliced rather than archived. Add `--mesh-postprocess-no-simplify` to the model's *Extra launch arguments* to keep the full mesh instead - roughly 5.5 million triangles and a 180 MB file at the 512 profile - and `--mesh-decimation-target N` to aim the simplify pass at a different triangle count.

A few properties of this backend are worth knowing before you wire it into something:

- It is **single-threaded**: while a generation is running, that backend serves no other request. Another model on another port is unaffected, and the dashboard stays live (it reads logs and metrics, not the backend).
- Loading is **lazy but eager per process**: the first request pays the weight load, later ones do not, and the server stays up until its TTL expires or VRAM forces an eviction.
- A request made before the weights are complete fails in microseconds with `503 not ready: ...` naming the missing file, instead of starting a doomed generation.

## What it is good at

A **single, well-lit subject that fills the frame**, photographed or rendered against a plain background: toys, tools, a character turnaround, a product shot. Those come back as a mesh with real volume and a baked PBR texture.

A cluttered photo, a busy background, or a subject that is mostly texture and no silhouette is where it struggles, and the failure is quiet: you get a valid GLB that is a flat shell instead of a solid. Cut the subject out first (the browser's remover, or any segmentation model - see *Segmentation*) and it behaves.

Output meshes have no UV padding when the atlas runs out of room at high component counts, so a few seams in the texture are expected rather than a sign that something is misconfigured.
