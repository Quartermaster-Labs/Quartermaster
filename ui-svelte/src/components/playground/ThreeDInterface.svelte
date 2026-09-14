<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { get } from "svelte/store";
  import { models } from "../../stores/api";
  import { userPref } from "../../stores/prefs";
  import { selectedTabStore } from "../../stores/playground";
  import {
    threeDSessions,
    activeThreeDChatId,
    generatingThreeDChatId,
    newThreeDChatId,
    deriveThreeDTitle,
    type ThreeDSession,
    type Turn,
  } from "../../stores/threeDHistory";
  import { generateMesh, fmtBytes } from "../../lib/threeDApi";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import Select from "./Select.svelte";
  import Composer from "./Composer.svelte";
  import GlbViewer from "./GlbViewer.svelte";
  import { dropZone } from "../../lib/dropZone";
  import { scrollFade } from "../../lib/scrollFade";
  import { Box, X, Download, Plus, RefreshCw, Sparkles, Maximize2, ImagePlus, Send } from "lucide-svelte";
  import {
    THREED_DEFAULTS,
    TEXTURE_SIZE_OPTIONS,
    PIPELINE_OPTIONS,
    estimateLabel,
    fmtDur,
  } from "./threeDGen";

  // The 3D tab: TRELLIS.2 image-to-mesh, in the thread shape the Images and
  // Video tabs use. A close sibling of VideoInterface.svelte on purpose (same
  // store helpers, same bubbles, same composer chrome).
  //
  // What is genuinely different, and why:
  //
  //   - THERE IS NO PROMPT. The whole request is one image, so the composer
  //     drops its textarea (Composer's `hideTextarea`) and grows an explicit
  //     Generate button: with no text field there is no Enter to send on.
  //     A thread's title is therefore never derived, only defaulted or renamed.
  //   - THE REQUEST IS SYNCHRONOUS and has no cancel route (see lib/threeDApi).
  //     Stop abandons the RESPONSE; the backend keeps rendering to completion
  //     and stays busy, which the stop tooltip says outright rather than
  //     implying the GPU came back.
  //   - THE BACKEND IS SINGLE-THREADED, so one generation at a time is not a UI
  //     nicety: a second request would sit behind the first inside the backend
  //     with no queue document to show for it.

  const selectedModelStore = userPref<string>("playground-3d-model", "");
  const stepsStore = userPref<number>("playground-3d-steps", THREED_DEFAULTS.steps);
  const textureSizeStore = userPref<string>("playground-3d-texture", String(THREED_DEFAULTS.textureSize));
  const pipelineStore = userPref<string>("playground-3d-pipeline", String(THREED_DEFAULTS.pipeline));
  const shapeOnlyStore = userPref<boolean>("playground-3d-shape-only", THREED_DEFAULTS.shapeOnly);
  const seedStore = userPref<number>("playground-3d-seed", THREED_DEFAULTS.seed);

  function initThreeDChats() {
    const sessions = get(threeDSessions);
    let id = get(activeThreeDChatId);
    if (!sessions.some((s) => s.id === id)) {
      const recent = sessions.reduce<ThreeDSession | null>(
        (best, s) => (!best || s.updatedAt > best.updatedAt ? s : best),
        null,
      );
      id = recent ? recent.id : "";
      if (!id) {
        const s: ThreeDSession = { id: newThreeDChatId(), title: "New mesh", turns: [], updatedAt: Date.now() };
        threeDSessions.set([s]);
        id = s.id;
      }
      activeThreeDChatId.set(id);
    }
  }
  initThreeDChats();

  let activeSession = $derived($threeDSessions.find((s) => s.id === $activeThreeDChatId));
  let turns = $derived(activeSession?.turns ?? []);

  let genId = $state<string | null>(null);
  let isGenerating = $derived(genId !== null);
  $effect(() => {
    generatingThreeDChatId.set(genId);
  });
  $effect(() => {
    playgroundStores.threeDGenerating.set(isGenerating);
  });

  // --- store helpers: turns live in threeDSessions, keyed by session id ---
  function sessionById(id: string): ThreeDSession | undefined {
    return get(threeDSessions).find((s) => s.id === id);
  }
  function patchSession(id: string, fields: Partial<ThreeDSession>, bump = false) {
    threeDSessions.update((ss) => {
      const i = ss.findIndex((s) => s.id === id);
      if (i === -1) return ss; // session deleted - don't resurrect it
      const copy = [...ss];
      copy[i] = { ...copy[i], ...fields, ...(bump ? { updatedAt: Date.now() } : {}) };
      return copy;
    });
  }
  function appendTurn(id: string, turn: Turn) {
    const s = sessionById(id);
    if (!s) return;
    const nextTurns = [...s.turns, turn];
    const title = s.titled ? s.title : deriveThreeDTitle(nextTurns);
    patchSession(id, { turns: nextTurns, title }, true);
  }
  function updateTurn(id: string, ti: number, patch: Partial<Turn>) {
    const s = sessionById(id);
    if (!s) return;
    patchSession(id, { turns: s.turns.map((t, i) => (i === ti ? { ...t, ...patch } : t)) });
  }
  function setTurns(id: string, next: Turn[], bump = false) {
    const s = sessionById(id);
    if (!s) return;
    const title = s.titled ? s.title : deriveThreeDTitle(next);
    patchSession(id, { turns: next, title }, bump);
  }

  let abortController = $state<AbortController | null>(null);
  let showSettings = $state(false);
  let elapsed = $state(0);
  let threadEl = $state<HTMLDivElement | undefined>();
  let fullscreenMesh = $state<string | null>(null);
  let dropActive = $state(false);

  // The image waiting to be sent, as a data: URL. One at a time: the route takes
  // a single image as its whole body, so a multi-attachment chip row would be
  // offering something that cannot be sent.
  let pending = $state<string | null>(null);
  let pendingName = $state("");
  let pickError = $state("");
  let fileInput = $state<HTMLInputElement | null>(null);

  let hasModels = $derived($models.some((m) => !m.unlisted));
  let canSend = $derived(!!$selectedModelStore && !!pending && !isGenerating);

  // Elapsed tick. Load-bearing, not decoration: there is no progress field and
  // no job document, so on a generation that runs for a minute and a half this
  // counter plus the estimate are the only signs anything is alive.
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

  $effect(() => {
    void turns.length;
    void isGenerating;
    void elapsed;
    if (threadEl) threadEl.scrollTop = threadEl.scrollHeight;
  });

  let estLabel = $derived(estimateLabel(Number($stepsStore) || THREED_DEFAULTS.steps, Number($pipelineStore), $shapeOnlyStore));

  // --- picking the source image ---
  function acceptFile(file: File) {
    if (!file.type.startsWith("image/")) {
      pickError = `${file.name || "That file"} is not an image.`;
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      pending = reader.result as string;
      pendingName = file.name || "image";
      pickError = "";
    };
    reader.onerror = () => (pickError = "Could not read that image.");
    reader.readAsDataURL(file);
  }

  function pickImage(event: Event) {
    const input = event.target as HTMLInputElement;
    const file = input.files?.[0];
    if (file) acceptFile(file);
    input.value = "";
  }

  // Only the FIRST image of a multi-file drop is taken, and the rest are named
  // rather than silently dropped: the route has room for exactly one.
  function handleDrop(files: File[], skipped: string[]) {
    const imgs = files.filter((f) => f.type.startsWith("image/"));
    if (!imgs.length) {
      pickError = skipped.length ? `${skipped.join(", ")} is a folder.` : "Drop an image file.";
      return;
    }
    acceptFile(imgs[0]);
    if (imgs.length > 1) pickError = `Using ${imgs[0].name} - this backend takes one image per mesh.`;
  }

  function handlePaste(e: ClipboardEvent) {
    const item = Array.from(e.clipboardData?.items ?? []).find((i) => i.type.startsWith("image/"));
    const file = item?.getAsFile();
    if (file) {
      e.preventDefault();
      acceptFile(file);
    }
  }

  // --- generating ---
  async function runTurn(id: string, ti: number, image: string, onAbort: () => void, prevTurns: Turn[]) {
    genId = id;
    abortController = new AbortController();
    try {
      const { src, bytes } = await generateMesh(
        image,
        {
          model: $selectedModelStore,
          steps: Number($stepsStore) || undefined,
          textureSize: Number($textureSizeStore) || undefined,
          pipeline: Number($pipelineStore) || undefined,
          shapeOnly: $shapeOnlyStore,
          seed: $seedStore,
        },
        abortController.signal,
      );
      updateTurn(id, ti, { meshes: [src], secs: elapsed, bytes });
    } catch (err) {
      if (err instanceof Error && err.name === "AbortError") {
        patchSession(id, { turns: prevTurns });
        onAbort();
      } else {
        updateTurn(id, ti, { error: err instanceof Error ? err.message : "An error occurred" });
      }
    } finally {
      genId = null;
      abortController = null;
    }
  }

  async function send() {
    if (!canSend) return;
    const id = $activeThreeDChatId;
    if (!sessionById(id)) return;
    const image = pending!;
    const name = pendingName;
    // The image is CONSUMED by the send, like the other tabs' prompt text: it
    // moves out of the composer and into the turn that used it, so the next
    // generation cannot silently reuse the previous subject.
    pending = null;
    pendingName = "";
    pickError = "";
    const prevTurns = sessionById(id)!.turns;
    const ti = prevTurns.length;
    appendTurn(id, { image, meshes: [], model: $selectedModelStore });
    await runTurn(id, ti, image, () => {
      // Put it back if the send was abandoned, so an aborted generation does
      // not cost the user their pick.
      pending = image;
      pendingName = name;
    }, prevTurns);
  }

  async function regenerate(idx: number) {
    if (isGenerating || !$selectedModelStore) return;
    const id = $activeThreeDChatId;
    const s = sessionById(id);
    const t = s?.turns[idx];
    if (!s || !t) return;
    const prevTurns = s.turns;
    setTurns(id, [...prevTurns.slice(0, idx), { image: t.image, meshes: [], model: $selectedModelStore }], true);
    await runTurn(id, idx, t.image, () => {}, prevTurns);
  }

  // Stop drops the response only. The backend has no cancel route and is
  // single-threaded, so it keeps rendering and stays busy until it is done:
  // saying otherwise would have the user start a second generation into a
  // process that cannot take it.
  function cancelGeneration() {
    abortController?.abort();
  }

  function newThread() {
    if (isGenerating) return;
    const cur = sessionById($activeThreeDChatId);
    if (cur && cur.turns.length === 0) return; // already on a blank thread
    const s: ThreeDSession = { id: newThreeDChatId(), title: "New mesh", turns: [], updatedAt: Date.now() };
    threeDSessions.update((ss) => [s, ...ss]);
    activeThreeDChatId.set(s.id);
  }

  function downloadMesh(src: string, ti: number) {
    const link = document.createElement("a");
    link.href = src;
    link.download = `mesh-${ti + 1}-${Date.now()}.glb`;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
  }

  // Paste anywhere in the pane, not only in a field: with no textarea there is
  // nothing for a paste to land in otherwise.
  $effect(() => {
    if ($selectedTabStore !== "3d") return;
    const on = (e: ClipboardEvent) => handlePaste(e);
    window.addEventListener("paste", on);
    return () => window.removeEventListener("paste", on);
  });
