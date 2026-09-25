<script lang="ts">
  import { tip } from "../lib/tooltip";
  import type { HistogramData } from "../lib/types";

  // HTML bars, not an SVG viewBox: a viewBox scales its text with the chart's
  // width, so at page width the axis numbers came out several times the size of
  // the table beside them, in the SVG's own sans font. Here the labels are
  // ordinary text on the page's type scale and only the bars stretch.
  let {
    data,
    values,
    label,
    unit = "t/s",
    barClass = "bg-primary/45 hover:bg-primary/75",
  }: {
    data: HistogramData;
    // The raw samples behind `data`; with fewer than STRIP_BELOW of them the
    // chart draws one tick per sample instead of bins.
    values?: number[];
    label: string;
    unit?: string;
    barClass?: string;
  } = $props();

  const STRIP_BELOW = 10;

  let maxCount = $derived(Math.max(1, ...data.bins));
  let total = $derived(data.bins.reduce((a, b) => a + b, 0));
  let range = $derived(data.max - data.min);

  function fmt(v: number): string {
    return v >= 100 ? v.toFixed(0) : v.toFixed(1);
  }

  // A single-valued sample has no range to place a marker in; centre it over
  // the one bar rather than pinning it to the left edge.
  function pos(v: number): number {
    return range > 0 ? ((v - data.min) / range) * 100 : 50;
  }

  // Strip mode only: the extremes ARE the axis ends, so with two samples both
  // ticks sat on the frame and vanished into it. Pull them 2% inside.
  function inset(p: number): number {
    return 2 + p * 0.96;
  }

  function binTip(i: number): string {
    const lo = data.min + i * data.binSize;
    const n = data.bins[i];
    const span = data.binSize > 0 ? `${fmt(lo)}-${fmt(lo + data.binSize)}` : fmt(lo);
    return `${span} ${unit} · ${n} request${n === 1 ? "" : "s"}`;
  }
</script>

<div class="min-w-0">
  <div class="flex items-baseline gap-2 mb-1.5 font-mono text-micro uppercase tracking-wide text-txtsecondary tabular-nums">
    <span>{label}</span>
    <span class="ml-auto normal-case tracking-normal">
      p50 <span class="text-txtmain">{fmt(data.p50)}</span>
      · p95 <span class="text-txtmain">{fmt(data.p95)}</span>
      {unit} · n {total}
    </span>
  </div>

  <!-- Same 64px box in both modes, so the page doesn't jump as requests land. -->
  <div class="relative h-16 flex items-end gap-1 border-b border-card-border">
    {#if values && values.length < STRIP_BELOW}
      <!-- Too few samples to bin: calculateHistogramData always makes at
           least 5 bins, so two requests drew as two full-height slabs at the
           edges with nothing between. One tick per request is the honest
           picture until there are enough to show a shape. -->
      {#each values as v, i (i)}
        <div
          class="absolute bottom-0 h-full w-1 -translate-x-1/2 rounded-t-sm transition-colors {barClass}"
          style="left:{inset(pos(v))}%"
          use:tip={`${fmt(v)} ${unit}`}
        ></div>
      {/each}
    {:else}
      {#each data.bins as count, i}
        <div class="flex-1 h-full flex items-end" use:tip={binTip(i)}>
          {#if count > 0}
            <div class="w-full rounded-t-sm transition-colors {barClass}" style="height:{(count / maxCount) * 100}%"></div>
          {/if}
        </div>
      {/each}
    {/if}

    <!-- The median is the one marker worth a line: p95 is already in the
         header, and a second rule over a dozen bars reads as another bar. -->
    <div class="pointer-events-none absolute -top-1 bottom-0 w-px bg-txtmain/50" style="left:{values && values.length < STRIP_BELOW ? inset(pos(data.p50)) : pos(data.p50)}%"></div>
  </div>

  <div class="flex justify-between mt-1 font-mono text-micro text-txtsecondary tabular-nums">
    <span>{fmt(data.min)}</span>
    <span>{fmt(data.max)}</span>
  </div>
</div>
