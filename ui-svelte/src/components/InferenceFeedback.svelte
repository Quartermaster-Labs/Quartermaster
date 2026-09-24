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
  import { tip } from "../lib/tooltip";
  import { inFlightRequests, metrics, liveTokens, upstreamLogs, backendMetrics } from "../stores/api";
  import { persistentStore } from "../stores/persistent";
  import type { Model, ActivityLogEntry } from "../lib/types";
  import type { FireMode } from "../lib/fireField";
  import { formatRelativeTime } from "../lib/activityFormat";
  import FireField from "./FireField.svelte";

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

  let phase = $state(0);
  let now = $state(Date.now());
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

  // ONE stable interval drives the text readouts (the fire runs its own frame
  // loop). It reads busy/loading/startMs inside the (async) callback, which
  // Svelte does not track as dependencies — so the effect has no reactive deps,
  // runs once, and never rebuilds. That keeps the tick rate constant (no
  // overlapping intervals speeding it up).
  $effect(() => {
    const t = setInterval(() => {
      phase = (phase + 1) % 1_000_000;
      now = Date.now();
      elapsedMs = busy || loading ? Date.now() - startMs : 0;
      loadElapsedMs = loading && gLoadStart ? Date.now() - gLoadStart : 0;
    }, 225);
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
  // "-" while generating (never a stale value from the previous request) and the
  // final figure once idle. Idle => all neutral, all from the last request.
  // Time-to-first-token: live = the measured TTFT pushed once the first token
  // lands; final = the backend's prompt-eval time (prefill finishes right before
  // the first token, so it's the accurate TTFT). -1 => not yet known.
  const liveTtftMs = $derived($liveTokens && $liveTokens.first_token_ms >= 0 ? $liveTokens.first_token_ms : -1);

  // Each readout is a value and its unit, kept apart so the unit can sit small
  // beside a large number. "-" with no unit = not known yet.
  type Stat = { value: string; unit: string };
  const NONE: Stat = { value: "-", unit: "" };
  function speed(n: number): Stat {
    return n > 0 ? { value: n.toFixed(1), unit: "tok/s" } : NONE;
  }
  function dur(ms: number): Stat {
    if (ms < 0) return NONE;
    const [v, u] = fmtDur(ms).match(/^([\d.]+)(\D+)$/)!.slice(1);
    return { value: v, unit: u };
  }
  const stats = $derived.by(() => {
    const lt = $liveTokens;
    const t = last?.tokens;
    return {
      // The three the operator watches: decode rate (the hero), TTFT, prefill rate.
      gen: speed(busy ? liveTps : (t?.tokens_per_second ?? -1)),
      ttft: busy ? dur(liveTtftMs) : t ? dur(t.time_to_first_ms) : NONE,
      prompt: speed(busy ? (activeBackend?.prompt_tokens_seconds ?? -1) : (t?.prompt_per_second ?? -1)),
      duration: busy ? (elapsedMs > 0 ? fmtDur(elapsedMs) : "-") : last ? fmtDur(last.duration_ms) : "-",
      input: busy ? (activeBackend?.prompt_tokens ? activeBackend.prompt_tokens.toLocaleString() : "-") : (t?.input_tokens ?? 0).toLocaleString(),
      output: (busy ? (lt?.output_tokens ?? 0) : (t?.output_tokens ?? 0)).toLocaleString(),
    };
  });

  // What the fire is doing. Prefill ends at the first token, which is also where
  // the determinate prompt progress stops being reported.
  const decoding = $derived(($liveTokens?.output_tokens ?? 0) > 0);
  const fireMode = $derived<FireMode>(loading ? "loading" : !busy ? "idle" : decoding ? "generating" : "prefill");
  const fireProgress = $derived(loading ? (loadPct >= 0 ? loadPct / 100 : -1) : promptProgress);

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
  // Token counts, abbreviated: 65536 -> "64k", 1.5M -> "1.5M". Always k once >=1k
  // so the readout reads "1k/100k", never "1000/100k".
  function fmtK(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_048_576).toFixed(1)}M`;
    if (n >= 1000) return `${Math.round(n / 1024)}k`;
    return String(n);
  }

  // Header status + animated ellipsis (0–3 dots, 450ms/step) for active states.
  const statusText = $derived(loading ? "Loading" : busy ? "Inferencing" : "Idle");
  const dots = $derived(".".repeat(Math.floor(phase / 2) % 4));

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

{#snippet hero(label: string, st: Stat, size: string, help: string)}
  <div class="min-w-0">
    <div class="text-micro font-medium uppercase tracking-wide text-txtsecondary truncate" use:tip={help}>{label}</div>
    <div class="font-mono tabular-nums leading-tight whitespace-nowrap {size} {busy && st.value !== '-' ? 'text-primary' : 'text-txtmain'}">
      {st.value}<span class="ml-1 text-micro text-txtsecondary">{st.unit}</span>
    </div>
  </div>
{/snippet}

<div class="h-full flex flex-col min-h-0">
  <!-- min-h matches the staging card's header row, whose icon buttons make it
       taller than text alone — so the dot/label baselines line up across cards. -->
  <div class="flex items-center gap-2 shrink-0 min-h-[30px]">
    <span class="inline-block w-2.5 h-2.5 rounded-full {busy || loading ? 'bg-primary animate-pulse' : 'bg-txtsecondary'}"></span>
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">
      {statusText}{#if busy || loading}<span class="inline-block w-3 text-left text-primary">{dots}</span>{/if}
    </span>
    <!-- Right corner: a determinate percentage while there is one (model load,
         then prompt processing), otherwise how long ago the last request ended.
         Decode has no determinate signal, so it shows neither. -->
    {#if loading}
      <span class="ml-auto font-mono text-micro tabular-nums text-primary" use:tip={"Model load, estimated from past load times"}>{loadPct >= 0 ? `${loadPct.toFixed(0)}%` : "loading…"}</span>
    {:else if busy && promptProgress >= 0}
      <span class="ml-auto font-mono text-micro tabular-nums text-primary" use:tip={"Prompt processing"}>prompt {Math.round(promptProgress * 100)}%</span>
    {:else if !busy && last}
      <span class="ml-auto text-micro font-medium uppercase tracking-wide text-txtsecondary">Last request · {formatRelativeTime(last.timestamp, now)}</span>
    {/if}
  </div>

  <!-- The fire (lib/fireField.ts): takes whatever height the band has left, so
       the numbers below stay put and the animation absorbs the slack. -->
  <FireField mode={fireMode} progress={fireProgress} tps={liveTps} tokens={$liveTokens?.output_tokens ?? 0} class="flex-1 min-h-10 my-1" />

  <div class="grid grid-cols-[1.3fr_1fr_1fr] gap-x-4 shrink-0">
    {@render hero("Generation", stats.gen, "text-3xl", "Decode speed: live while generating, else the last request")}
    {@render hero("Time to first token", stats.ttft, "text-xl mt-1.5", "Queue + prompt processing, until the first token streamed")}
    {@render hero("Prompt", stats.prompt, "text-xl mt-1.5", "Prompt processing (prefill) speed")}
  </div>

  <!-- Live backend KV-cache fill (from llama-server /slots): how full the
       context window is — the "about to evict / truncate" signal the
       per-request stats can't see. -->
  {#if activeBackend}
    <div class="mt-3 shrink-0" use:tip={"KV cache fill"}>
      <div class="flex items-center gap-2 text-micro">
        <span class="font-medium uppercase tracking-wide text-txtsecondary">Context in use</span>
        <span class="ml-auto font-mono tabular-nums text-txtmain">
          {fmtK(activeBackend.kv_cache_tokens)}<span class="text-txtsecondary"> / {fmtK(activeBackend.n_ctx)} tok · {Math.round(kvPct)}%</span>
        </span>
        {#if activeBackend.requests_deferred > 0}<span class="font-mono tabular-nums text-warning">· {activeBackend.requests_deferred} queued</span>{/if}
      </div>
      <div class="mt-1.5 h-1.5 rounded-full bg-secondary overflow-hidden">
        <div class="h-full rounded-full transition-[width] duration-500 {kvPct >= 90 ? 'bg-warning' : 'bg-primary'}" style="width: {kvPct}%"></div>
      </div>
    </div>
  {/if}

  <div class="mt-2.5 flex gap-4 shrink-0 font-mono text-micro tabular-nums text-txtsecondary">
    <span>in <span class="text-txtmain">{stats.input}</span></span>
    <span>out <span class={busy ? "text-primary" : "text-txtmain"}>{stats.output}</span></span>
    <span>wall <span class={busy ? "text-primary" : "text-txtmain"}>{stats.duration}</span></span>
  </div>
</div>
