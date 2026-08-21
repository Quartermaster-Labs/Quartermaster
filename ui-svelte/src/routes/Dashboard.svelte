<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { link } from "svelte-spa-router";
  import { models, loadModel, loadCounts } from "../stores/api";
  import { prettifyModelName } from "../lib/modelUtils";
  import ActiveModelsPanel from "../components/ActiveModelsPanel.svelte";
  import type { Model } from "../lib/types";

  // Sum the load tally across a model's whole family so loading ANY variant (not
  // only the default) floats the family up. The tally is keyed by the exact id
  // loaded, so ranking by a single id under-counts variant loads.
  function familyCount(m: Model): number {
    if (!m.family) return $loadCounts[m.id] ?? 0;
    return $models
      .filter((x) => x.family === m.family)
      .reduce((sum, x) => sum + ($loadCounts[x.id] ?? 0), 0);
  }

  // Loadable = listed, currently stopped. Most-loaded families float to the top
  // (tie-break alphabetical). Cap the list; full catalog lives on the Models page.
  const loadable = $derived(
    $models
      .filter((m) => !m.unlisted && m.state === "stopped")
      .sort((a, b) => familyCount(b) - familyCount(a) || a.id.localeCompare(b.id))
      .slice(0, 12),
  );

  let busy = $state<Record<string, boolean>>({});
  async function load(m: Model): Promise<void> {
    busy = { ...busy, [m.id]: true };
    try {
      await loadModel(m.id);
    } finally {
      busy = { ...busy, [m.id]: false };
    }
  }
</script>

<div class="max-w-5xl mx-auto flex flex-col gap-4">
  <!-- Live models: launch params + inference feedback (empty until one loads) -->
  <ActiveModelsPanel category="all" />

  <!-- Quick load -->
  <div class="card">
    <h6 class="mb-3">Quick load</h6>
    {#if loadable.length > 0}
      <div class="flex flex-wrap gap-2">
        {#each loadable as m (m.id)}
          <button
            class="flex items-center gap-2 rounded-md border border-card-border bg-surface px-3 py-1.5 font-mono text-sm text-txtmain shadow-sm transition-colors hover:border-primary hover:text-primary hover:bg-secondary/40 disabled:opacity-60 max-w-[18rem]"
            onclick={() => load(m)}
            disabled={busy[m.id]}
            use:tip={m.id}
          >
            <span class="w-1.5 h-1.5 rounded-full bg-txtsecondary shrink-0"></span>
            <span class="truncate">{busy[m.id] ? "Loading…" : prettifyModelName(m.name || m.id)}</span>
          </button>
        {/each}
      </div>
    {:else}
      <p class="text-label text-txtsecondary">
        No idle models. Add some on the <a href="/models" use:link class="text-primary hover:underline">Models</a> page.
      </p>
    {/if}
  </div>
</div>
