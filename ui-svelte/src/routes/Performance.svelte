<script lang="ts">
  import { onMount } from "svelte";
  import { fetchPerformance } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { observeWindowIdx, OBSERVE_WINDOWS } from "../stores/observe";
  import { RefreshCw, Cpu, Gauge, MemoryStick } from "lucide-svelte";
  import type { SysStat, GpuStat } from "../lib/types";
  import PerformanceChart from "../components/PerformanceChart.svelte";

  const COLORS = [
    "#3b82f6",
    "#ef4444",
    "#10b981",
    "#f59e0b",
    "#8b5cf6",
    "#ec4899",
    "#06b6d4",
    "#84cc16",
    "#f97316",
    "#14b8a6",
    "#a855f7",
    "#e11d48",
    "#0ea5e9",
    "#eab308",
    "#d946ef",
    "#22d3ee",
  ];

  const INTERVALS = [
    { label: "Off", ms: 0 },
    { label: "5s", ms: 5000 },
    { label: "10s", ms: 10000 },
    { label: "30s", ms: 30000 },
    { label: "60s", ms: 60000 },
  ] as const;

  let selectedInterval = persistentStore("perf-refresh-interval", 0);
  let sysData = $state<SysStat[]>([]);
  let gpuData = $state<GpuStat[]>([]);
  let refreshing = $state(false);

  let pollTimer: ReturnType<typeof setInterval> | null = null;
  let visible = $state(true);
  let mounted = $state(false);

  function cutoffTime(): number {
    const ms = OBSERVE_WINDOWS[$observeWindowIdx]?.ms ?? 0;
    return ms <= 0 ? 0 : Date.now() - ms;
  }

  function formatDelta(ts: string, refTime: number): string {
    const diffMs = new Date(ts).getTime() - refTime;
    const diffSec = Math.round(diffMs / 1000);
    const absSec = Math.abs(diffSec);
    const sign = diffSec <= 0 ? "-" : "+";
    if (absSec < 60) return `${sign}${absSec}s`;
    const min = Math.floor(absSec / 60);
    const sec = absSec % 60;
    if (sec === 0) return `${sign}${min}m`;
    return `${sign}${min}:${sec.toString().padStart(2, "0")}`;
  }

  const sysLabels = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length === 0) return [];
    const refTime = new Date(stats[stats.length - 1].timestamp).getTime();
    return stats.map((s) => formatDelta(s.timestamp, refTime));
  });

  async function loadAll() {
    const resp = await fetchPerformance();
    if (resp) {
      sysData = resp.sys_stats ?? [];
      gpuData = resp.gpu_stats ?? [];
    }
  }

  async function loadIncremental() {
    const lastTs = sysData.length > 0 ? sysData[sysData.length - 1].timestamp : undefined;
    const resp = await fetchPerformance(lastTs);
    if (resp) {
      const newSys = resp.sys_stats ?? [];
      const newGpu = resp.gpu_stats ?? [];
      if (newSys.length > 0) {
        sysData = [...sysData, ...newSys];
      }
      if (newGpu.length > 0) {
        gpuData = [...gpuData, ...newGpu];
      }
    }
  }

  function startPolling() {
    stopPolling();
    const ms = INTERVALS[$selectedInterval].ms;
    if (ms <= 0) return;
    pollTimer = setInterval(() => {
      if (visible) {
        loadIncremental();
      }
    }, ms);
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function handleVisibility() {
    visible = !document.hidden;
    if (visible && mounted) {
      loadAll().then(() => startPolling());
    } else {
      stopPolling();
    }
  }

  function handleIntervalChange(i: number) {
    $selectedInterval = i;
    if (visible && mounted) {
      startPolling();
    }
  }

  async function manualRefresh() {
    refreshing = true;
    await loadIncremental();
    refreshing = false;
  }

  $effect(() => {
    return () => {
      stopPolling();
    };
  });

  onMount(() => {
    mounted = true;
    document.addEventListener("visibilitychange", handleVisibility);
    loadAll().then(() => startPolling());

    return () => {
      mounted = false;
      stopPolling();
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  });

  // --- System charts (filtered by time window) ---

  const filteredSysStats = $derived(sysData.filter((s) => new Date(s.timestamp).getTime() >= cutoffTime()));

  const cpuDatasets = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length === 0) return [];
    const coreCount = stats[0].cpu_util_per_core.length;
    const datasets = [];
    for (let i = 0; i < coreCount; i++) {
      datasets.push({
        label: `Core ${i}`,
        data: stats.map((s) => s.cpu_util_per_core[i]),
        borderColor: COLORS[i % COLORS.length],
      });
    }
    return datasets;
  });

  const memSwapDatasets = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length === 0) return [];
    return [
      {
        label: "Memory Used %",
        data: stats.map((s) => (s.mem_used_mb / s.mem_total_mb) * 100),
        borderColor: "#3b82f6",
      },
      {
        label: "Swap Used %",
        data: stats.map((s) => (s.swap_total_mb > 0 ? (s.swap_used_mb / s.swap_total_mb) * 100 : 0)),
        borderColor: "#8b5cf6",
      },
    ];
  });

  const latestMemSwap = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length === 0) return null;
    const s = stats[stats.length - 1];
    return {
      mem_total_mb: s.mem_total_mb,
      mem_used_mb: s.mem_used_mb,
      mem_used_pct: ((s.mem_used_mb / s.mem_total_mb) * 100).toFixed(1),
      swap_total_mb: s.swap_total_mb,
      swap_used_mb: s.swap_used_mb,
      swap_used_pct: s.swap_total_mb > 0 ? ((s.swap_used_mb / s.swap_total_mb) * 100).toFixed(1) : null,
    };
  });

  const loadDatasets = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length === 0) return [];
    return [
      {
        label: "1 min",
        data: stats.map((s) => s.load_avg_1),
        borderColor: "#10b981",
      },
      {
        label: "5 min",
        data: stats.map((s) => s.load_avg_5),
        borderColor: "#f59e0b",
      },
      {
        label: "15 min",
        data: stats.map((s) => s.load_avg_15),
        borderColor: "#ef4444",
      },
    ];
  });

  const netBandwidthDatasets = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length < 2) return [];

    const ifaceNames = new Set<string>();
    for (const s of stats) {
      for (const n of s.net_io ?? []) {
        ifaceNames.add(n.name);
      }
    }

    const interfaces = [...ifaceNames].sort();
    if (interfaces.length === 0) return [];

    const datasets: { label: string; data: number[]; borderColor: string }[] = [];
    let colorIdx = 0;

    for (const iface of interfaces) {
      const recvData: number[] = [];
      const sentData: number[] = [];

      for (let i = 1; i < stats.length; i++) {
        const prev = stats[i - 1];
        const curr = stats[i];
        const prevIO = (prev.net_io ?? []).find((n) => n.name === iface);
        const currIO = (curr.net_io ?? []).find((n) => n.name === iface);

        if (!prevIO || !currIO) {
          recvData.push(0);
          sentData.push(0);
          continue;
        }

        const dtMs = new Date(curr.timestamp).getTime() - new Date(prev.timestamp).getTime();
        if (dtMs <= 0) {
          recvData.push(0);
          sentData.push(0);
          continue;
        }

        const dtSec = dtMs / 1000;
        recvData.push((((currIO.bytes_recv - prevIO.bytes_recv) / dtSec) * 8) / 1_000_000);
        sentData.push((((currIO.bytes_sent - prevIO.bytes_sent) / dtSec) * 8) / 1_000_000);
      }

      datasets.push({
        label: `${iface} in`,
        data: recvData,
        borderColor: COLORS[colorIdx % COLORS.length],
      });
      colorIdx++;
      datasets.push({
        label: `${iface} out`,
        data: sentData,
        borderColor: COLORS[colorIdx % COLORS.length],
      });
      colorIdx++;
    }

    return datasets;
  });

  const netBandwidthLabels = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length < 2) return [];
    const refTime = new Date(stats[stats.length - 1].timestamp).getTime();
    return stats.slice(1).map((s) => formatDelta(s.timestamp, refTime));
  });

  // --- GPU charts (filtered by time window) ---

  const filteredGpuStats = $derived(gpuData.filter((g) => new Date(g.timestamp).getTime() >= cutoffTime()));

  const hasGpuData = $derived(gpuData.length > 0);

  const gpuLabels = $derived.by(() => {
    const seen = new Set<string>();
    const labels: string[] = [];
    const stats = filteredGpuStats;
    if (stats.length === 0) return [];
    const refTime = new Date(stats[stats.length - 1].timestamp).getTime();
    for (const g of stats) {
      const label = formatDelta(g.timestamp, refTime);
      if (!seen.has(label)) {
        seen.add(label);
        labels.push(label);
      }
    }
    return labels;
  });

  function buildGpuDatasets(
    stats: GpuStat[],
    field: keyof Pick<GpuStat, "gpu_util_pct" | "mem_util_pct" | "temp_c" | "vram_temp_c" | "power_draw_w">,
  ) {
    if (stats.length === 0) return [];

    const byId = new Map<number, { name: string; values: number[] }>();
    for (const g of stats) {
      if (!byId.has(g.id)) {
        byId.set(g.id, { name: g.name, values: [] });
      }
      byId.get(g.id)!.values.push(g[field] as number);
    }

    const datasets = [];
    let colorIdx = 0;
    for (const [id, entry] of byId) {
      datasets.push({
        label: entry.name || `GPU ${id}`,
        data: entry.values,
        borderColor: COLORS[colorIdx % COLORS.length],
      });
      colorIdx++;
    }
    return datasets;
  }

  const gpuUtilDatasets = $derived(buildGpuDatasets(filteredGpuStats, "gpu_util_pct"));
  const gpuMemDatasets = $derived(buildGpuDatasets(filteredGpuStats, "mem_util_pct"));
  const gpuTempDatasets = $derived(buildGpuDatasets(filteredGpuStats, "temp_c"));
  const gpuVramTempDatasets = $derived(buildGpuDatasets(filteredGpuStats, "vram_temp_c"));
  const gpuPowerDatasets = $derived(buildGpuDatasets(filteredGpuStats, "power_draw_w"));
  const hasVramTemp = $derived(filteredGpuStats.some((g) => g.vram_temp_c > 0));

  // --- Latest-sample readout (the KPI strip above the charts) ---

  // Newest sample per GPU id, in id order.
  const latestGpus = $derived.by(() => {
    const byId = new Map<number, GpuStat>();
    for (const g of filteredGpuStats) byId.set(g.id, g);
    return [...byId.entries()].sort((a, b) => a[0] - b[0]).map(([, g]) => g);
  });

  const latestCpu = $derived.by(() => {
    const stats = filteredSysStats;
    if (stats.length === 0) return null;
    const cores = stats[stats.length - 1].cpu_util_per_core;
    if (!cores?.length) return null;
    return {
      avg: cores.reduce((a, b) => a + b, 0) / cores.length,
      max: Math.max(...cores),
      cores: cores.length,
    };
  });
  // Windows nvidia-smi path no longer queries power.draw (avoids WDDM stalls),
  // so only show the power chart when some backend actually reports it.
  const hasPower = $derived(filteredGpuStats.some((g) => g.power_draw_w > 0));