</script>

<div
  class="relative flex flex-col h-full"
  use:dropZone={{ onFiles: handleDrop, onActive: (v) => (dropActive = v), enabled: hasModels && !isGenerating }}
>
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <Box class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No models configured. Add models to your configuration to generate meshes.</p>
    </div>
  {:else}
    <div class="flex-1 flex flex-col min-w-0 min-h-0 w-full">
      <!-- Thread -->
      <div bind:this={threadEl} class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b mb-2" use:scrollFade>
        <div class="w-full max-w-3xl mx-auto px-2 pt-4 flex flex-col gap-4 pb-2 {turns.length === 0 && !isGenerating ? 'h-full' : ''}">
          {#if turns.length === 0 && !isGenerating}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary text-center px-6">
              <Box class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Drop or pick an image to turn it into a 3D mesh.</p>
              <p class="text-xs max-w-sm">
                Works best on a single, well-lit subject filling the frame against a plain background.
                A cluttered photo comes back as a flat shell rather than a solid.
              </p>
            </div>
          {/if}

          {#each turns as t, ti (ti)}
            <!-- Source image (right). The 3D tab's "message" IS the picture, so
                 it fills the bubble the other tabs give to prompt text. -->
            <div class="flex justify-end">
              <div class="relative max-w-[85%] rounded-2xl rounded-br-none bg-[#141414] p-1.5">
                <img src={t.image} alt="source" class="max-h-40 w-auto rounded-xl object-contain" />
              </div>
            </div>
            <!-- Mesh reply (left). -->
            <div class="flex flex-col items-start">
              {#if t.model}
                <span class="flex items-center gap-1 mb-1 px-3 text-[0.6875rem] font-medium text-txtsecondary">
                  <Sparkles class="w-3 h-3 shrink-0" />{t.model}
                </span>
              {/if}
              <div class="relative group rounded-2xl rounded-bl-sm px-3 py-2 text-[0.8125rem] w-fit max-w-full sm:max-w-[60%]">
                {#if t.error}
                  <div class="text-red-500">{t.error}</div>
                {:else if t.meshes.length}
                  <div class="w-64 sm:w-72">
                    <GlbViewer src={t.meshes[0]} />
                  </div>
                  <div class="flex flex-wrap items-center gap-1 mt-2 pt-1 border-t border-card-border">
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary disabled:opacity-40"
                      onclick={() => regenerate(ti)}
                      disabled={isGenerating}
                      use:tip={"Regenerate"}
                    >
                      <RefreshCw class="w-4 h-4" />
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
                      onclick={() => downloadMesh(t.meshes[0], ti)}
                      use:tip={"Download GLB"}
                    >
                      <Download class="w-4 h-4" />
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
                      onclick={() => (fullscreenMesh = t.meshes[0])}
                      use:tip={"View large"}
                    >
                      <Maximize2 class="w-4 h-4" />
                    </button>
                    {#if t.bytes}
                      <span class="flex items-center self-center text-[0.6875rem] text-txtsecondary tabular-nums">{fmtBytes(t.bytes)}</span>
                    {/if}
                    {#if t.secs != null}
                      <span class="ml-auto flex items-center self-center text-[0.6875rem] text-txtsecondary tabular-nums">{fmtDur(t.secs)}</span>
                    {/if}
                  </div>
                {:else if genId !== $activeThreeDChatId || ti !== turns.length - 1}
                  <div class="text-red-500">No mesh returned.</div>
                {:else}
                  <!-- In-flight. No progress field exists on this route, so the
                       readout is the elapsed counter against a rough estimate
                       and nothing finer. A percentage here would be invented. -->
                  <div class="flex flex-col gap-1.5 min-w-52">
                    <div class="flex items-center gap-2 text-txtsecondary">
                      <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                      <span class="reason-shimmer-white font-medium">Building mesh…</span>
                    </div>
                    <div class="flex items-center justify-between text-[0.6875rem] text-txtsecondary tabular-nums mt-1 pt-1 border-t border-card-border">
                      <span>usually {estLabel}</span>
                      <span>{fmtDur(elapsed)}</span>
                    </div>
                  </div>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </div>

      <!-- Composer -->
      {#snippet threeDSettingsPanel()}
        <div class="flex flex-col gap-2">
          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Steps</span>
              <input type="number" min="1" max="100" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$stepsStore} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Seed</span>
              <input type="number" min="-1" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$seedStore} />
            </div>
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                Profile
                <span class="cursor-help opacity-60" use:tip={"The coordinate resolution the model works at. 1024 is the higher-quality profile and is not safe on every GPU: it can exhaust VRAM or fail the texture bake."}>(?)</span>
              </span>
              <Select bind:value={$pipelineStore} disabled={isGenerating} compact options={PIPELINE_OPTIONS} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Texture</span>
              <Select bind:value={$textureSizeStore} disabled={isGenerating || $shapeOnlyStore} compact options={TEXTURE_SIZE_OPTIONS} />
            </div>
          </div>
          <label class="flex items-center gap-2 pt-1">
            <input type="checkbox" class="accent-primary" bind:checked={$shapeOnlyStore} disabled={isGenerating} />
            <span class="text-xs">Shape only</span>
            <span class="cursor-help opacity-60 text-xs" use:tip={"Geometry with no texture bake. Much faster, and the resulting GLB is a fraction of the size."}>(?)</span>
          </label>
          <p class="text-xs text-txtsecondary">
            Backend default · {THREED_DEFAULTS.steps} steps · {THREED_DEFAULTS.pipeline} profile · {THREED_DEFAULTS.textureSize} texture
          </p>
        </div>
      {/snippet}

      {#snippet threeDTopExtra()}
        <!-- The pending image lives at the TOP of the composer, where the other
             tabs put their prompt: it is the message, not an attachment to one. -->
        <div class="flex items-center gap-3 pb-2 {pending ? 'border-b border-card-border' : ''}">
          {#if pending}
            <div class="group relative w-16 h-16 shrink-0 rounded-lg overflow-hidden border border-card-border bg-secondary">
              <img src={pending} alt="Source" class="w-full h-full object-cover" />
              <button
                class="absolute top-0 right-0 w-5 h-5 flex items-center justify-center bg-black/60 text-white rounded-bl opacity-0 group-hover:opacity-100 transition-opacity"
                onclick={() => { pending = null; pendingName = ""; }}
                aria-label="Remove image"
              ><X class="w-3 h-3" /></button>
            </div>
            <div class="min-w-0 flex flex-col gap-0.5">
              <span class="text-[0.8125rem] truncate">{pendingName}</span>
              <span class="text-xs text-txtsecondary">Takes {estLabel}{$shapeOnlyStore ? " · shape only" : ""}</span>
            </div>
          {:else}
            <button
              class="flex items-center gap-2 text-[0.8125rem] text-txtsecondary hover:text-txtmain transition-colors"
              onclick={() => fileInput?.click()}
              disabled={isGenerating}
            >
              <ImagePlus class="w-4 h-4" />
              Pick an image, or drop or paste one
            </button>
          {/if}
        </div>
      {/snippet}

      {#snippet threeDLeftButtons()}
        <button
          class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors disabled:opacity-40 {pending ? 'text-primary bg-secondary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
          onclick={() => fileInput?.click()}
          disabled={isGenerating}
          use:tip={"Choose the image to turn into a mesh"}
        >
          <ImagePlus class="w-[1.125rem] h-[1.125rem]" />
        </button>
      {/snippet}

      {#snippet threeDExtraRightButtons()}
        <button
          class="composer-icon-btn"
          onclick={newThread}
          disabled={isGenerating || turns.length === 0}
          use:tip={"New thread"}
        >
          <Plus class="w-[1.125rem] h-[1.125rem]" />
        </button>
        <!-- The tab has no textarea, so there is no Enter to send on: this
             button is the ONLY way to start a generation. -->
        <button
          class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors disabled:opacity-40 {canSend ? 'text-primary hover:bg-secondary' : 'text-txtsecondary'}"
          onclick={send}
          disabled={!canSend}
          use:tip={!$selectedModelStore ? "Select a 3D model first" : !pending ? "Pick an image first" : "Generate the mesh"}
          aria-label="Generate mesh"
        >
          <Send class="w-[1.125rem] h-[1.125rem]" />
        </button>
      {/snippet}

      <div class="shrink-0 relative w-full max-w-2xl mx-auto">
        {#if pickError}
          <p class="text-xs text-red-500 mb-2 px-2">{pickError}</p>
        {/if}
        <input type="file" accept="image/*" class="hidden" bind:this={fileInput} onchange={pickImage} />
        <Composer
          hideTextarea
          bind:modelValue={$selectedModelStore}
          modelPlaceholder="Select a 3D model..."
          category="3d"
          busy={isGenerating}
          onStop={cancelGeneration}
          stopTitle="Stop waiting for this mesh. The backend has no cancel route: it keeps rendering and stays busy until it finishes."
          bind:showSettings
          settingsTitle="Settings"
          topExtra={threeDTopExtra}
          leftButtons={threeDLeftButtons}
          extraRightButtons={threeDExtraRightButtons}
          settingsPanel={threeDSettingsPanel}
        />
      </div>
    </div>
  {/if}

  {#if dropActive}
    <div class="absolute inset-0 z-20 flex items-center justify-center bg-surface/80 border-2 border-dashed border-primary rounded-lg pointer-events-none">
      <span class="text-sm font-medium text-primary">Drop an image to turn it into a mesh</span>
    </div>
  {/if}
</div>

<!-- Fullscreen viewer. A second GlbViewer rather than a moved one: the thread's
     copy keeps its own camera, so closing this returns to the view you left. -->
{#if fullscreenMesh}
  <div
    class="fixed inset-0 bg-black/90 z-50 flex items-center justify-center p-4"
    onclick={() => (fullscreenMesh = null)}
    onkeydown={(e) => e.key === "Escape" && (fullscreenMesh = null)}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
  >
    <button class="absolute top-4 right-4 text-white hover:text-gray-300 w-10 h-10 flex items-center justify-center rounded-full hover:bg-white/10 transition-colors z-10" onclick={() => (fullscreenMesh = null)} aria-label="Close">
      <X class="w-6 h-6" />
    </button>
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <div class="w-full h-full max-w-4xl max-h-[85vh]" onclick={(e) => e.stopPropagation()}>
      <div class="w-full h-full [&>div]:h-full [&>div]:max-h-none [&>div]:aspect-auto">
        <GlbViewer src={fullscreenMesh} />
      </div>
    </div>
  </div>
{/if}
