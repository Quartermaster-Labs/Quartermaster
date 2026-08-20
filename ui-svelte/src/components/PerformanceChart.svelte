<script lang="ts">
  import { onMount } from "svelte";
  // Type-only: erased at build, so it does not pull chart.js into the main
  // chunk. The runtime copy is imported on demand in onMount below.
  import type { Chart as ChartJS } from "chart.js";
  import { isDarkMode } from "../stores/theme";

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

  function buildOptions(dark: boolean) {
    const colors = getChartColors(dark);
    return {
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
    chart.options = buildOptions(_dark);
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

<div class="card p-4 h-[300px]">
  <canvas bind:this={canvas}></canvas>
</div>
