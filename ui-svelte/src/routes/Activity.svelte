<script lang="ts">
  import { metrics, getCapture } from "../stores/api";
  import ActivityStats from "../components/ActivityStats.svelte";
  import Tooltip from "../components/Tooltip.svelte";
  import MetadataTooltip from "../components/MetadataTooltip.svelte";
  import CaptureDialog from "../components/CaptureDialog.svelte";
  import { persistentStore } from "../stores/persistent";
  import { observeWindowIdx, OBSERVE_WINDOWS } from "../stores/observe";
  import { onMount } from "svelte";
  import { Search, X, Columns3, GripVertical } from "lucide-svelte";
  import type { ReqRespCapture } from "../lib/types";

  type ColumnKey = string;

  interface ColumnDef {
    key: ColumnKey;
    label: string;
    defaultVisible: boolean;
  }

  const columns: ColumnDef[] = [
    { key: "id", label: "ID", defaultVisible: true },
    { key: "time", label: "Time", defaultVisible: true },
    { key: "model", label: "Model", defaultVisible: true },
    { key: "req_path", label: "Path", defaultVisible: false },
    { key: "resp_status_code", label: "Status", defaultVisible: false },
    { key: "resp_content_type", label: "Content-Type", defaultVisible: false },
    { key: "cached", label: "Cached", defaultVisible: true },
    { key: "prompt", label: "Prompt", defaultVisible: true },
    { key: "generated", label: "Generated", defaultVisible: true },
    { key: "prompt_speed", label: "Prompt t/s", defaultVisible: true },
    { key: "gen_speed", label: "Gen t/s", defaultVisible: true },
    { key: "duration", label: "Duration", defaultVisible: true },
    { key: "capture", label: "Capture", defaultVisible: true },
    { key: "meta", label: "Meta", defaultVisible: false },
  ];

  const defaultVisibleKeys = columns.filter((c) => c.defaultVisible).map((c) => c.key);

  const visibleColumns = persistentStore<ColumnKey[]>("activity-columns", defaultVisibleKeys);
  const columnOrder = persistentStore<ColumnKey[]>(
    "activity-column-order",
    columns.map((c) => c.key)
  );

  let columnsMenuOpen = $state(false);
  let dropdownContainer: HTMLDivElement | null = null;
  let dragKey: ColumnKey | null = $state(null);
  let dragOverKey: ColumnKey | null = $state(null);

  onMount(() => {
    function handleKeydown(e: KeyboardEvent) {
      if (e.key === "Escape" && columnsMenuOpen) {
        columnsMenuOpen = false;
      }
    }
    function handleClick(e: MouseEvent) {
      if (columnsMenuOpen && dropdownContainer && !dropdownContainer.contains(e.target as Node)) {
        columnsMenuOpen = false;
      }
    }
    document.addEventListener("keydown", handleKeydown);
    document.addEventListener("click", handleClick);
    return () => {
      document.removeEventListener("keydown", handleKeydown);
      document.removeEventListener("click", handleClick);
    };
  });

  function toggleColumn(key: ColumnKey) {
    const current = $visibleColumns;
    if (current.includes(key)) {
      if (current.length > 1) {
        visibleColumns.set(current.filter((k) => k !== key));
      }
    } else {
      visibleColumns.set([...current, key]);
    }
  }

  function isColumnVisible(key: ColumnKey): boolean {
    return $visibleColumns.includes(key);
  }

  function handleDragStart(e: DragEvent, key: ColumnKey) {
    dragKey = key;
    e.dataTransfer?.setData("text/plain", key);
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = "move";
    }
  }

  function handleDragOver(e: DragEvent, key: ColumnKey) {
    e.preventDefault();
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = "move";
    }
    dragOverKey = key;
  }

  function handleDrop(e: DragEvent, targetKey: ColumnKey) {
    e.preventDefault();
    if (!dragKey || dragKey === targetKey) return;
    const order = [...$columnOrder];
    const fromIndex = order.indexOf(dragKey);
    let toIndex = order.indexOf(targetKey);
    if (fromIndex === -1 || toIndex === -1) return;
    order.splice(fromIndex, 1);
    if (fromIndex < toIndex) {
      toIndex -= 1;
    }
    order.splice(toIndex, 0, dragKey);
    columnOrder.set(order);
  }

  function handleDragEnd() {
    dragKey = null;
    dragOverKey = null;
  }

  let orderedColumns = $derived(
    columns.slice().sort((a, b) => {
      const aIndex = $columnOrder.indexOf(a.key);
      const bIndex = $columnOrder.indexOf(b.key);
      if (aIndex === -1 && bIndex === -1) return 0;
      if (aIndex === -1) return 1;
      if (bIndex === -1) return -1;
      return aIndex - bIndex;
    })
  );

  let activeVisibleColumns = $derived(
    columns
      .filter((c) => isColumnVisible(c.key))
      .sort((a, b) => {
        const aIndex = $columnOrder.indexOf(a.key);
        const bIndex = $columnOrder.indexOf(b.key);
        if (aIndex === -1 && bIndex === -1) return 0;
        if (aIndex === -1) return 1;
        if (bIndex === -1) return -1;
        return aIndex - bIndex;
      })
      .map((c) => c.key)
  );

  let columnLabelMap = $derived(Object.fromEntries(columns.map((c) => [c.key, c.label])));

  $effect(() => {
    const staticKeys = new Set(columns.map((c) => c.key));
    const order = $columnOrder;
    const hasStale = order.some((k) => !staticKeys.has(k));
    const missing = columns.filter((c) => !order.includes(c.key)).map((c) => c.key);
    if (hasStale || missing.length > 0) {
      const cleaned = order.filter((k) => staticKeys.has(k));
      columnOrder.set([...cleaned, ...missing]);
    }
  });

  function formatSpeed(speed: number): string {
    return speed < 0 ? "—" : speed.toFixed(1);
  }

  function formatDuration(ms: number): string {
    return (ms / 1000).toFixed(2) + "s";
  }

  function formatRelativeTime(timestamp: string): string {
    const now = new Date();
    const date = new Date(timestamp);
    const diffInSeconds = Math.floor((now.getTime() - date.getTime()) / 1000);

    // Handle future dates by returning "just now"
    if (diffInSeconds < 5) {
      return "now";
    }

    if (diffInSeconds < 60) {
      return `${diffInSeconds}s ago`;
    }

    const diffInMinutes = Math.floor(diffInSeconds / 60);
    if (diffInMinutes < 60) {
      return `${diffInMinutes}m ago`;
    }

    const diffInHours = Math.floor(diffInMinutes / 60);
    if (diffInHours < 24) {
      return `${diffInHours}h ago`;
    }

    return "a while ago";
  }

  // Free-text row filter (model / path / status), applied on top of the shared
  // Observe time window.
  let search = $state("");

  let windowMetrics = $derived.by(() => {
    const ms = OBSERVE_WINDOWS[$observeWindowIdx]?.ms ?? 0;
    const cutoff = ms <= 0 ? 0 : Date.now() - ms;
    return [...$metrics]
      .filter((m) => cutoff === 0 || new Date(m.timestamp).getTime() >= cutoff)
      .sort((a, b) => b.id - a.id);
  });

  let sortedMetrics = $derived.by(() => {
    const q = search.trim().toLowerCase();
    if (!q) return windowMetrics;
    return windowMetrics.filter((m) =>
      `${m.model} ${m.req_path ?? ""} ${m.resp_status_code ?? ""}`.toLowerCase().includes(q),
    );
  });

  // Numeric cells get right-aligned tabular figures so magnitudes line up.
  const NUMERIC = new Set(["id", "cached", "prompt", "generated", "prompt_speed", "gen_speed", "duration", "resp_status_code"]);

  function statusClass(code: number): string {
    if (!code) return "text-txtsecondary";
    if (code >= 500) return "text-error";
    if (code >= 400) return "text-warning";
    return "text-success";
  }

  let selectedCapture = $state<ReqRespCapture | null>(null);
  let dialogOpen = $state(false);
  let loadingCaptureId = $state<number | null>(null);

  async function viewCapture(id: number) {
    loadingCaptureId = id;
    const capture = await getCapture(id);
    loadingCaptureId = null;
    selectedCapture = capture;
    dialogOpen = true;
  }

  function closeDialog() {
    dialogOpen = false;
    selectedCapture = null;
  }
