<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Known issues & hardware limits

Known limitations and their workarounds, mostly GPU/backend specific.

**AMD + Vulkan: large image models (Flux, SDXL hi-res) fail to allocate.**
Symptom: image gen returns `500 - generate_image returned no results`; sd-server log shows `ggml_vulkan: Requested buffer size exceeds device buffer size limit: ErrorOutOfDeviceMemory` and `flux: failed to allocate the compute buffer`. Cause: AMD's Windows Vulkan driver caps a *single* memory allocation at **2 GiB** (`maxMemoryAllocationSize`/`maxBufferSize`), regardless of how much VRAM is free. Flux/SDXL need one contiguous compute buffer larger than that at higher resolutions, so it is rejected even with 20+ GB free. `--diffusion-fa` (flash attention) is already on and cannot bring it under 2 GiB at high res.
- Workaround: lower the generation resolution (768x768 usually works, 512 is safe). Smaller/distilled diffusion models stay under the cap - e.g. Z-Image-Turbo works fine on Vulkan.
- Real fix for hi-res Flux/SDXL on AMD: use a ROCm/HIP sd-server build (HIP has no 2 GiB single-allocation cap). Set it in Settings -> Backends. Text (llama-server) is unaffected because model weights are split across many sub-2 GiB buffers; only large single diffusion tensors hit the wall.

**`ggml_cuda_init: failed to initialize CUDA: (null)` on every model load.**
This means the backend binary you are running is a **CUDA-compiled** build, but no NVIDIA GPU is present (e.g. on an AMD box). At startup llama.cpp/sd.cpp probe for a CUDA device, fail, log this line, then fall back to CPU (slow). It is harmless as a message but means that process is NOT on the GPU. Fix: point Settings -> Backends at a Vulkan (or ROCm) build. A binary compiled without CUDA never prints this line.

**AMD GPUs report VRAM only - no temperature / fan / power.**
On non-NVIDIA Windows GPUs, quartermaster reads VRAM (total/used) and utilization via DXGI + PDH, but the driver does not expose temp/fan/power the way nvidia-smi does, so those gauges stay blank. Expected, not a bug.

**Choosing Vulkan vs ROCm/HIP on AMD.** For text (llama-server), Vulkan is the easy, working default. For image generation, prefer ROCm/HIP where you need higher resolution, because of the Vulkan 2 GiB single-allocation cap above.
