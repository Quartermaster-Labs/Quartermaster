<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { link, push } from "svelte-spa-router";
  import { models, metrics, loadModel, loadCounts } from "../stores/api";
  import { latestGpu } from "../stores/perf";
  import { observeTab } from "../stores/observe";
  import { prettifyModelName, modelCategory, MODEL_CATEGORIES } from "../lib/modelUtils";
  import { formatSpeed, formatDuration, formatRelativeTime, shortReqPath } from "../lib/activityFormat";
  import ActiveModelsPanel from "../components/ActiveModelsPanel.svelte";
  import { ArrowRight } from "lucide-svelte";
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

  const live = $derived($models.filter((m) => m.state === "ready" || m.state === "starting" || m.state === "stopping"));

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

  // --- At a glance ---------------------------------------------------------
  // Local, listed models only: peers live on someone else's disk, and unlisted
  // entries are variants the catalog deliberately hides.
  const listed = $derived($models.filter((m) => !m.unlisted && !m.peerID));
  // An approximation, and labelled as one: sizeGB is per catalog ROW, so a model
  // whose quants are separate rows counts each of them.
  const catalogGB = $derived(listed.reduce((sum, m) => sum + (m.sizeGB ?? 0), 0));
  const catBreakdown = $derived(
    MODEL_CATEGORIES.map((c) => ({ label: c.label, n: listed.filter((m) => modelCategory(m) === c.id).length }))
      .filter((c) => c.n > 0),
  );

  // The metrics store is this SESSION's request log (it is cleared on reconnect),
  // so these are "since the server came up", not all-time counters.
  const tokensOut = $derived($metrics.reduce((sum, e) => sum + (e.tokens?.output_tokens ?? 0), 0));
  const avgTps = $derived.by(() => {
    const rates = $metrics.map((e) => e.tokens?.tokens_per_second ?? 0).filter((r) => r > 0);
    if (rates.length === 0) return 0;
    return rates.reduce((a, b) => a + b, 0) / rates.length;
  });
  const vramFreeGB = $derived($latestGpu ? ($latestGpu.mem_total_mb - $latestGpu.mem_used_mb) / 1024 : null);

  const recent = $derived($metrics.slice(0, 8));

  // Observe remembers which tab you left it on, so send it to Activity rather
  // than dropping you on Logs because that is where you were an hour ago.
  function openActivity(): void {
    observeTab.set("activity");
    push("/observe");
  }
</script>

<!-- Bands, not boxes - the idiom the rest of the app now uses (see KvCache.svelte).
     min-h-full rather than h-full: the last band stretches to the bottom on a tall
     window, and the page still scrolls when the recent list outgrows it. -->
