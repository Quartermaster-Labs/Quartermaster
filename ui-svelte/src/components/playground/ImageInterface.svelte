<script lang="ts">
  import { models, upstreamLogs } from "../../stores/api";
  import { persistentStore } from "../../stores/persistent";
  import { generateImage } from "../../lib/imageApi";
  import { generateSdImage, generateSdImg2Img, fetchSdLoras } from "../../lib/sdApi";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import ModelSelector from "./ModelSelector.svelte";
  import Select from "./Select.svelte";
  import { Image as ImageIcon, Send, Square, Layers, Trash2, Download, X, Type, ImagePlus, Wand2, Maximize2 } from "lucide-svelte";
  import type { ImageApiMode, ImageGenMode, SdApiLora, SdApiLoraRef, ImageStylePreset } from "../../lib/types";

  const selectedModelStore = persistentStore<string>("playground-image-model", "");
  const selectedSizeStore = persistentStore<string>("playground-image-size", "1024x1024");
  const apiModeStore = persistentStore<ImageApiMode>("playground-image-api-mode", "openai");
  // txt2img | img2img. img2img drives sd-server's /sdapi/v1/img2img with a
  // source image + denoise strength, so it forces the SDAPI param set on.
  const modeStore = persistentStore<ImageGenMode>("playground-image-mode", "txt2img");
  const sdDenoiseStore = persistentStore<number>("playground-sdapi-denoise", 0.6);
  const sdUpscaleStore = persistentStore<number>("playground-sdapi-upscale", 2);
  // Flux Kontext reference edit: send the source as ref_image (identity-preserving
  // re-pose / restyle) instead of a plain denoise. Only meaningful with a
  // Kontext-class model loaded.
  const sdRefEditStore = persistentStore<boolean>("playground-sdapi-ref-edit", false);

  // SDAPI persistent settings
  const sdNegativePromptStore = persistentStore<string>("playground-sdapi-negative-prompt", "");
  const sdStepsStore = persistentStore<number>("playground-sdapi-steps", 20);
  const sdCfgScaleStore = persistentStore<number>("playground-sdapi-cfg-scale", 7);
  const sdSeedStore = persistentStore<number>("playground-sdapi-seed", -1);
  const sdSamplerStore = persistentStore<string>("playground-sdapi-sampler", "");
  const sdSchedulerStore = persistentStore<string>("playground-sdapi-scheduler", "");
  const sdBatchSizeStore = persistentStore<number>("playground-sdapi-batch-size", 1);

  // Batch / consistency settings (SDAPI only — needs per-request seed/params).
  const styleSuffixStore = persistentStore<string>("playground-image-style-suffix", "");
  const batchModeStore = persistentStore<boolean>("playground-image-batch-mode", false);
  const seedModeStore = persistentStore<"random" | "fixed" | "increment">("playground-image-seed-mode", "random");
  const presetsStore = persistentStore<ImageStylePreset[]>("playground-image-presets", []);

  let prompt = $state("");
  let sourceImage = $state<string | null>(null); // img2img source as a data URL
  let isGenerating = $state(false);
  let generatedImages = $state<string[]>([]);
  let error = $state<string | null>(null);
  let abortController = $state<AbortController | null>(null);
  let showFullscreen = $state(false);
  let fullscreenIndex = $state(0);
  let batchDone = $state(0);
  let batchTotal = $state(0);
  let presetName = $state("");
  let elapsed = $state(0);
  let step = $state(0);
  let totalSteps = $state(0);
  let secPerIt = $state(0);

  // Flattened option lists for the Select dropdowns (Select.svelte has no optgroups).
  const SIZE_OPTIONS = [
    { value: "512x512", label: "512×512 · Square" },
    { value: "1024x1024", label: "1024×1024 · Square" },
    { value: "1024x768", label: "1024×768 · 4:3" },
    { value: "1280x720", label: "1280×720 · 16:9" },
    { value: "1792x1024", label: "1792×1024 · SDXL wide" },
    { value: "768x1024", label: "768×1024 · 3:4" },
    { value: "720x1280", label: "720×1280 · 9:16" },
    { value: "1024x1792", label: "1024×1792 · SDXL tall" },
  ];
  const SAMPLER_OPTIONS = ["", "euler_a", "euler", "heun", "dpm2", "dpmpp2s_a", "dpmpp2m", "dpmpp2mv2", "ipndm", "ipndm_v", "lcm", "ddim_trailing", "tcd"].map(
    (v) => ({ value: v, label: v || "Default sampler" })
  );
  const SCHEDULER_OPTIONS = ["", "discrete", "karras", "exponential", "ays", "gits"].map(
    (v) => ({ value: v, label: v || "Auto for model" })
  );

  // Elapsed-seconds tick so a slow (offloaded) generation looks alive, since the
  // blocking /v1/images endpoint returns no progress.
  $effect(() => {
    if (!isGenerating) {
      elapsed = 0;
      return;
    }
    const start = Date.now();
    const id = setInterval(() => {
      elapsed = Math.floor((Date.now() - start) / 1000);
    }, 250);
    return () => clearInterval(id);
  });

  // Step progress from sd-server's stdout (mirrored into the upstreamLogs SSE
  // stream): lines like "  |===> | 3/20 - 18.40s/it". Take the last match in the
  // log tail — newest wins, so stale lines from a prior generation lose.
  // ponytail: tail-scan + regex, no per-model log isolation; if two image models
  // ever stream at once this could read the wrong one (they can't — swap-exclusive).
  $effect(() => {
    if (!isGenerating) {
      step = 0;
      totalSteps = 0;
      secPerIt = 0;
      return;
    }
    const tail = $upstreamLogs.slice(-4000);
    const re = /(\d+)\/(\d+)\s*-\s*([\d.]+)s\/it/g;
    let m: RegExpExecArray | null;
    let last: RegExpExecArray | null = null;
    while ((m = re.exec(tail)) !== null) last = m;
    if (last) {
      step = +last[1];
      totalSteps = +last[2];
      secPerIt = +last[3];
    }
  });

  let etaSec = $derived(
    totalSteps > 0 && secPerIt > 0 ? Math.round((totalSteps - step) * secPerIt) : 0
  );

  // SDAPI lora state
  let availableLoras = $state<SdApiLora[]>([]);
  let selectedLoras = $state<SdApiLoraRef[]>([]);
  let isLoadingLoras = $state(false);
  let lorasLoaded = $state(false);
  let loraError = $state<string | null>(null);

  let hasModels = $derived($models.some((m) => !m.unlisted));
  let isImg2img = $derived($modeStore === "img2img");
  // img2img always speaks SDAPI; show the SDAPI param set for either trigger.
  let showSdSettings = $derived($apiModeStore === "sdapi" || isImg2img);
  let isSdapi = $derived(showSdSettings);

  $effect(() => {
    playgroundStores.imageGenerating.set(isGenerating);
  });

  async function loadLoras() {
    if (!$selectedModelStore || isLoadingLoras) return;
    isLoadingLoras = true;
    loraError = null;
    try {
      const loras = await fetchSdLoras($selectedModelStore);
      availableLoras = loras;
      lorasLoaded = true;
    } catch (err) {
      availableLoras = [];
      loraError = err instanceof Error ? err.message : "Failed to load LoRAs";
      lorasLoaded = false;
    } finally {
      isLoadingLoras = false;
    }
  }

  function addLora(event: Event) {
    const select = event.target as HTMLSelectElement;
    const path = select.value;
    if (!path) return;

    const lora = availableLoras.find((l) => l.path === path);
    if (lora && !selectedLoras.some((l) => l.path === path)) {
      selectedLoras = [...selectedLoras, { path: lora.path, multiplier: 1.0 }];
    }
    select.value = "";
  }

  function removeLora(path: string) {
    selectedLoras = selectedLoras.filter((l) => l.path !== path);
  }

  function updateLoraMultiplier(path: string, multiplier: number) {
    selectedLoras = selectedLoras.map((l) =>
      l.path === path ? { ...l, multiplier } : l
    );
  }

  function getLoraName(path: string): string {
    return availableLoras.find((l) => l.path === path)?.name ?? path;
  }

  function applyPreset(p: ImageStylePreset) {
    $styleSuffixStore = p.suffix;
    $sdNegativePromptStore = p.negativePrompt;
    $sdStepsStore = p.steps;
    $sdCfgScaleStore = p.cfgScale;
    $sdSamplerStore = p.sampler;
    $sdSchedulerStore = p.scheduler;
    $selectedSizeStore = p.size;
    selectedLoras = p.loras.map((l) => ({ ...l }));
  }

  function savePreset() {
    const name = presetName.trim();
    if (!name) return;
    const preset: ImageStylePreset = {
      name,
      suffix: $styleSuffixStore,
      negativePrompt: $sdNegativePromptStore,
      steps: $sdStepsStore,
      cfgScale: $sdCfgScaleStore,
      sampler: $sdSamplerStore,
      scheduler: $sdSchedulerStore,
      size: $selectedSizeStore,
      loras: selectedLoras.map((l) => ({ ...l })),
    };
    // Replace same-named, then append — names are the key.
    $presetsStore = [...$presetsStore.filter((p) => p.name !== name), preset];
    presetName = "";
  }

  function deletePreset(name: string) {
    $presetsStore = $presetsStore.filter((p) => p.name !== name);
  }

  // Build the final prompt: per-asset line + shared style suffix.
  function composePrompt(line: string): string {
    const suffix = $styleSuffixStore.trim();
    return suffix ? `${line.trim()}, ${suffix}` : line.trim();
  }

  // One SDAPI generation → array of data-URI images.
  async function genSdOne(promptText: string, seed: number, signal: AbortSignal): Promise<string[]> {
    const [w, h] = $selectedSizeStore.split("x").map(Number);
    const response = await generateSdImage(
      {
        model: $selectedModelStore,
        prompt: promptText,
        negative_prompt: $sdNegativePromptStore || undefined,
        width: w,
        height: h,
        steps: $sdStepsStore,
        cfg_scale: $sdCfgScaleStore,
        seed,
        batch_size: $sdBatchSizeStore,
        sampler_name: $sdSamplerStore || undefined,
        scheduler: $sdSchedulerStore || undefined,
        lora: selectedLoras.length > 0 ? selectedLoras : undefined,
      },
      signal
    );
    return (response.images ?? []).map((img) => `data:image/png;base64,${img}`);
  }

  // One SDAPI img2img generation. Strips the data-URI prefix off the source —
  // sd-server's sdapi wants raw base64 in init_images (A1111 convention).
  async function genImg2ImgOne(promptText: string, seed: number, signal: AbortSignal): Promise<string[]> {
    const [w, h] = $selectedSizeStore.split("x").map(Number);
    const initB64 = (sourceImage ?? "").replace(/^data:[^,]+,/, "");
    const response = await generateSdImg2Img(
      {
        model: $selectedModelStore,
        prompt: promptText,
        negative_prompt: $sdNegativePromptStore || undefined,
        init_images: [initB64],
        denoising_strength: $sdDenoiseStore,
        // Kontext route: also hand the source in as ref_image so the model
        // preserves the subject while the prompt drives the change.
        ref_image: $sdRefEditStore ? initB64 : undefined,
        width: w,
        height: h,
        steps: $sdStepsStore,
        cfg_scale: $sdCfgScaleStore,
        seed,
        batch_size: $sdBatchSizeStore,
        sampler_name: $sdSamplerStore || undefined,
        scheduler: $sdSchedulerStore || undefined,
        lora: selectedLoras.length > 0 ? selectedLoras : undefined,
      },
      signal
    );
    return (response.images ?? []).map((img) => `data:image/png;base64,${img}`);
  }

  // One OpenAI generation → array of data-URI / URL images.
  async function genOpenAiOne(promptText: string, signal: AbortSignal): Promise<string[]> {
    const response = await generateImage($selectedModelStore, promptText, $selectedSizeStore, signal);
    const d = response.data?.[0];
    if (!d) return [];
    if (d.b64_json) return [`data:image/png;base64,${d.b64_json}`];
    if (d.url) return [d.url];
    return [];
  }

  async function generate() {
    const trimmedPrompt = prompt.trim();
    if (!$selectedModelStore || isGenerating) return;
    if (isImg2img && !sourceImage) return; // img2img needs a source image
    if (!isImg2img && !trimmedPrompt) return; // txt2img needs a prompt

    // Batch = one prompt per non-empty line; otherwise the whole prompt is one.
    // img2img with an empty prompt still runs once (denoise the source alone).
    const lines = $batchModeStore
      ? trimmedPrompt.split("\n").map((l) => l.trim()).filter(Boolean)
      : [trimmedPrompt];
    if (lines.length === 0) lines.push("");

    isGenerating = true;
    error = null;
    abortController = new AbortController();
    generatedImages = [];
    batchDone = 0;
    batchTotal = lines.length;

    // Seed base for fixed/increment modes (random => -1 each gen).
    const seedBase = $sdSeedStore < 0 ? 0 : $sdSeedStore;

    try {
      const collected: string[] = [];
      for (let i = 0; i < lines.length; i++) {
        const promptText = composePrompt(lines[i]);
        const seed =
          $seedModeStore === "random" ? -1 : $seedModeStore === "increment" ? seedBase + i : seedBase;
        let imgs: string[];
        if (isImg2img) {
          imgs = await genImg2ImgOne(promptText, seed, abortController.signal);
        } else if (isSdapi) {
          imgs = await genSdOne(promptText, seed, abortController.signal);
        } else {
          imgs = await genOpenAiOne(promptText, abortController.signal);
        }
        collected.push(...imgs);
        generatedImages = [...collected];
        batchDone = i + 1;
      }
    } catch (err) {
      if (err instanceof Error && err.name === "AbortError") {
        // User cancelled — keep whatever completed so far.
      } else {
        error = err instanceof Error ? err.message : "An error occurred";
      }
    } finally {
      isGenerating = false;
      abortController = null;
    }
  }

  function cancelGeneration() {
    abortController?.abort();
  }

  function clearImage() {
    generatedImages = [];
    error = null;
    prompt = "";
    sourceImage = null;
  }

  function onSourceFile(event: Event) {
    const file = (event.target as HTMLInputElement).files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => (sourceImage = reader.result as string);
    reader.readAsDataURL(file);
  }

  // Feed a generated tile back in as the img2img source, then switch to img2img.
  function useAsSource(img: string) {
    sourceImage = img;
    $modeStore = "img2img";
  }

  // Natural pixel size of a data-URL image (for sizing the upscale target).
  function imgSize(dataUrl: string): Promise<[number, number]> {
    return new Promise((resolve, reject) => {
      const im = new window.Image();
      im.onload = () => resolve([im.naturalWidth, im.naturalHeight]);
      im.onerror = reject;
      im.src = dataUrl;
    });
  }

  // Diffusion upscale: re-run the image through img2img at N× its own size with a
  // low denoise, so structure is kept while detail is added at higher res. The
  // factor comes from the sidebar (sdUpscaleStore).
  // ponytail: not ESRGAN — that needs the model launched with --upscale-model.
  // This needs no backend change and is the common "make it bigger" case. The
  // long edge is capped at 4096 (so a big factor can't OOM) and dims snap to 64,
  // which sd-server requires.
  const UPSCALE_DENOISE = 0.4;
  async function upscaleImage(img: string) {
    if (isGenerating || !$selectedModelStore) return;
    isGenerating = true;
    error = null;
    abortController = new AbortController();
    batchTotal = 1;
    batchDone = 0;
    try {
      const [sw, sh] = await imgSize(img);
      const factor = $sdUpscaleStore > 0 ? $sdUpscaleStore : 2;
      const scale = Math.min(factor, 4096 / Math.max(sw, sh));
      const snap = (n: number) => Math.max(64, Math.round((n * scale) / 64) * 64);
      const w = snap(sw);
      const h = snap(sh);
      const initB64 = img.replace(/^data:[^,]+,/, "");
      const response = await generateSdImg2Img(
        {
          model: $selectedModelStore,
          prompt: composePrompt(prompt.trim()),
          negative_prompt: $sdNegativePromptStore || undefined,
          init_images: [initB64],
          denoising_strength: UPSCALE_DENOISE,
          width: w,
          height: h,
          steps: $sdStepsStore,
          cfg_scale: $sdCfgScaleStore,
          seed: -1,
          batch_size: 1,
          sampler_name: $sdSamplerStore || undefined,
          scheduler: $sdSchedulerStore || undefined,
          lora: selectedLoras.length > 0 ? selectedLoras : undefined,
        },
        abortController.signal
      );
      const out = (response.images ?? []).map((x) => `data:image/png;base64,${x}`);
      generatedImages = [...generatedImages, ...out];
    } catch (err) {
      if (!(err instanceof Error && err.name === "AbortError")) {
        error = err instanceof Error ? err.message : "An error occurred";
      }
    } finally {
      isGenerating = false;
      abortController = null;
    }
  }

  function downloadImage(index: number = 0) {
    const img = generatedImages[index];
    if (!img) return;

    const link = document.createElement("a");
    link.href = img;
    link.download = `generated-image-${Date.now()}-${index}.png`;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
  }

  function downloadAll() {
    // ponytail: fires N sequential download clicks, no zip dep. Browser may
    // prompt for multiple downloads; fine for a placeholder batch.
    generatedImages.forEach((_, i) => downloadImage(i));
  }

  function openFullscreen(index: number = 0) {
    fullscreenIndex = index;
    showFullscreen = true;
  }

  function closeFullscreen(event?: MouseEvent) {
    if (event && event.target !== event.currentTarget) {
      return;
    }
    showFullscreen = false;
  }

  function handleKeyDown(event: KeyboardEvent) {
    // Batch mode needs newlines, so don't hijack Enter — use the button.
    if ($batchModeStore) return;
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      generate();
    }
  }

  let canGenerate = $derived(!!$selectedModelStore && (isImg2img ? !!sourceImage : !!prompt.trim()));
