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
  import Toggle from "../Toggle.svelte";
  import ModelSelector from "./ModelSelector.svelte";
  import PaneHeader from "./PaneHeader.svelte";
  import GlbViewer from "./GlbViewer.svelte";
  import { dropZone } from "../../lib/dropZone";
  import { Box, X, Download, RefreshCw, Sparkles, ImagePlus, Loader2, HelpCircle, Dices, Square, Expand } from "lucide-svelte";
  import {
    THREED_DEFAULTS,
    TEXTURE_SIZE_OPTIONS,
    PIPELINE_OPTIONS,
    estimateLabel,
    fmtDur,
  } from "./threeDGen";

  // The 3D tab: TRELLIS.2 image-to-mesh, in the thread shape the Images and
  // Video tabs use. A close sibling of VideoInterface.svelte on purpose (same
  // store helpers, same params panel + canvas + thread strip).
  //
  // What is genuinely different, and why:
  //
  //   - THERE IS NO PROMPT. The whole request is one image, so the image
  //     picker takes the prompt box's slot and Generate is the only way to
  //     send: with no text field there is no Enter to send on.
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
  let elapsed = $state(0);
  // The thumbnail strip under the canvas (the thread, oldest first).
  let threadEl = $state<HTMLDivElement | undefined>();
  // Which turn the canvas shows. null = follow the newest; clicking the strip
  // pins an older one until the next send, regenerate or thread switch.
  let selTurn = $state<number | null>(null);
  let sel = $derived(selTurn !== null && selTurn < turns.length ? selTurn : turns.length - 1);
  let cur = $derived(turns[sel] as Turn | undefined);
  $effect(() => {
    void turns.length;
    void $activeThreeDChatId;
    selTurn = null;
  });
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

  // Keep the newest mesh in view on the strip as the thread grows.
  $effect(() => {
    void turns.length;
    if (threadEl) threadEl.scrollLeft = threadEl.scrollWidth;
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

{#snippet hint(text: string)}
  <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary normal-case tracking-normal" use:tip={text}>
    <HelpCircle class="w-3.5 h-3.5" />
  </span>
{/snippet}

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
    <PaneHeader
      title={activeSession?.title || "New mesh"}
      meta={`${turns.length} mesh${turns.length === 1 ? "" : "es"}`}
      updatedAt={activeSession?.updatedAt}
      newLabel="New thread"
      onNew={newThread}
    />

    <div class="flex-1 min-h-0 flex">
      <!-- Params. There is no prompt: the source image IS the request, so it
           takes the slot the other tabs give their prompt box. -->
      <aside class="w-[25rem] shrink-0 flex flex-col min-h-0 bg-rail border-r border-card-border-inner">
        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll">
          <div class="px-4 py-3.5 border-b border-card-border-inner flex flex-col gap-2.5">
            <div class="rounded-xl border border-composer-border bg-surface hover:ring-1 hover:ring-composer-ring transition-colors flex flex-col">
              {#if pending}
                <div class="group relative m-2 mb-0 h-[8.5rem] rounded-lg overflow-hidden bg-secondary flex items-center justify-center">
                  <img src={pending} alt="Source" class="max-h-full max-w-full object-contain" />
                  <button
                    class="absolute top-1.5 right-1.5 w-6 h-6 flex items-center justify-center rounded-full bg-black/60 text-white opacity-0 group-hover:opacity-100 transition-opacity"
                    onclick={() => { pending = null; pendingName = ""; }}
                    aria-label="Remove image"
                  ><X class="w-3.5 h-3.5" /></button>
                </div>
              {:else}
                <button
                  class="m-2 mb-0 h-[8.5rem] rounded-lg border border-dashed border-card-border flex flex-col items-center justify-center gap-2 text-[0.8125rem] text-txtsecondary hover:text-txtmain hover:border-txtsecondary transition-colors"
                  onclick={() => fileInput?.click()}
                  disabled={isGenerating}
                >
                  <ImagePlus class="w-5 h-5" />
                  Pick an image, or drop or paste one
                </button>
              {/if}
              <div class="flex items-center gap-1 px-2 py-2 min-w-0">
                {#if pending}
                  <span class="min-w-0 truncate px-1 text-xs text-txtsecondary" use:tip={pendingName}>{pendingName}</span>
                {/if}
                <div class="ml-auto min-w-0 max-w-[60%]">
                  <ModelSelector bind:value={$selectedModelStore} placeholder="Select a 3D model…" category="3d" ghost />
                </div>
              </div>
            </div>
            {#if pickError}
              <div class="p-2 bg-error/10 text-error rounded text-sm">{pickError}</div>
            {/if}
            <p class="text-xs text-txtsecondary">
              Works best on a single, well-lit subject filling the frame against a plain background. A cluttered photo comes back as a flat shell rather than a solid.
            </p>
            <input type="file" accept="image/*" class="hidden" bind:this={fileInput} onchange={pickImage} />
          </div>

          <div class="px-4 py-3.5 border-b border-card-border-inner flex flex-col gap-3">
            <div class="flex flex-col gap-2">
              <span class="flex items-center gap-1.5 text-micro font-medium uppercase tracking-wide text-txtsecondary">
                Profile {@render hint("The coordinate resolution the model works at. 1024 is the higher-quality profile and is not safe on every GPU: it can exhaust VRAM or fail the texture bake.")}
              </span>
              <div class="seg w-full" role="group" aria-label="Profile">
                {#each PIPELINE_OPTIONS as o (o.value)}
                  <button
                    class="flex-1 font-mono !normal-case !tracking-normal {o.warn ? '!text-orange-400' : ''}"
                    aria-pressed={$pipelineStore === o.value}
                    disabled={isGenerating}
                    onclick={() => ($pipelineStore = o.value)}
                    use:tip={o.title ?? ""}
                  >{o.label}</button>
                {/each}
              </div>
            </div>
            <div class="flex flex-col gap-2">
              <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">Texture</span>
              <div class="seg w-full" role="group" aria-label="Texture size">
                {#each TEXTURE_SIZE_OPTIONS as o (o.value)}
                  <button
                    class="flex-1 font-mono !normal-case !tracking-normal disabled:opacity-40 disabled:pointer-events-none"
                    aria-pressed={$textureSizeStore === o.value}
                    disabled={isGenerating || $shapeOnlyStore}
                    onclick={() => ($textureSizeStore = o.value)}
                  >{o.label}</button>
                {/each}
              </div>
            </div>
            <label class="flex items-center gap-2 cursor-pointer">
              <span class="flex items-center gap-1.5 text-micro font-medium uppercase tracking-wide text-txtsecondary">
                Shape only {@render hint("Geometry with no texture bake. Much faster, and the resulting GLB is a fraction of the size.")}
              </span>
              <span class="ml-auto"><Toggle size="sm" bind:checked={$shapeOnlyStore} /></span>
            </label>
          </div>

          <div class="px-4 py-3.5 border-b border-card-border-inner flex flex-col gap-2.5">
            <div class="grid grid-cols-2 gap-x-3 gap-y-2.5">
              <label class="flex flex-col gap-1.5">
                <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">Steps</span>
                <input type="number" min="1" max="100" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface font-mono text-xs tabular-nums focus:outline-none focus:border-primary" bind:value={$stepsStore} />
              </label>
              <div class="flex flex-col gap-1.5">
                <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">Seed</span>
                <div class="flex items-stretch gap-1.5">
                  <input type="number" min="-1" aria-label="Seed" class="w-full min-w-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface font-mono text-xs tabular-nums focus:outline-none focus:border-primary" bind:value={$seedStore} />
                  <button class="btn btn--sm btn--icon shrink-0" aria-pressed={$seedStore === -1} onclick={() => ($seedStore = -1)} use:tip={"Random seed each run (-1)"} aria-label="Random seed">
                    <Dices class="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>
            </div>
            <p class="font-mono text-micro text-txtsecondary tabular-nums">
              Backend default · {THREED_DEFAULTS.steps} steps · {THREED_DEFAULTS.pipeline} profile · {THREED_DEFAULTS.textureSize} texture
            </p>
          </div>
        </div>

        <div class="shrink-0 px-4 py-3 flex flex-col gap-2 border-t border-card-border-inner">
          <div class="flex items-center gap-2">
            <button
              class="btn btn--primary flex-1 inline-flex items-center justify-center gap-2 h-9"
              onclick={send}
              disabled={!canSend}
              use:tip={!$selectedModelStore ? "Select a 3D model first" : !pending ? "Pick an image first" : ""}
            >
              <Sparkles class="w-4 h-4" /> Generate
            </button>
            {#if isGenerating}
              <button
                class="btn btn--danger-outline inline-flex items-center gap-1.5 h-9"
                onclick={cancelGeneration}
                use:tip={"Stop waiting for this mesh. The backend has no cancel route: it keeps rendering and stays busy until it finishes."}
              >
                <Square class="w-3.5 h-3.5" /> Stop
              </button>
            {/if}
          </div>
          <div class="font-mono text-micro text-txtsecondary tabular-nums">
            Takes {estLabel}{$shapeOnlyStore ? " · shape only" : ""}
          </div>
        </div>
      </aside>

      <!-- Canvas: one mesh at a time, big. The strip underneath is the thread. -->
      <section class="flex-1 min-w-0 flex flex-col min-h-0">
        {#if turns.length === 0 && !isGenerating}
          <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
            <Box class="w-10 h-10 opacity-40" strokeWidth={1.5} />
            <p>Drop or pick an image to turn it into a 3D mesh.</p>
          </div>
        {:else if cur}
          {@const t = cur}
          {@const ti = sel}
          {@const inFlight = !t.meshes.length && !t.error && genId === $activeThreeDChatId && ti === turns.length - 1}
          <div class="shrink-0 flex items-center gap-2 px-6 h-10 min-w-0">
            <span class="font-mono text-micro text-txtsecondary tabular-nums">{ti + 1}/{turns.length}</span>
            {#if t.model}
              <span class="flex items-center gap-1 min-w-0 text-micro font-medium text-txtsecondary">
                <Sparkles class="w-3 h-3 shrink-0" /><span class="truncate">{t.model}</span>
              </span>
            {/if}
            {#if t.bytes}
              <span class="font-mono text-micro text-txtsecondary tabular-nums">· {fmtBytes(t.bytes)}</span>
            {/if}
            {#if t.secs != null}
              <span class="font-mono text-micro text-txtsecondary tabular-nums">· {fmtDur(t.secs)}</span>
            {/if}
            {#if t.meshes.length}
              <div class="ml-auto flex items-center gap-0.5 shrink-0">
                <button class="icon-btn" onclick={() => regenerate(ti)} disabled={isGenerating} use:tip={"Regenerate"} aria-label="Regenerate">
                  <RefreshCw class="w-4 h-4" />
                </button>
                <button class="icon-btn" onclick={() => downloadMesh(t.meshes[0], ti)} use:tip={"Download GLB"} aria-label="Download GLB">
                  <Download class="w-4 h-4" />
                </button>
                <button class="icon-btn" onclick={() => (fullscreenMesh = t.meshes[0])} use:tip={"Fullscreen"} aria-label="Fullscreen">
                  <Expand class="w-4 h-4" />
                </button>
              </div>
            {/if}
          </div>

          <div class="flex-1 min-h-0 flex items-center justify-center px-6 pb-3">
            {#if t.error}
              <div class="max-w-lg p-3 rounded-lg bg-error/10 text-error text-sm">{t.error}</div>
            {:else if t.meshes.length}
              <!-- GlbViewer is sized for a bubble (square, capped); here it
                   fills the canvas instead. Keyed so switching turns gives each
                   mesh a fresh camera rather than inheriting the last one's. -->
              <div class="w-full h-full [&>div]:h-full [&>div]:max-h-none [&>div]:aspect-auto">
                {#key t.meshes[0]}
                  <GlbViewer src={t.meshes[0]} />
                {/key}
              </div>
            {:else if inFlight}
              <!-- No progress field exists on this route, so the readout is the
                   elapsed counter against a rough estimate and nothing finer. A
                   percentage here would be invented. -->
              <div class="w-72 flex flex-col gap-2 rounded-xl border border-card-border bg-surface px-4 py-3.5">
                <div class="flex items-center gap-2 text-sm text-txtsecondary">
                  <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                  <span class="reason-shimmer-white font-medium">Building mesh…</span>
                </div>
                <div class="flex items-center justify-between font-mono text-micro text-txtsecondary tabular-nums pt-1.5 border-t border-card-border-inner">
                  <span>usually {estLabel}</span>
                  <span>{fmtDur(elapsed)}</span>
                </div>
              </div>
            {:else}
              <div class="text-sm text-error">No mesh returned.</div>
            {/if}
          </div>

          <!-- The image this mesh was built from: the 3D tab's "prompt". -->
          <div class="shrink-0 px-6 pb-3">
            <div class="max-w-3xl mx-auto flex items-center gap-3 rounded-xl border border-card-border bg-surface px-3.5 py-2.5">
              <img src={t.image} alt="source" class="h-12 w-auto rounded-md border border-card-border object-contain" />
              <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">Source image</span>
            </div>
          </div>
        {/if}

        {#if turns.length > 0}
          <div class="shrink-0 flex items-center gap-3 px-6 py-2.5 border-t border-card-border-inner min-w-0">
            <span class="shrink-0 text-micro font-medium uppercase tracking-wide text-txtsecondary">This thread</span>
            <div bind:this={threadEl} class="flex-1 min-w-0 flex gap-2 overflow-x-auto pretty-scroll py-0.5">
              {#each turns as tt, i (i)}
                <!-- Thumbnailed by the SOURCE image: a mesh has no cheap still,
                     and the source is what tells the turns apart anyway. -->
                <button
                  class="relative shrink-0 w-16 h-12 rounded-md overflow-hidden border bg-secondary flex items-center justify-center transition-shadow {i === sel ? 'border-primary ring-1 ring-primary' : 'border-card-border hover:border-txtsecondary'}"
                  onclick={() => (selTurn = i)}
                  aria-label="Mesh {i + 1}"
                >
                  <img src={tt.image} alt="" class="w-full h-full object-cover {tt.meshes.length ? '' : 'opacity-50'}" />
                  {#if tt.error}
                    <span class="absolute inset-0 flex items-center justify-center"><X class="w-4 h-4 text-error" /></span>
                  {:else if !tt.meshes.length && genId === $activeThreeDChatId && i === turns.length - 1}
                    <span class="absolute inset-0 flex items-center justify-center"><Loader2 class="w-4 h-4 text-primary animate-spin" /></span>
                  {/if}
                </button>
              {/each}
            </div>
          </div>
        {/if}
      </section>
    </div>
  {/if}

  {#if dropActive}
    <div class="pointer-events-none absolute inset-2 z-30 flex flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed border-primary bg-surface/85 backdrop-blur-[2px]">
      <ImagePlus class="w-7 h-7 text-primary" />
      <span class="text-sm font-medium text-txtmain">Drop an image to turn it into a mesh</span>
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
