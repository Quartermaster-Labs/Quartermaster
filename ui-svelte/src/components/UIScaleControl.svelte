<script lang="ts">
  import { RotateCcw } from "lucide-svelte";
  import { uiScale, setScale, resetScale, MIN_SCALE, MAX_SCALE, SCALE_STEP } from "../stores/uiScale";
  import { tip } from "../lib/tooltip";

  // A snapped range rather than +/- buttons: the whole span is 14 steps, so
  // dragging reaches any size in one gesture, and `step` does the snapping the
  // old buttons did by hand. `setScale` still rounds — the browser reports
  // 0.7000000000000001 for some positions.

  // The scale is COMMITTED on release (`change`), never on `input`.
  //
  // This slider is the one control that resizes itself: --qm-scale drives `zoom`
  // on :root, so applying every intermediate value re-lays-out the track and the
  // thumb *under the cursor mid-drag*. The pointer then sits over a different
  // fraction of the track than it did a frame ago, which feeds back into the
  // next value — the control fights the hand holding it and overshoots to an end
  // stop. Committing on release breaks the loop: the geometry the drag started
  // in is the geometry it ends in.
  //
  // `preview` is the in-flight value, shown in the label so the drag still reads
  // as live. null means "not dragging — show the committed scale". The <input>
  // is deliberately left uncontrolled during the drag (nothing writes `value`
  // while $uiScale holds still), so the browser moves the thumb natively.
  let preview = $state<number | null>(null);

  const shown = $derived(preview ?? $uiScale);
  const pct = $derived(Math.round(shown * 100));
  // The reset button tracks the COMMITTED scale, not the preview: enabling and
  // disabling it as the drag sweeps past 100% is just flicker.
  const atDefault = $derived(Math.round($uiScale * 100) === 100);

  function commit(v: number) {
    preview = null;
    setScale(v);
  }
</script>

<div class="flex items-center gap-2">
  <input
    type="range"
    min={MIN_SCALE}
    max={MAX_SCALE}
    step={SCALE_STEP}
    value={$uiScale}
    oninput={(e) => (preview = Number((e.currentTarget as HTMLInputElement).value))}
    onchange={(e) => commit(Number((e.currentTarget as HTMLInputElement).value))}
    aria-label="Interface size"
    aria-valuetext="{pct}%"
    use:tip={"Interface size, applies when you let go (Ctrl+Plus / Ctrl+Minus / Ctrl+0)"}
    class="w-40"
  />

  <!-- tabular-nums so dragging does not shuffle the reset button sideways as
       the label goes 90% -> 100% -> 110%. -->
  <span class="min-w-11 text-right text-label tabular-nums text-txtmain select-none">{pct}%</span>

  <button
    class="flex size-7 items-center justify-center rounded text-txtsecondary transition-colors hover:bg-secondary/60 hover:text-txtmain disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-txtsecondary"
    onclick={resetScale}
    disabled={atDefault}
    aria-label="Reset interface size to 100%"
    use:tip={"Reset to 100% (Ctrl+0)"}
  >
    <RotateCcw size={14} />
  </button>
</div>
