<script lang="ts">
  import { inFlightRequests, metrics, liveTokens } from "../stores/api";
  import type { Model, ActivityLogEntry } from "../lib/types";

  interface Props {
    // The active (loaded/staged) models shown in the top panel. Used to pick the
    // most recent completed request for an idle readout.
    models: Model[];
  }
  let { models }: Props = $props();

  // This fork serves one model on the GPU at a time (exclusive groups), so the
  // global in-flight count maps cleanly to "the active model is thinking".
  // But a fresh request first triggers a model load (state=starting) while the
  // in-flight count is already >0 — so split that out as "loading" rather than
  // mislabelling the spin-up as generation.
  // "loading" also covers the pre-spawn gap: a request can bump the in-flight
  // count before the process state flips to "starting", and during that window
  // nothing is actually serving — so treat in-flight-with-no-ready-model as
  // loading rather than letting `busy` flash "Inferencing".
  const ready = $derived(models.some((m) => m.state === "ready"));
  const loading = $derived(models.some((m) => m.state === "starting") || ($inFlightRequests > 0 && !ready));

  // Hold "active" true for a short grace period after all activity signals drop.
  // Both the in-flight count and the loading→generating handoff can briefly read
  // zero/idle (especially on a cold first prompt while the model is still
  // loading); without this hold the panel flashes its idle state mid-inference.
  const BUSY_GRACE_MS = 600;
  let activeHold = $state(false);
  let holdTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    const active = loading || $inFlightRequests > 0;
    if (active) {
      if (holdTimer) {
        clearTimeout(holdTimer);
        holdTimer = null;
      }
      activeHold = true;
    } else if (activeHold && !holdTimer) {
      holdTimer = setTimeout(() => {
        activeHold = false;
        holdTimer = null;
      }, BUSY_GRACE_MS);
    }
  });
  // Generating = held-active but not in the model-loading phase.
  const busy = $derived(activeHold && !loading);

  // Animated ASCII wave: a strip of block glyphs whose heights shift each tick,
  // reading like a live activity trace while the model generates.
  const BLOCKS = "▁▂▃▄▅▆▇█";
  const WIDTH = 30;
  let phase = $state(0);
  let elapsedMs = $state(0);
  let startMs = $state(0);

  // Track when an active window begins so the tick can compute elapsed time.
  $effect(() => {
    if (busy || loading) {
      if (startMs === 0) startMs = Date.now();
    } else {
      startMs = 0;
    }
  });

  // ONE stable interval drives every animation. It reads busy/loading/startMs
  // inside the (async) callback, which Svelte does not track as dependencies —
  // so the effect has no reactive deps, runs once, and never rebuilds. That
  // keeps the tick rate constant (no overlapping intervals speeding it up).
  $effect(() => {
    const t = setInterval(() => {
      phase = (phase + 1) % 1_000_000;
      elapsedMs = busy || loading ? Date.now() - startMs : 0;
    }, 90);
    return () => clearInterval(t);
  });

  function waveAt(p: number): string {
    let s = "";
    for (let i = 0; i < WIDTH; i++) {
      // Two summed sines give a non-repetitive, organic-looking trace.
      const v = (Math.sin(i * 0.5 + p * 0.3) + Math.sin(i * 0.23 - p * 0.17)) / 2; // -1..1
      const idx = Math.min(BLOCKS.length - 1, Math.max(0, Math.floor(((v + 1) / 2) * BLOCKS.length)));
      s += BLOCKS[idx];
    }
    return s;
  }
  const wave = $derived(waveAt(phase));

  // Idle "standby" scanner: a single marker drifting slowly back and forth over
  // a dim dotted track. Distinct from the loading wave so idle never reads as a
  // frozen animation.
  function scannerAt(p: number): string {
    const pos = Math.round(((Math.sin(p * 0.06) + 1) / 2) * (WIDTH - 1));
    let s = "";
    for (let i = 0; i < WIDTH; i++) {
      s += i === pos ? "●" : Math.abs(i - pos) === 1 ? "•" : "·";
    }
    return s;
  }
  const idleScan = $derived(scannerAt(phase));

  // Data-stream animation for active generation: a horizontal band of braille
  // glyphs whose dot-density flows leftward each tick (sampling the field at
  // c + p makes features drift toward lower columns). Reads as live throughput.
  const STREAM_W = 26;
  const STREAM_H = 3;
  const STREAM_RAMP = " ⠂⠆⠖⠶⠷⠿"; // sparse → dense braille
  // Warm gradient keyed to dot-density: dim ember → red → orange → amber → hot
  // yellow. Indexed by the ramp position so denser glyphs read "hotter".
  const STREAM_HEAT = [
    "text-orange-900/40",
    "text-red-700",
    "text-red-500",
    "text-orange-600",
    "text-orange-400",
    "text-amber-400",
    "text-yellow-300",
  ];
  type Cell = { ch: string; cls: string };
  function streamAt(p: number): Cell[][] {
    const rows: Cell[][] = [];
    for (let r = 0; r < STREAM_H; r++) {
      const line: Cell[] = [];
      for (let c = 0; c < STREAM_W; c++) {
        const x = c + p * 1.5; // advancing phase scrolls the field left
        // Horizontal shimmer (texture flowing left).
        const flow = (Math.sin(x * 0.5 + r * 1.7) + Math.sin(x * 0.27 - r * 1.1)) / 2; // -1..1
        // Per-column vertical wave: the hot band's boundary licks up and down
        // over time, so the stream reads like a flame rather than a flat field.
        const wave = (Math.sin(c * 0.55 + p * 0.3) + Math.sin(c * 0.27 - p * 0.19)) / 2; // -1..1
        // Vertical gradient: hotter toward the bottom row, boundary shifted by the wave.
        const vert = (r - 1 + wave * 1.2) / (STREAM_H - 1); // ~0 top .. ~1 bottom
        const v = vert * 1.3 + flow * 0.5; // combine heat sources
        const idx = Math.min(STREAM_RAMP.length - 1, Math.max(0, Math.floor(((v + 1) / 2) * STREAM_RAMP.length)));
        line.push({ ch: STREAM_RAMP[idx], cls: STREAM_HEAT[idx] });
      }
      rows.push(line);
    }
    return rows;
  }
  const stream = $derived(streamAt(phase));

  // Live tokens/sec for the in-flight stream (server-pushed, throttled).
  const liveTps = $derived.by<number>(() => {
    const lt = $liveTokens;
    if (!lt || lt.elapsed_ms <= 0) return 0;
    return (lt.output_tokens / lt.elapsed_ms) * 1000;
  });

  // Unified stat readout: always show all six values. While generating, the
  // metrics we can observe live (gen rate, output tokens, duration) are pulled
  // from the live stream and rendered in the accent colour; everything else
  // falls back to the last completed request. Idle => all from `last`, neutral.
  // The six stats are fixed and always rendered — only their values and colour
  // change. The three we can observe mid-stream (Gen rate, Duration, Out) glow
  // accent and update live; the rest we can't see until completion, so they show
  // "—" while generating (never a stale value from the previous request) and the
  // final figure once idle. Idle => all neutral, all from the last request.
  type Stat = { label: string; value: string; live: boolean };
  const stats = $derived.by<Stat[]>(() => {
    const lt = $liveTokens;
    const t = last?.tokens;
    return [
      { label: "Prompt", value: busy ? "—" : fmtSpeed(t?.prompt_per_second ?? -1), live: false },
      { label: "Gen", value: fmtSpeed(busy ? liveTps : (t?.tokens_per_second ?? -1)), live: true },
      { label: "Duration", value: busy ? (elapsedMs > 0 ? fmtDur(elapsedMs) : "—") : last ? fmtDur(last.duration_ms) : "—", live: true },
      { label: "In", value: busy ? "—" : String(t?.input_tokens ?? 0), live: false },
      { label: "Out", value: busy ? String(lt?.output_tokens ?? 0) : String(t?.output_tokens ?? 0), live: true },
      { label: "Cached", value: busy ? "—" : String(t?.cache_tokens ?? 0), live: false },
    ];
  });

  // Most recent completed request for any active model (highest id = newest).
  const last = $derived.by<ActivityLogEntry | null>(() => {
    const ids = new Set(models.map((m) => m.id));
    let best: ActivityLogEntry | null = null;
    for (const m of $metrics) {
      if (!ids.has(m.model)) continue;
      if (!best || m.id > best.id) best = m;
    }
    return best;
  });

  // Header status + animated ellipsis (0–3 dots, ~450ms/step) for active states.
  const statusText = $derived(loading ? "Loading" : busy ? "Inferencing" : "Idle");
  const dots = $derived(".".repeat(Math.floor(phase / 5) % 4));

  function fmtSpeed(n: number): string {
    return n > 0 ? `${n.toFixed(1)} tok/s` : "—";
  }
  // Promote the unit once the count would hit triple digits: ms -> s -> m -> h,
  // so the readout never grows past "99.9" in any unit.
  function fmtDur(ms: number): string {
    if (ms < 1000) return `${ms}ms`;
    const s = ms / 1000;
    if (s < 100) return `${s.toFixed(1)}s`;
    const m = s / 60;
    if (m < 100) return `${m.toFixed(1)}m`;
    return `${(m / 60).toFixed(1)}h`;
  }
