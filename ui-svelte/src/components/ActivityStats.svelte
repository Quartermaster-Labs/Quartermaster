<script lang="ts">
  import { inFlightRequests, metrics } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import { calculateHistogramData } from "../lib/histogram";
  import TokenHistogram from "./TokenHistogram.svelte";

  const nf = new Intl.NumberFormat();
  const histogramCollapsed = persistentStore<boolean>("activity-histogram-collapsed", false);

  let stats = $derived.by(() => {
    const totalRequests = $metrics.length;
    const totalInputTokens = $metrics.reduce((sum, m) => sum + m.tokens.input_tokens, 0);
    const totalOutputTokens = $metrics.reduce((sum, m) => sum + m.tokens.output_tokens, 0);
    const totalCacheTokens = $metrics.reduce((sum, m) => sum + Math.max(0, m.tokens.cache_tokens), 0);

    const promptPerSecond = $metrics.filter((m) => m.tokens.prompt_per_second > 0).map((m) => m.tokens.prompt_per_second);

    const tokensPerSecond = $metrics.filter((m) => m.tokens.tokens_per_second > 0).map((m) => m.tokens.tokens_per_second);

    const promptHistogramData =
      promptPerSecond.length > 0 ? calculateHistogramData(promptPerSecond) : null;

    const genHistogramData =
      tokensPerSecond.length > 0 ? calculateHistogramData(tokensPerSecond) : null;

    return {
      totalRequests,
      totalInputTokens,
      totalOutputTokens,
      totalCacheTokens,
      inFlightRequests: $inFlightRequests,
      promptHistogramData,
      genHistogramData,
    };
  });
</script>

<div class="card relative p-3">
  <button
    class="absolute top-2 right-2 w-6 h-6 flex items-center justify-center rounded-full border border-card-border text-txtsecondary hover:text-txtmain hover:border-border transition-colors"
    onclick={() => ($histogramCollapsed = !$histogramCollapsed)}
    title={$histogramCollapsed ? "Show histograms" : "Hide histograms"}
  >
    {#if $histogramCollapsed}
      <svg class="w-3.5 h-3.5" viewBox="0 0 16 16" fill="currentColor">
        <path d="M4.5 6l3.5 4 3.5-4H4.5z" />
      </svg>
    {:else}
      <svg class="w-3 h-3" viewBox="0 0 16 16" fill="currentColor">
        <path d="M3.5 3.5l9 9M12.5 3.5l-9 9" stroke="currentColor" stroke-width="2" stroke-linecap="round" fill="none" />
      </svg>
    {/if}
  </button>
  {#if !$histogramCollapsed}
    <div class="flex flex-col sm:flex-row gap-6 mb-3">
      <div class="w-full sm:w-1/2 min-w-0">
        <div class="text-sm font-medium text-txtsecondary mb-1">Prompt Processing</div>
        {#if stats.promptHistogramData}
          <TokenHistogram
            data={stats.promptHistogramData}
            unit="prompt tokens/sec"
            colorClass="text-amber-500 dark:text-amber-400"
          />
        {:else}
          <div class="py-6 text-center text-sm text-txtsecondary">No prompt speed data yet</div>
        {/if}
      </div>
      <div class="w-full sm:w-1/2 min-w-0">
        <div class="text-sm font-medium text-txtsecondary mb-1">Token Generation</div>
        {#if stats.genHistogramData}
          <TokenHistogram data={stats.genHistogramData} unit="tokens/sec" />
        {:else}
          <div class="py-6 text-center text-sm text-txtsecondary">No generation speed data yet</div>
        {/if}
      </div>
    </div>
  {/if}
  <div class="grid grid-cols-4 gap-x-6 gap-y-1 text-sm">
    <div class="text-xs uppercase tracking-wider text-txtsecondary">Requests</div>
    <div class="text-xs uppercase tracking-wider text-txtsecondary">Cached</div>
    <div class="text-xs uppercase tracking-wider text-txtsecondary">Processed</div>
    <div class="text-xs uppercase tracking-wider text-txtsecondary">Generated</div>
    <div class="text-sm text-txtmain">
      <span class="font-semibold">{nf.format(stats.totalRequests)}</span> completed,
      <span class="font-semibold">{nf.format(stats.inFlightRequests)}</span> waiting
    </div>
    <div class="text-sm text-txtmain">
      <span class="font-semibold">{nf.format(stats.totalCacheTokens)}</span> tokens
    </div>
    <div class="text-sm text-txtmain">
      <span class="font-semibold">{nf.format(stats.totalInputTokens)}</span> tokens
    </div>
    <div class="text-sm text-txtmain">
      <span class="font-semibold">{nf.format(stats.totalOutputTokens)}</span> tokens
    </div>
  </div>
</div>
