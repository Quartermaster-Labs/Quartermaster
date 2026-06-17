<script lang="ts">
  import { models, loadModel, unloadAllModels, unloadSingleModel } from "../stores/api";
  import { isNarrow } from "../stores/theme";
  import { persistentStore } from "../stores/persistent";
  import type { Model } from "../lib/types";
  import ModelConfigModal from "./ModelConfigModal.svelte";

  let isUnloading = $state(false);
  let menuOpen = $state(false);
  let pendingLoads = $state<Record<string, boolean>>({});
  const loadControllers = new Map<string, AbortController>();

  // Per-model config editor (cogwheel) state.
  let configModelId = $state<string | null>(null);
  let configOpen = $state(false);
  function openConfig(id: string): void {
    configModelId = id;
    configOpen = true;
  }
  function closeConfig(): void {
    configOpen = false;
  }

  const showUnlistedStore = persistentStore<boolean>("showUnlisted", true);
  const showIdorNameStore = persistentStore<"id" | "name">("showIdorName", "id");

  // A variant group: models sharing one gguf (family). primary is the base
  // entry (shortest id, i.e. no ctx/game/judge suffix); variants are the rest.
  type ModelGroup = { key: string; primary: Model; variants: Model[] };

  // Per-family expand/collapse state (collapsed by default).
  let expandedFamilies = $state<Record<string, boolean>>({});
  function toggleFamily(key: string): void {
    expandedFamilies[key] = !expandedFamilies[key];
  }

  let filteredModels = $derived.by(() => {
    const filtered = $models.filter((model) => $showUnlistedStore || !model.unlisted);
    const peerModels = filtered.filter((m) => m.peerID);

    // Group peer models by peerID
    const grouped = peerModels.reduce(
      (acc, model) => {
        const peerId = model.peerID || "unknown";
        if (!acc[peerId]) acc[peerId] = [];
        acc[peerId].push(model);
        return acc;
      },
      {} as Record<string, Model[]>
    );

    // Group local models by gguf family. A family with a single member stays a
    // standalone row; 2+ members collapse under the base (shortest-id) entry.
    const byFamily = new Map<string, Model[]>();
    const standalone: Model[] = [];
    for (const m of filtered.filter((x) => !x.peerID)) {
      if (!m.family) {
        standalone.push(m);
        continue;
      }
      const arr = byFamily.get(m.family) ?? [];
      arr.push(m);
      byFamily.set(m.family, arr);
    }

    const groups: ModelGroup[] = [];
    for (const [key, members] of byFamily) {
      if (members.length === 1) {
        standalone.push(members[0]);
        continue;
      }
      const sorted = [...members].sort((a, b) => a.id.length - b.id.length || a.id.localeCompare(b.id));
      groups.push({
        key,
        primary: sorted[0],
        variants: sorted.slice(1).sort((a, b) => a.id.localeCompare(b.id)),
      });
    }
    groups.sort((a, b) => a.primary.id.localeCompare(b.primary.id));
    standalone.sort((a, b) => a.id.localeCompare(b.id));

    return {
      groups,
      standaloneModels: standalone,
      peerModelsByPeerId: grouped,
    };
  });

  // Count of a group's variants (incl. primary) that are not stopped.
  function activeCount(group: ModelGroup): number {
    return [group.primary, ...group.variants].filter((m) => m.state !== "stopped").length;
  }

  async function handleUnloadAllModels(): Promise<void> {
    isUnloading = true;
    try {
      await unloadAllModels();
    } catch (e) {
      console.error(e);
    } finally {
      setTimeout(() => (isUnloading = false), 1000);
    }
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

  function toggleIdorName(): void {
    showIdorNameStore.update((prev) => (prev === "name" ? "id" : "name"));
  }

  function toggleShowUnlisted(): void {
    showUnlistedStore.update((prev) => !prev);
  }

  // prettify turns a raw model id/name into a display label: dashes become
  // spaces and each word is capitalized ("gemma-4-12b-it" -> "Gemma 4 12b It").
  // Cosmetic only — model.id is still used for links and load/unload calls.
  function prettify(s: string): string {
    return s
      .split("-")
      .map((w) => (w ? w[0].toUpperCase() + w.slice(1) : w))
      .join(" ");
  }

  function getModelDisplay(model: Model): string {
    return prettify($showIdorNameStore === "id" ? model.id : (model.name || model.id));
  }
</script>

<div class="card h-full flex flex-col">
  <div class="shrink-0">
    <div class="flex justify-between items-baseline">
      <h2 class={$isNarrow ? "text-xl" : ""}>Models</h2>
      {#if $isNarrow}
        <div class="relative">
          <button class="btn text-base flex items-center gap-2 py-1" onclick={() => (menuOpen = !menuOpen)} aria-label="Toggle menu">
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
              <path fill-rule="evenodd" d="M3 6.75A.75.75 0 0 1 3.75 6h16.5a.75.75 0 0 1 0 1.5H3.75A.75.75 0 0 1 3 6.75ZM3 12a.75.75 0 0 1 .75-.75h16.5a.75.75 0 0 1 0 1.5H3.75A.75.75 0 0 1 3 12Zm0 5.25a.75.75 0 0 1 .75-.75h16.5a.75.75 0 0 1 0 1.5H3.75a.75.75 0 0 1-.75-.75Z" clip-rule="evenodd" />
            </svg>
          </button>
          {#if menuOpen}
            <div class="absolute right-0 mt-2 w-48 bg-surface border border-gray-200 dark:border-white/10 rounded shadow-lg z-20">
              <button
                class="w-full text-left px-4 py-2 hover:bg-secondary-hover flex items-center gap-2"
                onclick={() => { toggleIdorName(); menuOpen = false; }}
              >
                <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
                  <path fill-rule="evenodd" d="M15.97 2.47a.75.75 0 0 1 1.06 0l4.5 4.5a.75.75 0 0 1 0 1.06l-4.5 4.5a.75.75 0 1 1-1.06-1.06l3.22-3.22H7.5a.75.75 0 0 1 0-1.5h11.69l-3.22-3.22a.75.75 0 0 1 0-1.06Zm-7.94 9a.75.75 0 0 1 0 1.06l-3.22 3.22H16.5a.75.75 0 0 1 0 1.5H4.81l3.22 3.22a.75.75 0 1 1-1.06 1.06l-4.5-4.5a.75.75 0 0 1 0-1.06l4.5-4.5a.75.75 0 0 1 1.06 0Z" clip-rule="evenodd" />
                </svg>
                {$showIdorNameStore === "id" ? "Show Name" : "Show ID"}
              </button>
              <button
                class="w-full text-left px-4 py-2 hover:bg-secondary-hover flex items-center gap-2"
                onclick={() => { toggleShowUnlisted(); menuOpen = false; }}
              >
                {#if $showUnlistedStore}
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
                    <path d="M3.53 2.47a.75.75 0 0 0-1.06 1.06l18 18a.75.75 0 1 0 1.06-1.06l-18-18ZM22.676 12.553a11.249 11.249 0 0 1-2.631 4.31l-3.099-3.099a5.25 5.25 0 0 0-6.71-6.71L7.759 4.577a11.217 11.217 0 0 1 4.242-.827c4.97 0 9.185 3.223 10.675 7.69.12.362.12.752 0 1.113Z" />
                    <path d="M15.75 12c0 .18-.013.357-.037.53l-4.244-4.243A3.75 3.75 0 0 1 15.75 12ZM12.53 15.713l-4.243-4.244a3.75 3.75 0 0 0 4.244 4.243Z" />
                    <path d="M6.75 12c0-.619.107-1.213.304-1.764l-3.1-3.1a11.25 11.25 0 0 0-2.63 4.31c-.12.362-.12.752 0 1.114 1.489 4.467 5.704 7.69 10.675 7.69 1.5 0 2.933-.294 4.242-.827l-2.477-2.477A5.25 5.25 0 0 1 6.75 12Z" />
                  </svg>
                {:else}
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
                    <path d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" />
                    <path fill-rule="evenodd" d="M1.323 11.447C2.811 6.976 7.028 3.75 12.001 3.75c4.97 0 9.185 3.223 10.675 7.69.12.362.12.752 0 1.113-1.487 4.471-5.705 7.697-10.677 7.697-4.97 0-9.186-3.223-10.675-7.69a1.762 1.762 0 0 1 0-1.113ZM17.25 12a5.25 5.25 0 1 1-10.5 0 5.25 5.25 0 0 1 10.5 0Z" clip-rule="evenodd" />
                  </svg>
                {/if}
                {$showUnlistedStore ? "Hide Unlisted" : "Show Unlisted"}
              </button>
              <button
                class="w-full text-left px-4 py-2 hover:bg-secondary-hover flex items-center gap-2"
                onclick={() => { handleUnloadAllModels(); menuOpen = false; }}
                disabled={isUnloading}
              >
                <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-6 h-6">
                  <path fill-rule="evenodd" d="M12 2.25c-5.385 0-9.75 4.365-9.75 9.75s4.365 9.75 9.75 9.75 9.75-4.365 9.75-9.75S17.385 2.25 12 2.25Zm.53 5.47a.75.75 0 0 0-1.06 0l-3 3a.75.75 0 1 0 1.06 1.06l1.72-1.72v5.69a.75.75 0 0 0 1.5 0v-5.69l1.72 1.72a.75.75 0 1 0 1.06-1.06l-3-3Z" clip-rule="evenodd" />
                </svg>
                {isUnloading ? "Unloading..." : "Unload All"}
              </button>
            </div>
          {/if}
        </div>
      {/if}
    </div>
    {#if !$isNarrow}
      <div class="flex justify-between">
        <div class="flex gap-2">
          <button class="btn text-base flex items-center gap-2" onclick={toggleIdorName} style="line-height: 1.2">
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
              <path fill-rule="evenodd" d="M15.97 2.47a.75.75 0 0 1 1.06 0l4.5 4.5a.75.75 0 0 1 0 1.06l-4.5 4.5a.75.75 0 1 1-1.06-1.06l3.22-3.22H7.5a.75.75 0 0 1 0-1.5h11.69l-3.22-3.22a.75.75 0 0 1 0-1.06Zm-7.94 9a.75.75 0 0 1 0 1.06l-3.22 3.22H16.5a.75.75 0 0 1 0 1.5H4.81l3.22 3.22a.75.75 0 1 1-1.06 1.06l-4.5-4.5a.75.75 0 0 1 0-1.06l4.5-4.5a.75.75 0 0 1 1.06 0Z" clip-rule="evenodd" />
            </svg>
            {$showIdorNameStore === "id" ? "ID" : "Name"}
          </button>

          <button class="btn text-base flex items-center gap-2" onclick={toggleShowUnlisted} style="line-height: 1.2">
            {#if $showUnlistedStore}
              <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
                <path d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" />
                <path fill-rule="evenodd" d="M1.323 11.447C2.811 6.976 7.028 3.75 12.001 3.75c4.97 0 9.185 3.223 10.675 7.69.12.362.12.752 0 1.113-1.487 4.471-5.705 7.697-10.677 7.697-4.97 0-9.186-3.223-10.675-7.69a1.762 1.762 0 0 1 0-1.113ZM17.25 12a5.25 5.25 0 1 1-10.5 0 5.25 5.25 0 0 1 10.5 0Z" clip-rule="evenodd" />
              </svg>
            {:else}
              <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
                <path d="M3.53 2.47a.75.75 0 0 0-1.06 1.06l18 18a.75.75 0 1 0 1.06-1.06l-18-18ZM22.676 12.553a11.249 11.249 0 0 1-2.631 4.31l-3.099-3.099a5.25 5.25 0 0 0-6.71-6.71L7.759 4.577a11.217 11.217 0 0 1 4.242-.827c4.97 0 9.185 3.223 10.675 7.69.12.362.12.752 0 1.113Z" />
                <path d="M15.75 12c0 .18-.013.357-.037.53l-4.244-4.243A3.75 3.75 0 0 1 15.75 12ZM12.53 15.713l-4.243-4.244a3.75 3.75 0 0 0 4.244 4.243Z" />
                <path d="M6.75 12c0-.619.107-1.213.304-1.764l-3.1-3.1a11.25 11.25 0 0 0-2.63 4.31c-.12.362-.12.752 0 1.114 1.489 4.467 5.704 7.69 10.675 7.69 1.5 0 2.933-.294 4.242-.827l-2.477-2.477A5.25 5.25 0 0 1 6.75 12Z" />
              </svg>
            {/if}
            unlisted
          </button>
        </div>
        <button class="btn text-base flex items-center gap-2" onclick={handleUnloadAllModels} disabled={isUnloading}>
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-6 h-6">
            <path fill-rule="evenodd" d="M12 2.25c-5.385 0-9.75 4.365-9.75 9.75s4.365 9.75 9.75 9.75 9.75-4.365 9.75-9.75S17.385 2.25 12 2.25Zm.53 5.47a.75.75 0 0 0-1.06 0l-3 3a.75.75 0 1 0 1.06 1.06l1.72-1.72v5.69a.75.75 0 0 0 1.5 0v-5.69l1.72 1.72a.75.75 0 1 0 1.06-1.06l-3-3Z" clip-rule="evenodd" />
          </svg>
          {isUnloading ? "Unloading..." : "Unload All"}
        </button>
      </div>
    {/if}
  </div>

  {#snippet modelRow(model: Model, group: ModelGroup | null, variant: boolean)}
    <tr class="border-b hover:bg-secondary-hover border-gray-200">
      <td class="{variant ? 'pl-8 ' : ''}{model.unlisted ? 'text-txtsecondary' : ''}">
        <div class="flex items-center gap-2">
          {#if group}
            <button
              class="text-txtsecondary hover:text-txt"
              onclick={() => toggleFamily(group.key)}
              aria-label="Toggle variants"
              aria-expanded={!!expandedFamilies[group.key]}
            >
              <svg
                xmlns="http://www.w3.org/2000/svg"
                viewBox="0 0 20 20"
                fill="currentColor"
                class="w-4 h-4 transition-transform {expandedFamilies[group.key] ? 'rotate-90' : ''}"
              >
                <path
                  fill-rule="evenodd"
                  d="M7.21 14.77a.75.75 0 0 1 .02-1.06L11.168 10 7.23 6.29a.75.75 0 1 1 1.04-1.08l4.5 4.25a.75.75 0 0 1 0 1.08l-4.5 4.25a.75.75 0 0 1-1.06-.02Z"
                  clip-rule="evenodd"
                />
              </svg>
            </button>
          {/if}
          <a href="/upstream/{model.id}/" class="font-semibold" target="_blank">
            {getModelDisplay(model)}
          </a>
          {#if group}
            <button class="text-xs text-txtsecondary hover:underline" onclick={() => toggleFamily(group.key)}>
              {group.variants.length + 1} variants
            </button>
            {#if activeCount(group) > 0}
              <span class="text-xs status status--ready px-1.5">{activeCount(group)} on</span>
            {/if}
          {/if}
        </div>
        {#if model.description}
          <p class={model.unlisted ? "text-opacity-70" : ""}><em>{model.description}</em></p>
        {/if}
        {#if model.aliases && model.aliases.length > 0}
          <p class="text-xs text-txtsecondary">Aliases: {model.aliases.join(", ")}</p>
        {/if}
      </td>
      <td class="w-10 align-middle text-center">
        {#if !variant}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-background transition-colors"
            onclick={() => openConfig(model.id)}
            aria-label="Edit model parameters"
            title="Edit parameters / create variant"
          >
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-4 h-4">
              <path fill-rule="evenodd" d="M8.34 1.804A1 1 0 0 1 9.32 1h1.36a1 1 0 0 1 .98.804l.295 1.473c.497.144.97.342 1.41.587l1.25-.834a1 1 0 0 1 1.262.125l.962.962a1 1 0 0 1 .125 1.262l-.834 1.25c.245.44.443.913.587 1.41l1.473.294a1 1 0 0 1 .804.98v1.361a1 1 0 0 1-.804.98l-1.473.295a6.95 6.95 0 0 1-.587 1.41l.834 1.25a1 1 0 0 1-.125 1.262l-.962.962a1 1 0 0 1-1.262.125l-1.25-.834c-.44.245-.913.443-1.41.587l-.294 1.473a1 1 0 0 1-.98.804H9.32a1 1 0 0 1-.98-.804l-.295-1.473a6.95 6.95 0 0 1-1.41-.587l-1.25.834a1 1 0 0 1-1.262-.125l-.962-.962a1 1 0 0 1-.125-1.262l.834-1.25a6.95 6.95 0 0 1-.587-1.41l-1.473-.294A1 1 0 0 1 1 10.68V9.32a1 1 0 0 1 .804-.98l1.473-.295c.144-.497.342-.97.587-1.41l-.834-1.25a1 1 0 0 1 .125-1.262l.962-.962A1 1 0 0 1 5.38 3.03l1.25.834c.44-.245.913-.443 1.41-.587l.294-1.473ZM10 13a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" clip-rule="evenodd" />
            </svg>
          </button>
        {/if}
      </td>
      <td class="w-12 align-middle">
        {#if model.state === "stopped" && pendingLoads[model.id]}
          <button class="btn btn--sm" onclick={() => cancelLoad(model.id)}>Cancel</button>
        {:else if model.state === "stopped"}
          <button class="btn btn--sm" onclick={() => handleLoadModel(model.id)}>Load</button>
        {:else}
          <button class="btn btn--sm" onclick={() => unloadSingleModel(model.id)} disabled={model.state !== "ready"}>Unload</button>
        {/if}
      </td>
      <td class="w-20">
        {#if model.state === "stopped" && pendingLoads[model.id]}
          <span class="w-16 text-center status status--queued">queued</span>
        {:else}
          <span class="w-16 text-center status status--{model.state}">{model.state}</span>
        {/if}
      </td>
    </tr>
  {/snippet}

  <div class="flex-1 overflow-y-auto">
    <table class="w-full">
      <thead class="sticky top-0 bg-card z-10">
        <tr class="text-left border-b border-gray-200 dark:border-white/10 bg-surface">
          <th>{$showIdorNameStore === "id" ? "Model ID" : "Name"}</th>
          <th></th>
          <th></th>
          <th>State</th>
        </tr>
      </thead>
      <tbody>
        {#each filteredModels.standaloneModels as model (model.id)}
          {@render modelRow(model, null, false)}
        {/each}
        {#each filteredModels.groups as group (group.key)}
          {@render modelRow(group.primary, group, false)}
          {#if expandedFamilies[group.key]}
            {#each group.variants as model (model.id)}
              {@render modelRow(model, null, true)}
            {/each}
          {/if}
        {/each}
      </tbody>
    </table>

    {#if Object.keys(filteredModels.peerModelsByPeerId).length > 0}
      <h3 class="mt-8 mb-2">Peer Models</h3>
      {#each Object.entries(filteredModels.peerModelsByPeerId).sort(([a], [b]) => a.localeCompare(b)) as [peerId, peerModels] (peerId)}
        <div class="mb-4">
          <table class="w-full">
            <thead class="sticky top-0 bg-card z-10">
              <tr class="text-left border-b border-gray-200 dark:border-white/10 bg-surface">
                <th class="font-semibold">{peerId}</th>
              </tr>
            </thead>
            <tbody>
              {#each peerModels as model (model.id)}
                <tr class="border-b hover:bg-secondary-hover border-gray-200">
                  <td class="pl-8 {model.unlisted ? 'text-txtsecondary' : ''}">
                    <span>{model.id}</span>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/each}
    {/if}
  </div>
</div>

<ModelConfigModal modelId={configModelId} open={configOpen} onclose={closeConfig} />
