<script lang="ts">
  import { push } from "svelte-spa-router";
  import { slide } from "svelte/transition";
  import { models, loadModel, unloadSingleModel, getModelConfig, getSettings, pickModelsFolder, type ModelConfig } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { selectedTabStore as chatTabStore, selectedModelStore as chatModelStore } from "../stores/playground";
  import { prettifyModelName, modelCategory, MODEL_CATEGORIES, type ModelCategory } from "../lib/modelUtils";
  import { scrollFade } from "../lib/scrollFade";
  import type { Model } from "../lib/types";
  import ModelConfigModal from "./ModelConfigModal.svelte";
  import InferenceFeedback from "./InferenceFeedback.svelte";

  let { category = "llm" as ModelCategory }: { category?: ModelCategory } = $props();

  let pendingLoads = $state<Record<string, boolean>>({});
  const loadControllers = new Map<string, AbortController>();

  // Per-model config editor (cogwheel) state.
  let configModelId = $state<string | null>(null);
  let configOpenFor = $state(""); // full id of the clicked row (selects its variant)
  let configOpen = $state(false);
  // family = the model whose override holds the variants (the card's base);
  // openFor = the actual row clicked, so the modal lands on its variant.
  function openConfig(family: string, openFor = ""): void {
    configModelId = family;
    configOpenFor = openFor || family;
    configOpen = true;
  }
  function closeConfig(): void {
    configOpen = false;
    // The editor saves + reloads, re-rendering launch commands. The per-member
    // config cache is keyed by id and never expires, so drop it to force a
    // refetch — otherwise the view-only staging card keeps showing the stale
    // pre-edit params (e.g. spec none after switching it to draft-mtp).
    configs = {};
  }

  const showUnlistedStore = persistentStore<boolean>("showUnlisted", true);
  const showIdorNameStore = persistentStore<"id" | "name">("showIdorName", "name");

  // Per-category scan folder (folder icon in the toolbar). Loaded for the
  // tooltip; effective path falls back to the shared modelsRoot when unset.
  let folderPath = $state("");
  let picking = $state(false);
  async function refreshFolder(): Promise<void> {
    try {
      const s = await getSettings();
      folderPath = s.categoryRoots?.[category] || s.modelsRoot || "";
    } catch {
      folderPath = "";
    }
  }
  $effect(() => {
    category; // re-run when the tab changes
    refreshFolder();
  });
  async function pickFolder(): Promise<void> {
    if (picking) return;
    picking = true;
    try {
      const path = await pickModelsFolder(category);
      if (path) folderPath = path; // null => user cancelled; regen+reload already ran
    } catch (e) {
      console.error(e);
    } finally {
      picking = false;
    }
  }

  // Shared playground singletons — set them, then jump to /test. Image models
  // open the Images tab (generate), everything else the Chat tab.
  function chatWith(m: Model): void {
    chatModelStore.set(m.id);
    chatTabStore.set(m.capabilities?.image_generation ? "images" : "chat");
    push("/test");
  }

  // A card: one gguf family. primary is the base entry; variants are the rest.
  // group/ports come off the primary (members share a swap group). active = any
  // member is on the GPU.
  type Card = {
    key: string;
    primary: Model;
    variants: Model[];
    group: string;
    ports: string[];
  };

  function isLive(m: Model): boolean {
    return m.state === "ready" || m.state === "starting" || m.state === "stopping";
  }

  // Collapse a flat model list into family cards (single-member families become
  // standalone one-entry cards).
  function buildCards(members: Model[]): Card[] {
    const byFamily = new Map<string, Model[]>();
    const standalone: Model[] = [];
    for (const m of members) {
      if (!m.family) {
        standalone.push(m);
        continue;
      }
      const arr = byFamily.get(m.family) ?? [];
      arr.push(m);
      byFamily.set(m.family, arr);
    }
    const cards: Card[] = [];
    const mk = (key: string, fam: Model[]): Card => {
      const sorted = [...fam].sort((a, b) => a.id.length - b.id.length || a.id.localeCompare(b.id));
      const primary = sorted[0];
      return {
        key,
        primary,
        variants: sorted.slice(1).sort((a, b) => a.id.localeCompare(b.id)),
        group: primary.group ?? "",
        ports: primary.listeners ?? [],
      };
    };
    for (const [key, fam] of byFamily) cards.push(mk(key, fam));
    for (const m of standalone) cards.push(mk(m.id, [m]));
    cards.sort((a, b) => a.primary.id.localeCompare(b.primary.id));
    return cards;
  }

  let view = $derived.by(() => {
    const listed = $models.filter((m) => modelCategory(m) === category && ($showUnlistedStore || !m.unlisted));
    const local = listed.filter((m) => !m.peerID);
    const peers = listed.filter((m) => m.peerID);

    const cards = buildCards(local);
    // Active families lift into the top panel; the rest fill the card grid.
    const activeCards = cards.filter((c) => isLive(c.primary) || c.variants.some(isLive));
    const idleCards = cards.filter((c) => !(isLive(c.primary) || c.variants.some(isLive)));

    // The live members themselves (each gets a mini-settings block up top).
    const liveMembers: Model[] = [];
    for (const c of [...activeCards]) {
      for (const m of [c.primary, ...c.variants]) if (isLive(m)) liveMembers.push(m);
    }

    const peersByPeerId = peers.reduce(
      (acc, m) => {
        const k = m.peerID || "unknown";
        (acc[k] ??= []).push(m);
        return acc;
      },
      {} as Record<string, Model[]>,
    );

    return { idleCards, liveMembers, peersByPeerId };
  });

  // --- Staged selection: a model placed into the top card to edit then load ---
  let stagedId = $state<string | null>(null);
  function unstage(): void {
    stagedId = null;
  }
  const stagedModel = $derived(stagedId ? ($models.find((m) => m.id === stagedId) ?? null) : null);
  // Top card shows every live member, plus the staged model when it isn't live.
  const topMembers = $derived.by(() => {
    const list = [...view.liveMembers];
    if (stagedModel && !isLive(stagedModel) && !list.some((m) => m.id === stagedModel.id)) {
      list.push(stagedModel);
    }
    return list;
  });

  // Once the staged model is actually live, drop the staging marker — it now
  // shows under the live members anyway.
  $effect(() => {
    if (stagedModel && isLive(stagedModel)) stagedId = null;
  });

  // --- Active model launch params (fetched per top member) ---
  let configs = $state<Record<string, ModelConfig>>({});
  $effect(() => {
    const ids = topMembers.map((m) => m.id);
    for (const id of ids) {
      if (configs[id]) continue;
      getModelConfig(id)
        .then((c) => (configs = { ...configs, [id]: c }))
        .catch(() => {});
    }
  });

  // Pull a curated set of effective launch flags straight off the run command,
  // so the panel reflects what's actually running.
  const FLAG_MAP: Record<string, string> = {
    "-c": "ctx",
    "--ctx-size": "ctx",
    "-ngl": "ngl",
    "--n-gpu-layers": "ngl",
    "--n-cpu-moe": "cpuMoe",
    "-ctk": "kvK",
    "--cache-type-k": "kvK",
    "-ctv": "kvV",
    "--cache-type-v": "kvV",
    "--spec-type": "spec",
    "--reasoning-format": "reasoningFmt",
    "--reasoning": "reasoning", // captures the `--reasoning off` switch
    // sd-server image generation defaults
    "--steps": "steps",
    "--cfg-scale": "cfg",
    "--sampling-method": "sampler",
    "--width": "width",
    "--height": "height",
  };
  function parseFlags(cmd: string): Record<string, string> {
    const out: Record<string, string> = {};
    const toks = cmd.split(/\s+/).filter(Boolean);
    for (let i = 0; i < toks.length; i++) {
      const key = FLAG_MAP[toks[i]];
      if (key && i + 1 < toks.length) out[key] = toks[i + 1];
    }
    return out;
  }

  // Effective (resolved) values the empty "inherit" option falls back to, parsed
  // from the actual launch command, so the dropdown shows what "default" means.
  // parseFlags collapses repeated --spec-type to the last one; the spec chain
  // emits one per backend, so collect them all for an honest readout.
  function specList(cmd: string): string {
    const toks = cmd.split(/\s+/).filter(Boolean);
    const specs: string[] = [];
    for (let i = 0; i < toks.length - 1; i++) {
      if (toks[i] === "--spec-type") specs.push(toks[i + 1]);
    }
    return specs.length ? specs.join(" + ") : "none";
  }
  function reasonDefault(f: Record<string, string>): string {
    return f.reasoning === "off" ? "off" : f.reasoningFmt || "auto";
  }
  // GPU layers as value/max (max = transformer blocks). -ngl 99 is the "all
  // layers" sentinel, so clamp to the block count. Falls back to the raw value
  // when the block count is unknown (0).
  function nglDisplay(ngl: string | undefined, blocks: number): string {
    if (ngl == null) return "—";
    const n = Number(ngl);
    if (!blocks || !Number.isFinite(n)) return ngl;
    return `${Math.min(n, blocks)}/${blocks}`;
  }

  async function handleLoadModel(modelId: string): Promise<void> {
    if (pendingLoads[modelId]) return;
    const controller = new AbortController();
    loadControllers.set(modelId, controller);
    pendingLoads[modelId] = true;
    try {
      await loadModel(modelId, controller.signal);
    } catch (e) {
      console.error(e);
    } finally {
      loadControllers.delete(modelId);
      delete pendingLoads[modelId];
    }
  }

  function cancelLoad(modelId: string): void {
    loadControllers.get(modelId)?.abort();
  }

  function display(m: Model): string {
    return $showIdorNameStore === "id" ? m.id : prettifyModelName(m.name || m.id);
  }

  // Card outline + status text color by state.
  function stateRing(state: string): string {
    if (state === "ready") return "border-success/60";
    if (state === "starting") return "border-warning/60";
    if (state === "stopping") return "border-error/60";
    return "border-card-border";
  }
  function dotClass(state: string): string {
    if (state === "ready") return "bg-success";
    if (state === "starting") return "bg-warning animate-pulse";
    if (state === "stopping") return "bg-error animate-pulse";
    return "bg-txtsecondary";
  }

  // Per-card variant expansion (load individual variants). dropUp flips the
  // popup above the trigger when a bottom-row card lacks room below, so it stays
  // in view instead of being clipped by the scroll container.
  let expanded = $state<Record<string, boolean>>({});
  let dropUp = $state<Record<string, boolean>>({});
  const POPUP_H = 260; // ~max-h-60 (240px) + margin
  function toggleExpand(key: string, e?: MouseEvent): void {
    const opening = !expanded[key];
    if (opening && e) {
      const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
      dropUp[key] = window.innerHeight - rect.bottom < POPUP_H && rect.top > POPUP_H;
    }
    expanded[key] = opening;
  }
