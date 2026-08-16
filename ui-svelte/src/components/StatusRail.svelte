<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { push } from "svelte-spa-router";
  import { models, inFlightRequests, unloadAllModels } from "../stores/api";
  import { latestGpu, latestSys } from "../stores/perf";
  import { vramBreakdown } from "../stores/vram";
  import { prettifyModelName, modelCategory } from "../lib/modelUtils";
  import type { Model } from "../lib/types";
  import VramGauge from "./VramGauge.svelte";
  import DownloadsMenu from "./DownloadsMenu.svelte";

  // Models currently occupying the GPU (or about to). The whole tool is
  // VRAM-exclusive single-model, so this is the headline state.
  const liveModels = $derived(
    $models.filter((m) => m.state === "ready" || m.state === "starting" || m.state === "stopping"),
  );

  let unloading = $state(false);
  async function handleUnloadAll(): Promise<void> {
    unloading = true;
    try {
      await unloadAllModels();
    } finally {
      unloading = false;
    }
  }

  // Loaded model is already lifted into the Models page top panel, so just route
  // to its category tab and let it show there.
  function openInModels(m: Model): void {
    push(`/models/${modelCategory(m)}`);
  }

  function dotClass(state: string): string {
    if (state === "ready") return "bg-success";
    if (state === "starting") return "bg-warning animate-pulse";
    if (state === "stopping") return "bg-error animate-pulse";
    return "bg-txtsecondary";
  }
</script>

<div
  class="flex items-center gap-4 border-b border-border bg-surface shadow-inset-sm px-4 h-10 shrink-0 text-label overflow-x-auto whitespace-nowrap pretty-scroll"
>
  <!-- Loaded model(s) -->
  <div class="flex items-center gap-2 min-w-0">
    {#if liveModels.length === 0}
      <span class="inline-block w-2 h-2 rounded-full bg-txtsecondary"></span>
      <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">No model loaded</span>
    {:else}
      {#each liveModels as m (m.id)}
        <!-- Chip, not bare text: it's the one clickable thing in a rail of
             read-only readouts, so it needs to look pressable. -->
        <button
          class="group flex items-center gap-1.5 min-w-0 rounded border border-transparent px-1.5 py-0.5 transition-colors cursor-pointer hover:border-card-border hover:bg-secondary"
          onclick={() => openInModels(m)}
          use:tip={`${m.id} - open in Models`}
        >
          <span class="inline-block w-2 h-2 rounded-full shrink-0 {dotClass(m.state)}"></span>
          <span class="font-mono text-micro text-txtsecondary truncate min-w-0 max-w-[14rem] group-hover:text-txtmain">{prettifyModelName(m.name || m.id)}</span>
        </button>
      {/each}
    {/if}
  </div>

  <div class="h-4 w-px bg-border"></div>

  <!-- VRAM -->
  <div class="flex items-center gap-2 w-72 shrink-0">
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">VRAM</span>
    {#if $latestGpu}
      <div class="flex-1">
        <VramGauge
          usedMb={$latestGpu.mem_used_mb}
          totalMb={$latestGpu.mem_total_mb}
          segments={$vramBreakdown?.segments}
          showLabel={false}
          showLegend={false}
          height="0.4rem"
        />
      </div>
      <span class="font-mono text-micro text-txtmain tabular-nums shrink-0">
        {($latestGpu.mem_used_mb / 1024).toFixed(1)}/{($latestGpu.mem_total_mb / 1024).toFixed(1)}G
      </span>
    {:else}
      <span class="text-txtsecondary">-</span>
    {/if}
  </div>

  <!-- RAM -->
  {#if $latestSys}
    <div class="h-4 w-px bg-border"></div>
    <div class="flex items-center gap-1.5 shrink-0">
      <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">RAM</span>
      <span class="font-mono text-micro text-txtmain tabular-nums">
        {($latestSys.mem_used_mb / 1024).toFixed(1)}/{($latestSys.mem_total_mb / 1024).toFixed(1)}G
      </span>
    </div>
  {/if}

  <!-- In-flight -->
  <div class="h-4 w-px bg-border"></div>
  <div class="flex items-center gap-1.5 shrink-0">
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">In-flight</span>
    <span class="font-mono text-micro text-txtmain tabular-nums">{$inFlightRequests}</span>
  </div>

  <!-- Downloads: an icon with a live count, opening a panel (see the component
       for why it is a menu and not a page). -->
  <div class="ml-auto flex items-center gap-2 shrink-0">
    <DownloadsMenu />
  </div>

  <!-- Unload -->
  <div class="shrink-0">
    {#if liveModels.length > 0}
      <button
        class="btn btn--sm uppercase tracking-wide hover:border-error hover:text-error"
        onclick={handleUnloadAll}
        disabled={unloading}
      >
        {unloading ? "Unloading…" : "Unload all"}
      </button>
    {/if}
  </div>
</div>
