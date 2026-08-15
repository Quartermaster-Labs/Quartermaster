<script lang="ts">
  // Model browser: browse a hub, read the repo, pick a file, download it into
  // the models folder. On completion the server regenerates the config and
  // hot-reloads, so the model appears in the Models table without a restart.
  import { onMount } from "svelte";
  import { Search, Download, X, ExternalLink, Lock, AlertTriangle, Check, Heart, ArrowDownToLine, Clock, RefreshCw, SlidersHorizontal } from "lucide-svelte";
  import HubAvatar from "../components/HubAvatar.svelte";
  import { hubJobs, refreshHubJobs, isRunningJob } from "../stores/hubJobs";
  import {
    getHubSources,
    searchHub,
    getHubModel,
    startHubDownload,
    groupFiles,
    verdictFor,
    estimateHubFile,
    humanBytes,
    humanCount,
    humanCtx,
    MAX_PARAMS_B,
    HubApiError,
    type HubModel,
    type HubDetail,
    type FileOption,
    type FitVerdict,
    type HubEstimate,
  } from "../lib/hubApi";
  import { renderModelCard } from "../lib/hubMarkdown";
  import { MODEL_CATEGORIES, type ModelCategory } from "../lib/modelUtils";
  import { getSettings } from "../stores/api";

  let query = $state("");
  let sort = $state("downloads");
  // Category tab, same ids as the Models page. "llm" is the default because it
  // is what the other kinds are a minority of.
  let kind = $state<ModelCategory>("llm");
  let results = $state<HubModel[]>([]);
  let selected = $state<HubDetail | null>(null);
  let modelsRoot = $state("");
  let hasToken = $state(false);
  let targetVramGB = $state(0);
  let available = $state(true);
  let searching = $state(false);
  let loadingModel = $state(false);
  let err = $state<string | null>(null);
  let searched = $state(false);
  let showFilters = $state(false);
  // Real header-derived sizings for the open repo, keyed by file group. Empty
  // until they land; a row falls back to the size-only verdict meanwhile.
  let estimates = $state<Record<string, HubEstimate>>({});

  // Two kinds of filter in one menu, and the difference matters. `maxParamsB`
  // and `limit` are HUB-side (the search is re-run; see searchFilters/capParams
  // in internal/hub/hf.go) — narrowing locally would leave a 30-row page nearly
  // empty. `minDownloads`/`maxAgeDays` are applied to the page already fetched,
  // because the hub has no filter for either and re-asking would change nothing.
  interface HubFilters {
    maxParamsB: number; // 0 = no cap
    limit: number;
    minDownloads: number;
    maxAgeDays: number; // 0 = any age
  }
  // The size cap defaults ON: an unfiltered top-downloads page is mostly
  // frontier-size repos this box can't run, which buries everything it can.
  const DEFAULT_FILTERS: HubFilters = { maxParamsB: MAX_PARAMS_B, limit: 30, minDownloads: 0, maxAgeDays: 0 };
  let filters = $state<HubFilters>({ ...DEFAULT_FILTERS });

  const SIZE_CAPS = [
    { v: 0, label: "Any" },
    { v: 8, label: "8B" },
    { v: 16, label: "16B" },
    { v: 32, label: "32B" },
    { v: 70, label: "70B" },
    { v: 120, label: "120B" },
  ];
  const MIN_DOWNLOADS = [
    { v: 0, label: "Any" },
    { v: 100, label: "100+" },
    { v: 1_000, label: "1k+" },
    { v: 10_000, label: "10k+" },
  ];
  const AGES = [
    { v: 0, label: "Any" },
    { v: 30, label: "30d" },
    { v: 180, label: "6mo" },
    { v: 365, label: "1y" },
  ];
  const LIMITS = [30, 60, 100];

  const filterCount = $derived(
    (Object.keys(DEFAULT_FILTERS) as (keyof HubFilters)[]).filter((k) => filters[k] !== DEFAULT_FILTERS[k]).length
  );

  // Hub-side fields need a re-search; the rest re-filter what is already here.
  function setFilter<K extends keyof HubFilters>(k: K, v: HubFilters[K]): void {
    if (filters[k] === v) return;
    filters = { ...filters, [k]: v };
    if (k === "maxParamsB" || k === "limit") runSearch();
  }

  function resetFilters(): void {
    filters = { ...DEFAULT_FILTERS };
    runSearch();
  }

  // The locally-applied half of the filter menu. An unknown value is KEPT, the
  // same posture as the name-derived size cap: hiding a repo because its author
  // published no date is worse than showing one that may be stale.
  const shown = $derived(
    results.filter((m) => {
      if (filters.minDownloads && (m.downloads ?? 0) < filters.minDownloads) return false;
      if (filters.maxAgeDays) {
        const d = ageDays(m.updated);
        if (d !== null && d > filters.maxAgeDays) return false;
      }
      return true;
    })
  );

  const repoFiles = $derived<FileOption[]>(selected ? groupFiles(selected.files) : []);
  // The card is sanitized third-party HTML (see hubMarkdown.ts) and rendering
  // it is not free, so it is derived once per repo rather than per re-render.
  const cardHTML = $derived(selected?.readme ? renderModelCard(selected.readme, selected.id) : "");
  // Jobs are in a store, not local state: a pull outlives this page. This page
  // only asks whether the open repo is busy — monitoring downloads is the status
  // rail's Downloads menu, which is the one place that draws them.
  const busyRepo = $derived($hubJobs.some((j) => isRunningJob(j) && j.repo === selected?.id));

  const SORTS = [
    { id: "downloads", label: "Popular" },
    { id: "likes", label: "Liked" },
    { id: "modified", label: "Newest" },
  ];

  onMount(async () => {
    try {
      const s = await getHubSources();
      modelsRoot = s.modelsRoot;
      hasToken = s.hasToken;
    } catch (e) {
      // 501 is the one case where the feature genuinely isn't in this build;
      // anything else is a fault the user needs to see rather than a blank tab.
      if (e instanceof HubApiError && e.status === 501) {
        available = false;
        return;
      }
      err = e instanceof Error ? e.message : String(e);
    }
    try {
      targetVramGB = (await getSettings()).targetVramGB || 0;
    } catch {
      // Without a VRAM target the picker simply shows no fit verdict.
    }
    await refreshHubJobs();
    // Land on something to look at. An empty query is a valid search — the hub
    // answers it with its own top-by-downloads listing — so the page opens as a
    // browser rather than as an empty box demanding a query first.
    await runSearch();
  });

  async function runSearch(): Promise<void> {
    if (searching) return;
    searching = true;
    err = null;
    try {
      results = await searchHub(query.trim(), sort, filters.maxParamsB, kind, "hf", filters.limit);
      searched = true;
      selected = null;
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      searching = false;
    }
  }

  function setSort(id: string): void {
    if (sort === id) return;
    sort = id;
    runSearch();
  }

  function setKind(id: ModelCategory): void {
    if (kind === id) return;
    kind = id;
    // The open repo belongs to the category that was showing, so drop it rather
    // than leave a TTS model docked beside a page of image repos.
    selected = null;
    runSearch();
  }

  async function openModel(m: HubModel): Promise<void> {
    loadingModel = true;
    err = null;
    estimates = {};
    try {
      selected = await getHubModel(m.id, m.source);
      void sizeRepo(selected);
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
      selected = null;
    } finally {
      loadingModel = false;
    }
  }

  // Size every candidate in the open repo by reading its GGUF header off the
  // hub (a Range request server-side, no download). This is the only way to say
  // "92k context" rather than "fits" — the window comes from KV geometry in the
  // header, which file size cannot imply.
  //
  // Each row is a CDN round trip plus a few MB of header, so a repo offering a
  // dozen quants filled in one row per second when this ran serially — the whole
  // table was still settling long after the user had read it. A small pool runs
  // them together instead; the cap is there because these are multi-MB requests
  // sharing the browser's per-host connection budget with the rest of the page,
  // and firing twelve at once mostly makes them queue somewhere less visible.
  const SIZE_CONCURRENCY = 5;

  async function sizeRepo(det: HubDetail): Promise<void> {
    // The planner is LLM-shaped (layers, KV, expert share). A diffusion or TTS
    // gguf has none of that, so asking would produce a confident wrong number.
    if (kind !== "llm") return;
    // A projector is charged on top of whichever file is picked, so sizing it on
    // its own answers the wrong question — the row says "companion".
    const queue = groupFiles(det.files).filter((o) => !o.projector && o.files[0]?.path);
    let next = 0;
    const worker = async (): Promise<void> => {
      while (next < queue.length) {
        const opt = queue[next++];
        try {
          const e = await estimateHubFile(det.id, opt.files[0].path, det.source);
          // The user may have moved on while this was in flight; a late answer
          // must not paint a number onto a different repo's table.
          if (selected?.id !== det.id) return;
          estimates = { ...estimates, [opt.group]: e };
        } catch {
          // Best-effort: the row keeps the size-only verdict it already had.
          if (selected?.id !== det.id) return;
        }
      }
    };
    await Promise.all(Array.from({ length: Math.min(SIZE_CONCURRENCY, queue.length) }, worker));
  }

  async function download(opt: FileOption): Promise<void> {
    if (!selected) return;
    err = null;
    try {
      await startHubDownload(
        selected.id,
        opt.files.map((f) => f.path),
        opt.label,
        selected.source
      );
      await refreshHubJobs();
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    }
  }

  // What the Estimate column says. A landed header sizing wins over the
  // size-only verdict, because it answers the question the verdict could only
  // gesture at: not whether the weights fit, but how much context is left over
  // once they do.
  function estimateLabel(opt: FileOption, v: FitVerdict): string {
    const e = estimates[opt.group];
    if (!e || e.err) return verdictLabel(v);
    if (!e.fits) return "too big";
    const ctx = e.atMax ? "max" : humanCtx(e.ctx);
    // "fits" with layers on the CPU is true and misleading on its own: it runs,
    // at a fraction of the speed. Say which one this is.
    return e.offload ? `partly on CPU, ${ctx} context` : `fits, ${ctx} context`;
  }

  function estimateClass(opt: FileOption, v: FitVerdict): string {
    const e = estimates[opt.group];
    if (!e || e.err) return verdictClass(v);
    if (!e.fits) return "text-red-500";
    return e.offload ? "text-amber-500" : "text-emerald-500";
  }

  // The tooltip carries the numbers behind the label, so the column can stay one
  // short phrase without the estimate becoming unaccountable.
  function estimateTitle(opt: FileOption): string {
    const e = estimates[opt.group];
    if (!e) return "Sized from file size alone — reading this file's header…";
    if (e.err) return `Could not read this file's header (${e.err}) — falling back to a size-only guess.`;
    const parts = [`≈${e.estVramGB.toFixed(1)} GB of your ${e.targetVramGB} GB target`];
    if (e.maxCtx) parts.push(`trained ceiling ${humanCtx(e.maxCtx)}`);
    if (e.offload) parts.push("part of the model runs on the CPU");
    return `From this file's GGUF header: ${parts.join(", ")}.`;
  }

  function verdictLabel(v: FitVerdict): string {
    switch (v) {
      case "fits":
        return "fits on GPU";
      case "spills":
        return "spills to CPU";
      case "toobig":
        return "too big";
      default:
        return "";
    }
  }

  function verdictClass(v: FitVerdict): string {
    switch (v) {
      case "fits":
        return "text-emerald-500";
      case "spills":
        return "text-amber-500";
      case "toobig":
        return "text-red-500";
      default:
        return "text-txtsecondary";
    }
  }

  function hubURL(id: string): string {
    return `https://huggingface.co/${id}`;
  }

  function paramsLabel(m: HubModel): string {
    if (!m.paramsB) return "";
    return m.paramsB >= 10 || Number.isInteger(m.paramsB) ? `${m.paramsB}B` : `${m.paramsB.toFixed(1)}B`;
  }

  // null, not 0, when the repo states no date: "unknown" and "touched today"
  // are opposite answers for the recency filter.
  function ageDays(iso?: string): number | null {
    if (!iso) return null;
    const then = new Date(iso).getTime();
    if (!then) return null;
    return Math.floor((Date.now() - then) / 86_400_000);
  }

  // "3 days ago" beats a timestamp here: what matters about a repo's date is
  // whether it is still maintained, not the exact day it was touched.
  function ago(iso?: string): string {
    const days = ageDays(iso);
    if (days === null) return "";
    if (days <= 0) return "today";
    if (days === 1) return "yesterday";
    if (days < 30) return `${days}d ago`;
    if (days < 365) return `${Math.floor(days / 30)}mo ago`;
    return `${Math.floor(days / 365)}y ago`;
  }

