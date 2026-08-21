<script lang="ts">
  import { RotateCcw } from "lucide-svelte";
  import { uiScale, setScale, resetScale, MIN_SCALE, MAX_SCALE, SCALE_STEP } from "../stores/uiScale";

  // A snapped range rather than +/- buttons: the whole span is 14 steps, so
  // dragging reaches any size in one gesture, and `step` does the snapping the
  // old buttons did by hand. `setScale` still rounds — the browser reports
  // 0.7000000000000001 for some positions.
  const pct = $derived(Math.round($uiScale * 100));
</script>

<div class="flex items-center gap-2">
  <input
    type="range"
    min={MIN_SCALE}
    max={MAX_SCALE}
    step={SCALE_STEP}
    value={$uiScale}
    oninput={(e) => setScale(Number((e.currentTarget as HTMLInputElement).value))}
    aria-label="Interface size"
    title="Interface size (Ctrl+Plus / Ctrl+Minus / Ctrl+0)"
    class="w-40"
  />

  <!-- tabular-nums so dragging does not shuffle the reset button sideways as
       the label goes 90% -> 100% -> 110%. -->
  <span class="min-w-11 text-right text-label tabular-nums text-txtmain select-none">{pct}%</span>

  <button
    class="flex size-7 items-center justify-center rounded text-txtsecondary transition-colors hover:bg-secondary/60 hover:text-txtmain disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-txtsecondary"
    onclick={resetScale}
    disabled={pct === 100}
    aria-label="Reset interface size to 100%"
    title="Reset to 100% (Ctrl+0)"
  >
    <RotateCcw size={14} />
  </button>
</div>
