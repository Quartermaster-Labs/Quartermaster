<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { push } from "svelte-spa-router";
  import { models, inFlightRequests, unloadAllModels } from "../stores/api";
  import { latestGpu, latestSys } from "../stores/perf";
  import { vramBreakdown } from "../stores/vram";
  import { prettifyModelName, modelCategory, largestModel, modelWeightGB } from "../lib/modelUtils";
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

  // With several models resident the rail names only the biggest one - the strip
  // is a fixed-height row of readouts, and two or three chips pushed the VRAM
  // gauge off the end of it. The rest are one click away in the picker below.
  // One ordering for both the chip and the picker, so the model the rail names
  // is always the one at the top of the list it opens.
  const ordered = $derived(
    liveModels.slice().sort((a, b) => modelWeightGB(b) - modelWeightGB(a) || a.id.localeCompare(b.id)),
  );
  const head = $derived(largestModel(liveModels));
  let pickerOpen = $state(false);
  $effect(() => {
    // Nothing left to pick from once a model unloads: close rather than leave a
    // panel floating over the page listing one model, or none.
    if (liveModels.length < 2) pickerOpen = false;
  });

  function chipClick(): void {
    if (liveModels.length > 1) pickerOpen = !pickerOpen;
    else if (head) openInModels(head);
  }

  function pick(m: Model): void {
    pickerOpen = false;
    openInModels(m);
  }

  function dotClass(state: string): string {
    if (state === "ready") return "bg-success";
    if (state === "starting") return "bg-warning animate-pulse";
    if (state === "stopping") return "bg-error animate-pulse";
    return "bg-txtsecondary";
  }
</script>

<div
  class="flex items-center gap-4 rounded-tl-lg border-y border-l border-border bg-rail shadow-inset-sm px-4 h-10 shrink-0 text-label overflow-x-auto whitespace-nowrap pretty-scroll"
>
  <!-- Loaded model(s) -->
  <div class="flex items-center gap-2 min-w-0">
    {#if liveModels.length === 0}
      <span class="inline-block w-2 h-2 rounded-full bg-txtsecondary"></span>
      <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">No model loaded</span>
    {:else if head}
      <!-- Chip, not bare text: it's the one clickable thing in a rail of
           read-only readouts, so it needs to look pressable. -->
      <button
        class="group flex items-center gap-1.5 min-w-0 rounded border border-transparent px-1.5 py-0.5 transition-colors cursor-pointer hover:border-card-border hover:bg-secondary {pickerOpen ? 'border-card-border bg-secondary' : ''}"
        onclick={chipClick}
        use:tip={liveModels.length > 1
          ? `${liveModels.length} models loaded - show them`
          : `${head.id} - open in Models`}
      >
        <span class="inline-block w-2 h-2 rounded-full shrink-0 {dotClass(head.state)}"></span>
        <span class="font-mono text-micro text-txtsecondary truncate min-w-0 max-w-[14rem] group-hover:text-txtmain">{prettifyModelName(head.name || head.id)}</span>
        {#if liveModels.length > 1}
          <span class="shrink-0 rounded-full border border-card-border px-1 font-mono text-[0.6rem] leading-4 text-txtsecondary tabular-nums">+{liveModels.length - 1}</span>
        {/if}
      </button>
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

<svelte:window onkeydown={(e) => e.key === "Escape" && (pickerOpen = false)} />

{#if pickerOpen}
  <!-- Scrim: catches the click that dismisses the panel, and nothing else. -->
  <button class="fixed inset-0 z-40 cursor-default" aria-label="Close model list" onclick={() => (pickerOpen = false)}></button>
  <!-- Fixed, not absolute: the status rail is an overflow-x-auto strip, so an
       absolutely positioned child of it would be clipped at the rail's edge.
       Same reason (and the same left/top idiom) as DownloadsMenu.
       left-16 clears the collapsed side rail (w-14) by a hair. -->
  <div class="fixed left-16 top-11 z-50 w-80 max-w-[calc(100vw/var(--qm-scale)-5rem)] rounded-md border border-card-border bg-surface shadow-xl overflow-hidden">
    <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner">
      <h6>Loaded models</h6>
      <span class="ml-auto font-mono text-micro text-txtsecondary tabular-nums">{liveModels.length}</span>
    </div>
    <div class="divide-y divide-card-border-inner">
      {#each ordered as m (m.id)}
        <button
          class="w-full flex items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-secondary"
          onclick={() => pick(m)}
          use:tip={`${m.id} - open in Models`}
        >
          <span class="inline-block w-2 h-2 rounded-full shrink-0 {dotClass(m.state)}"></span>
          <span class="font-mono text-micro text-txtmain truncate min-w-0 flex-1">{prettifyModelName(m.name || m.id)}</span>
          <span class="shrink-0 font-mono text-micro text-txtsecondary tabular-nums">
            {modelWeightGB(m) > 0 ? modelWeightGB(m).toFixed(1) + "G" : m.state}
          </span>
        </button>
      {/each}
    </div>
  </div>
{/if}