</script>

<div class="flex flex-col h-full">
  <!-- Empty state for no models configured -->
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <ImageIcon class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No models configured. Add models to your configuration to generate images.</p>
    </div>
  {:else}
    <!-- Main: gallery (left) + always-on settings sidebar (right) -->
    <div class="flex-1 flex gap-4 min-h-0 mb-4">
      <!-- Gallery -->
      <div class="flex-1 min-w-0 overflow-auto pretty-scroll bg-surface border border-card-border rounded-lg">
        {#if isGenerating && generatedImages.length === 0}
          <div class="h-full flex items-center justify-center">
            <div class="text-center text-txtsecondary w-full max-w-sm px-6">
              <div class="inline-block w-8 h-8 border-4 border-primary border-t-transparent rounded-full animate-spin mb-2"></div>
              {#if batchTotal > 1}
                <p class="font-medium text-txtmain">Asset {Math.min(batchDone + 1, batchTotal)}/{batchTotal}</p>
              {/if}
              {#if totalSteps > 0}
                <p>Step {step}/{totalSteps} · {elapsed}s elapsed{#if etaSec > 0} · ~{etaSec}s left{/if}</p>
                <div class="mt-2 h-2 w-full rounded bg-card-border overflow-hidden">
                  <div class="h-full bg-primary transition-all" style="width: {Math.round((step / totalSteps) * 100)}%"></div>
                </div>
                <p class="text-xs mt-1">{secPerIt.toFixed(1)}s/it</p>
              {:else}
                <p>Generating image... {elapsed}s</p>
              {/if}
            </div>
          </div>
        {:else if error}
          <div class="h-full flex items-center justify-center">
            <div class="text-center text-red-500 p-4">
              <p class="font-medium">Error</p>
              <p class="text-sm mt-1">{error}</p>
            </div>
          </div>
        {:else if generatedImages.length > 0}
          <!-- Consistent square tiles: object-cover keeps every result the same size. -->
          <div class="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-3 p-3">
            {#each generatedImages as img, i (i)}
              <div class="group relative aspect-square overflow-hidden rounded-lg border border-card-border bg-secondary">
                <button
                  class="absolute inset-0 w-full h-full cursor-pointer focus:outline-none focus:ring-2 focus:ring-primary"
                  onclick={() => openFullscreen(i)}
                  aria-label="View image {i + 1} fullscreen"
                >
                  <img
                    src={img}
                    alt="AI generated content {i + 1}"
                    class="w-full h-full object-cover group-hover:opacity-90 transition-opacity"
                  />
                </button>
                <!-- Hover actions: edit (img2img from this), upscale, download. -->
                <div class="absolute top-1 right-1 flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                  <button
                    class="inline-flex items-center justify-center p-1 rounded bg-black/60 text-white hover:bg-black/80 disabled:opacity-40"
                    onclick={() => useAsSource(img)}
                    disabled={isGenerating}
                    title="Edit — use as img2img source"
                  >
                    <Wand2 class="w-3.5 h-3.5" />
                  </button>
                  <button
                    class="inline-flex items-center justify-center p-1 rounded bg-black/60 text-white hover:bg-black/80 disabled:opacity-40"
                    onclick={() => upscaleImage(img)}
                    disabled={isGenerating}
                    title="Upscale 2× (diffusion)"
                  >
                    <Maximize2 class="w-3.5 h-3.5" />
                  </button>
                  <button
                    class="inline-flex items-center justify-center p-1 rounded bg-black/60 text-white hover:bg-black/80"
                    onclick={() => downloadImage(i)}
                    title="Download"
                  >
                    <Download class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>
            {/each}
            {#if isGenerating}
              <div class="aspect-square rounded-lg border border-card-border bg-secondary flex items-center justify-center">
                <div class="w-6 h-6 border-4 border-primary border-t-transparent rounded-full animate-spin"></div>
              </div>
            {/if}
          </div>
        {:else}
          <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
            <ImageIcon class="w-10 h-10 opacity-40" strokeWidth={1.5} />
            <p>Generated images appear here.</p>
          </div>
        {/if}
      </div>

      <!-- Settings sidebar (always visible) -->
      <aside class="w-72 shrink-0 overflow-y-auto pretty-scroll flex flex-col gap-3 p-3 rounded-lg border border-card-border bg-surface text-[0.8125rem]">
        <div class="flex flex-col gap-1">
          <span class="text-xs uppercase tracking-wide text-txtsecondary">Model</span>
          <ModelSelector bind:value={$selectedModelStore} placeholder="Select an image model..." disabled={isGenerating} capabilities={["image_generation", "image_to_image"]} matchAny={true} compact />
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div class="flex flex-col gap-1">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">API</span>
            {#if isImg2img}
              <div class="px-2.5 py-1.5 rounded-md border border-card-border bg-secondary text-txtsecondary">SDAPI (img2img)</div>
            {:else}
              <Select
                bind:value={$apiModeStore}
                disabled={isGenerating}
                compact
                options={[
                  { value: "openai", label: "OpenAI" },
                  { value: "sdapi", label: "SDAPI" },
                ]}
              />
            {/if}
          </div>
          <div class="flex flex-col gap-1">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Size</span>
            <Select bind:value={$selectedSizeStore} disabled={isGenerating} compact options={SIZE_OPTIONS} />
          </div>
        </div>

        {#if isSdapi}
          {#if isImg2img}
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Denoising strength · {$sdDenoiseStore.toFixed(2)}</span>
              <input type="range" min="0" max="1" step="0.05" class="w-full accent-primary" bind:value={$sdDenoiseStore} />
              <p class="text-xs text-txtsecondary">Low = stay close to the source; high = freer reinterpretation.</p>
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Upscale factor</span>
              <input type="number" min="1" max="8" step="0.5" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdUpscaleStore} />
              <p class="text-xs text-txtsecondary">Upscale target = source × factor (long edge capped at 4096).</p>
            </div>
            <div class="flex flex-col gap-1">
              <label class="flex items-center gap-2 cursor-pointer">
                <input type="checkbox" class="accent-primary" bind:checked={$sdRefEditStore} />
                <span class="text-xs uppercase tracking-wide text-txtsecondary">Reference edit (Kontext)</span>
              </label>
              <p class="text-xs text-txtsecondary">Keep the subject (face/identity), change pose or style per the prompt. Needs a Flux Kontext model.</p>
            </div>
          {/if}
          <div class="flex flex-col gap-1">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Style suffix</span>
            <input
              type="text"
              class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
              bind:value={$styleSuffixStore}
              placeholder="e.g. flat vector icon, muted palette"
            />
            <p class="text-xs text-txtsecondary">Appended to every prompt — keeps a batch consistent.</p>
          </div>

          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Steps</span>
              <input type="number" min="1" max="150" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdStepsStore} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">CFG Scale</span>
              <input type="number" min="1" max="30" step="0.5" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdCfgScaleStore} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Seed (-1 random)</span>
              <input type="number" min="-1" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdSeedStore} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Batch Size</span>
              <input type="number" min="1" max="8" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdBatchSizeStore} />
            </div>
          </div>

          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Seed mode</span>
              <Select bind:value={$seedModeStore} compact options={[
                { value: "random", label: "Random (varied)" },
                { value: "fixed", label: "Fixed (same seed)" },
                { value: "increment", label: "Increment (+index)" },
              ]} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Sampler</span>
              <Select bind:value={$sdSamplerStore} compact options={SAMPLER_OPTIONS} />
            </div>
          </div>

          <div class="flex flex-col gap-1">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Scheduler</span>
            <Select bind:value={$sdSchedulerStore} compact options={SCHEDULER_OPTIONS} />
          </div>

          <!-- Presets -->
          <div class="flex flex-col gap-1.5">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Style presets</span>
            <div class="flex gap-1.5">
              <input
                type="text"
                class="flex-1 min-w-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                bind:value={presetName}
                placeholder="Save current as…"
              />
              <button
                class="shrink-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
                onclick={savePreset}
                disabled={!presetName.trim()}
              >
                Save
              </button>
            </div>
            {#if $presetsStore.length > 0}
              <div class="flex flex-wrap gap-1.5">
                {#each $presetsStore as preset (preset.name)}
                  <span class="inline-flex items-center gap-1 px-2 py-1 rounded-md border border-card-border bg-surface">
                    <button class="hover:text-primary transition-colors" onclick={() => applyPreset(preset)} title="Apply preset">{preset.name}</button>
                    <button class="opacity-60 hover:opacity-100 hover:text-red-500 transition-colors" onclick={() => deletePreset(preset.name)} aria-label="Delete preset">×</button>
                  </span>
                {/each}
              </div>
            {/if}
          </div>

          <!-- LoRAs -->
          <div class="flex flex-col gap-1.5">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">LoRAs</span>
            <div class="flex gap-1.5">
              <button
                class="shrink-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
                onclick={loadLoras}
                disabled={!$selectedModelStore || isLoadingLoras}
              >
                {isLoadingLoras ? "Loading…" : lorasLoaded ? "Reload" : "Load LoRAs"}
              </button>
              {#if lorasLoaded && availableLoras.length > 0}
                <select
                  class="flex-1 min-w-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                  onchange={addLora}
                >
                  <option value="">Add a LoRA…</option>
                  {#each availableLoras.filter((l) => !selectedLoras.some((s) => s.path === l.path)) as lora}
                    <option value={lora.path}>{lora.name}</option>
                  {/each}
                </select>
              {/if}
            </div>
            {#if loraError}
              <p class="text-xs text-red-500">{loraError}</p>
            {/if}
            {#if lorasLoaded && availableLoras.length === 0}
              <p class="text-xs text-txtsecondary">No LoRAs available</p>
            {/if}
            {#if selectedLoras.length > 0}
              <div class="flex flex-col gap-1.5">
                {#each selectedLoras as lora (lora.path)}
                  <div class="flex items-center gap-2">
                    <span class="flex-1 truncate">{getLoraName(lora.path)}</span>
                    <input
                      type="number"
                      class="w-16 px-1.5 py-1 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                      value={lora.multiplier}
                      oninput={(e) => updateLoraMultiplier(lora.path, parseFloat((e.target as HTMLInputElement).value) || 1)}
                      min="0"
                      max="2"
                      step="0.1"
                    />
                    <button
                      class="inline-flex items-center justify-center p-1 rounded-md text-txtsecondary hover:text-red-500 hover:bg-secondary transition-colors"
                      onclick={() => removeLora(lora.path)}
                      aria-label="Remove LoRA"
                    >
                      <X class="w-4 h-4" />
                    </button>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      </aside>
    </div>

    <!-- Toolbar: mode + actions, sitting above the prompt so the icons read
         clearly instead of stacking as a cryptic column. -->
    <div class="shrink-0 flex items-center gap-2 mb-2">
      <!-- Mode: txt2img | img2img -->
      <div class="inline-flex rounded-md border border-card-border overflow-hidden text-sm font-mono">
        <button
          class="flex items-center gap-1.5 px-3 py-1.5 transition-colors {!isImg2img ? 'bg-primary/10 text-primary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
          onclick={() => ($modeStore = "txt2img")}
          disabled={isGenerating}
          title="Generate from a text prompt"
        >
          <Type class="w-4 h-4" /> Text → Image
        </button>
        <button
          class="flex items-center gap-1.5 px-3 py-1.5 transition-colors {isImg2img ? 'bg-primary/10 text-primary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
          onclick={() => ($modeStore = "img2img")}
          disabled={isGenerating}
          title="Transform a source image (SDAPI)"
        >
          <ImagePlus class="w-4 h-4" /> Image → Image
        </button>
      </div>

      <div class="ml-auto flex items-center gap-1.5">
        <button
          class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm font-mono transition-colors {$batchModeStore ? 'bg-secondary text-txtmain shadow-inner' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
          onclick={() => ($batchModeStore = !$batchModeStore)}
          disabled={isGenerating}
          title="Batch: one prompt per line becomes an asset"
        >
          <Layers class="w-4 h-4" /> Batch
        </button>
        {#if generatedImages.length > 1}
          <button
            class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm font-mono text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
            onclick={downloadAll}
            title="Download every generated image"
          >
            <Download class="w-4 h-4" /> All
          </button>
        {/if}
        <button
          class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm font-mono text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
          onclick={clearImage}
          disabled={isGenerating || (generatedImages.length === 0 && !error && !prompt.trim() && !sourceImage)}
          title="Clear prompt, source and results"
        >
          <Trash2 class="w-4 h-4" /> Clear
        </button>
        {#if isGenerating}
          <button
            class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm font-mono bg-secondary text-txtmain hover:bg-card-border transition-colors"
            onclick={cancelGeneration}
            title="Stop"
          >
            <Square class="w-4 h-4" fill="currentColor" /> Stop
          </button>
        {:else if isImg2img}
          <!-- Source loaded: edit (prompt-driven transform) or upscale it. -->
          <button
            class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm font-mono text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
            onclick={() => sourceImage && upscaleImage(sourceImage)}
            disabled={!sourceImage || !$selectedModelStore}
            title="Upscale the source {$sdUpscaleStore}× (diffusion)"
          >
            <Maximize2 class="w-4 h-4" /> Upscale {$sdUpscaleStore}×
          </button>
          <button
            class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm font-mono bg-primary text-white hover:opacity-90 transition-opacity disabled:opacity-40"
            onclick={generate}
            disabled={!canGenerate}
            title="Edit the source with the prompt (img2img)"
          >
            <Wand2 class="w-4 h-4" /> Edit
          </button>
        {:else}
          <button
            class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm font-mono bg-primary text-white hover:opacity-90 transition-opacity disabled:opacity-40"
            onclick={generate}
            disabled={!canGenerate}
            title="Generate"
          >
            <Send class="w-4 h-4" /> Generate
          </button>
        {/if}
      </div>
    </div>

    <!-- Composer: source (img2img) | positive | negative prompts -->
    <div class="shrink-0 flex gap-2">
      {#if isImg2img}
        <div class="shrink-0 flex flex-col gap-1">
          <span class="text-xs uppercase tracking-wide text-txtsecondary">Source</span>
          <label
            class="relative w-24 h-24 rounded-lg border border-dashed border-card-border bg-surface flex items-center justify-center cursor-pointer hover:border-primary overflow-hidden"
            title="Choose a source image"
          >
            {#if sourceImage}
              <img src={sourceImage} alt="img2img source" class="w-full h-full object-cover" />
            {:else}
              <ImagePlus class="w-6 h-6 text-txtsecondary" />
            {/if}
            <input type="file" accept="image/*" class="hidden" onchange={onSourceFile} disabled={isGenerating} />
          </label>
          {#if sourceImage}
            <button class="text-xs text-txtsecondary hover:text-red-500 transition-colors" onclick={() => (sourceImage = null)} disabled={isGenerating}>Remove</button>
          {/if}
        </div>
      {/if}

      <div class="flex-1 flex flex-col gap-1">
        <span class="text-xs uppercase tracking-wide text-txtsecondary">Prompt</span>
        <textarea
          class="w-full h-24 px-2.5 py-2 rounded-lg border border-card-border bg-surface text-[0.8125rem] leading-relaxed resize-none focus:outline-none focus:border-primary placeholder:text-txtsecondary"
          placeholder={$batchModeStore ? "One prompt per line — each becomes an asset…" : isImg2img ? "Describe the change (optional)…" : "Describe the image you want to generate…"}
          bind:value={prompt}
          onkeydown={handleKeyDown}
          disabled={isGenerating || !$selectedModelStore}
        ></textarea>
      </div>

      {#if isSdapi}
        <div class="flex-1 flex flex-col gap-1">
          <span class="text-xs uppercase tracking-wide text-txtsecondary">Negative prompt</span>
          <textarea
            class="w-full h-24 px-2.5 py-2 rounded-lg border border-card-border bg-surface text-[0.8125rem] leading-relaxed resize-none focus:outline-none focus:border-primary placeholder:text-txtsecondary"
            placeholder="Elements to avoid…"
            bind:value={$sdNegativePromptStore}
            disabled={isGenerating || !$selectedModelStore}
          ></textarea>
        </div>
      {/if}
    </div>
  {/if}
</div>

<!-- Fullscreen dialog -->
{#if showFullscreen && generatedImages[fullscreenIndex]}
  <div
    class="fixed inset-0 bg-black/90 z-50 flex items-center justify-center p-4"
    onclick={(e) => closeFullscreen(e)}
    onkeydown={(e) => e.key === 'Escape' && closeFullscreen()}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
  >
    <div class="absolute top-4 right-4 flex items-center gap-2">
      <button
        class="text-white hover:text-gray-300 w-10 h-10 flex items-center justify-center rounded-full hover:bg-white/10 transition-colors"
        onclick={() => downloadImage(fullscreenIndex)}
        aria-label="Download image"
        title="Download"
      >
        <Download class="w-5 h-5" />
      </button>
      <button
        class="text-white hover:text-gray-300 text-2xl w-10 h-10 flex items-center justify-center rounded-full hover:bg-white/10 transition-colors"
        onclick={() => closeFullscreen()}
        aria-label="Close fullscreen"
      >
        ×
      </button>
    </div>
    <img
      src={generatedImages[fullscreenIndex]}
      alt="AI generated content"
      class="max-w-full max-h-full object-contain pointer-events-none"
    />
  </div>
{/if}
