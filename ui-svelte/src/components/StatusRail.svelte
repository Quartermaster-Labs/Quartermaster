<script lang="ts">
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
  class="flex items-center gap-4 border-b border-border bg-surface px-4 h-10 shrink-0 font-mono text-xs overflow-x-auto whitespace-nowrap pretty-scroll"
>
  <!-- Loaded model(s) -->
  <div class="flex items-center gap-2 min-w-0">
    {#if liveModels.length === 0}
      <span class="inline-block w-2 h-2 rounded-full bg-txtsecondary"></span>
      <span class="text-txtsecondary uppercase tracking-wide">No model loaded</span>
    {:else}
      {#each liveModels as m (m.id)}
        <button
          class="group flex items-center gap-1.5 min-w-0 transition-colors cursor-pointer"
          onclick={() => openInModels(m)}
          title="{m.id} - open in Models"
        >
          <span class="inline-block w-2 h-2 rounded-full shrink-0 {dotClass(m.state)}"></span>
          <span class="text-[0.6rem] uppercase tracking-widest text-txtsecondary truncate min-w-0 max-w-[14rem] group-hover:text-txtmain">{prettifyModelName(m.name || m.id)}</span>
        </button>
      {/each}
    {/if}
  </div>

  <div class="h-4 w-px bg-border"></div>

  <!-- VRAM -->
  <div class="flex items-center gap-2 w-56 shrink-0">
    <span class="text-txtsecondary uppercase tracking-wide">VRAM</span>
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
      <span class="text-txtmain tabular-nums shrink-0">
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
      <span class="text-txtsecondary uppercase tracking-wide">RAM</span>
      <span class="text-txtmain tabular-nums">
        {($latestSys.mem_used_mb / 1024).toFixed(1)}/{($latestSys.mem_total_mb / 1024).toFixed(1)}G
      </span>
    </div>
  {/if}

  <!-- In-flight -->
  <div class="h-4 w-px bg-border"></div>
  <div class="flex items-center gap-1.5 shrink-0">
    <span class="text-txtsecondary uppercase tracking-wide">Reqs</span>
    <span class="text-txtmain tabular-nums">{$inFlightRequests} in-flight</span>
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