</script>

<div class="flex flex-col h-full gap-3">
  {#if !available}
    <div class="card text-sm text-txtsecondary">The model browser is unavailable in this build.</div>
  {:else}
    <!-- Same category tabs, in the same order, as the Models page: what you can
         browse and what you already have are one vocabulary. The narrowing is
         done hub-side (see searchFilters in internal/hub/hf.go) — a 30-row page
         filtered here would leave most tabs empty. -->
    <div class="flex flex-wrap items-end gap-x-1 gap-y-2 border-b border-card-border shrink-0">
      {#each MODEL_CATEGORIES as c (c.id)}
        <button
          class="px-3 py-2 -mb-px border-b-2 font-mono text-xs uppercase tracking-wide transition-colors {kind === c.id
            ? 'border-primary text-txtmain'
            : 'border-transparent text-txtsecondary hover:text-txtmain'}"
          onclick={() => setKind(c.id)}
        >
          {c.label}
        </button>
      {/each}
    </div>

    <!-- Toolbar: one row, search left, controls right (mirrors the Models page).
         Every control is pinned to the SAME h-7 — .btn, .seg and a bare input
         each compute their own height from padding, which is how the refresh
         button ended up shorter than the segmented controls beside it. -->
    <div class="shrink-0 flex flex-wrap items-stretch gap-2">
      <!-- Fixed width, not flex-1: a search box that grows to fill a 2560px
           window is a huge empty field for a two-word query. Border and radius
           are spelled out because this app has no `.input` class — the
           surrounding pages style their fields inline the same way. -->
      <div class="relative w-[22rem] max-w-full h-7 shrink-0">
        <Search class="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-txtsecondary pointer-events-none" />
        <input
          class="w-full h-full pl-8 pr-7 py-0 rounded-full border border-card-border bg-background text-xs text-txtmain placeholder:text-txtsecondary focus:outline-none focus:border-primary transition-colors"
          placeholder="Search Hugging Face for GGUF models…"
          bind:value={query}
          onkeydown={(e) => e.key === "Enter" && runSearch()}
        />
        {#if query}
          <!-- Clearing goes straight back to the hub's own listing rather than
               leaving the last query's results under an empty box. -->
          <button
            class="absolute right-2 top-1/2 -translate-y-1/2 text-txtsecondary hover:text-txtmain transition-colors"
            title="Clear"
            aria-label="Clear the search"
            onclick={() => {
              query = "";
              runSearch();
            }}
          >
            <X class="w-3.5 h-3.5" />
          </button>
        {/if}
      </div>

      <div class="seg h-7">
        {#each SORTS as s (s.id)}
          <button type="button" aria-pressed={sort === s.id} onclick={() => setSort(s.id)}>{s.label}</button>
        {/each}
      </div>

      <!-- Filters live in a menu rather than on the toolbar: the size cap was
           one visible toggle, and every filter added beside it would be another
           permanent control for a choice made once. The count on the button is
           what keeps a narrowed listing from looking like an empty hub. -->
      <div class="relative shrink-0">
        <div class="seg h-7">
          <button type="button" aria-pressed={showFilters || filterCount > 0} aria-expanded={showFilters} onclick={() => (showFilters = !showFilters)}>
            <span class="inline-flex items-center gap-1">
              <SlidersHorizontal class="w-3 h-3" />
              Filters{#if filterCount}<span class="tabular-nums">· {filterCount}</span>{/if}
            </span>
          </button>
        </div>

        {#if showFilters}
          <!-- Click-away backdrop, same shape as the status rail's Downloads
               menu: it swallows the closing click so it can't also land on
               whatever was underneath. -->
          <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
          <div class="fixed inset-0 z-40" onclick={() => (showFilters = false)}></div>

          <div class="absolute left-0 top-8 z-50 w-[19rem] rounded-md border border-card-border bg-surface shadow-xl p-3 flex flex-col gap-3">
            <div class="flex flex-col gap-1.5">
              <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">Max size</span>
              <div class="seg h-6 flex-wrap">
                {#each SIZE_CAPS as o (o.v)}
                  <button type="button" aria-pressed={filters.maxParamsB === o.v} onclick={() => setFilter("maxParamsB", o.v)}>{o.label}</button>
                {/each}
              </div>
              <!-- Said plainly because it is the one filter that can hide a repo
                   for a reason that has nothing to do with the model. -->
              <span class="text-[0.6rem] text-txtsecondary">Read from the repo NAME. A repo that states no size is always shown.</span>
            </div>

            <div class="flex flex-col gap-1.5">
              <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">Min downloads</span>
              <div class="seg h-6 flex-wrap">
                {#each MIN_DOWNLOADS as o (o.v)}
                  <button type="button" aria-pressed={filters.minDownloads === o.v} onclick={() => setFilter("minDownloads", o.v)}>{o.label}</button>
                {/each}
              </div>
            </div>

            <div class="flex flex-col gap-1.5">
              <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">Updated within</span>
              <div class="seg h-6 flex-wrap">
                {#each AGES as o (o.v)}
                  <button type="button" aria-pressed={filters.maxAgeDays === o.v} onclick={() => setFilter("maxAgeDays", o.v)}>{o.label}</button>
                {/each}
              </div>
            </div>

            <div class="flex flex-col gap-1.5">
              <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">Results</span>
              <div class="seg h-6">
                {#each LIMITS as n (n)}
                  <button type="button" aria-pressed={filters.limit === n} onclick={() => setFilter("limit", n)}>{n}</button>
                {/each}
              </div>
            </div>

            <div class="flex items-center justify-between pt-1 border-t border-card-border-inner">
              <span class="text-[0.6rem] text-txtsecondary tabular-nums">{shown.length} of {results.length} shown</span>
              <button class="btn btn--sm" disabled={!filterCount} onclick={resetFilters}>Reset</button>
            </div>
          </div>
        {/if}
      </div>

      <!-- One button, and it re-runs whatever the list currently is: a query if
           one is typed, the hub's listing if not. Enter does the same, so a
           separate "Search" verb was only ever a second name for refresh.
           Chromeless (.icon-btn, the status rail's own style) — it is a repeat
           of what the page already did, not the toolbar's primary action. -->
      <button
        class="icon-btn h-7 shrink-0"
        onclick={runSearch}
        disabled={searching}
        title={query.trim() ? "Re-run this search" : "Refresh the listing"}
        aria-label="Refresh"
      >
        <RefreshCw class="w-3.5 h-3.5 {searching ? 'animate-spin' : ''}" />
      </button>
    </div>

    {#if err}
      <div class="shrink-0 flex items-start gap-2 rounded-md border border-error/40 bg-error/10 px-3 py-2 text-xs text-error">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-px" />
        <span>{err}</span>
      </div>
    {/if}

    <!-- Results | detail -->
    <div class="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[22rem_1fr] gap-3">
      <div class="min-h-0 overflow-y-auto pretty-scroll rounded-md border border-card-border bg-surface">
        {#if searching && !results.length}
          <div class="p-3 text-xs text-txtsecondary">Loading Hugging Face…</div>
        {:else if !searched}
          <div class="p-3 text-xs text-txtsecondary">Search a hub to get started.</div>
        {:else if !results.length}
          <div class="p-3 text-xs text-txtsecondary">No GGUF repos matched that search.</div>
        {:else if !shown.length}
          <!-- Distinct from "nothing matched": the hub answered, the filters
               emptied it, and the fix is one click away rather than a re-word. -->
          <div class="p-3 text-xs text-txtsecondary">
            All {results.length} results are hidden by your filters.
            <button class="text-primary hover:underline" onclick={resetFilters}>Reset them</button>.
          </div>
        {:else}
          {#each shown as m (m.id)}
            <button
              class="w-full text-left px-3 py-2.5 border-b border-card-border-inner last:border-b-0 transition-colors relative {selected?.id === m.id
                ? 'bg-secondary/60'
                : 'hover:bg-secondary/40'}"
              onclick={() => openModel(m)}
            >
              {#if selected?.id === m.id}
                <span class="absolute left-0 top-0 bottom-0 w-0.5 bg-primary"></span>
              {/if}
              <div class="flex items-start gap-2.5">
                <HubAvatar author={m.author} source={m.source} size="w-8 h-8" />
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-1.5">
                    <span class="font-mono text-xs text-txtmain truncate">{m.name}</span>
                    {#if m.gated}<Lock class="w-3 h-3 text-warning shrink-0" />{/if}
                  </div>
                  <div class="text-[0.65rem] text-txtsecondary truncate">{m.author}</div>
                  <div class="mt-1 flex items-center gap-2 text-[0.65rem] text-txtsecondary tabular-nums">
                    {#if paramsLabel(m)}
                      <span class="font-mono px-1.5 py-px rounded bg-secondary/70 text-txtmain">{paramsLabel(m)}</span>
                    {/if}
                    <span class="inline-flex items-center gap-0.5"><ArrowDownToLine class="w-3 h-3" />{humanCount(m.downloads)}</span>
                    <span class="inline-flex items-center gap-0.5"><Heart class="w-3 h-3" />{humanCount(m.likes)}</span>
                    {#if ago(m.updated)}
                      <span class="ml-auto inline-flex items-center gap-0.5 shrink-0"><Clock class="w-3 h-3" />{ago(m.updated)}</span>
                    {/if}
                  </div>
                </div>
              </div>
            </button>
          {/each}
        {/if}
      </div>

      <div class="min-h-0 overflow-y-auto pretty-scroll rounded-md border border-card-border bg-surface">
        {#if loadingModel}
          <div class="p-4 text-xs text-txtsecondary">Loading the model page…</div>
        {:else if !selected}
          <div class="p-4 text-xs text-txtsecondary">Pick a repo to see its files and model card.</div>
        {:else}
          <!-- Repo header -->
          <div class="p-4 border-b border-card-border flex items-start gap-3">
            <HubAvatar author={selected.author} source={selected.source} size="w-11 h-11" />
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 flex-wrap">
                <h3 class="font-mono text-sm text-txtmain truncate">{selected.name}</h3>
                <a class="text-txtsecondary hover:text-primary transition-colors" href={hubURL(selected.id)} target="_blank" rel="noreferrer" title="Open on Hugging Face">
                  <ExternalLink class="w-3.5 h-3.5" />
                </a>
                {#if selected.gated}
                  <span class="status status--starting">gated — accept the license first</span>
                {/if}
              </div>
              <div class="text-[0.7rem] text-txtsecondary">{selected.author}</div>
              <div class="mt-1.5 flex items-center gap-3 text-[0.65rem] text-txtsecondary tabular-nums">
                {#if paramsLabel(selected)}
                  <span class="font-mono px-1.5 py-px rounded bg-secondary/70 text-txtmain">{paramsLabel(selected)}</span>
                {/if}
                <span class="inline-flex items-center gap-0.5"><ArrowDownToLine class="w-3 h-3" />{humanCount(selected.downloads)}</span>
                <span class="inline-flex items-center gap-0.5"><Heart class="w-3 h-3" />{humanCount(selected.likes)}</span>
                {#if ago(selected.updated)}
                  <span class="inline-flex items-center gap-0.5"><Clock class="w-3 h-3" />{ago(selected.updated)}</span>
                {/if}
              </div>
            </div>
          </div>

          <!-- File picker -->
          <div class="p-4 border-b border-card-border">
            {#if !repoFiles.length}
              <div class="text-xs text-txtsecondary">This repo carries no GGUF files.</div>
            {:else}
              <table class="w-full text-xs">
                <thead>
                  <tr class="text-txtsecondary text-left">
                    <th class="font-mono text-[0.6rem] uppercase tracking-wide pb-1.5">File</th>
                    <th class="font-mono text-[0.6rem] uppercase tracking-wide pb-1.5 whitespace-nowrap">Size</th>
                    <th class="font-mono text-[0.6rem] uppercase tracking-wide pb-1.5">Estimate</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {#each repoFiles as q, i (q.group)}
                    {@const v = verdictFor(q.sizeBytes, targetVramGB)}
                    <!-- Banded rows: a filename now runs the full width of the
                         cell and wraps, so the row borders alone stopped being
                         enough to tell one file's line from the next's. -->
                    <tr class="border-t border-card-border-inner hover:bg-secondary/30 transition-colors {i % 2 ? 'bg-secondary/15' : ''}">
                      <td class="py-2 font-mono text-txtmain">
                        <span class="break-all">{q.label}</span>
                        {#if q.projector}
                          <span class="ml-1 rounded bg-secondary px-1 py-0.5 text-[0.55rem] uppercase tracking-wide text-txtsecondary" title="Vision/audio projector — download it alongside the model's weights, not instead of them">
                            projector
                          </span>
                        {/if}
                        {#if q.files.length > 1}
                          <span class="text-[0.65rem] text-txtsecondary">· {q.files.length} parts</span>
                        {/if}
                      </td>
                      <td class="py-2 tabular-nums text-txtsecondary whitespace-nowrap">{humanBytes(q.sizeBytes)}</td>
                      <!-- A projector is a companion file, so "fits in VRAM" is the
                           wrong question: it is charged on top of whichever file
                           the user picks, never sized on its own. -->
                      <td class="py-2 whitespace-nowrap {q.projector ? 'text-txtsecondary' : estimateClass(q, v)}" title={q.projector ? "" : estimateTitle(q)}>
                        {q.projector ? "companion" : estimateLabel(q, v)}
                      </td>
                      <td class="py-2 text-right">
                        <button
                          class="icon-btn"
                          disabled={busyRepo}
                          title={busyRepo ? "This repo already has a download running" : `Download ${q.label}`}
                          aria-label="Download {q.label}"
                          onclick={() => download(q)}
                        >
                          <Download class="w-3.5 h-3.5" />
                        </button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
              <p class="mt-2 text-[0.65rem] text-txtsecondary">
                {#if kind === "llm"}
                  Context figures come from each file's GGUF header, read off the hub without downloading it, planned against your
                  {targetVramGB || "?"} GB VRAM target. A row still showing a plain fit verdict is one whose header hasn't been read (hover it for why).
                {:else}
                  This estimate is from file size against your {targetVramGB || "?"} GB VRAM target — a hint, not the sizer. The model's own config page
                  has the real numbers once it is downloaded.
                {/if}
              </p>
            {/if}
          </div>

          <!-- Model card. Third-party HTML: sanitized in hubMarkdown.ts, images
               routed through the server's proxy. Shown expanded — it is the
               whole reason for reading a repo page rather than a file list. -->
          {#if cardHTML}
            <div class="p-4 hub-card prose-sm text-xs text-txtmain">
              <!-- eslint-disable-next-line svelte/no-at-html-tags -->
              {@html cardHTML}
            </div>
          {/if}
        {/if}
      </div>
    </div>

    <div class="shrink-0 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[0.6rem] text-txtsecondary">
      {#if modelsRoot}
        <span>Downloads land in <span class="text-txtmain">{modelsRoot}</span>, one folder per repo.</span>
      {/if}
      {#if !hasToken}
        <span>No Hugging Face token set — gated repos will refuse.</span>
      {/if}
      {#if $hubJobs.some((j) => j.phase === "done")}
        <span class="inline-flex items-center gap-1 text-success">
          <Check class="w-3 h-3" /> Finished downloads are in the config already — check the Models page.
        </span>
      {/if}
    </div>
  {/if}
</div>

<style>
  /* Model cards are written for a full-width page on the hub; these scoped
     rules keep one from blowing out the pane (giant headings, tables wider than
     the column, full-bleed banner images) without touching chat markdown.
     Plain CSS against the theme variables rather than @apply: Tailwind v4 needs
     a @reference to resolve utilities inside a component <style>, and nothing
     else in this app does it. */
  .hub-card :global(h1),
  .hub-card :global(h2) {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.85rem;
    color: var(--color-txtmain);
    margin: 1rem 0 0.5rem;
    padding-bottom: 0.25rem;
    border-bottom: 1px solid var(--color-card-border-inner);
  }
  .hub-card :global(h3),
  .hub-card :global(h4) {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.78rem;
    color: var(--color-txtmain);
    margin: 0.75rem 0 0.35rem;
  }
  .hub-card :global(p) {
    margin: 0.5rem 0;
    line-height: 1.6;
  }
  .hub-card :global(ul),
  .hub-card :global(ol) {
    margin: 0.5rem 0;
    padding-left: 1.25rem;
    list-style: disc;
  }
  .hub-card :global(ol) {
    list-style: decimal;
  }
  .hub-card :global(li) {
    margin: 0.15rem 0;
    line-height: 1.6;
  }
  .hub-card :global(a) {
    color: var(--color-primary);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  /* Images are the reason for rendering a card, and also the thing most likely
     to wreck it: cards are written for a full-width hub page and open with a
     banner plus a row of badges, all at their natural size. Cap BOTH axes and
     let the aspect ratio follow — `height: auto` also neutralises a card that
     sets height alone, which otherwise renders a squashed image. */
  .hub-card :global(img) {
    display: inline-block;
    max-width: 100%;
    max-height: 14rem;
    height: auto;
    object-fit: contain;
    border-radius: 0.375rem;
    margin: 0.3rem 0.25rem;
    vertical-align: middle;
  }
  /* A paragraph that is nothing but images is the badge row / hero shot. Laying
     it out as a centered wrap keeps a five-badge row from becoming five lines
     with a text baseline gap between them. */
  .hub-card :global(p:has(> img):not(:has(> :not(img):not(a):not(br)))) {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    align-items: center;
    justify-content: center;
  }
  /* Cards centre their header with `<div align="center">` / `<p align="center">`;
     the attribute survives sanitising but does nothing on its own in a modern
     document, so it is honoured here. */
  .hub-card :global([align="center"]) {
    text-align: center;
  }
  .hub-card :global([align="right"]) {
    text-align: right;
  }
  /* Nothing in a card may be wider than the pane. A stray inline width, or a
     long unbroken URL in a heading, otherwise scrolls the entire column
     sideways — and the results list next to it goes with it. */
  .hub-card {
    max-width: 62rem;
    overflow-wrap: anywhere;
  }
  .hub-card :global(*) {
    max-width: 100%;
  }
  .hub-card :global(pre) {
    background: var(--color-background);
    border: 1px solid var(--color-card-border);
    border-radius: 0.375rem;
    padding: 0.6rem;
    margin: 0.5rem 0;
    overflow-x: auto;
    font-size: 0.7rem;
  }
  .hub-card :global(code) {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.7rem;
  }
  .hub-card :global(:not(pre) > code) {
    background: var(--color-secondary);
    border-radius: 0.25rem;
    padding: 0.05rem 0.25rem;
  }
  /* A markdown table is the one element that reliably overflows; giving it its
     own scroller keeps the pane from scrolling sideways as a whole. */
  .hub-card :global(table) {
    display: block;
    width: 100%;
    overflow-x: auto;
    margin: 0.5rem 0;
    font-size: 0.7rem;
    border-collapse: collapse;
  }
  .hub-card :global(th),
  .hub-card :global(td) {
    border: 1px solid var(--color-card-border-inner);
    padding: 0.25rem 0.5rem;
    text-align: left;
    white-space: nowrap;
  }
  .hub-card :global(blockquote) {
    border-left: 2px solid var(--color-card-border);
    padding-left: 0.75rem;
    margin: 0.5rem 0;
    color: var(--color-txtsecondary);
  }
  .hub-card :global(hr) {
    margin: 0.75rem 0;
    border: 0;
    border-top: 1px solid var(--color-card-border);
  }
</style>