</script>

<div class="flex flex-col h-full gap-3">
  {#snippet playIcon()}
    <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><path d="M6 4l10 6-10 6V4z" /></svg>
  {/snippet}
  {#snippet stopIcon()}
    <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><rect x="5" y="5" width="10" height="10" rx="1.5" /></svg>
  {/snippet}
  {#snippet hint(text: string)}
    <span
      class="inline-flex items-center justify-center w-3 h-3 shrink-0 rounded-full border border-card-border text-txtsecondary text-[0.5rem] leading-none cursor-help normal-case hover:text-txtmain hover:border-txtmain"
      title={text}
      aria-label={text}>?</span>
  {/snippet}
  {#snippet roField(label: string, value: string, tip: string)}
    <div>
      <div class="text-txtsecondary uppercase tracking-wide flex items-center gap-1">{label} {@render hint(tip)}</div>
      <div class="text-txtmain tabular-nums pt-1.5 break-all">{value}</div>
    </div>
  {/snippet}

  <!-- Header / toolbar -->
  <div class="flex items-center justify-between shrink-0">
    <h2 class="!pb-0">{MODEL_CATEGORIES.find((c) => c.id === category)?.label ?? "Models"}</h2>
    <div class="flex items-center gap-2">
      <button
        class="btn btn--sm inline-flex items-center justify-center disabled:opacity-50"
        onclick={pickFolder}
        disabled={picking}
        aria-label="Set models folder"
        title={`Models folder${folderPath ? ": " + folderPath : ""} — click to choose`}
      >
        <svg viewBox="0 0 20 20" fill="currentColor" class="w-3.5 h-3.5" aria-hidden="true"><path d="M3.5 4A1.5 1.5 0 0 0 2 5.5v9A1.5 1.5 0 0 0 3.5 16h13a1.5 1.5 0 0 0 1.5-1.5v-7A1.5 1.5 0 0 0 16.5 6H10L8.4 4.4A1.5 1.5 0 0 0 7.35 4H3.5Z" /></svg>
      </button>
      <button
        class="btn btn--sm uppercase tracking-wide"
        onclick={() => showIdorNameStore.update((p) => (p === "name" ? "id" : "name"))}
        title="Toggle id / name display"
      >
        {$showIdorNameStore === "id" ? "ID" : "Name"}
      </button>
      <button
        class="btn btn--sm uppercase tracking-wide"
        onclick={() => showUnlistedStore.update((p) => !p)}
        title="Show or hide unlisted models"
      >
        {$showUnlistedStore ? "Hide unlisted" : "Show unlisted"}
      </button>
    </div>
  </div>

  <!-- TOP: active model(s) — mini launch params + inference feedback. Absent
       when nothing is loaded so the card grid fills the screen; slides in (and
       pushes the grid down) once a model is staged/live. -->
  {#if topMembers.length > 0}
    <div class="shrink-0 h-72 min-h-[14rem]" transition:slide={{ duration: 250 }}>
      <div class="grid h-full grid-cols-1 lg:grid-cols-2 gap-3">
        <!-- Active model settings -->
        <div class="card h-full overflow-auto pretty-scroll">
          {#each topMembers as m (m.id)}
            {@const cfg = configs[m.id]}
            {@const flags = cfg ? parseFlags(cfg.cmd) : {}}
            {@const live = isLive(m)}
            <div class="mb-3 last:mb-0">
              <div class="flex items-center gap-2">
                <span class="inline-block w-2.5 h-2.5 rounded-full {dotClass(m.state)}"></span>
                <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{live ? m.state : "staged"}</span>
                <div class="ml-auto flex items-center gap-1.5">
                  <button
                    class="inline-flex items-center justify-center p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-background transition-colors"
                    onclick={() => openConfig(m.id)}
                    aria-label="Edit parameters"
                    title="Edit parameters / variants"
                  >
                    <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-4 h-4">
                      <path fill-rule="evenodd" d="M8.34 1.804A1 1 0 0 1 9.32 1h1.36a1 1 0 0 1 .98.804l.295 1.473c.497.144.97.342 1.41.587l1.25-.834a1 1 0 0 1 1.262.125l.962.962a1 1 0 0 1 .125 1.262l-.834 1.25c.245.44.443.913.587 1.41l1.473.294a1 1 0 0 1 .804.98v1.361a1 1 0 0 1-.804.98l-1.473.295a6.95 6.95 0 0 1-.587 1.41l.834 1.25a1 1 0 0 1-.125 1.262l-.962.962a1 1 0 0 1-1.262.125l-1.25-.834c-.44.245-.913.443-1.41.587l-.294 1.473a1 1 0 0 1-.98.804H9.32a1 1 0 0 1-.98-.804l-.295-1.473a6.95 6.95 0 0 1-1.41-.587l-1.25.834a1 1 0 0 1-1.262-.125l-.962-.962a1 1 0 0 1-.125-1.262l.834-1.25a6.95 6.95 0 0 1-.587-1.41l-1.473-.294A1 1 0 0 1 1 10.68V9.32a1 1 0 0 1 .804-.98l1.473-.295c.144-.497.342-.97.587-1.41l-.834-1.25a1 1 0 0 1 .125-1.262l.962-.962A1 1 0 0 1 5.38 3.03l1.25.834c.44-.245.913-.443 1.41-.587l.294-1.473ZM10 13a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" clip-rule="evenodd" />
                    </svg>
                  </button>
                  {#if live}
                    <button
                      class="btn btn--sm py-1.5 inline-flex items-center gap-1.5 uppercase tracking-wide hover:border-primary hover:text-primary"
                      onclick={() => chatWith(m)}
                      disabled={m.state !== "ready"}
                      title={m.capabilities?.image_generation
                        ? "Open this model in the image playground"
                        : "Open this model in the chat playground"}
                    >
                      {#if m.capabilities?.image_generation}
                        <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><path fill-rule="evenodd" d="M1 5.25A2.25 2.25 0 0 1 3.25 3h13.5A2.25 2.25 0 0 1 19 5.25v9.5A2.25 2.25 0 0 1 16.75 17H3.25A2.25 2.25 0 0 1 1 14.75v-9.5Zm1.5 5.81v3.69c0 .414.336.75.75.75h13.5a.75.75 0 0 0 .75-.75v-2.69l-2.22-2.219a.75.75 0 0 0-1.06 0l-1.91 1.909.47.47a.75.75 0 1 1-1.06 1.06L6.53 8.091a.75.75 0 0 0-1.06 0l-2.97 2.97ZM12 7a1 1 0 1 1 2 0 1 1 0 0 1-2 0Z" clip-rule="evenodd" /></svg>
                        Generate
                      {:else}
                        <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><path fill-rule="evenodd" d="M10 3c-4.418 0-8 2.91-8 6.5 0 1.66.77 3.17 2.03 4.32-.1.9-.42 1.78-.95 2.5a.5.5 0 0 0 .5.78c1.46-.25 2.7-.78 3.66-1.42.86.21 1.78.32 2.76.32 4.418 0 8-2.91 8-6.5S14.418 3 10 3Z" clip-rule="evenodd" /></svg>
                        Chat
                      {/if}
                    </button>
                    <button
                      class="btn btn--sm py-1.5 inline-flex items-center gap-1.5 uppercase tracking-wide hover:border-error hover:text-error"
                      onclick={() => unloadSingleModel(m.id)}
                      disabled={m.state !== "ready"}
                    >
                      {@render stopIcon()}
                      Unload
                    </button>
                  {:else}
                    <button
                      class="btn btn--sm btn--primary py-1.5 inline-flex items-center gap-1.5 uppercase tracking-wide"
                      onclick={() => handleLoadModel(m.id)}
                      disabled={pendingLoads[m.id]}
                      title="Load this model with the parameters shown"
                    >
                      {@render playIcon()}
                      {pendingLoads[m.id] ? "Loading…" : "Load"}
                    </button>
                    <button
                      class="inline-flex items-center justify-center p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-error hover:border-error transition-colors"
                      onclick={unstage}
                      aria-label="Remove from staging"
                      title="Clear selection"
                    >
                      <svg viewBox="0 0 20 20" fill="currentColor" class="w-4 h-4"><path d="M6 6l8 8M14 6l-8 8" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
                    </button>
                  {/if}
                </div>
              </div>
              <div class="mt-1.5 font-mono text-sm uppercase tracking-widest text-txtsecondary break-words" title={m.id}>{display(m)}</div>

              <!-- Effective launch params, read off the run command (view only;
                   edit via the cogwheel). -->
              {#if cfg?.isImage}
                <!-- Image (sd-server) generation defaults -->
                <div class="mt-2 grid grid-cols-3 gap-x-3 gap-y-2 font-mono text-xs">
                  {@render roField("Steps", flags.steps ?? "—", "Sampling steps per image (--steps). More = slower, usually higher quality. Distilled/Turbo models want few (4–8).")}
                  {@render roField("CFG", flags.cfg ?? "—", "Classifier-free guidance scale (--cfg-scale). Turbo/distilled models REQUIRE 1.0 — higher blurs. Standard models use ~7.")}
                  {@render roField("Sampler", flags.sampler ?? "—", "Sampling method (--sampling-method). euler / euler_a are safe defaults; lcm pairs with low-step distilled models.")}
                  {@render roField("Width", flags.width ?? "—", "Default image width in px (--width). Per-request width still overrides this.")}
                  {@render roField("Height", flags.height ?? "—", "Default image height in px (--height). Per-request height still overrides this.")}
                  {@render roField("CPU offload", cfg.cmd.includes("--offload-to-cpu") ? "on" : "off", "Page diffusion weights to RAM (--offload-to-cpu): saves VRAM, slower per step.")}
                </div>
              {:else if cfg}
                <div class="mt-2 grid grid-cols-3 gap-x-3 gap-y-2 font-mono text-xs">
                  {@render roField("Ctx", flags.ctx ?? "—", "Context window (tokens) this model loaded with (-c), as sized by the autogen sizer to fit free VRAM.")}
                  {@render roField("GPU layers", nglDisplay(flags.ngl, cfg.blockCount ?? 0), "Layers resident on the GPU (-ngl), as chosen by the sizer for the current plan.")}
                  {@render roField("CPU MoE", flags.cpuMoe ?? "—", "Expert layers offloaded to the CPU (--n-cpu-moe) for MoE models.")}
                  {@render roField("KV K", flags.kvK ?? "—", "Quantization of the attention key cache (-ctk). Lower bits = less VRAM, slightly less accuracy.")}
                  {@render roField("KV V", flags.kvV ?? "—", "Quantization of the attention value cache (-ctv). Lower bits = less VRAM.")}
                  {@render roField("Spec", specList(cfg.cmd), "Speculative decoding chain (--spec-type), one entry per backend. none = disabled.")}
                  {@render roField("Reasoning", reasonDefault(flags), "How the model's chain-of-thought is parsed (--reasoning-format). auto = llama.cpp detects it; off = reasoning disabled.")}
                </div>
              {/if}

              {#if cfg}
                <details class="mt-2">
                  <summary class="font-mono text-[0.65rem] text-txtsecondary cursor-pointer hover:text-txtmain">Launch command</summary>
                  <pre class="mt-1 whitespace-pre-wrap break-all font-mono text-[0.65rem] text-txtsecondary bg-background rounded p-2">{cfg.cmd}</pre>
                </details>
              {/if}
            </div>
          {/each}
        </div>

        <!-- Live inference feedback (replaces the upstream log here) -->
        <div class="h-full min-h-0">
          <InferenceFeedback models={topMembers} />
        </div>
      </div>
    </div>
  {/if}

  <!-- BOTTOM: flat card grid of other models -->
  <div class="flex-1 overflow-y-auto min-h-0 pretty-scroll scroll-fade-y px-0.5 py-1" use:scrollFade>
    {#if view.idleCards.length > 0}
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
        {#each view.idleCards as card (card.key)}
          {@const m = card.primary}
          {@const variantCount = card.variants.length + 1}
          {@const staged = stagedId === m.id || card.variants.some((v) => v.id === stagedId)}
          <div class="card !overflow-visible border {stateRing(m.state)} {staged ? 'ring-2 ring-primary' : ''} flex flex-col gap-2 p-3 transition-colors">
            <!-- Top row: status + badges + cog -->
            <div class="flex items-center gap-2">
              <span class="inline-block w-2 h-2 rounded-full {dotClass(m.state)}"></span>
              <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{m.state}</span>
              <div class="ml-auto flex items-center gap-1">
                {#each card.ports as port (port)}
                  <span class="font-mono text-[0.55rem] text-primary border border-primary/40 rounded px-1 py-0.5" title="Listener {port}">{port}</span>
                {/each}
                <button
                  class="inline-flex items-center justify-center p-1 rounded border border-card-border text-txtsecondary hover:text-txtmain hover:bg-background transition-colors"
                  onclick={() => openConfig(m.id)}
                  aria-label="Edit parameters"
                  title="Edit parameters / variants"
                >
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-3.5 h-3.5">
                    <path fill-rule="evenodd" d="M8.34 1.804A1 1 0 0 1 9.32 1h1.36a1 1 0 0 1 .98.804l.295 1.473c.497.144.97.342 1.41.587l1.25-.834a1 1 0 0 1 1.262.125l.962.962a1 1 0 0 1 .125 1.262l-.834 1.25c.245.44.443.913.587 1.41l1.473.294a1 1 0 0 1 .804.98v1.361a1 1 0 0 1-.804.98l-1.473.295a6.95 6.95 0 0 1-.587 1.41l.834 1.25a1 1 0 0 1-.125 1.262l-.962.962a1 1 0 0 1-1.262.125l-1.25-.834c-.44.245-.913.443-1.41.587l-.294 1.473a1 1 0 0 1-.98.804H9.32a1 1 0 0 1-.98-.804l-.295-1.473a6.95 6.95 0 0 1-1.41-.587l-1.25.834a1 1 0 0 1-1.262-.125l-.962-.962a1 1 0 0 1-.125-1.262l.834-1.25a6.95 6.95 0 0 1-.587-1.41l-1.473-.294A1 1 0 0 1 1 10.68V9.32a1 1 0 0 1 .804-.98l1.473-.295c.144-.497.342-.97.587-1.41l-.834-1.25a1 1 0 0 1 .125-1.262l.962-.962A1 1 0 0 1 5.38 3.03l1.25.834c.44-.245.913-.443 1.41-.587l.294-1.473ZM10 13a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" clip-rule="evenodd" />
                  </svg>
                </button>
              </div>
            </div>

            <!-- Name -->
            <span class="font-mono text-[0.7rem] uppercase tracking-widest text-txtsecondary truncate {m.unlisted ? 'opacity-70' : ''}" title={m.id}>
              {display(m)}
            </span>

            <!-- Footer: variants + load -->
            <div class="mt-auto flex items-center gap-2 relative">
              {#if variantCount > 1}
                <button
                  class="font-mono text-[0.65rem] text-txtsecondary hover:text-txtmain border border-card-border rounded px-1.5 py-0.5 tabular-nums"
                  onclick={(e) => toggleExpand(card.key, e)}
                  title="Show variants"
                >
                  {variantCount} variants {expanded[card.key] ? "▴" : "▾"}
                </button>
              {/if}
              <div class="ml-auto">
                {#if pendingLoads[m.id]}
                  <button class="btn btn--sm inline-flex items-center gap-1.5" onclick={() => cancelLoad(m.id)}>Cancel</button>
                {:else}
                  <button class="btn btn--sm inline-flex items-center gap-1.5 hover:border-primary hover:text-primary" onclick={() => handleLoadModel(m.id)}>
                    {@render playIcon()}
                    Load
                  </button>
                {/if}
              </div>

              <!-- Variants popup menu: pick one to open in the top editor card -->
              {#if expanded[card.key] && card.variants.length > 0}
                <!-- click-catcher: closes the menu on outside click -->
                <button class="fixed inset-0 z-10 cursor-default" aria-label="Close variants" onclick={() => toggleExpand(card.key)}></button>
                <div class="absolute z-20 left-0 right-0 {dropUp[card.key] ? 'bottom-full mb-1' : 'top-full mt-1'} rounded-md border border-card-border bg-surface shadow-lg p-1.5 flex flex-col gap-0.5 max-h-60 overflow-y-auto pretty-scroll">
                  <div class="px-1.5 pb-1 font-mono text-[0.55rem] uppercase tracking-wide text-txtsecondary">Load a variant</div>
                  {#each [card.primary, ...card.variants] as v (v.id)}
                    <div class="flex items-center gap-1 rounded hover:bg-background transition-colors {stagedId === v.id ? 'bg-background' : ''}">
                      <button
                        class="flex-1 min-w-0 text-left flex items-start gap-2 px-1.5 py-1"
                        onclick={() => { toggleExpand(card.key); handleLoadModel(v.id); }}
                        title="Load {v.id}"
                      >
                        <span class="inline-block w-1.5 h-1.5 rounded-full mt-1 shrink-0 {dotClass(v.state)}"></span>
                        <span class="font-mono text-xs text-txtmain break-words whitespace-normal">{display(v)}</span>
                        {#if isLive(v)}<span class="ml-auto shrink-0 font-mono text-[0.55rem] uppercase text-txtsecondary">{v.state}</span>{/if}
                      </button>
                      <button
                        class="shrink-0 inline-flex items-center justify-center p-1 mr-0.5 rounded border border-card-border text-txtsecondary hover:text-txtmain hover:bg-card-border/40 transition-colors"
                        onclick={(e) => { e.stopPropagation(); openConfig(card.primary.id, v.id); }}
                        aria-label="Edit parameters"
                        title="Edit parameters / variants"
                      >
                        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3">
                          <path fill-rule="evenodd" d="M8.34 1.804A1 1 0 0 1 9.32 1h1.36a1 1 0 0 1 .98.804l.295 1.473c.497.144.97.342 1.41.587l1.25-.834a1 1 0 0 1 1.262.125l.962.962a1 1 0 0 1 .125 1.262l-.834 1.25c.245.44.443.913.587 1.41l1.473.294a1 1 0 0 1 .804.98v1.361a1 1 0 0 1-.804.98l-1.473.295a6.95 6.95 0 0 1-.587 1.41l.834 1.25a1 1 0 0 1-.125 1.262l-.962.962a1 1 0 0 1-1.262.125l-1.25-.834c-.44.245-.913.443-1.41.587l-.294 1.473a1 1 0 0 1-.98.804H9.32a1 1 0 0 1-.98-.804l-.295-1.473a6.95 6.95 0 0 1-1.41-.587l-1.25.834a1 1 0 0 1-1.262-.125l-.962-.962a1 1 0 0 1-.125-1.262l.834-1.25a6.95 6.95 0 0 1-.587-1.41l-1.473-.294A1 1 0 0 1 1 10.68V9.32a1 1 0 0 1 .804-.98l1.473-.295c.144-.497.342-.97.587-1.41l-.834-1.25a1 1 0 0 1 .125-1.262l.962-.962A1 1 0 0 1 5.38 3.03l1.25.834c.44-.245.913-.443 1.41-.587l.294-1.473ZM10 13a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" clip-rule="evenodd" />
                        </svg>
                      </button>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <p class="font-mono text-xs text-txtsecondary px-1">No other models.</p>
    {/if}

    <!-- Peer models -->
    {#if Object.keys(view.peersByPeerId).length > 0}
      <div class="mt-6">
        <h3 class="mb-2">Peer models</h3>
        {#each Object.entries(view.peersByPeerId).sort(([a], [b]) => a.localeCompare(b)) as [peerId, peerModels] (peerId)}
          <div class="mb-3">
            <div class="font-mono text-xs uppercase tracking-wide text-txtsecondary mb-1">{peerId}</div>
            <div class="flex flex-wrap gap-2">
              {#each peerModels as m (m.id)}
                <span class="font-mono text-xs border border-card-border rounded px-2 py-1 text-txtmain {m.unlisted ? 'opacity-70' : ''}">{m.id}</span>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>

<ModelConfigModal modelId={configModelId} openForId={configOpenFor} open={configOpen} onclose={closeConfig} />
