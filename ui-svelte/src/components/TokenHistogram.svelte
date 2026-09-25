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
    // Percentiles for the header; the bars are binned here from `values`.
    data: HistogramData;
    values: number[];
    label: string;
    unit?: string;
    barClass?: string;
  } = $props();

  // Fine bins, one chart at every sample count: a handful of requests land as
  // separate thin ticks, a few hundred build up into a histogram. The old
  // 5-bin chart drew 11 samples as fat slabs and 2 as a pair at the edges.
  const BINS = 40;
  // The axis spans at least this share of the median. Fitting it to min..max
  // stretched 896 vs 901 t/s (0.5% apart, i.e. noise) across the full width,
  // as if they were far apart. With the floor, a tight cluster reads as tight.
  const MIN_SPAN = 0.2;

  function niceStep(raw: number): number {
    const mag = 10 ** Math.floor(Math.log10(raw));
    const f = raw / mag;
    return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * mag;
  }

  let axis = $derived.by(() => {
    const min = Math.min(...values);
    const max = Math.max(...values);
    const span = Math.max(max - min, data.p50 * MIN_SPAN, 1e-9);
    const mid = (min + max) / 2;
    const step = niceStep(span / 4);
    const lo = Math.max(0, Math.floor((mid - span / 2) / step) * step);
    const hi = Math.ceil((mid + span / 2) / step) * step;
    return { lo, hi, range: hi - lo };
  });

  let bins = $derived.by(() => {
    const b = new Array(BINS).fill(0);
    for (const v of values) {
      b[Math.min(BINS - 1, Math.floor(((v - axis.lo) / axis.range) * BINS))]++;
    }
    return b;
  });
  let maxCount = $derived(Math.max(1, ...bins));

  function fmt(v: number): string {
    return v >= 100 ? v.toFixed(0) : v.toFixed(1);
  }

  function pos(v: number): number {
    return ((v - axis.lo) / axis.range) * 100;
  }

  function binTip(i: number): string {
    const w = axis.range / BINS;
    const lo = axis.lo + i * w;
    const n = bins[i];
    return `${fmt(lo)}-${fmt(lo + w)} ${unit} · ${n} request${n === 1 ? "" : "s"}`;
  }
</script>

<div class="min-w-0">
  <div class="flex items-baseline gap-2 mb-2 font-mono text-micro uppercase tracking-wide text-txtsecondary tabular-nums">
    <span>{label}</span>
    <span class="ml-auto normal-case tracking-normal">
      p50 <span class="text-txtmain">{fmt(data.p50)}</span>
      · p95 <span class="text-txtmain">{fmt(data.p95)}</span>
      {unit} · n {values.length}
    </span>
  </div>

  <div class="relative h-16 flex items-end gap-0.5 border-b border-card-border">
    {#each bins as count, i}
      <div class="flex-1 h-full flex items-end" use:tip={count > 0 ? binTip(i) : undefined}>
        {#if count > 0}
          <!-- Floor at 12% so a lone sample in a busy chart stays visible. -->
          <div class="w-full rounded-t-[1px] transition-colors {barClass}" style="height:{Math.max(12, (count / maxCount) * 100)}%"></div>
        {/if}
      </div>
    {/each}

    <!-- Median: dashed and capped, so it can't be read as one more sample. -->
    <div class="pointer-events-none absolute -top-1.5 bottom-0 -translate-x-1/2 flex flex-col items-center" style="left:{pos(data.p50)}%">
      <div class="size-1.5 rounded-full bg-txtmain/70"></div>
      <div class="flex-1 border-l border-dashed border-txtmain/50"></div>
    </div>
  </div>

  <div class="flex justify-between mt-1 font-mono text-micro text-txtsecondary tabular-nums">
    <span>{fmt(axis.lo)}</span>
    <span>{fmt(axis.lo + axis.range / 2)}</span>
    <span>{fmt(axis.hi)}</span>
  </div>
</div>
