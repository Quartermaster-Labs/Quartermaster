<script module lang="ts">
  // Load-tracking state lives at MODULE scope so it survives this component
  // unmounting when the user navigates away from the Models page mid-load.
  // Instance-local state resets on remount, which restarted the progress bar at
  // 0% (looking like a slower/restarted load) and recorded a too-short duration
  // into the load-time EMA. Module scope persists for the page's lifetime; a
  // full reload resetting it is fine (the load is usually finished by then).
  let gLoadStart = 0;
  let gLoadId: string | null = null;
  let gPrevLoading = false;
</script>

<script lang="ts">
  import { inFlightRequests, metrics, liveTokens, upstreamLogs, backendMetrics } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
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
  // Track whether the current active window ever saw a real in-flight request.
  // A pure model load (e.g. UI load via /upstream/, preload) flips state
  // starting→ready with no in-flight generation behind it; without this guard
  // the grace hold below would carry over into `busy` and flash "Inferencing"
  // for BUSY_GRACE_MS between load completing and the panel settling to idle.
  let sawInflight = $state(false);
  $effect(() => {
    if ($inFlightRequests > 0) sawInflight = true;
  });
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
        sawInflight = false;
        holdTimer = null;
      }, BUSY_GRACE_MS);
    }
  });
  // Generating = held-active, not in the model-loading phase, and actually
  // backed by an in-flight request (not just a model load winding down).
  const busy = $derived(activeHold && !loading && sawInflight);

  // Width of the single-line idle scanner. Kept a touch under the data-stream
  // footprint (STREAM_W=26) so the wider ●/• marker glyphs don't overrun the
  // card and clip at the edges.
  const WIDTH = 22;
  let phase = $state(0);
  let elapsedMs = $state(0);
  let startMs = $state(0);
  // Elapsed since the load began, derived from the module-scoped start so it is
  // correct immediately on remount (survives navigation mid-load).
  let loadElapsedMs = $state(0);

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
      loadElapsedMs = loading && gLoadStart ? Date.now() - gLoadStart : 0;
    }, 90);
    return () => clearInterval(t);
  });

  // ---- Loading progress bar ----
  // llama.cpp exposes no real load-progress signal, so estimate it from a
  // learned per-model load time: record each successful load's duration (EMA)
  // and render elapsed/expected as a percentage. With no history yet, fall back
  // to an indeterminate sweeping bar.
  const loadMsStore = persistentStore<Record<string, number>>("modelLoadMs", {});
  const loadingModelId = $derived(models.find((m) => m.state === "starting")?.id ?? null);

  $effect(() => {
    const nowLoading = loading; // tracked
    const startingId = loadingModelId; // tracked
    if (nowLoading && !gPrevLoading) {
      gLoadStart = Date.now();
      gLoadId = startingId;
    } else if (nowLoading && !gLoadId && startingId) {
      gLoadId = startingId; // id only known once the process flips to "starting"
    } else if (!nowLoading && gPrevLoading) {
      const dur = Date.now() - gLoadStart;
      const id = gLoadId;
      const isReady = id !== null && models.some((m) => m.id === id && m.state === "ready");
      if (id && isReady && dur > 500 && dur < 10 * 60 * 1000) {
        loadMsStore.update((prev) => ({ ...prev, [id]: Math.round(prev[id] ? prev[id] * 0.6 + dur * 0.4 : dur) }));
      }
      gLoadId = null;
      gLoadStart = 0;
    }
    gPrevLoading = nowLoading;
  });

  // Mean of every learned load time — a cross-model fallback so a model with no
  // history of its own (or the pre-spawn gap where the id isn't known yet) still
  // gets a determinate bar instead of flipping to the indeterminate sweep.
  const avgLoadMs = $derived.by<number>(() => {
    const vals = Object.values($loadMsStore);
    return vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : 0;
  });
  const expLoadMs = $derived((loadingModelId ? $loadMsStore[loadingModelId] : 0) || avgLoadMs);
  // -1 => indeterminate (no history); otherwise clamped 3..99 while loading.
  const loadPct = $derived.by<number>(() => {
    if (!loading || expLoadMs <= 0) return -1;
    return Math.min(99, Math.max(3, (loadElapsedMs / expLoadMs) * 100));
  });

  // Bar width tracks the data-stream footprint so loading reads at the same size
  // as the generating animation.
  const BAR_W = 24;
  const PARTIAL = " ▏▎▍▌▋▊▉█";
  // Indeterminate (no learned time): a 4-cell block sweeping over a dim track.
  function sweepAt(p: number): string {
    const span = 4;
    const pos = Math.round(((Math.sin(p * 0.12) + 1) / 2) * (BAR_W - span));
    let s = "";
    for (let i = 0; i < BAR_W; i++) s += i >= pos && i < pos + span ? "█" : "░";
    return s;
  }
  const loadSweep = $derived(sweepAt(phase));
  // Determinate: split bright fill from dim track so the two render in different
  // colours (filled = primary, track = muted) rather than one flat band.
  const loadFilled = $derived.by<string>(() => {
    if (loadPct < 0) return "";
    const filled = (loadPct / 100) * BAR_W;
    const full = Math.floor(filled);
    let s = "█".repeat(Math.min(full, BAR_W));
    if (full < BAR_W) s += PARTIAL[Math.floor((filled - full) * 8)];
    return s;
  });
  const loadTrack = $derived.by<string>(() => {
    if (loadPct < 0) return "";
    const used = loadFilled.length;
    return "░".repeat(Math.max(0, BAR_W - used));
  });

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

  // Live decode tokens/sec. The server pushes CUMULATIVE tokens + elapsed +
  // measured time-to-first-token. Dividing the post-first-token count by the
  // decode window (elapsed − TTFT) excludes prompt-processing time, so this
  // converges to the backend's authoritative predicted_per_second instead of a
  // prompt-diluted lifetime average. 0 until generation actually starts.
  const liveTps = $derived.by<number>(() => {
    const lt = $liveTokens;
    if (!lt || lt.first_token_ms < 0 || lt.output_tokens <= 1) return 0;
    const decodeMs = lt.elapsed_ms - lt.first_token_ms;
    if (decodeMs <= 0) return 0;
    return ((lt.output_tokens - 1) / decodeMs) * 1000;
  });

  // ---- Prompt-processing progress ----
  // Prefill is the one generation phase with a determinate progress signal:
  // llama-server logs "...prompt processing... progress = 0.46..." to stderr as it
  // chews through the prompt (captured into upstreamLogs). Decode (token streaming)
  // has no such signal. Parse the latest value from the log tail and surface it in
  // the header while prefilling; -1 => unknown/indeterminate.
  const PROMPT_PROGRESS_RE = /progress\s*=\s*([01](?:\.\d+)?)/g;
  let promptProgress = $state(-1);
  let prevBusyForProg = false;
  $effect(() => {
    const logs = $upstreamLogs; // tracked: re-run as new log lines stream in
    const decoding = ($liveTokens?.output_tokens ?? 0) > 0; // first token => prefill done
    if (!busy) {
      promptProgress = -1;
      prevBusyForProg = false;
      return;
    }
    if (!prevBusyForProg) {
      promptProgress = -1; // new active window: drop any stale value until a fresh line lands
      prevBusyForProg = true;
    }
    if (decoding) {
      promptProgress = -1; // no determinate progress once tokens are flowing
      return;
    }
    const tail = logs.slice(-4000);
    const matches = tail.match(PROMPT_PROGRESS_RE);
    if (matches && matches.length) {
      const v = parseFloat(matches[matches.length - 1].split("=")[1]);
      if (!Number.isNaN(v)) promptProgress = v;
    }
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
  // Time-to-first-token: live = the measured TTFT pushed once the first token
  // lands; final = the backend's prompt-eval time (prefill finishes right before
  // the first token, so it's the accurate TTFT). -1 => not yet known.
  const liveTtftMs = $derived($liveTokens && $liveTokens.first_token_ms >= 0 ? $liveTokens.first_token_ms : -1);

  type Stat = { label: string; value: string; live: boolean };
  const stats = $derived.by<Stat[]>(() => {
    const lt = $liveTokens;
    const t = last?.tokens;
    return [
      // The three the operator watches: TTFT, prefill rate, decode rate.
      { label: "TTFT", value: busy ? (liveTtftMs >= 0 ? fmtDur(liveTtftMs) : "-") : t && t.time_to_first_ms >= 0 ? fmtDur(t.time_to_first_ms) : "-", live: true },
      { label: "Prompt", value: busy ? fmtSpeed(activeBackend?.prompt_tokens_seconds ?? -1) : fmtSpeed(t?.prompt_per_second ?? -1), live: true },
      { label: "Gen", value: fmtSpeed(busy ? liveTps : (t?.tokens_per_second ?? -1)), live: true },
      { label: "Duration", value: busy ? (elapsedMs > 0 ? fmtDur(elapsedMs) : "-") : last ? fmtDur(last.duration_ms) : "-", live: true },
      { label: "In", value: busy ? (activeBackend?.prompt_tokens ? String(activeBackend.prompt_tokens) : "-") : String(t?.input_tokens ?? 0), live: true },
      { label: "Out", value: busy ? String(lt?.output_tokens ?? 0) : String(t?.output_tokens ?? 0), live: true },
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

  // Live backend gauges (KV-cache fill, slot/queue saturation) for the active
  // model, scraped from its llama-server /metrics + /props. Prefer the ready
  // model; fall back to any running backend with a successful scrape.
  const activeBackend = $derived.by(() => {
    const ids = models.filter((m) => m.state === "ready").map((m) => m.id);
    const order = ids.length ? ids : models.map((m) => m.id);
    for (const id of order) {
      const bm = $backendMetrics[id];
      if (bm && bm.ok) return bm;
    }
    return null;
  });
  const kvPct = $derived(activeBackend ? Math.min(100, Math.max(0, activeBackend.kv_cache_usage_ratio * 100)) : -1);
  // Compact partial-block bar for the header row, mirroring the loading bar's
  // glyph set but narrow enough to sit beside the status label.
  const HEAD_KV_W = 12;
  const kvBarHead = $derived.by<string>(() => {
    if (kvPct < 0) return "";
    const filled = (kvPct / 100) * HEAD_KV_W;
    const full = Math.floor(filled);
    let s = "█".repeat(Math.min(full, HEAD_KV_W));
    if (full < HEAD_KV_W) s += PARTIAL[Math.floor((filled - full) * 8)];
    return s + "░".repeat(Math.max(0, HEAD_KV_W - s.length));
  });
  const kvHeadFill = $derived(kvPct >= 0 ? Math.round((kvPct / 100) * HEAD_KV_W) : 0);
  // Token counts, abbreviated: 65536 -> "64k", 1.5M -> "1.5M". Always k once >=1k
  // so the readout reads "1k/100k", never "1000/100k".
  function fmtK(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_048_576).toFixed(1)}M`;
    if (n >= 1000) return `${Math.round(n / 1024)}k`;
    return String(n);
  }

  // Header status + animated ellipsis (0–3 dots, ~450ms/step) for active states.
  const statusText = $derived(loading ? "Loading" : busy ? "Inferencing" : "Idle");
  const dots = $derived(".".repeat(Math.floor(phase / 5) % 4));

  function fmtSpeed(n: number): string {
    return n > 0 ? `${n.toFixed(1)} tok/s` : "-";
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
  <div class="relative flex items-center gap-2 shrink-0 min-h-[30px]">
    <span class="inline-block w-2.5 h-2.5 rounded-full {busy || loading ? 'bg-primary animate-pulse' : 'bg-txtsecondary'}"></span>
    <span class="font-mono text-[0.6rem] uppercase tracking-wide text-txtsecondary">
      {statusText}{#if busy || loading}<span class="inline-block w-3 text-left text-primary">{dots}</span>{/if}
    </span>
    <!-- Progress %: model load while loading, prompt-processing (prefill) while
         generating. Decode has no determinate signal, so the corner clears once
         tokens stream — the elapsed duration lives in the stat grid (shown once,
         not duplicated here). -->
    {#if busy && promptProgress >= 0}
      <span class="ml-auto font-mono text-[0.6rem] tabular-nums text-primary" title="Prompt processing">{Math.round(promptProgress * 100)}%</span>
    {/if}
    <!-- Live backend KV-cache fill (from llama-server /slots): how full the
         context window is — the "about to evict / truncate" signal the
         per-request stats can't see. X/Y tok rides on the bar line. -->
    {#if activeBackend}
      <div class="absolute left-1/2 -translate-x-1/2 flex items-center gap-1.5 font-mono text-[0.55rem]" title="KV cache fill">
        <pre class="m-0 leading-none whitespace-pre"><span class="text-primary">{kvBarHead.slice(0, kvHeadFill)}</span><span class="text-txtsecondary/30">{kvBarHead.slice(kvHeadFill)}</span></pre>
        <span class="tabular-nums text-txtsecondary">{fmtK(activeBackend.kv_cache_tokens)}/{fmtK(activeBackend.n_ctx)} tok</span>
        {#if activeBackend.requests_deferred > 0}<span class="tabular-nums text-amber-400">· {activeBackend.requests_deferred} q</span>{/if}
      </div>
    {/if}
  </div>

  <div class="flex-1 min-h-0 flex flex-col items-center justify-center gap-3 overflow-hidden">
    <!-- Animation: flame while generating, wave while loading, scanner when idle.
         Fixed-height box keeps the multi-row flame from being clipped. -->
    <div class="flex items-center justify-center h-24 shrink-0">
      {#if loading}
        <div class="flex flex-col items-center justify-center gap-2 select-none">
          <pre class="font-mono text-2xl leading-none tracking-tight m-0 whitespace-pre">{#if loadPct < 0}<span class="text-primary">{loadSweep}</span>{:else}<span class="text-primary">{loadFilled}</span><span class="text-txtsecondary/30">{loadTrack}</span>{/if}</pre>
          <span class="font-mono text-primary text-sm tabular-nums">{loadPct >= 0 ? `${loadPct.toFixed(0)}%` : "loading…"}</span>
        </div>
      {:else if busy}
        <pre class="font-mono text-3xl leading-tight tracking-tight select-none m-0 whitespace-pre">{#each stream as row, ri (ri)}{#if ri > 0}{"\n"}{/if}{#each row as cell, ci (ci)}<span class={cell.cls}>{cell.ch}</span>{/each}{/each}</pre>
      {:else}
        <pre class="font-mono text-txtsecondary/50 text-3xl leading-tight tracking-tight select-none m-0 whitespace-pre">{idleScan}</pre>
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