</script>

<div class="space-y-5 p-2">
  <!-- Toolbar -->
  <div class="flex items-center gap-3 flex-wrap">
    <div class="flex items-baseline gap-2">
      <h6 class="!pb-0">Performance</h6>
      <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary border border-card-border rounded px-1.5 py-0.5">experimental</span>
    </div>
    <span class="text-xs text-txtsecondary hidden md:inline">Live system &amp; GPU telemetry — cadence and available metrics depend on the platform backend.</span>

    <div class="flex items-center gap-2 ml-auto">
      <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">Refresh</span>
      <div class="seg">
        {#each INTERVALS as intv, i (intv.label)}
          <button aria-pressed={$selectedInterval === i} onclick={() => handleIntervalChange(i)}>{intv.label}</button>
        {/each}
      </div>
      <button class="icon-btn" title="Refresh now" onclick={manualRefresh} disabled={refreshing}>
        <RefreshCw size={15} class={refreshing ? "animate-spin" : ""} />
      </button>
    </div>
  </div>

  <!-- Latest-sample KPI strip -->
  {#if latestGpus.length > 0 || latestCpu || latestMemSwap}
    <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-2">
      {#each latestGpus as g (g.id)}
        <div class="tile">
          <span class="tile__label"><Cpu size={11} /> {g.name || `GPU ${g.id}`}</span>
          <span class="tile__value">{g.gpu_util_pct.toFixed(0)}<span class="text-xs text-txtsecondary">% util</span></span>
          <span class="tile__sub">
            {g.mem_util_pct.toFixed(0)}% vram · {g.temp_c.toFixed(0)}°C{#if g.power_draw_w > 0} · {g.power_draw_w.toFixed(0)} W{/if}
          </span>
        </div>
      {/each}
      {#if latestCpu}
        <div class="tile">
          <span class="tile__label"><Gauge size={11} /> CPU</span>
          <span class="tile__value">{latestCpu.avg.toFixed(0)}<span class="text-xs text-txtsecondary">% avg</span></span>
          <span class="tile__sub">peak core {latestCpu.max.toFixed(0)}% · {latestCpu.cores} cores</span>
        </div>
      {/if}
      {#if latestMemSwap}
        <div class="tile">
          <span class="tile__label"><MemoryStick size={11} /> Memory</span>
          <span class="tile__value">{latestMemSwap.mem_used_pct}<span class="text-xs text-txtsecondary">%</span></span>
          <span class="tile__sub">
            {(latestMemSwap.mem_used_mb / 1024).toFixed(1)} / {(latestMemSwap.mem_total_mb / 1024).toFixed(0)} GB{#if latestMemSwap.swap_used_pct !== null} · swap {latestMemSwap.swap_used_pct}%{/if}
          </span>
        </div>
      {/if}
    </div>
  {/if}

  <!-- GPU Section -->
  <section class="space-y-3">
    <h6 class="!pb-0 uppercase tracking-wide text-txtsecondary text-xs">GPU</h6>
    {#if !hasGpuData}
      <p class="text-txtsecondary card p-4 text-sm">No GPU data available</p>
    {:else}
      <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <PerformanceChart
          title="GPU Utilization (%)"
          labels={gpuLabels}
          datasets={gpuUtilDatasets}
          yMin={0}
          yMax={100}
          yLabel="%"
        />
        <PerformanceChart
          title="GPU Memory Utilization (%)"
          labels={gpuLabels}
          datasets={gpuMemDatasets}
          yMin={0}
          yMax={100}
          yLabel="%"
        />
        <PerformanceChart
          title="GPU Temperature (°C)"
          labels={gpuLabels}
          datasets={gpuTempDatasets}
          yMin={0}
          yLabel="°C"
        />
        {#if hasVramTemp}
          <PerformanceChart
            title="GPU VRAM Temperature (°C)"
            labels={gpuLabels}
            datasets={gpuVramTempDatasets}
            yMin={0}
            yLabel="°C"
          />
        {/if}
        {#if hasPower}
          <PerformanceChart
            title="GPU Power Draw (W)"
            labels={gpuLabels}
            datasets={gpuPowerDatasets}
            yMin={0}
            yLabel="W"
          />
        {/if}
      </div>
    {/if}
  </section>

  <!-- System Section -->
  <section class="space-y-3">
    <h6 class="!pb-0 uppercase tracking-wide text-txtsecondary text-xs">System</h6>
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <PerformanceChart
        title="CPU Utilization (%)"
        labels={sysLabels}
        datasets={cpuDatasets}
        yMin={0}
        yMax={100}
        yLabel="%"
        showLegend={false}
      />
      <!-- Absolute MB figures live in the Memory KPI tile above. -->
      <PerformanceChart
        title="Memory & Swap Usage (%)"
        labels={sysLabels}
        datasets={memSwapDatasets}
        yMin={0}
        yMax={100}
        yLabel="%"
      />
      <PerformanceChart title="Load Average" labels={sysLabels} datasets={loadDatasets} yMin={0} />
      {#if netBandwidthDatasets.length > 0}
        <PerformanceChart
          title="Network Bandwidth (Mbit/s)"
          labels={netBandwidthLabels}
          datasets={netBandwidthDatasets}
          yMin={0}
          yLabel="Mbit/s"
          showLegend={false}
        />
      {/if}
    </div>
  </section>
</div>
