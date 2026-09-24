<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { link, push } from "svelte-spa-router";
  import { models, metrics, loadModel, loadCounts } from "../stores/api";
  import { observeTab } from "../stores/observe";
  import { prettifyModelName, modelCategory, MODEL_CATEGORIES, type ModelCategory } from "../lib/modelUtils";
  import { formatSpeed, formatDuration, formatRelativeTime, shortReqPath } from "../lib/activityFormat";
  import ActiveModelsPanel from "../components/ActiveModelsPanel.svelte";
  import { ArrowRight, MessageSquare, Image, Film, Box, Volume2, Mic, Layers, ScanLine } from "lucide-svelte";
  import type { Model } from "../lib/types";
  import { onMount } from "svelte";
  import { getHubDiskUsage, type HubDiskUsage } from "../lib/hubApi";

  // --- Categories ----------------------------------------------------------
  // Five hues, not eight: segment, speech and embedding are a handful of rows
  // each and share "other" (see the --color-cat-* note in index.css). Icons
  // stay per category, so they are still told apart up close.
  type Hue = "llm" | "image" | "video" | "3d" | "other";
  const HUE: Record<ModelCategory, Hue> = {
    llm: "llm", image: "image", video: "video", "3d": "3d",
    segment: "other", tts: "other", transcribe: "other", embed: "other",
  };
  const HUE_LABEL: Record<Hue, string> = { llm: "Language", image: "Image", video: "Video", "3d": "3D", other: "Other" };
  const HUE_SHORT: Record<Hue, string> = { llm: "llm", image: "img", video: "vid", "3d": "3d", other: "other" };
  const HUES: Hue[] = ["llm", "image", "video", "3d", "other"];
  const ICON = {
    llm: MessageSquare, image: Image, video: Film, "3d": Box,
    segment: ScanLine, tts: Volume2, transcribe: Mic, embed: Layers,
  } satisfies Record<ModelCategory, unknown>;
  const hueVar = (h: Hue) => `var(--color-cat-${h})`;

  const byId = $derived(new Map($models.map((m) => [m.id, m])));

  // --- Quick load ----------------------------------------------------------
  // One row per MODEL, not per quant: modelKey is the server's "these are one
  // model" key (one pill per quant on the Models page), so four quants of the
  // same gemma are one line with a "+3" rather than four near-identical chips.
  // The row loads whichever variant has been loaded most (then the smallest,
  // the one most likely to fit beside whatever else is resident).
  interface QuickRow {
    key: string;
    pick: Model;
    others: Model[];
    count: number;
    hue: Hue;
    cat: ModelCategory;
  }
  const quick = $derived.by(() => {
    const groups = new Map<string, Model[]>();
    for (const m of $models) {
      if (m.unlisted || m.state !== "stopped") continue;
      const k = m.modelKey || m.family || m.id;
      const g = groups.get(k);
      if (g) g.push(m);
      else groups.set(k, [m]);
    }
    const rows: QuickRow[] = [];
    for (const [key, ms] of groups) {
      const count = ms.reduce((s, m) => s + ($loadCounts[m.id] ?? 0), 0);
      const sorted = [...ms].sort(
        (a, b) => ($loadCounts[b.id] ?? 0) - ($loadCounts[a.id] ?? 0) || (a.sizeGB ?? 0) - (b.sizeGB ?? 0) || a.id.localeCompare(b.id),
      );
      // Ctx tiers and the -vision twin are rows over the SAME file; only a
      // different file is a variant worth counting.
      const seen = new Set([sorted[0].family || sorted[0].id]);
      const others = sorted.slice(1).filter((m) => {
        const f = m.family || m.id;
        if (seen.has(f)) return false;
        seen.add(f);
        return true;
      });
      const cat = modelCategory(sorted[0]);
      rows.push({ key, pick: sorted[0], others, count, hue: HUE[cat], cat });
    }
    return rows.sort((a, b) => b.count - a.count || a.pick.id.localeCompare(b.pick.id));
  });
  // Grouped by hue in catalog order, most used first inside each; capped so the
  // band stays a shortcut list - the full catalog is the Models page.
  const QUICK_MAX = 12;
  const quickGroups = $derived.by(() => {
    const top = quick.slice(0, QUICK_MAX);
    return HUES.map((h) => ({ hue: h, rows: top.filter((r) => r.hue === h) })).filter((g) => g.rows.length > 0);
  });

  let busy = $state<Record<string, boolean>>({});
  async function load(m: Model): Promise<void> {
    busy = { ...busy, [m.id]: true };
    try {
      await loadModel(m.id);
    } finally {
      busy = { ...busy, [m.id]: false };
    }
  }

  // The quant has its own badge on the row, so it comes off the name - left on,
  // every line read "... Q4 K M  [Q4_K_M]".
  function displayName(m: Model): string {
    const raw = m.name || m.id;
    const q = m.quant?.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const bare = q ? raw.replace(new RegExp(`[-_. ]?${q}$`, "i"), "") : raw;
    return prettifyModelName(bare || raw);
  }
  function quantOf(m: Model): string {
    return m.quant || m.quantLabel || "";
  }

  // --- At a glance ---------------------------------------------------------
  // Local, listed models only: peers live on someone else's disk, and unlisted
  // entries are variants the catalog deliberately hides.
  const listed = $derived($models.filter((m) => !m.unlisted && !m.peerID));
  // The real figure is the models folder walked server-side, which agrees with
  // the OS file manager. The catalog sum is only the fallback for a build with
  // no models root, and it must count each FILE once: every ctx tier and the
  // -vision twin are listed rows over the same gguf, and summing rows counted
  // one file up to five times (956 GB shown for a 420 GB folder).
  let disk = $state<HubDiskUsage | null>(null);
  onMount(() => {
    getHubDiskUsage().then((d) => (disk = d)).catch(() => {});
  });
  const uniqueFiles = $derived([...new Map(listed.map((m) => [m.family || m.id, m])).values()]);
  const catalogGB = $derived(uniqueFiles.reduce((a, m) => a + (m.sizeGB ?? 0), 0));
  const diskGB = $derived(disk ? disk.bytes / 1024 ** 3 : catalogGB);
  const catCounts = $derived(
    HUES.map((h) => ({ hue: h, n: listed.filter((m) => HUE[modelCategory(m)] === h).length })).filter((c) => c.n > 0),
  );
  // The folder walk has no idea what a file is, so the bar splits by what the
  // CATALOG weighs per category. Proportions, not a claim about the total.
  const diskSplit = $derived(
    HUES.map((h) => ({ hue: h, gb: uniqueFiles.filter((m) => HUE[modelCategory(m)] === h).reduce((a, m) => a + (m.sizeGB ?? 0), 0) })).filter(
      (c) => c.gb > 0,
    ),
  );
  const catTip = $derived(
    MODEL_CATEGORIES.map((c) => ({ label: c.label, n: listed.filter((m) => modelCategory(m) === c.id).length }))
      .filter((c) => c.n > 0)
      .map((c) => `${c.n} ${c.label}`)
      .join(", "),
  );

  // The metrics store is this SESSION's request log (it is cleared on reconnect),
  // so these are "since the server came up", not all-time counters.
  const tokensOut = $derived($metrics.reduce((sum, e) => sum + (e.tokens?.output_tokens ?? 0), 0));
  const rates = $derived($metrics.map((e) => e.tokens?.tokens_per_second ?? 0).filter((r) => r > 0));
  const avgTps = $derived(rates.length ? rates.reduce((a, b) => a + b, 0) / rates.length : 0);

  // Requests per 2-minute bucket over the last 30 minutes. `now` ticks every
  // 20 s so an idle dashboard still watches its bars slide out to the left.
  const BUCKETS = 15;
  const BUCKET_MS = 2 * 60_000;
  let now = $state(Date.now());
  onMount(() => {
    const t = setInterval(() => (now = Date.now()), 20_000);
    return () => clearInterval(t);
  });
  const hist = $derived.by(() => {
    const b = new Array<number>(BUCKETS).fill(0);
    for (const e of $metrics) {
      const age = now - new Date(e.timestamp).getTime();
      const i = BUCKETS - 1 - Math.floor(Math.max(0, age) / BUCKET_MS);
      if (i >= 0) b[i]++;
    }
    return b;
  });
  const histMax = $derived(Math.max(1, ...hist));
  const recentWindow = $derived(hist.reduce((a, b) => a + b, 0));

  // Last 24 speeds, oldest to newest ($metrics is newest first). One point is
  // not a line, so the sparkline needs two.
  const spark = $derived.by(() => {
    const pts = $metrics
      .map((e) => e.tokens?.tokens_per_second ?? 0)
      .filter((r) => r > 0)
      .slice(0, 24)
      .reverse();
    if (pts.length < 2) return null;
    const lo = Math.min(...pts);
    const hi = Math.max(...pts);
    const span = hi - lo || 1;
    const xy = pts.map((p, i) => [(i / (pts.length - 1)) * 100, 22 - ((p - lo) / span) * 18 + 1] as const);
    const line = xy.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(2)} ${y.toFixed(2)}`).join(" ");
    return { line, area: `${line} L100 24 L0 24 Z`, lo, hi };
  });

  // --- Recent --------------------------------------------------------------
  const recent = $derived($metrics.slice(0, 9));
  const recentMaxTps = $derived(Math.max(1, ...recent.map((e) => e.tokens?.tokens_per_second ?? 0)));
  // The endpoint names the verb better than the model's category does: an LLM
  // can answer an embeddings call, and a peer's model is not in our catalog.
  function endpoint(path: string): { label: string; icon: (typeof ICON)[ModelCategory] } {
    const p = shortReqPath(path);
    if (p.includes("video")) return { label: "video", icon: Film };
    if (p.includes("images") || p.startsWith("sdapi")) return { label: "image", icon: Image };
    if (p.includes("transcriptions")) return { label: "transcribe", icon: Mic };
    if (p.includes("audio") || p.includes("speech")) return { label: "speech", icon: Volume2 };
    if (p.includes("embeddings")) return { label: "embed", icon: Layers };
    if (p.includes("segment")) return { label: "segment", icon: ScanLine };
    if (p.includes("3d")) return { label: "3d", icon: Box };
    if (p.includes("rerank")) return { label: "rerank", icon: Layers };
    return { label: p.startsWith("chat") ? "chat" : p.split("/")[0] || p, icon: MessageSquare };
  }
  function hueOfModel(id: string): Hue {
    const m = byId.get(id);
    return m ? HUE[modelCategory(m)] : "other";
  }

  // Observe remembers which tab you left it on, so send it to Activity rather
  // than dropping you on Logs because that is where you were an hour ago.
  function openActivity(): void {
    observeTab.set("activity");
    push("/observe");
  }

  function fmtGB(gb: number): { v: string; u: string } {
    return gb >= 1024 ? { v: (gb / 1024).toFixed(2), u: "TB" } : { v: gb.toFixed(0), u: "GB" };
  }
</script>

{#snippet stack(parts: { flex: number; color: string; tip: string }[], h = "h-1.5")}
  <div class="flex w-full {h} gap-0.5 overflow-hidden rounded-full self-center">
    {#each parts as p, i (i)}
      <span class="h-full min-w-0.5" style="flex: {p.flex}; background: {p.color}" use:tip={p.tip}></span>
    {/each}
  </div>
{/snippet}

{#snippet key(color: string, text: string)}
  <span class="inline-flex items-center gap-1 mr-2.5 whitespace-nowrap">
    <i class="inline-block w-1.5 h-1.5 rounded-sm" style="background: {color}"></i>{text}
  </span>
{/snippet}

<!-- Bands, not boxes - the idiom the rest of the app now uses (see KvCache.svelte).
     min-h-full rather than h-full: the lower split stretches to the bottom on a
     tall window, and the page still scrolls when a list outgrows it. -->
<div class="flex flex-col min-h-full bg-surface">
  <!-- ── Now running ─────────────────────────────────────────────────────── -->
  <!-- Always present, even idle. This is the landing page, and the state you
       land in after a cold start is "nothing loaded" - a band that disappears
       exactly then left the home page empty at the one moment it had a job. -->
  <!-- No band header: the status rail directly above already names what is
       loaded, so a "Now running / idle" strip repeated it in bigger type. -->
  <section class="shrink-0">
    {#if $models.some((m) => m.state === "ready" || m.state === "starting" || m.state === "stopping")}
      <ActiveModelsPanel category="all" />
    {:else}
      <div class="flex flex-col items-center justify-center gap-1 py-10 border-b border-card-border-inner">
        <span class="inline-block w-2.5 h-2.5 rounded-full bg-txtsecondary"></span>
        <p class="text-label text-txtsecondary mt-1">No model is loaded. VRAM is free.</p>
        <p class="text-micro text-txtsecondary">Pick one below, or browse the full catalog on the Models page.</p>
      </div>
    {/if}
  </section>

  <!-- ── At a glance ─────────────────────────────────────────────────────── -->
  <!-- No VRAM tile: the status rail above shows the same bar on every page
       (free is on its tooltip). No header row: four labelled tiles name themselves, and the strip sits
       between two bands that do have one. Each tile is number + one small
       picture of where it came from + one line of detail. -->
  <section class="shrink-0 grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-2.5 px-3 py-3 border-b border-card-border-inner">
    <div class="tile !gap-0 min-w-0">
      <span class="tile__label" use:tip={catTip}>Catalog</span>
      <span class="tile__value !text-2xl">{listed.length}<small class="glance-unit">models</small></span>
      <div class="h-6 mt-1 flex">
        {@render stack(catCounts.map((c) => ({ flex: c.n, color: hueVar(c.hue), tip: `${c.n} ${HUE_LABEL[c.hue]}` })))}
      </div>
      <span class="tile__sub truncate mt-1">
        {#each catCounts as c (c.hue)}{@render key(hueVar(c.hue), `${c.n} ${HUE_SHORT[c.hue]}`)}{:else}no models{/each}
      </span>
    </div>

    <div class="tile !gap-0 min-w-0">
      <span
        class="tile__label"
        use:tip={disk
          ? `Every file under ${disk.root}, the same total the OS file manager shows. Rescanned every few minutes. The bar splits it by what the catalog weighs per category.`
          : "Weights of the listed models, each file counted once. Projectors, drafts, VAEs and encoders are not included."}
        >On disk</span
      >
      <span class="tile__value !text-2xl">{fmtGB(diskGB).v}<small class="glance-unit">{fmtGB(diskGB).u}</small></span>
      <div class="h-6 mt-1 flex">
        {@render stack(diskSplit.map((c) => ({ flex: c.gb, color: hueVar(c.hue), tip: `${HUE_LABEL[c.hue]}: ${c.gb.toFixed(0)} GB of catalog weights` })))}
      </div>
      <span class="tile__sub truncate mt-1" use:tip={disk?.root}>
        {disk ? `${disk.files.toLocaleString()} files${disk.skipped ? `, ${disk.skipped} unreadable` : ""} · ${disk.root}` : "catalog weights"}
      </span>
    </div>

    <div class="tile !gap-0 min-w-0">
      <span class="tile__label" use:tip={"Requests served since the server started. The log resets when it restarts. Bars are 2-minute buckets over the last half hour."}>Requests</span>
      <span class="tile__value !text-2xl">{$metrics.length}<small class="glance-unit">this session</small></span>
      <div class="h-6 mt-1 flex items-end gap-[3px]">
        {#each hist as n, i (i)}
          <span
            class="flex-1 rounded-t-[1.5px] {i === BUCKETS - 1 && n > 0 ? 'bg-primary' : 'bg-txtsecondary/45'}"
            style="height: {n ? Math.max(12, (n / histMax) * 100) : 8}%; {n ? '' : 'opacity: .35'}"
          ></span>
        {/each}
      </div>
      <span class="tile__sub truncate mt-1">{recentWindow} in the last 30 min</span>
    </div>

    <div class="tile !gap-0 min-w-0">
      <span class="tile__label" use:tip={"Mean generation speed across every request that reported one. The line is the last 24, oldest on the left."}>Avg speed</span>
      <span class="tile__value !text-2xl">{avgTps > 0 ? avgTps.toFixed(1) : "-"}<small class="glance-unit">{avgTps > 0 ? "tok/s" : ""}</small></span>
      <div class="h-6 mt-1">
        {#if spark}
          <svg class="w-full h-full overflow-visible" viewBox="0 0 100 24" preserveAspectRatio="none" aria-hidden="true">
            <path d={spark.area} class="fill-success" opacity="0.12" />
            <path d={spark.line} fill="none" class="stroke-success" stroke-width="1.6" vector-effect="non-scaling-stroke" stroke-linejoin="round" />
          </svg>
        {:else}
          <div class="h-full flex items-center"><div class="w-full border-t border-dashed border-card-border"></div></div>
        {/if}
      </div>
      <span class="tile__sub truncate mt-1">
        {tokensOut.toLocaleString()} tok out{spark ? ` · ${formatSpeed(spark.lo)}–${formatSpeed(spark.hi)}` : ""}
      </span>
    </div>
  </section>

  <!-- ── Quick load | Recent ─────────────────────────────────────────────── -->
  <!-- Side by side: both are short lists you scan, and stacked they pushed
       Recent below the fold on any laptop. flex-1 so a tall window ends on
       content rather than on a stripe of empty surface. -->
  <div class="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[1fr_1.15fr]">
    <section class="flex flex-col min-w-0 border-b lg:border-b-0 border-card-border-inner">
      <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner shrink-0">
        <h6>Quick load</h6>
        <span class="font-mono text-micro text-txtsecondary">{quick.length} idle · most used first</span>
        <a href="/models" use:link class="ml-auto inline-flex items-center gap-1 text-micro uppercase tracking-wide text-txtsecondary hover:text-primary">
          All models <ArrowRight size={12} />
        </a>
      </div>
      {#if quickGroups.length > 0}
        <div class="pb-2">
          {#each quickGroups as g (g.hue)}
            <div class="flex items-center gap-2 px-3 pt-2.5 pb-1 text-micro font-semibold uppercase tracking-wider text-txtsecondary">
              <i class="inline-block w-1.5 h-1.5 rounded-sm" style="background: {hueVar(g.hue)}"></i>{HUE_LABEL[g.hue]}
            </div>
            {#each g.rows as r (r.key)}
              {@const Icon = ICON[r.cat]}
              <button
                class="quick-row group grid w-full grid-cols-[1.375rem_minmax(0,1fr)_auto_3.5rem_3.5rem] items-center gap-2.5 px-3 py-1.5 text-left transition-colors hover:bg-secondary disabled:opacity-60"
                style="--cat: {hueVar(r.hue)}"
                onclick={() => load(r.pick)}
                disabled={busy[r.pick.id]}
                use:tip={r.others.length ? `Loads ${r.pick.id}. Other variants: ${r.others.map((o) => quantOf(o) || o.id).join(", ")} (Models page)` : r.pick.id}
              >
                <span class="quick-icon grid place-items-center w-[1.375rem] h-[1.375rem] rounded-[5px]"><Icon size={13} /></span>
                <span class="truncate font-mono text-sm text-txtmain group-hover:text-primary">{displayName(r.pick)}</span>
                <span class="flex items-center gap-1.5 text-micro text-txtsecondary whitespace-nowrap">
                  {#if quantOf(r.pick)}<span class="font-mono px-1.5 rounded border border-card-border">{quantOf(r.pick)}</span>{/if}
                  {#if r.others.length}+{r.others.length}{/if}
                </span>
                <span class="font-mono text-micro text-txtsecondary text-right tabular-nums">{r.pick.sizeGB ? r.pick.sizeGB.toFixed(1) + " GB" : ""}</span>
                <!-- Always there, so the row reads as an action before you hover
                     it; grey at rest, the accent once the row is under the cursor. -->
                <span
                  class="justify-self-end rounded border px-2 py-0.5 text-micro uppercase tracking-wide transition-colors {busy[r.pick.id]
                    ? 'border-primary text-primary'
                    : 'border-card-border text-txtsecondary group-hover:border-primary group-hover:text-primary group-focus-visible:border-primary group-focus-visible:text-primary'}"
                >
                  {busy[r.pick.id] ? "…" : "Load"}
                </span>
              </button>
            {/each}
          {/each}
        </div>
      {:else}
        <p class="px-3 py-4 text-label text-txtsecondary">
          No idle models. Add some on the <a href="/models" use:link class="text-primary hover:underline">Models</a> page.
        </p>
      {/if}
    </section>

    <section class="flex flex-col min-w-0 lg:border-l border-card-border-inner">
      <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner shrink-0">
        <h6>Recent</h6>
        <span class="font-mono text-micro text-txtsecondary">{$metrics.length} {$metrics.length === 1 ? "request" : "requests"}</span>
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
            {@const ep = endpoint(e.req_path)}
            {@const tps = e.tokens?.tokens_per_second ?? 0}
            <!-- One grid, not a table: nine rows do not need sticky headers or
                 column resizing, and the Activity page already owns that. -->
            <div class="grid grid-cols-[3.5rem_minmax(0,1fr)_6rem_2.75rem_3.25rem_7.5rem] items-center gap-2.5 px-3 h-8 font-mono text-micro tabular-nums">
              <span class="text-txtsecondary" use:tip={new Date(e.timestamp).toLocaleString()}>{formatRelativeTime(e.timestamp, now)}</span>
              <span class="flex items-center gap-2 min-w-0" use:tip={e.model}>
                <i class="inline-block w-1.5 h-1.5 rounded-full shrink-0" style="background: {hueVar(hueOfModel(e.model))}"></i>
                <span class="truncate text-txtmain">{prettifyModelName(e.model)}</span>
              </span>
              <span class="inline-flex items-center gap-1.5 text-txtsecondary truncate" use:tip={shortReqPath(e.req_path)}>
                <ep.icon size={12} class="shrink-0" />{ep.label}
              </span>
              <span class="text-center {e.resp_status_code >= 400 ? 'text-error' : 'text-success'}">{e.resp_status_code}</span>
              <span class="text-right text-txtsecondary">{e.tokens?.output_tokens ? (e.tokens.output_tokens >= 1000 ? (e.tokens.output_tokens / 1000).toFixed(1) + "k" : e.tokens.output_tokens) + "t" : "-"}</span>
              <span class="flex items-center justify-end gap-2 text-txtsecondary" use:tip={"Generation speed, or wall time for a request that reported none"}>
                {#if tps > 0}
                  <span class="w-11 h-1 rounded-full bg-secondary overflow-hidden"><span class="block h-full bg-txtsecondary/70" style="width: {(tps / recentMaxTps) * 100}%"></span></span>
                  {formatSpeed(tps)}/s
                {:else}
                  {formatDuration(e.duration_ms)}
                {/if}
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
</div>

<style>
  .glance-unit {
    font-size: 0.75rem;
    margin-left: 0.25rem;
    color: var(--color-txtsecondary);
  }
  /* The category tint behind a quick-load icon. color-mix, not an /alpha
     utility, because the hue arrives as a variable per row. */
  .quick-icon {
    color: var(--cat);
    background: color-mix(in srgb, var(--cat) 16%, transparent);
  }
</style>