</script>

<div class="card h-full flex flex-col min-h-0">
  <!-- min-h matches the staging card's header row, whose icon buttons make it
       taller than text alone — so the dot/label baselines line up across cards. -->
  <div class="flex items-center gap-2 shrink-0 min-h-[30px]">
    <span class="inline-block w-2.5 h-2.5 rounded-full {busy || loading ? 'bg-primary animate-pulse' : 'bg-txtsecondary'}"></span>
    <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">
      {statusText}{#if busy || loading}<span class="inline-block w-3 text-left text-primary">{dots}</span>{/if}
    </span>
    {#if busy || loading}
      <span class="ml-auto font-mono text-[0.6rem] tabular-nums text-primary">{fmtDur(elapsedMs)}</span>
    {/if}
  </div>

  <div class="flex-1 min-h-0 flex flex-col items-center justify-center gap-3 overflow-hidden">
    <!-- Animation: flame while generating, wave while loading, scanner when idle.
         Fixed-height box keeps the multi-row flame from being clipped. -->
    <div class="flex items-center justify-center h-24 shrink-0">
      {#if loading}
        <pre class="font-mono text-primary/70 text-base leading-none tracking-tight select-none m-0 whitespace-pre">{wave}</pre>
      {:else if busy}
        <pre class="font-mono text-3xl leading-tight tracking-tight select-none m-0 whitespace-pre">{#each stream as row, ri (ri)}{#if ri > 0}{"\n"}{/if}{#each row as cell, ci (ci)}<span class={cell.cls}>{cell.ch}</span>{/each}{/each}</pre>
      {:else}
        <pre class="font-mono text-txtsecondary/50 text-base leading-none tracking-tight select-none m-0 whitespace-pre">{idleScan}</pre>
      {/if}
    </div>

<!-- Always-on stat grid. Live metrics (gen/duration/out) glow accent while
         generating; otherwise everything is neutral. -->
    <div class="grid grid-cols-3 gap-x-4 gap-y-2 font-mono text-xs text-center shrink-0 w-full max-w-xs">
      {#each stats as s (s.label)}
        <div>
          <div class="text-txtsecondary uppercase tracking-wide text-[0.55rem]">{s.label}</div>
          <div class="tabular-nums {busy && s.live ? 'text-primary' : 'text-txtmain'}">{s.value}</div>
        </div>
      {/each}
    </div>
  </div>
</div>
