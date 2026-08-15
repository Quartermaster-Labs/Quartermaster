<script lang="ts">
  // A slider over a table of stops rather than over a number line: the range
  // input's own value is the INDEX, so the spacing of the values is whatever the
  // table says (see lib/logScale.ts — geometric, so a step is a constant
  // proportion). The caller passes the stops and the label formatter.
  import { nearestStop } from "../lib/logScale";

  interface Props {
    label: string;
    stops: number[];
    value: number;
    format: (stop: number) => string;
    commit: (stop: number) => void;
  }
  let { label, stops, value, format, commit }: Props = $props();

  // Two positions, deliberately. `idx` is where the committed value sits and is
  // what the thumb snaps back to if a drag is abandoned; `live` is where the
  // thumb is right now, so the readout tracks the finger during a drag that has
  // not been committed yet.
  const idx = $derived(nearestStop(stops, value));
  let live = $state<number | null>(null);
  const pos = $derived(live ?? idx);

  function onInput(e: Event): void {
    live = +(e.currentTarget as HTMLInputElement).value;
  }

  // Committed on release, not on every stop crossed: some of these filters
  // re-run the hub search, and dragging across the whole track would otherwise
  // fire a request per stop.
  function onChange(e: Event): void {
    const i = +(e.currentTarget as HTMLInputElement).value;
    live = null;
    commit(stops[i]);
  }
</script>

<div class="flex flex-col gap-1">
  <div class="flex items-baseline justify-between">
    <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">{label}</span>
    <span class="font-mono text-[0.65rem] tabular-nums text-txtprimary">{format(stops[pos])}</span>
  </div>
  <input
    type="range"
    class="w-full accent-primary"
    min="0"
    max={stops.length - 1}
    step="1"
    value={pos}
    oninput={onInput}
    onchange={onChange}
    aria-label={label}
    aria-valuetext={format(stops[pos])}
  />
</div>
