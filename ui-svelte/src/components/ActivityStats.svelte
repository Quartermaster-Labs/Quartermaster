<script lang="ts">
  import { inFlightRequests, metrics } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { calculateHistogramData } from "../lib/histogram";
  import { ChevronDown, ChevronUp, Hash, Database, ArrowDownToLine, ArrowUpFromLine, Gauge } from "lucide-svelte";
  import TokenHistogram from "./TokenHistogram.svelte";
  import type { ActivityLogEntry } from "../lib/types";

  interface Props {
    // Rows to summarize — the caller passes its window-filtered set so the tiles
    // agree with the table below them. Omitted => every recorded request.
    rows?: ActivityLogEntry[];
  }
  let { rows }: Props = $props();

  const nf = new Intl.NumberFormat();
  const histogramCollapsed = persistentStore<boolean>("activity-histogram-collapsed", false);

  const source = $derived(rows ?? $metrics);

  function median(xs: number[]): number {
    if (xs.length === 0) return 0;
    const s = [...xs].sort((a, b) => a - b);
    const mid = s.length >> 1;
    return s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2;
  }

  let stats = $derived.by(() => {
    const ms = source;
    const totalInputTokens = ms.reduce((sum, m) => sum + m.tokens.input_tokens, 0);
    const totalCacheTokens = ms.reduce((sum, m) => sum + Math.max(0, m.tokens.cache_tokens), 0);
    const promptPerSecond = ms.filter((m) => m.tokens.prompt_per_second > 0).map((m) => m.tokens.prompt_per_second);
    const tokensPerSecond = ms.filter((m) => m.tokens.tokens_per_second > 0).map((m) => m.tokens.tokens_per_second);

    return {
      totalRequests: ms.length,
      totalInputTokens,
      totalOutputTokens: ms.reduce((sum, m) => sum + m.tokens.output_tokens, 0),
      totalCacheTokens,
      // Share of prompt tokens served from cache — the number that actually says
      // whether prefix reuse is working.
      cacheHitPct: totalCacheTokens + totalInputTokens > 0 ? (totalCacheTokens / (totalCacheTokens + totalInputTokens)) * 100 : null,
      inFlightRequests: $inFlightRequests,
      medPrompt: median(promptPerSecond),
      medGen: median(tokensPerSecond),
      promptHistogramData: promptPerSecond.length > 0 ? calculateHistogramData(promptPerSecond) : null,
      genHistogramData: tokensPerSecond.length > 0 ? calculateHistogramData(tokensPerSecond) : null,
    };
  });
</script>

<div class="p-3">
  <!-- KPI row -->
  <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-2">
    <div class="tile">
      <span class="tile__label"><Hash size={11} /> Requests</span>
      <span class="tile__value">{nf.format(stats.totalRequests)}</span>
      <span class="tile__sub">
        {#if stats.inFlightRequests > 0}
          <span class="text-warning">{nf.format(stats.inFlightRequests)} in flight</span>
        {:else}
          idle
        {/if}
      </span>
    </div>
    <div class="tile">
      <span class="tile__label"><Database size={11} /> Cached</span>
      <span class="tile__value">{nf.format(stats.totalCacheTokens)}</span>
      <span class="tile__sub">{stats.cacheHitPct === null ? "no prompts yet" : `${stats.cacheHitPct.toFixed(0)}% of prompt tokens`}</span>
    </div>
    <div class="tile">
      <span class="tile__label"><ArrowDownToLine size={11} /> Processed</span>
      <span class="tile__value">{nf.format(stats.totalInputTokens)}</span>
      <span class="tile__sub">new prompt tokens</span>
    </div>
    <div class="tile">
      <span class="tile__label"><ArrowUpFromLine size={11} /> Generated</span>
      <span class="tile__value">{nf.format(stats.totalOutputTokens)}</span>
      <span class="tile__sub">output tokens</span>
    </div>
    <div class="tile">
      <span class="tile__label"><Gauge size={11} /> Median speed</span>
      <span class="tile__value">{stats.medGen > 0 ? stats.medGen.toFixed(1) : "-"}<span class="text-xs text-txtsecondary"> t/s gen</span></span>
      <span class="tile__sub">{stats.medPrompt > 0 ? `${stats.medPrompt.toFixed(0)} t/s prompt` : "no prompt data"}</span>
    </div>
  </div>

  <!-- Speed distributions -->
  <div class="mt-2 border-t border-card-border-inner pt-1">
    <button
      class="w-full flex items-center gap-1.5 py-1 font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary hover:text-txtmain transition-colors"
      onclick={() => ($histogramCollapsed = !$histogramCollapsed)}
      aria-expanded={!$histogramCollapsed}
    >
      {#if $histogramCollapsed}<ChevronDown size={12} />{:else}<ChevronUp size={12} />{/if}
      Speed distribution
    </button>

    {#if !$histogramCollapsed}
      <div class="flex flex-col sm:flex-row gap-6 mt-1">
        <div class="w-full sm:w-1/2 min-w-0">
          <div class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary mb-1">Prompt processing</div>
          {#if stats.promptHistogramData}
            <TokenHistogram data={stats.promptHistogramData} unit="prompt tokens/sec" colorClass="text-amber-500 dark:text-amber-400" />
          {:else}
            <div class="py-6 text-center text-sm text-txtsecondary">No prompt speed data yet</div>
          {/if}
        </div>
        <div class="w-full sm:w-1/2 min-w-0">
          <div class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary mb-1">Token generation</div>
          {#if stats.genHistogramData}
            <TokenHistogram data={stats.genHistogramData} unit="tokens/sec" />
          {:else}
            <div class="py-6 text-center text-sm text-txtsecondary">No generation speed data yet</div>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</div>
