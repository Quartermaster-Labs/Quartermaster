<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { push } from "svelte-spa-router";
  import { get } from "svelte/store";
  import { Folder } from "lucide-svelte";
  import { models, loadModel, getSettings, pickModelsFolder } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { playgroundPort } from "../stores/playgroundAuth";
  import { modelCategory, MODEL_CATEGORIES, type ModelCategory } from "../lib/modelUtils";
  import { nextSort, type SortDir, type SortKey, type StateFilter } from "../lib/modelTable";
  import type { Model } from "../lib/types";
  import ModelConfigModal from "./ModelConfigModal.svelte";
  import ModelsTable from "./ModelsTable.svelte";

  // The route's category is the INITIAL tab only — switching tabs from here is
  // page-local state, so it doesn't push a history entry per click.
  let { category = "llm" as ModelCategory }: { category?: ModelCategory } = $props();
  // Seeded from the prop's own default, then reconciled by the effect below on
  // its first run — reading `category` here would only capture its initial value.
  let tab = $state<ModelCategory>("llm");
  let lastRouteCategory = $state<ModelCategory>("llm");
  $effect(() => {
    // Follow the route only when it actually changed (a deep link / sidebar
    // click), never over the user's own tab pick.
    if (category !== lastRouteCategory) {
      lastRouteCategory = category;
      tab = category;
    }
  });

  let pendingLoads = $state<Record<string, boolean>>({});
  const loadControllers = new Map<string, AbortController>();

  // Per-model config editor (cogwheel).
  let configModelId = $state<string | null>(null);
  let configOpenFor = $state("");
  let configOpen = $state(false);
  function openConfig(family: string, openFor = ""): void {
    configModelId = family;
    configOpenFor = openFor || family;
    configOpen = true;
  }

  const showUnlistedStore = persistentStore<boolean>("showUnlisted", true);
  const showIdorNameStore = persistentStore<"id" | "name">("showIdorName", "name");
  const sortKeyStore = persistentStore<SortKey>("modelsSortKey", "name");
  const sortDirStore = persistentStore<SortDir>("modelsSortDir", "asc");
  // Favourites are keyed by ROW (the base model), not by model id: pinning is a
  // statement about the model, and it must survive switching quant or variant.
  const favoritesStore = persistentStore<string[]>("modelsFavorites", []);
  function toggleFavorite(key: string): void {
    favoritesStore.update((f) => (f.includes(key) ? f.filter((k) => k !== key) : [...f, key]));
  }

  // Search lives in the table's Model header (the column it filters); the table
  // owns the expand/collapse, this is just the value.
  let search = $state("");
  let stateFilter = $state<StateFilter>("all");

  // Ascending → descending → off, so the catalog's own order is reachable
  // without a reset button. A different column always starts over at ascending.
  function onSort(key: SortKey): void {
    const next = nextSort(get(sortKeyStore), get(sortDirStore), key);
    sortKeyStore.set(next.key);
    sortDirStore.set(next.dir);
  }

  // Per-category scan folder (folder icon in the toolbar).
  let folderPath = $state("");
  let picking = $state(false);
  async function refreshFolder(): Promise<void> {
    try {
      const s = await getSettings();
      folderPath = s.categoryRoots?.[tab] || s.modelsRoot || "";
    } catch {
      folderPath = "";
    }
  }
  $effect(() => {
    tab; // re-run when the tab changes
    refreshFolder();
  });
  async function pickFolder(): Promise<void> {
    if (picking) return;
    picking = true;
    try {
      const path = await pickModelsFolder(tab);
      if (path) folderPath = path; // null => user cancelled; regen+reload already ran
    } catch (e) {
      console.error(e);
    } finally {
      picking = false;
    }
  }

  // Every category except embed/segment has a playground tab (chat/images/
  // speech/audio). Embedders and rerankers have no interactive UI (API only);
  // SAM segmenters are driven from the Images playground's select tool.
  function playable(m: Model): boolean {
    const cat = modelCategory(m);
    return cat !== "embed" && cat !== "segment" && !m.capabilities?.reranker;
  }

  function playgroundTab(m: Model): string {
    const c = m.capabilities;
    if (c?.image_generation) return "images";
    if (c?.audio_speech) return "speech";
    if (c?.audio_transcriptions) return "audio";
    return "chat";
  }

  function playLabel(m: Model): string {
    const c = m.capabilities;
    if (c?.image_generation) return "Generate";
    if (c?.audio_speech) return "Speak";
    if (c?.audio_transcriptions) return "Transcribe";
    return "Chat";
  }

  // Playground is a separate app on its own port — open it with the model + tab
  // as launch params (different origin, so stores can't be shared directly).
  function chatWith(m: Model): void {
    const port = get(playgroundPort);
    if (!port) {
      push("/test"); // no playground port configured — show the stub
      return;
    }
    const u = `${window.location.protocol}//${window.location.hostname}:${port}/ui/?model=${encodeURIComponent(m.id)}&tab=${playgroundTab(m)}`;
    window.open(u, "_blank", "noopener");
  }

  // Kick off the load (non-blocking) AND jump to the playground.
  function chatAndLoad(m: Model): void {
    handleLoadModel(m.id);
    chatWith(m);
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

  let local = $derived($models.filter((m) => !m.peerID));
  let inTab = $derived(local.filter((m) => modelCategory(m) === tab));
  let peers = $derived($models.filter((m) => m.peerID));
  // Tab counts follow the unlisted toggle so the badge matches what the table
  // actually shows.
  function tabCount(id: ModelCategory): number {
    return local.filter((m) => modelCategory(m) === id && ($showUnlistedStore || !m.unlisted)).length;
  }

  let peersByPeerId = $derived(
    peers.reduce(
      (acc, m) => {
        const k = m.peerID || "unknown";
        (acc[k] ??= []).push(m);
        return acc;
      },
      {} as Record<string, Model[]>,
    ),
  );
</script>

<div class="flex flex-col h-full gap-3">
  <!-- One toolbar: category tabs left, table-wide controls right. Wraps rather
       than scrolls — a scrollbar under the tabs hides categories behind a drag. -->
  <div class="flex flex-wrap items-end gap-x-1 gap-y-2 border-b border-card-border shrink-0">
    {#each MODEL_CATEGORIES as c (c.id)}
      <button
        class="px-3 py-2 -mb-px border-b-2 font-mono text-xs uppercase tracking-wide transition-colors {tab === c.id
          ? 'border-primary text-txtmain'
          : 'border-transparent text-txtsecondary hover:text-txtmain'}"
        onclick={() => (tab = c.id)}
      >
        {c.label}
        <span class="ml-1.5 tabular-nums text-[0.65rem] text-txtsecondary">{tabCount(c.id)}</span>
      </button>
    {/each}

    <div class="ml-auto flex items-center gap-2 pb-1.5">
      <div class="flex items-center rounded border border-card-border overflow-hidden">
        {#each [{ id: "all", label: "All" }, { id: "loaded", label: "Loaded" }, { id: "idle", label: "Idle" }] as f (f.id)}
          <button
            class="px-2.5 py-1 font-mono text-[0.65rem] uppercase tracking-wide transition-colors {stateFilter === f.id
              ? 'bg-primary text-white'
              : 'text-txtsecondary hover:text-txtmain hover:bg-secondary/60'}"
            onclick={() => (stateFilter = f.id as StateFilter)}
          >
            {f.label}
          </button>
        {/each}
      </div>
      <button
        class="btn btn--sm inline-flex items-center justify-center disabled:opacity-50"
        onclick={pickFolder}
        disabled={picking}
        aria-label="Set models folder"
        use:tip={`Models folder${folderPath ? ": " + folderPath : ""} - click to choose`}
      >
        <Folder class="w-3.5 h-3.5" />
      </button>
      <button
        class="btn btn--sm uppercase tracking-wide"
        onclick={() => showIdorNameStore.update((p) => (p === "name" ? "id" : "name"))}
        use:tip={"Toggle id / name display"}
      >
        {$showIdorNameStore === "id" ? "ID" : "Name"}
      </button>
      <button class="btn btn--sm uppercase tracking-wide" onclick={() => showUnlistedStore.update((p) => !p)} use:tip={"Show or hide unlisted models"}>
        {$showUnlistedStore ? "Hide unlisted" : "Show unlisted"}
      </button>
    </div>
  </div>

  <ModelsTable
    models={inTab}
    bind:search
    {stateFilter}
    showUnlisted={$showUnlistedStore}
    display={$showIdorNameStore}
    sortKey={$sortKeyStore}
    sortDir={$sortDirStore}
    pending={pendingLoads}
    favorites={$favoritesStore}
    onFavorite={toggleFavorite}
    onLoad={handleLoadModel}
    onCancel={cancelLoad}
    onPlay={chatAndLoad}
    canPlay={playable}
    {playLabel}
    onConfig={openConfig}
    {onSort}
  />

  <!-- Peer models: read-only, no local actions. -->
  {#if Object.keys(peersByPeerId).length > 0}
    <div class="shrink-0">
      <h3 class="mb-2">Peer models</h3>
      {#each Object.entries(peersByPeerId).sort(([a], [b]) => a.localeCompare(b)) as [peerId, peerModels] (peerId)}
        <div class="mb-2">
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

<ModelConfigModal modelId={configModelId} openForId={configOpenFor} open={configOpen} onclose={() => (configOpen = false)} />