</script>

<div class="p-2 flex flex-col gap-3">
  <ActivityStats rows={windowMetrics} />

  <div class="card p-0 flex flex-col min-h-[24rem]">
    <!-- Toolbar: row count + search + column picker -->
    <div class="flex items-center gap-2 px-3 py-2 border-b border-card-border-inner" bind:this={dropdownContainer}>
      <span class="font-mono text-[0.7rem] uppercase tracking-wide text-txtsecondary">
        Requests
        <span class="text-txtmain">{sortedMetrics.length}</span>
        {#if sortedMetrics.length !== windowMetrics.length}<span class="text-txtsecondary">/ {windowMetrics.length}</span>{/if}
      </span>

      <div class="relative ml-auto">
        <Search size={13} class="absolute left-2 top-1/2 -translate-y-1/2 text-txtsecondary pointer-events-none" />
        <input
          type="text"
          bind:value={search}
          placeholder="filter model / path / status"
          class="w-56 rounded border border-card-border bg-surface pl-7 pr-6 py-1 font-mono text-xs text-txtmain placeholder:text-txtsecondary/60 focus:outline-none focus:ring-2 focus:ring-primary"
        />
        {#if search}
          <button
            class="absolute right-1.5 top-1/2 -translate-y-1/2 text-txtsecondary hover:text-txtmain"
            onclick={() => (search = "")}
            aria-label="Clear filter"
          ><X size={13} /></button>
        {/if}
      </div>

      <div class="relative">
        <button
          class="icon-btn"
          aria-pressed={columnsMenuOpen}
          onclick={() => (columnsMenuOpen = !columnsMenuOpen)}
          title="Select columns"
        ><Columns3 size={15} /></button>
        {#if columnsMenuOpen}
          <div class="absolute right-0 top-full mt-1 bg-surface border border-card-border rounded-md shadow-lg z-10 py-1 min-w-[16rem]" role="list">
            <div class="px-3 py-2 text-xs font-medium uppercase tracking-wide text-txtsecondary border-b border-card-border-inner" role="presentation">
              Columns
            </div>
            {#each orderedColumns as col (col.key)}
              {@const key = col.key}
              <div
                class="flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-secondary-hover transition-colors {dragOverKey === key && dragKey !== key ? 'bg-primary/10 ring-1 ring-primary/40' : ''} {dragKey === key ? 'opacity-40' : ''}"
                role="listitem"
                ondragover={(e) => handleDragOver(e, key)}
                ondrop={(e) => handleDrop(e, key)}
              >
                <span
                  class="text-txtsecondary select-none cursor-grab"
                  draggable={true}
                  role="button"
                  tabindex="-1"
                  aria-label="Drag to reorder {col.label}"
                  ondragstart={(e) => handleDragStart(e, key)}
                  ondragend={handleDragEnd}
                ><GripVertical size={13} /></span>
                <label class="flex items-center gap-2 flex-1 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={isColumnVisible(key)}
                    onchange={() => toggleColumn(key)}
                    class="rounded"
                  />
                  {col.label}
                </label>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>

    <div class="flex-1 overflow-auto pretty-scroll">
    <table class="min-w-full !border-0 !rounded-none">
      <thead>
        <tr class="text-left text-[0.65rem] uppercase tracking-wide text-txtsecondary">
          {#each activeVisibleColumns as key (key)}
            <th class="sticky top-0 z-[1] bg-surface px-3 py-2 font-medium whitespace-nowrap {NUMERIC.has(key) ? 'text-right' : ''}">
              {#if key === "cached"}
                Cached <Tooltip content="prompt tokens from cache" />
              {:else if key === "prompt"}
                Prompt <Tooltip content="new prompt tokens processed" />
              {:else}
                {columnLabelMap[key] ?? key}
              {/if}
            </th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#if sortedMetrics.length === 0}
          <tr>
            <td colspan={activeVisibleColumns.length} class="px-4 py-10 text-center text-sm text-txtsecondary">
              {search ? "No requests match the filter" : "No activity recorded"}
            </td>
          </tr>
        {:else}
          {#each sortedMetrics as metric (metric.id)}
            <tr class="whitespace-nowrap text-sm hover:bg-secondary/40 transition-colors">
              {#each activeVisibleColumns as key (key)}
                <td class="px-3 py-2 {NUMERIC.has(key) ? 'text-right font-mono tabular-nums' : ''}">
                  {#if key === "id"}
                    <span class="text-txtsecondary">{metric.id + 1}</span>
                  {:else if key === "time"}
                    <span class="text-txtsecondary" title={new Date(metric.timestamp).toLocaleString()}>{formatRelativeTime(metric.timestamp)}</span>
                  {:else if key === "model"}
                    <span class="font-mono text-xs">{metric.model}</span>
                  {:else if key === "req_path"}
                    <span class="font-mono text-xs text-txtsecondary">{metric.req_path || "-"}</span>
                  {:else if key === "resp_status_code"}
                    <span class={statusClass(metric.resp_status_code)}>{metric.resp_status_code || "-"}</span>
                  {:else if key === "resp_content_type"}
                    {metric.resp_content_type || "-"}
                  {:else if key === "cached"}
                    {#if metric.tokens.cache_tokens > 0}
                      <span class="text-success">{metric.tokens.cache_tokens.toLocaleString()}</span>
                    {:else}
                      <span class="text-txtsecondary">-</span>
                    {/if}
                  {:else if key === "prompt"}
                    {metric.tokens.input_tokens.toLocaleString()}
                  {:else if key === "generated"}
                    {metric.tokens.output_tokens.toLocaleString()}
                  {:else if key === "prompt_speed"}
                    {formatSpeed(metric.tokens.prompt_per_second)}
                  {:else if key === "gen_speed"}
                    {formatSpeed(metric.tokens.tokens_per_second)}
                  {:else if key === "duration"}
                    {formatDuration(metric.duration_ms)}
                  {:else if key === "capture"}
                    {#if metric.has_capture}
                      <button
                        onclick={() => viewCapture(metric.id)}
                        disabled={loadingCaptureId === metric.id}
                        class="btn btn--sm uppercase tracking-wide hover:border-primary hover:text-primary"
                      >
                        {loadingCaptureId === metric.id ? "..." : "View"}
                      </button>
                    {:else}
                      <span class="text-txtsecondary">-</span>
                    {/if}
                  {:else if key === "meta"}
                    {#if Object.keys(metric.metadata || {}).length > 0}
                      <MetadataTooltip metadata={metric.metadata}>
                        <span class="cursor-help text-txtsecondary hover:text-txtmain">...</span>
                      </MetadataTooltip>
                    {:else}
                      <span class="text-txtsecondary">-</span>
                    {/if}
                  {:else}
                    -
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
    </div>
  </div>
</div>

<CaptureDialog capture={selectedCapture} open={dialogOpen} onclose={closeDialog} />
