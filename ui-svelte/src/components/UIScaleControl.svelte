<script lang="ts">
  import { Minus, Plus, RotateCcw } from "lucide-svelte";
  import { uiScale, nudgeScale, resetScale, MIN_SCALE, MAX_SCALE } from "../stores/uiScale";

  // Compact by default: the dashboard shows this in the sidebar footer, where
  // there is room for three buttons and nothing else.
  let { compact = false }: { compact?: boolean } = $props();

  const pct = $derived(Math.round($uiScale * 100));
  const atMin = $derived($uiScale <= MIN_SCALE);
  const atMax = $derived($uiScale >= MAX_SCALE);

  const btn =
    "flex items-center justify-center rounded text-txtsecondary hover:text-txtmain hover:bg-secondary/60 transition-colors disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-txtsecondary";
</script>

<div class="flex items-center gap-1 {compact ? '' : 'rounded-md border border-card-border bg-surface p-1'}">
  <button
    class="{btn} {compact ? 'size-6' : 'size-7'}"
    onclick={() => nudgeScale(-1)}
    disabled={atMin}
    aria-label="Make the interface smaller"
    title="Smaller (Ctrl+-)"
  >
    <Minus size={compact ? 13 : 15} />
  </button>

  <!-- tabular-nums so stepping does not shuffle the +/- buttons sideways as the
       label goes 90% -> 100% -> 110%. -->
  <span class="min-w-11 text-center text-label tabular-nums text-txtmain select-none">{pct}%</span>

  <button
    class="{btn} {compact ? 'size-6' : 'size-7'}"
    onclick={() => nudgeScale(1)}
    disabled={atMax}
    aria-label="Make the interface larger"
    title="Larger (Ctrl++)"
  >
    <Plus size={compact ? 13 : 15} />
  </button>

  {#if pct !== 100}
    <button
      class="{btn} {compact ? 'size-6' : 'size-7'}"
      onclick={resetScale}
      aria-label="Reset interface size to 100%"
      title="Reset to 100% (Ctrl+0)"
    >
      <RotateCcw size={compact ? 12 : 14} />
    </button>
  {/if}
</div>
