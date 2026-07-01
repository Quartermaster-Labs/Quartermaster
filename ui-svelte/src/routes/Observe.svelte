<script lang="ts">
  import { onMount } from "svelte";
  import { observeTab, observeWindowIdx, OBSERVE_WINDOWS, type ObserveTab } from "../stores/observe";
  import { currentRoute } from "../stores/route";
  import { Activity as ActivityIcon, ScrollText, Gauge, Layers } from "lucide-svelte";
  import Activity from "./Activity.svelte";
  import LogViewer from "./LogViewer.svelte";
  import Performance from "./Performance.svelte";
  import Context from "./Context.svelte";

  const tabs = [
    { key: "activity" as ObserveTab, label: "Activity", icon: ActivityIcon },
    { key: "logs" as ObserveTab, label: "Logs", icon: ScrollText },
    { key: "performance" as ObserveTab, label: "Performance", icon: Gauge },
    { key: "context" as ObserveTab, label: "Context", icon: Layers },
  ];

  // Legacy deep-links (/activity, /logs, /performance) select the matching tab.
  const legacy: Record<string, ObserveTab> = {
    "/activity": "activity",
    "/logs": "logs",
    "/performance": "performance",
  };

  onMount(() => {
    const t = legacy[$currentRoute];
    if (t) observeTab.set(t);
  });

  // The time window only applies to Activity (row filter) and Performance (chart
  // cutoff). Logs and Context ignore it, so hide the selector there.
  const showWindow = $derived($observeTab === "activity" || $observeTab === "performance");
</script>

<div class="flex flex-col h-full gap-3">
  <!-- Tab bar + shared time window -->
  <div class="flex items-center justify-between border-b border-border pb-2">
    <div class="flex items-center gap-1">
      {#each tabs as t (t.key)}
        {@const active = $observeTab === t.key}
        <button
          class="flex items-center gap-2 px-3 py-1.5 font-mono text-sm border-b-2 -mb-[0.5625rem] transition-colors {active
            ? 'border-primary text-primary'
            : 'border-transparent text-txtsecondary hover:text-txtmain'}"
          onclick={() => observeTab.set(t.key)}
        >
          <t.icon size={15} strokeWidth={active ? 2.4 : 1.8} />
          {t.label}
        </button>
      {/each}
    </div>

    {#if showWindow}
      <div class="flex items-center gap-1">
        <span class="text-xs text-txtsecondary mr-1">Window:</span>
        {#each OBSERVE_WINDOWS as win, i}
          <button
            class="btn btn--sm"
            class:bg-primary={$observeWindowIdx === i}
            class:text-btn-primary-text={$observeWindowIdx === i}
            onclick={() => observeWindowIdx.set(i)}
          >
            {win.label}
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <!-- Panels stay mounted so their polling/scroll state survives tab switches. -->
  <div class="flex-1 overflow-auto min-h-0 pretty-scroll">
    <div class="h-full" class:hidden={$observeTab !== "activity"}>
      <Activity />
    </div>
    <div class="h-full" class:hidden={$observeTab !== "logs"}>
      <LogViewer />
    </div>
    <div class="h-full" class:hidden={$observeTab !== "performance"}>
      <Performance />
    </div>
    <div class="h-full" class:hidden={$observeTab !== "context"}>
      <Context />
    </div>
  </div>
</div>