<div class="flex flex-col min-h-full bg-surface">
  <!-- ── Now running ─────────────────────────────────────────────────────── -->
  <!-- Always present, even idle. This is the landing page, and the state you
       land in after a cold start is "nothing loaded" - a band that disappears
       exactly then left the home page empty at the one moment it had a job. -->
  <!-- No band header: the status rail directly above already names what is
       loaded, so a "Now running / idle" strip repeated it in bigger type. -->
  <section class="shrink-0">
    {#if live.length > 0}
      <ActiveModelsPanel category="all" />
    {:else}
      <div class="flex flex-col items-center justify-center gap-1 py-10 border-b border-card-border-inner">
        <span class="inline-block w-2.5 h-2.5 rounded-full bg-txtsecondary"></span>
        <p class="text-label text-txtsecondary mt-1">No model is loaded. VRAM is free.</p>
        <p class="text-micro text-txtsecondary">Pick one below, or browse the full catalog on the Models page.</p>
      </div>
    {/if}
  </section>

  <!-- ── Quick load ──────────────────────────────────────────────────────── -->
  <section class="shrink-0">
    <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner">
      <h6>Quick load</h6>
      <span class="font-mono text-micro text-txtsecondary">most used first</span>
      <a href="/models" use:link class="ml-auto inline-flex items-center gap-1 text-micro uppercase tracking-wide text-txtsecondary hover:text-primary">
        All models <ArrowRight size={12} />
      </a>
    </div>
    <div class="px-3 py-3 border-b border-card-border-inner">
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
  </section>

  <!-- ── At a glance ─────────────────────────────────────────────────────── -->
  <section class="shrink-0">
    <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner">
      <h6>At a glance</h6>
    </div>
    <!-- .tile chips in a padded strip, matching the Activity and Performance
         stat strips - the same numbers should look the same everywhere. -->
    <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-2 px-3 py-3 border-b border-card-border-inner">
      <div class="tile">
        <span class="tile__label">Catalog</span>
        <span class="tile__value">{listed.length}</span>
        <span class="tile__sub truncate" use:tip={catBreakdown.map((c) => `${c.n} ${c.label}`).join(", ")}>
          {catBreakdown.map((c) => `${c.n} ${c.label.toLowerCase()}`).join(" · ") || "no models"}
        </span>
      </div>
      <div class="tile">
        <span class="tile__label" use:tip={"Sum of the on-disk size of every listed row. A model with several quants counts each of them."}>On disk</span>
        <span class="tile__value">{catalogGB >= 1024 ? (catalogGB / 1024).toFixed(2) + " TB" : catalogGB.toFixed(0) + " GB"}</span>
        <span class="tile__sub">approx.</span>
      </div>
      <div class="tile">
        <span class="tile__label" use:tip={"VRAM not currently allocated, as reported by the GPU - not an estimate of what will fit."}>VRAM free</span>
        <span class="tile__value">{vramFreeGB === null ? "-" : vramFreeGB.toFixed(1) + "G"}</span>
        <span class="tile__sub">
          {$latestGpu ? `of ${($latestGpu.mem_total_mb / 1024).toFixed(0)}G` : "no GPU reading"}
        </span>
      </div>
      <div class="tile">
        <span class="tile__label" use:tip={"Requests served since the server started. The log resets when it restarts."}>Requests</span>
        <span class="tile__value">{$metrics.length}</span>
        <span class="tile__sub">this session</span>
      </div>
      <div class="tile">
        <span class="tile__label" use:tip={"Mean generation speed across every request that reported one."}>Avg speed</span>
        <span class="tile__value">{avgTps > 0 ? avgTps.toFixed(1) : "-"}</span>
        <span class="tile__sub">{tokensOut.toLocaleString()} tok out</span>
      </div>
    </div>
  </section>

  <!-- ── Recent ──────────────────────────────────────────────────────────── -->
  <!-- flex-1: the band that grows, so a tall window ends on content rather than
       on a stripe of empty surface below the last card. -->
  <section class="flex-1 min-h-0 flex flex-col">
    <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner shrink-0">
      <h6>Recent</h6>
      <button
        class="ml-auto inline-flex items-center gap-1 text-micro uppercase tracking-wide text-txtsecondary hover:text-primary"
        onclick={openActivity}
      >
        Activity log <ArrowRight size={12} />
      </button>
    </div>
    {#if recent.length > 0}
      <div class="divide-y divide-card-border-inner">
        {#each recent as e (e.id)}
          <!-- One grid, not a table: eight rows do not need sticky headers or
               column resizing, and the Activity page already owns that. -->
          <div class="grid grid-cols-[5rem_minmax(0,1fr)_minmax(0,10rem)_5rem_5rem_5rem] items-center gap-2 px-3 py-1.5 font-mono text-micro tabular-nums">
            <span class="text-txtsecondary" use:tip={new Date(e.timestamp).toLocaleString()}>{formatRelativeTime(e.timestamp)}</span>
            <span class="truncate text-txtmain" use:tip={e.model}>{prettifyModelName(e.model)}</span>
            <span class="truncate text-txtsecondary">{shortReqPath(e.req_path)}</span>
            <span class="text-right {e.resp_status_code >= 400 ? 'text-error' : 'text-txtsecondary'}">{e.resp_status_code}</span>
            <span class="text-right text-txtsecondary">{e.tokens?.output_tokens ? e.tokens.output_tokens + "t" : "-"}</span>
            <span class="text-right text-txtsecondary" use:tip={"Generation speed / wall time"}>
              {(e.tokens?.tokens_per_second ?? 0) > 0 ? formatSpeed(e.tokens.tokens_per_second) + "/s" : formatDuration(e.duration_ms)}
            </span>
          </div>
        {/each}
      </div>
    {:else}
      <p class="px-3 py-4 text-label text-txtsecondary">
        Nothing served yet this session. Requests to the OpenAI-compatible API show up here.
      </p>
    {/if}
  </section>
</div>
