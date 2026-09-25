<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { push } from "svelte-spa-router";
  import {
    models,
    inFlightRequests,
    unloadAllModels,
    fetchInflight,
    fetchInflightRequest,
    type InflightRequest,
  } from "../stores/api";
  import { latestSys, vramTotals } from "../stores/perf";
  import { vramBreakdown } from "../stores/vram";
  import { prettifyModelName, modelCategory, largestModel, modelWeightGB } from "../lib/modelUtils";
  import type { Model, ReqRespCapture } from "../lib/types";
  import { toLocalPx } from "../lib/uiZoom";
  import { ChevronDown } from "lucide-svelte";
  import VramGauge from "./VramGauge.svelte";
  import DownloadsMenu from "./DownloadsMenu.svelte";
  import CaptureDialog from "./CaptureDialog.svelte";

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

  // Pressure lives on the NUMBER, not the bar: the rail's gauge is segmented,
  // and the segments carry what is using VRAM, so the bar cannot also go red.
  // RAM gets the same rule - on this box a RAM OOM takes the machine down,
  // which is worse than a failed model load.
  function pressureClass(used: number, total: number): string {
    const p = total > 0 ? used / total : 0;
    return p >= 0.95 ? "text-error" : p >= 0.85 ? "text-warning" : "text-txtmain";
  }

  // Rail model name, by state. Motion means "in transition", and the sweep's
  // direction says which way: loading sweeps in warm, stopping sweeps back out
  // in the error tint. Ready is the only state at full contrast.
  function nameClass(state: string): string {
    if (state === "ready") return "text-txtmain";
    if (state === "starting") return "rail-sweep";
    if (state === "stopping") return "rail-sweep rail-sweep--out";
    return "text-txtsecondary";
  }

  const othersMoving = $derived(
    liveModels.some((m) => m.id !== head?.id && (m.state === "starting" || m.state === "stopping")),
  );

  // In-flight panel: the requests behind the count, for the ones that run long
  // enough to click (a short chat turn is gone before the panel opens, and
  // lands in Activity on its own). Click, not hover: a hover panel with
  // clickable rows closes as the pointer crosses the gap to reach it.
  let flightOpen = $state(false);
  let flightBtn: HTMLButtonElement | undefined = $state();
  let flightLeft = $state(0);
  let flights = $state<InflightRequest[]>([]);
  let flightNote = $state<string | null>(null);
  let now = $state(Date.now());
  let viewCapture = $state<ReqRespCapture | null>(null);
  let viewOpen = $state(false);

  function toggleFlights(): void {
    if (flightOpen) {
      flightOpen = false;
      return;
    }
    // Anchored under the readout. Fixed, like the model picker, because the
    // rail is an overflow-x strip that would clip an absolute child; the rect
    // is visual px, so it goes through toLocalPx (see lib/uiZoom.ts).
    if (flightBtn) {
      const left = toLocalPx(flightBtn.getBoundingClientRect().left, flightBtn);
      const maxLeft = toLocalPx(window.innerWidth, flightBtn) - 25 * 16;
      flightLeft = Math.max(8, Math.min(left, maxLeft));
    }
    flightNote = null;
    flightOpen = true;
  }

  // While open, re-list every second. That both ticks the elapsed column and
  // picks up the model name, which the server only learns once the request
  // gets as far as resolving one - a list fetched once on open would keep it
  // blank.
  $effect(() => {
    if (!flightOpen) return;
    const tick = async (): Promise<void> => {
      now = Date.now();
      flights = await fetchInflight();
    };
    void tick();
    const t = setInterval(() => void tick(), 1000);
    return () => clearInterval(t);
  });

  async function openFlight(f: InflightRequest): Promise<void> {
    const d = await fetchInflightRequest(f.id);
    if (!d) {
      flightNote = "That one just finished - it's in Activity now.";
      return;
    }
    viewCapture = d.capture;
    viewOpen = true;
    flightOpen = false;
  }

  function openActivity(): void {
    flightOpen = false;
    push("/activity");
  }

  function elapsed(started: string): string {
    const s = Math.max(0, Math.floor((now - Date.parse(started)) / 1000));
    return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  }

  // Still used by the multi-model picker, where several rows need telling apart
  // at a glance and there is no dashboard card doing it for them.
  function dotClass(state: string): string {
    if (state === "ready") return "bg-success";
    if (state === "starting") return "bg-warning animate-pulse";
    if (state === "stopping") return "bg-error animate-pulse";
    return "bg-txtsecondary";
  }
