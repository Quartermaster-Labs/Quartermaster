<script lang="ts">
  import { tip } from "../lib/tooltip";
  // Horizontal VRAM usage bar. Two modes:
  //  - single (default): one fill from used/total. Color shifts flame → warning
  //    (>85%) → error (>95%, OOM risk).
  //  - segmented: pass `segments` to split the fill (e.g. System vs Model). Each
  //    segment is hoverable (native tooltip) and shown in a small legend.
  interface Segment {
    label: string;
    mb: number;
    class: string;
    detail?: string;
  }

  let {
    usedMb,
    totalMb,
    height = "0.5rem",
    showLabel = true,
    segments,
    showLegend = true,
  }: {
    usedMb: number;
    totalMb: number;
    height?: string;
    showLabel?: boolean;
    segments?: Segment[];
    showLegend?: boolean;
  } = $props();

  const pct = $derived(totalMb > 0 ? Math.min(100, (usedMb / totalMb) * 100) : 0);
  const gb = (mb: number): string => (mb / 1024).toFixed(1);
  const barColor = $derived(pct >= 95 ? "bg-error" : pct >= 85 ? "bg-warning" : "bg-primary");
  const segPct = (mb: number): number => (totalMb > 0 ? Math.min(100, (mb / totalMb) * 100) : 0);
</script>

<div class="w-full">
  <div class="flex w-full rounded-full bg-secondary overflow-hidden" style="height: {height}">
    {#if segments && segments.length}
      {#each segments as seg (seg.label)}
        <div
          class="h-full {seg.class} transition-all duration-500"
          style="width: {segPct(seg.mb)}%"
          use:tip={`${seg.label}: ${gb(seg.mb)} GB - ${seg.detail ?? ''}`}
        ></div>
      {/each}
    {:else}
      <div class="h-full {barColor} transition-all duration-500" style="width: {pct}%"></div>
    {/if}
  </div>

  {#if showLabel}
    <div class="mt-1 flex justify-between font-mono text-xs text-txtsecondary tabular-nums">
      <span>{gb(usedMb)} / {gb(totalMb)} GB</span>
      <span>{pct.toFixed(0)}%</span>
    </div>
  {/if}

  {#if segments && segments.length && showLegend}
    <div class="mt-1.5 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[0.65rem] text-txtsecondary">
      {#each segments as seg (seg.label)}
        <span class="flex items-center gap-1.5" use:tip={seg.detail}>
          <span class="inline-block w-2 h-2 rounded-sm {seg.class}"></span>
          <span class="uppercase tracking-wide">{seg.label}</span>
          <span class="text-txtmain tabular-nums">{gb(seg.mb)}G</span>
        </span>
      {/each}
    </div>
  {/if}
</div>
