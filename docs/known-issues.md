<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Known issues & hardware limits

Known limitations and their workarounds, mostly GPU/backend specific.

**TRELLIS.2 3D: the `1024` profile resets an RDNA3 driver (Windows).**
Symptom: a generation sent with `pipeline=1024` (or `--pipeline 1024`) never reaches step 1, then the process dies; Windows logs `LiveKernelEvent P1: 141` (VIDEO_ENGINE_TIMEOUT_DETECTED), drops a dump in `C:\Windows\LiveKernelReports\WATCHDOG\`, and the client sees a dropped connection. Reproduced on an RX 7900 XTX three times, through both the HTTP server and the upstream CLI, with flash attention enabled. Cause: the shape stage dispatches one pass of 18078 tokens on the bf16 flash-attention path, and that pass outlives the ~2 s GPU watchdog, which then resets the driver. The 512 profile stays on the f16 path with a shorter dispatch and runs clean.
- Workaround: leave `pipeline` at its default 512. `texture_size=1024` is unaffected and independent.
- If you want the higher-quality profile: raise the watchdog timeout (`TdrDelay` / `TdrDdiDelay` under `HKLM\SYSTEM\CurrentControlSet\Control\GraphicsDrivers`, needs admin and a reboot), or run it on NVIDIA.

**TRELLIS.2 3D: a busy image can come back as a thin sheet.**
Symptom: the GLB is a flat shell rather than a solid object, most often from a photo with a background or several subjects. Cause: the sparse-structure stage estimates occupancy from short-range detail, so a cluttered image can collapse into a surface. This is upstream behaviour, not the port, and it is invisible from the API's side - the mesh is valid, just empty.
- Workaround: give the model one isolated subject against a plain background, and let the BiRefNet background remover run. If it still collapses, cut the subject out yourself (Segmentation) and send that.

**AMD + Vulkan: large image models (Flux, SDXL hi-res) fail to allocate.**
Symptom: image gen returns `500 - generate_image returned no results`; sd-server log shows `ggml_vulkan: Requested buffer size exceeds device buffer size limit: ErrorOutOfDeviceMemory` and `flux: failed to allocate the compute buffer`. Cause: AMD's Windows Vulkan driver caps a *single* memory allocation at **2 GiB** (`maxMemoryAllocationSize`/`maxBufferSize`), regardless of how much VRAM is free. Flux/SDXL need one contiguous compute buffer larger than that at higher resolutions, so it is rejected even with 20+ GB free. `--diffusion-fa` (flash attention) is already on and cannot bring it under 2 GiB at high res.
- Workaround: lower the generation resolution (768x768 usually works, 512 is safe). Smaller/distilled diffusion models stay under the cap - e.g. Z-Image-Turbo works fine on Vulkan.
- Real fix for hi-res Flux/SDXL on AMD: use a ROCm/HIP sd-server build (HIP has no 2 GiB single-allocation cap). Set it in Settings -> Backends. Text (llama-server) is unaffected because model weights are split across many sub-2 GiB buffers; only large single diffusion tensors hit the wall.

**`ggml_cuda_init: failed to initialize CUDA: (null)` on every model load.**
This means the backend binary you are running is a **CUDA-compiled** build, but no NVIDIA GPU is present (e.g. on an AMD box). At startup llama.cpp/sd.cpp probe for a CUDA device, fail, log this line, then fall back to CPU (slow). It is harmless as a message but means that process is NOT on the GPU. Fix: point Settings -> Backends at a Vulkan (or ROCm) build. A binary compiled without CUDA never prints this line.

**AMD GPUs report VRAM only - no temperature / fan / power.**
On non-NVIDIA Windows GPUs, Quartermaster reads VRAM (total/used) and utilization via DXGI + PDH, but the driver does not expose temp/fan/power the way nvidia-smi does, so those gauges stay blank. Expected, not a bug.

**Choosing Vulkan vs ROCm/HIP on AMD.** For text (llama-server), Vulkan is the easy, working default. For image generation, prefer ROCm/HIP where you need higher resolution, because of the Vulkan 2 GiB single-allocation cap above.