</script>

<!-- Every readout keeps its label at every width: tried shedding them via
     container queries as the rail narrowed, and a bare "14.7/31.1G" left you
     hovering to find out which memory it was. A narrow rail scrolls instead. -->
<div
  class="flex items-center gap-5 rounded-tl-lg border-t border-l border-border bg-rail px-4 h-10 shrink-0 text-label overflow-x-auto whitespace-nowrap pretty-scroll"
>
  <!-- Loaded model(s) -->
  <div class="flex items-center gap-2 min-w-0">
    {#if liveModels.length === 0}
      <!-- A state, not a label: normal case, so it doesn't read as one more
           VRAM/RAM heading waiting for a value. -->
      <span class="text-micro text-txtsecondary">No model loaded</span>
    {:else if head}
      <!-- No status dot and no frame: the Dashboard's model card already
           carries a Ready dot, and a second one here was the same fact twice.
           The TEXT is the state instead - dim while it is not serving, a sweep
           while it is moving, full white once ready. The hover wash is what
           says "clickable"; -ml cancels its padding so the name lines up with
           the rail's own edge. -->
      <button
        class="group -ml-1.5 flex items-center gap-1.5 min-w-0 rounded px-1.5 py-0.5 transition-colors cursor-pointer hover:bg-secondary {pickerOpen ? 'bg-secondary' : ''}"
        onclick={chipClick}
        use:tip={liveModels.length > 1
          ? `${liveModels.length} models loaded - show them`
          : `${head.id} - ${head.state === "ready" ? "open in Models" : head.state}`}
      >
        <span class="font-mono text-micro truncate min-w-0 max-w-[14rem] {nameClass(head.state)}">{prettifyModelName(head.name || head.id)}</span>
        {#if liveModels.length > 1}
          <!-- The rail names only the biggest model, so a smaller one loading
               beside it would otherwise be invisible here: the count sweeps. -->
          <span class="shrink-0 font-mono text-micro tabular-nums {othersMoving ? 'rail-sweep' : 'text-txtsecondary'}">+{liveModels.length - 1}</span>
          <ChevronDown class="w-3 h-3 shrink-0 text-txtsecondary transition-transform {pickerOpen ? 'rotate-180' : ''}" />
        {/if}
      </button>
    {/if}
  </div>

  <!-- VRAM -->
  <div class="flex items-center gap-2 w-72 shrink-0">
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">VRAM</span>
    {#if $vramTotals}
      <div class="flex-1">
        <VramGauge
          usedMb={$vramTotals.usedMb}
          totalMb={$vramTotals.totalMb}
          segments={$vramBreakdown?.segments}
          showLabel={false}
          showLegend={false}
          height="0.5rem"
        />
      </div>
      <!-- Free on hover: the Dashboard's "VRAM free" tile was retired in favour
           of this bar, and free is the number you want when deciding what fits. -->
      <!-- Used bright, capacity dim: the used figure is the one that moves.
           Colour is the pressure signal the segmented bar can't give. -->
      <!-- min-w in ch: the figure widens by a digit as it crosses 10G, and
           without a floor that nudges the bar's length with it. -->
      <span
        class="min-w-[10ch] text-right font-mono text-micro tabular-nums shrink-0 {pressureClass($vramTotals.usedMb, $vramTotals.totalMb)}"
        use:tip={`${(($vramTotals.totalMb - $vramTotals.usedMb) / 1024).toFixed(1)}G free${$vramTotals.devices > 1 ? ` across ${$vramTotals.devices} GPUs` : ""}, as reported by the GPU`}
      >
        {($vramTotals.usedMb / 1024).toFixed(1)}<span class="text-txtsecondary">/{($vramTotals.totalMb / 1024).toFixed(1)}G</span>
      </span>
    {:else}
      <span class="text-txtsecondary">-</span>
    {/if}
  </div>

  <!-- RAM: rendered from the first frame with a placeholder, like VRAM. It
       used to mount only once the first perf poll landed, so a whole readout
       popped into the rail a beat after everything else. -->
  <div class="flex items-center gap-1.5 shrink-0">
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">RAM</span>
    {#if $latestSys}
      <span
        class="min-w-[10ch] font-mono text-micro tabular-nums {pressureClass($latestSys.mem_used_mb, $latestSys.mem_total_mb)}"
        use:tip={`System RAM, ${(($latestSys.mem_total_mb - $latestSys.mem_used_mb) / 1024).toFixed(1)}G free`}
      >
        {($latestSys.mem_used_mb / 1024).toFixed(1)}<span class="text-txtsecondary">/{($latestSys.mem_total_mb / 1024).toFixed(1)}G</span>
      </span>
    {:else}
      <span class="min-w-[10ch] font-mono text-micro text-txtsecondary">-</span>
    {/if}
  </div>

  <!-- In-flight: the 0 is quiet (secondary text, the normal case) and a live
       count goes accent with a pulse. Only colour changes - never opacity on
       the group, which faded the label too until the whole readout looked
       absent and seemed to appear from nowhere on the first request. The dot's
       slot is always there so the count doesn't shift when it lights.
       Clickable only while there is something to list; -mx cancels the hover
       wash's padding so the label stays where it was. -->
  <button
    bind:this={flightBtn}
    class="-mx-1.5 flex items-center gap-1.5 shrink-0 rounded px-1.5 py-0.5 transition-colors enabled:cursor-pointer enabled:hover:bg-secondary {flightOpen ? 'bg-secondary' : ''}"
    disabled={$inFlightRequests === 0 && !flightOpen}
    onclick={toggleFlights}
    use:tip={$inFlightRequests > 0 ? "Show running requests" : "No requests running"}
  >
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">In-flight</span>
    <span class="font-mono text-micro tabular-nums {$inFlightRequests > 0 ? 'text-primary' : 'text-txtsecondary'}">{$inFlightRequests}</span>
    <span class="inline-block w-1.5 h-1.5 rounded-full {$inFlightRequests > 0 ? 'bg-primary animate-pulse' : 'bg-transparent'}"></span>
  </button>

  <!-- Unload sits LEFT of Downloads, so Downloads holds the right edge and
       never moves: the button comes and goes into the empty middle of the
       rail. (Hiding it in place left a hole that made Downloads look adrift;
       mounting it right of Downloads shoved the icon sideways.) Red at rest,
       not just on hover: it is the one destructive verb on every page. -->
  <div class="ml-auto flex items-center gap-3 shrink-0">
    {#if liveModels.length > 0}
      <button
        class="btn btn--sm uppercase tracking-wide text-error border-error/50 hover:bg-error/10 hover:border-error"
        onclick={handleUnloadAll}
        disabled={unloading}
      >
        {unloading ? "Unloading…" : "Unload all"}
      </button>
    {/if}
    <!-- Downloads: an icon with a live count, opening a panel (see the
         component for why it is a menu and not a page). -->
    <DownloadsMenu />
  </div>
</div>

<svelte:window
  onkeydown={(e) => {
    if (e.key === "Escape") {
      pickerOpen = false;
      flightOpen = false;
    }
  }}
/>

{#if flightOpen}
  <button class="fixed inset-0 z-40 cursor-default" aria-label="Close request list" onclick={() => (flightOpen = false)}></button>
  <div
    class="fixed top-11 z-50 w-96 max-w-[calc(100vw/var(--qm-scale)-1rem)] rounded-md border border-card-border bg-surface shadow-xl overflow-hidden"
    style:left="{flightLeft}px"
  >
    <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner">
      <h6>In flight</h6>
      <span class="ml-auto font-mono text-micro text-txtsecondary tabular-nums">{flights.length}</span>
    </div>
    {#if flights.length === 0}
      <!-- Stays open when the list empties rather than vanishing under the
           pointer; the request that just ended is one click away. -->
      <div class="px-3 py-3 text-micro text-txtsecondary">
        Nothing running. Finished requests are in
        <button class="text-primary hover:underline cursor-pointer" onclick={openActivity}>Activity</button>.
      </div>
    {:else}
      <div class="divide-y divide-card-border-inner max-h-80 overflow-y-auto pretty-scroll">
        {#each flights as f (f.id)}
          <!-- No body = a GET, or captures off (captureBuffer: 0): nothing to
               open, so the row is inert rather than a dead click. -->
          <button
            class="w-full flex items-center gap-3 px-3 py-2 text-left transition-colors enabled:hover:bg-secondary enabled:cursor-pointer"
            disabled={!f.has_body}
            onclick={() => openFlight(f)}
            use:tip={f.has_body ? "Open the request" : "No request body captured"}
          >
            <span class="w-14 shrink-0 font-mono text-micro text-primary tabular-nums">{elapsed(f.started)}</span>
            <span class="font-mono text-micro text-txtmain truncate min-w-0 flex-1">{f.model ? prettifyModelName(f.model) : "-"}</span>
            <span class="shrink-0 font-mono text-micro text-txtsecondary">{f.path.replace(/^\/v1\//, "")}</span>
          </button>
        {/each}
      </div>
    {/if}
    {#if flightNote}
      <div class="px-3 py-2 border-t border-card-border-inner text-micro text-txtsecondary">
        {flightNote}
        <button class="text-primary hover:underline cursor-pointer" onclick={openActivity}>Open Activity</button>
      </div>
    {/if}
  </div>
{/if}

<CaptureDialog capture={viewCapture} open={viewOpen} pending onclose={() => (viewOpen = false)} />

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

<style>
  /* :global because the classes arrive through nameClass() - the compiler
     cannot see them in the markup and would prune them as unused. Prefixed
     rail- so nothing else picks them up by accident.

     The sweep is a gradient clipped to the glyphs: dim text with a warm band
     (flame orange into warning yellow at the crest) that travels left to
     right. background-position runs 100% -> 0% because at 250% width the
     band starts off the left edge and ends off the right. */
  :global(.rail-sweep) {
    background-image: linear-gradient(
      90deg,
      var(--color-txtsecondary) 0%,
      var(--color-txtsecondary) 38%,
      var(--color-primary) 46%,
      var(--color-warning) 50%,
      var(--color-primary) 54%,
      var(--color-txtsecondary) 62%,
      var(--color-txtsecondary) 100%
    );
    background-size: 250% 100%;
    background-clip: text;
    -webkit-background-clip: text;
    color: transparent;
    animation: rail-sweep 1.8s linear infinite;
  }
  /* Stopping: the same motion run backwards, in the error tint. */
  :global(.rail-sweep--out) {
    background-image: linear-gradient(
      90deg,
      var(--color-txtsecondary) 0%,
      var(--color-txtsecondary) 40%,
      var(--color-error) 50%,
      var(--color-txtsecondary) 60%,
      var(--color-txtsecondary) 100%
    );
    animation-direction: reverse;
  }
  @keyframes rail-sweep {
    from {
      background-position: 100% 0;
    }
    to {
      background-position: 0% 0;
    }
  }
  /* No motion: keep the tint so the state still reads, drop the travel. */
  @media (prefers-reduced-motion: reduce) {
    :global(.rail-sweep) {
      animation: none;
      background: none;
      color: var(--color-warning);
    }
    :global(.rail-sweep--out) {
      color: var(--color-error);
    }
  }
</style>
