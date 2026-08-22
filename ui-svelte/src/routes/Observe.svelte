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

<!-- Full-bleed, and the same toolbar the Models and Browse pages use: these are
     dense readouts, so the window gets spent on data rather than on a gutter
     around a rounded box. -->
<div class="flex flex-col h-full">
  <!-- Tab bar + shared time window -->
  <div class="flex items-stretch gap-x-1 px-3 min-h-10 border-b border-card-border shrink-0">
    {#each tabs as t (t.key)}
      {@const active = $observeTab === t.key}
      <button
        class="flex items-center gap-2 px-3 -mb-px border-b-2 font-mono text-xs uppercase tracking-wide transition-colors {active
          ? 'border-primary text-txtmain'
          : 'border-transparent text-txtsecondary hover:text-txtmain'}"
        onclick={() => observeTab.set(t.key)}
      >
        <t.icon size={14} strokeWidth={active ? 2.4 : 1.8} />
        {t.label}
      </button>
    {/each}

    {#if showWindow}
      <!-- self-stretch + items-center: centred on the row the tabs define. -->
      <div class="ml-auto flex items-center gap-2 shrink-0 self-stretch">
        <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">Window</span>
        <div class="seg">
          {#each OBSERVE_WINDOWS as win, i (win.label)}
            <button aria-pressed={$observeWindowIdx === i} onclick={() => observeWindowIdx.set(i)}>{win.label}</button>
          {/each}
        </div>
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
