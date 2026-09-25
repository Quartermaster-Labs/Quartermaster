<script lang="ts" module>
  export interface SpeedPoint {
    id: number;
    model: string;
    value: number;
  }
</script>

<script lang="ts">
  import { tip } from "../lib/tooltip";

  // One bar per request, oldest to newest, on an axis from 0. This replaced a
  // histogram: a distribution needs dozens of samples before its shape means
  // anything, and at the counts this page usually holds it drew a few pillars
  // far apart with a median line in the gap between them. What an operator
  // wants from the panel is "is speed steady, and did it drop", and a 0-based
  // bar per request answers that at n=2 as well as at n=200.
  let {
    points,
    p50,
    p95,
    label,
    unit = "t/s",
    barClass = "bg-primary/45 hover:bg-primary/75",
  }: {
    points: SpeedPoint[];
    p50: number;
    p95: number;
    label: string;
    unit?: string;
    barClass?: string;
  } = $props();

  // Past this many the bars go sub-pixel; show the newest and say so.
  const MAX_BARS = 120;

  let shown = $derived(points.slice(-MAX_BARS));

  function niceStep(raw: number): number {
    const mag = 10 ** Math.floor(Math.log10(raw));
    const f = raw / mag;
    return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * mag;
  }

  let top = $derived.by(() => {
    const max = Math.max(...shown.map((p) => p.value), 1e-9);
    const step = niceStep(max / 2);
    return Math.ceil(max / step) * step;
  });

  function fmt(v: number): string {
    return v >= 100 ? v.toFixed(0) : v.toFixed(1);
  }
</script>

<div class="min-w-0">
  <div class="flex items-baseline gap-2 mb-2 font-mono text-micro uppercase tracking-wide text-txtsecondary tabular-nums">
    <span>{label}</span>
    <span class="ml-auto normal-case tracking-normal">
      p50 <span class="text-txtmain">{fmt(p50)}</span>
      · p95 <span class="text-txtmain">{fmt(p95)}</span>
      {unit}
    </span>
  </div>

  <!-- y labels in their own gutter column, so they never sit on a bar -->
  <div class="grid grid-cols-[auto_1fr] gap-x-2 gap-y-1 font-mono text-micro text-txtsecondary tabular-nums">
    <!-- Transforms, not margins: they centre each label on its line without
         moving the column's box, which the grid row height is measured from. -->
    <div class="flex flex-col justify-between h-16 text-right leading-none">
      <span class="-translate-y-1/2">{fmt(top)}</span>
      <span class="translate-y-1/2">0</span>
    </div>

    <div class="relative h-16 border-b border-card-border">
      <div class="pointer-events-none absolute inset-x-0 top-0 border-t border-dashed border-card-border"></div>
      <!-- Newest at the right, bars capped in width so two requests are two
           bars, not two slabs across the whole panel. -->
      <div class="absolute inset-0 flex items-end justify-end gap-0.5">
        {#each shown as p (p.id)}
          <div
            class="flex-1 max-w-4 rounded-t-[1px] transition-colors {barClass}"
            style="height:{(p.value / top) * 100}%"
            use:tip={`#${p.id} · ${fmt(p.value)} ${unit} · ${p.model}`}
          ></div>
        {/each}
      </div>
    </div>

    <div></div>
    <div class="flex justify-between">
      <span>{points.length > MAX_BARS ? `last ${MAX_BARS} of ${points.length}` : `${points.length} request${points.length === 1 ? "" : "s"}`}</span>
      <span>newest</span>
    </div>
  </div>
</div>
