<script lang="ts">
  import { onMount } from "svelte";
  // Type-only: erased at build, so it does not pull chart.js into the main
  // chunk. The runtime copy is imported on demand in onMount below.
  import type { Chart as ChartJS } from "chart.js";
  import { isDarkMode } from "../stores/theme";
  import { uiScale } from "../stores/uiScale";
  import { pixelRatio } from "../stores/pixelRatio";
  import { cssZoom } from "../lib/uiZoom";

  interface Dataset {
    label: string;
    data: number[];
    borderColor: string;
  }

  interface Props {
    title: string;
    labels: string[];
    datasets: Dataset[];
    yMin?: number;
    yMax?: number;
    yLabel?: string;
    showLegend?: boolean;
  }

  let { title, labels, datasets, yMin, yMax, yLabel, showLegend = true }: Props = $props();

  let canvas: HTMLCanvasElement;
  // $state.raw, not $state: a Chart instance must never be deep-proxied.
  // Raw still makes the assignment reactive, so the effects below re-run
  // once the async import resolves and the chart finally exists.
  let chart = $state.raw<ChartJS | undefined>();

  function getChartColors(dark: boolean) {
    return {
      grid: dark ? "rgba(255,255,255,0.08)" : "rgba(0,0,0,0.08)",
      tick: dark ? "#9ca3af" : "#6b7280",
      legend: dark ? "#d1d5db" : "#374151",
      tooltipBg: dark ? "#1f2937" : "#ffffff",
      tooltipText: dark ? "#f3f4f6" : "#111827",
      tooltipBorder: dark ? "#374151" : "#e5e7eb",
    };
  }

  // The canvas backing store has to cover the interface zoom as well as the
  // display's own pixel ratio: chart.js sizes it from the element's LOCAL css
  // size (canvas.clientWidth), which `zoom` then paints larger. Without this the
  // charts are a blurry upscale at any interface size above 100%.
  //
  // The display ratio comes from the store, not from `window.devicePixelRatio`,
  // so that a ratio which moves after mount (browser zoom, a display-scale
  // change, a drag to a monitor with a different scale) re-runs the effect
  // below. Pinning `options.devicePixelRatio` also disables chart.js's own
  // ratio watcher - it compares against the platform value and we have just
  // overridden it - so this store is the only thing left watching.
  function backingRatio(): number {
    return $pixelRatio * cssZoom(canvas);
  }

  // chart.js derives the hit position from the native event, and its own idea of
  // where the canvas is. Under `zoom` those disagree (see lib/uiZoom.ts), which
  // put the tooltip on the wrong sample. Recompute from the bounding rect - the
  // one measurement that is unambiguously in visual pixels - and divide.
  const zoomFix = {
    id: "qm-zoom-fix",
    beforeEvent(c: ChartJS, args: { event: { type: string; x: number | null; y: number | null; native: Event | null } }) {
      const ev = args.event;
      // mouseout carries no position; writing one keeps the tooltip alive.
      if (!ev || ev.type === "mouseout") return;
      const ne = ev.native as MouseEvent | null;
      if (!ne || typeof ne.clientX !== "number") return;
      const r = c.canvas.getBoundingClientRect();
      const z = cssZoom(c.canvas);
      ev.x = (ne.clientX - r.left) / z;
      ev.y = (ne.clientY - r.top) / z;
    },
  };

  function buildOptions(dark: boolean) {
    const colors = getChartColors(dark);
    return {
      devicePixelRatio: backingRatio(),
      responsive: true,
      maintainAspectRatio: false,
      animation: false as const,
      interaction: {
        mode: "index" as const,
        intersect: false,
      },
      plugins: {
        legend: {
          display: showLegend,
          position: "top" as const,
          labels: {
            color: colors.legend,
            usePointStyle: true,
            pointStyle: "circle" as const,
            padding: 12,
            font: { size: 11 },
          },
        },
        title: {
          display: true,
          text: title,
          color: colors.legend,
          font: { size: 14, weight: "bold" as const },
        },
        tooltip: {
          backgroundColor: colors.tooltipBg,
          titleColor: colors.tooltipText,
          bodyColor: colors.tooltipText,
          borderColor: colors.tooltipBorder,
          borderWidth: 1,
        },
      },
      scales: {
        x: {
          bounds: "data" as const,
          offset: false,
          ticks: { color: colors.tick, maxRotation: 0, font: { size: 10 }, maxTicksLimit: 10 },
          grid: { color: colors.grid },
        },
        y: {
          min: yMin,
          max: yMax,
          ticks: { color: colors.tick, font: { size: 10 } },
          grid: { color: colors.grid },
          title: yLabel
            ? { display: true, text: yLabel, color: colors.tick }
            : undefined,
        },
      },
    };
  }

  onMount(() => {
    // chart.js is the single biggest dependency in the bundle and only two
    // places need it (here, and the markdown chart blocks in lib/diagrams.ts).
    // BOTH must import it dynamically: one static import anywhere folds it
    // back into the main chunk and silently defeats the other split.
    let disposed = false;
    void (async () => {
      const { Chart, registerables } = await import("chart.js");
      Chart.register(...registerables);
      if (disposed) return;
      chart = new Chart(canvas, {
        type: "line",
        data: {
          labels: [...labels],
          datasets: datasets.map((ds) => ({
            label: ds.label,
            data: [...ds.data],
            borderColor: ds.borderColor,
            backgroundColor: ds.borderColor + "20",
            borderWidth: 1.5,
            pointRadius: 0,
            tension: 0.4,
            fill: false,
          })),
        },
        options: buildOptions($isDarkMode),
        plugins: [zoomFix],
      });
    })();

    return () => {
      disposed = true;
      chart?.destroy();
      chart = undefined;
    };
  });

  $effect(() => {
    if (!chart) return;
    const _dark = $isDarkMode;
    // $uiScale and $pixelRatio are dependencies, not arguments: either moving
    // means a new backing ratio, and resize() is what re-allocates the canvas
    // for it.
    void $uiScale;
    void $pixelRatio;
    chart.options = buildOptions(_dark);
    chart.resize();
    chart.update("none");
  });

  $effect(() => {
    if (!chart) return;
    const _l = labels;
    const _d = datasets;
    chart.data.labels = [..._l];
    chart.data.datasets = _d.map((ds) => ({
      label: ds.label,
      data: [...ds.data],
      borderColor: ds.borderColor,
      backgroundColor: ds.borderColor + "20",
      borderWidth: 1.5,
      pointRadius: 0,
      tension: 0.4,
      fill: false,
    }));
    chart.update("none");
  });
</script>

<!-- No card: the charts tile a hairline grid that spans the page, so the
     separation between them is the parent's gap-px, not a border each. -->
<div class="bg-surface p-4 h-[300px]">
  <canvas bind:this={canvas}></canvas>
</div>
