<script lang="ts">
  import { push } from "svelte-spa-router";
  import { get } from "svelte/store";
  import { models, loadModel, getSettings, pickModelsFolder } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { prettifyModelName, modelCategory, MODEL_CATEGORIES, type ModelCategory } from "../lib/modelUtils";
  import { scrollFade } from "../lib/scrollFade";
  import type { Model } from "../lib/types";
  import ModelConfigModal from "./ModelConfigModal.svelte";
  import ActiveModelsPanel from "./ActiveModelsPanel.svelte";

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

  // Every category except embed has a playground tab (chat/images/speech/
  // audio/rerank). Embedders have no interactive UI, so they get no button.
  function playable(m: Model): boolean {
    return modelCategory(m) !== "embed";
  }

  // Pick the playground tab for a model from its capabilities. Rerankers ride the
  // llm category but open the Rerank tab, not Chat.
  function playgroundTab(m: Model): string {
    const c = m.capabilities;
    if (c?.image_generation) return "images";
    if (c?.audio_speech) return "speech";
    if (c?.audio_transcriptions) return "audio";
    if (c?.reranker) return "rerank";
    return "chat";
  }

  // Playground is a separate app on its own port — open it with the model + tab
  // as launch params (different origin, so stores can't be shared directly).
  function chatWith(m: Model): void {
    const tab = playgroundTab(m);
    const port = get(playgroundPort);
    if (!port) {
      push("/test"); // no playground port configured — show the stub
      return;
    }
    const u = `${window.location.protocol}//${window.location.hostname}:${port}/ui/?model=${encodeURIComponent(m.id)}&tab=${tab}`;
    window.open(u, "_blank", "noopener");
  }

  // Kick off the load (non-blocking) AND jump to the playground with the model
  // selected. handleLoadModel no-ops if a load is already pending, so this is
  // safe to hit again on a model that's mid-load.
  function chatAndLoad(m: Model): void {
    handleLoadModel(m.id);
    chatWith(m);
  }

  // Playground action label per capability (matches the live-card button).
  function playLabel(m: Model): string {
    const c = m.capabilities;
    if (c?.image_generation) return "Generate";
    if (c?.audio_speech) return "Speak";
    if (c?.audio_transcriptions) return "Transcribe";
    if (c?.reranker) return "Rerank";
    return "Chat";
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

  // --- Staged card highlight (grid ring) ---
  // stagedId is currently always null — the pre-load staging flow was retired
  // when the live-models panel moved into the shared ActiveModelsPanel. The grid
  // still reads it for its "staged" ring, so keep the (dormant) hook.
  let stagedId = $state<string | null>(null);

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

  // Per-card variant expansion (load individual variants). The popup is
  // position:fixed (anchored to the trigger rect) so it escapes the bottom
  // grid's overflow-y-auto clip — top-row cards no longer get cut off. dropUp
  // flips it above the trigger when there's no room below.
  let expanded = $state<Record<string, boolean>>({});
  let dropUp = $state<Record<string, boolean>>({});
  let menuPos = $state<Record<string, { left: number; top: number; bottom: number; width: number }>>({});
  const POPUP_H = 260; // ~max-h-60 (240px) + margin
  function toggleExpand(key: string, e?: MouseEvent): void {
    const opening = !expanded[key];
    if (opening && e) {
      const card = (e.currentTarget as HTMLElement).closest(".card") as HTMLElement | null;
      const rect = (card ?? (e.currentTarget as HTMLElement)).getBoundingClientRect();
      const roomBelow = window.innerHeight - rect.bottom;
      dropUp[key] = roomBelow < POPUP_H && rect.top > roomBelow;
      menuPos[key] = { left: rect.left, top: rect.bottom, bottom: window.innerHeight - rect.top, width: rect.width };
    }
    expanded[key] = opening;
  }
</script>

<div class="flex flex-col h-full gap-3">
  {#snippet playIcon()}
    <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><path d="M6 4l10 6-10 6V4z" /></svg>
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

  <!-- TOP: live models — launch params + inference feedback (shared with the
       dashboard). Absent when nothing is loaded so the card grid fills the view. -->
  <ActiveModelsPanel {category} />

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
              <div class="ml-auto flex items-center gap-1.5">
                {#if playable(m)}
                  <!-- Load the model AND open it in the playground. Stays enabled
                       while the load is in flight so users can jump straight over. -->
                  <button
                    class="btn btn--sm inline-flex items-center gap-1.5 hover:border-primary hover:text-primary"
                    onclick={() => chatAndLoad(m)}
                    title="Load and open in the {playgroundTab(m)} playground"
                  >
                    <svg viewBox="0 0 20 20" fill="currentColor" class="w-3 h-3 shrink-0" aria-hidden="true"><path fill-rule="evenodd" d="M10 3c-4.418 0-8 2.91-8 6.5 0 1.66.77 3.17 2.03 4.32-.1.9-.42 1.78-.95 2.5a.5.5 0 0 0 .5.78c1.46-.25 2.7-.78 3.66-1.42.86.21 1.78.32 2.76.32 4.418 0 8-2.91 8-6.5S14.418 3 10 3Z" clip-rule="evenodd" /></svg>
                    {playLabel(m)}
                  </button>
                {/if}
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
                <div
                  class="fixed z-20 rounded-xl border border-primary/50 ring-1 ring-primary/20 bg-background shadow-xl p-1.5 flex flex-col gap-0.5 max-h-60 overflow-y-auto pretty-scroll"
                  style="left: {menuPos[card.key]?.left ?? 0}px; width: {menuPos[card.key]?.width ?? 200}px; {dropUp[card.key] ? `bottom: ${menuPos[card.key]?.bottom ?? 0}px; margin-bottom: 0.25rem;` : `top: ${menuPos[card.key]?.top ?? 0}px; margin-top: 0.25rem;`}"
                >
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
