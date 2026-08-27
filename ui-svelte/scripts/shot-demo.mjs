// Demo state for the screenshot harness (`npm run shots -- --demo`).
//
// An idle instance photographs badly: "No model is loaded", an empty activity
// log and a flat VRAM chart say nothing about what the app does. This puts a
// model in front of the camera -- loaded, with traffic behind it -- without
// loading anything and without sending a single inference request. Nothing here
// touches the scheduler, so a capture run can never evict what the machine is
// actually serving.
//
// It is a HALF-fake, deliberately. The catalog is recorded live from the
// instance being shot, so every model id, quant, context size and VRAM estimate
// in the picture is real and stays current as the catalog changes; only the
// *state* -- which model is up, what was served, how full the KV is -- is
// synthesized. That keeps the screenshots honest about what the app is, and
// keeps the fixture from rotting into a museum piece of an old schema.
//
// ## Why it replaces EventSource rather than routing /api/events
//
// The dashboard's model list, activity metrics, backend gauges, in-flight count
// and log stream all arrive over ONE SSE channel (`/api/models/` is not even a
// route). Playwright can intercept it -- but `route.fulfill` hands back a
// finite body and closes, and stores/api.ts reads a closed stream as `onerror`:
// it drops the connection, backs off, and flips `connectionState` to
// "connecting", which TitleBar renders as a pulsing, desaturated mark. Every
// shot would picture a half-disconnected app.
//
// So the fake goes in one layer up, in an init script, the same trick --native
// plays on the qm* bindings: replace the boundary the app reaches for, before
// any app code runs. Ours reports open, replays the canned envelopes and never
// errors, so the app believes it is connected for as long as the page lives.
//
// The plain-fetch endpoints (`/api/performance`, `/api/captures/*`) have no
// such problem and are ordinary `page.route` fulfillments.

/**
 * Which model the screenshots star. Explicit rather than computed, so a shot is
 * reproducible and does not change model the day a bigger download lands.
 *
 * `pickModel` falls back to the largest fully-offloaded chat model when this id
 * is not in the catalog, so the harness still produces something sensible on a
 * machine that has never heard of it.
 */
export const PREFERRED_MODEL = "qwen3.8-27b-ud-q4_k_xl";

// Throughput the fake traffic is generated around. These are screenshots of a
// real product: numbers that cannot be reproduced on the hardware in the shot
// are a performance claim, not a mockup. Defaults are the measured figures for
// a dense 27B at Q4 on the 24GB card this was built against (MTP on, depth 2)
// -- pass --demo-tps / --demo-pps when shooting on something else.
export const DEFAULT_TPS = 58; // decode, tokens/s
export const DEFAULT_PPS = 820; // prefill, tokens/s

/** Variant suffixes autogen emits for one gguf. The star of a shot is the plain one. */
const VARIANT = /-(?:\d+k|game|judge|vision|base)$/;

function isChat(m) {
  const c = m.capabilities ?? {};
  return !(
    c.image_generation ||
    c.audio_speech ||
    c.audio_transcriptions ||
    c.segmentation ||
    c.embeddings ||
    c.reranker
  );
}

/**
 * The model to show as loaded: `wanted` if the catalog has it, else the largest
 * chat model that fits entirely in VRAM (`estRamGB` set means part of it is
 * served from system memory -- a partial offload is a poor advertisement).
 */
export function pickModel(models, wanted = PREFERRED_MODEL) {
  const exact = models.find((m) => m.id === wanted);
  if (exact) return exact;
  const fits = models
    .filter((m) => isChat(m) && !VARIANT.test(m.id) && !m.unlisted && !m.estRamGB)
    .sort((a, b) => (b.sizeGB ?? 0) - (a.sizeGB ?? 0));
  return fits[0] ?? models[0];
}

/**
 * Reads the live catalog off the instance's own event stream.
 *
 * `GET /api/models/` does not exist -- the UI only ever learns the catalog from
 * SSE -- so recording means opening the stream and keeping the first
 * `modelStatus` frame. Resolves as soon as that arrives; the log frames that
 * come with it are kept so the log viewer has real text in it.
 */
export async function recordCatalog(url, timeoutMs = 15000) {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), timeoutMs);
  try {
    const res = await fetch(`${url}/api/events`, {
      headers: { accept: "text/event-stream" },
      signal: ctl.signal,
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    const logs = [];
    for (;;) {
      const { value, done } = await reader.read();
      if (done) throw new Error("stream ended before a modelStatus frame");
      buf += dec.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) !== -1) {
        const frame = buf.slice(0, i);
        buf = buf.slice(i + 2);
        const line = frame.split("\n").find((l) => l.startsWith("data:"));
        if (!line) continue;
        let env;
        try {
          env = JSON.parse(line.slice(5).trim());
        } catch {
          continue;
        }
        if (env.type === "logData") logs.push(env);
        if (env.type !== "modelStatus") continue;
        void reader.cancel().catch(() => {});
        return { models: JSON.parse(env.data), logs };
      }
    }
  } finally {
    clearTimeout(timer);
    ctl.abort();
  }
}

// One activity row each. `path` and `ct` are what the server records; `in`/`out`
// are prompt and completion tokens. Deliberately a spread of modalities -- the
// pitch is one engine for all of them, and an activity log of nothing but chat
// completions does not show that.
//
// Everything sits inside four minutes on purpose. Activity opens on its 5 MIN
// window, so traffic spread over half an hour photographs as three lonely rows
// and a two-bar histogram; a working session's requests arrive in bursts
// anyway. Keep new entries under agoMin 5 or they will not be in the picture.
const TRAFFIC = [
  { agoMin: 0.3, path: "/v1/chat/completions", ct: "text/event-stream", in: 8214, out: 612, cached: 7936 },
  { agoMin: 0.7, path: "/v1/chat/completions", ct: "text/event-stream", in: 2190, out: 389, cached: 1984 },
  { agoMin: 1.1, path: "/v1/chat/completions", ct: "application/json", in: 611, out: 148, cached: 0 },
  { agoMin: 1.5, path: "/v1/chat/completions", ct: "text/event-stream", in: 15602, out: 947, cached: 15104 },
  { agoMin: 1.9, path: "/v1/chat/completions", ct: "text/event-stream", in: 1042, out: 233, cached: 0 },
  { agoMin: 2.4, path: "/v1/chat/completions", ct: "application/json", in: 3480, out: 96, cached: 3200 },
  { agoMin: 2.8, path: "/v1/chat/completions", ct: "text/event-stream", in: 704, out: 1508, cached: 0 },
  { agoMin: 3.2, path: "/v1/chat/completions", ct: "text/event-stream", in: 9330, out: 421, cached: 8960 },
  { agoMin: 3.6, path: "/v1/chat/completions", ct: "text/event-stream", in: 24118, out: 806, cached: 23552 },
  { agoMin: 4.1, path: "/v1/chat/completions", ct: "application/json", in: 1875, out: 274, cached: 1664 },
  { agoMin: 4.5, path: "/v1/chat/completions", ct: "text/event-stream", in: 5602, out: 1133, cached: 5120 },
];

/**
 * Builds every fixture a demo run needs from the recorded catalog.
 *
 * Returns the doctored envelope list for the fake stream, the doctored
 * `/api/performance` body, the captures the activity log can open, and the
 * model that ended up starring (for the run's own log line).
 */
export function buildDemo(models, perf, opts = {}) {
  const tps = opts.tps ?? DEFAULT_TPS;
  const pps = opts.pps ?? DEFAULT_PPS;
  const star = pickModel(models, opts.model);
  const now = Date.now();

  // A model that is up plus its siblings left alone: exactly one "ready" row is
  // what the exclusive swap group would really look like.
  const doctored = models.map((m) => (m.id === star.id ? { ...m, state: "ready" } : m));

  // Something for the multi-modal rows to point at, when the catalog has one.
  const imageModel = models.find((m) => m.capabilities?.image_generation && !VARIANT.test(m.id));
  const speechModel = models.find((m) => m.capabilities?.audio_speech && !VARIANT.test(m.id));

  const chat = TRAFFIC.map((t, i) => {
    const promptMs = Math.round((t.in - t.cached) / pps * 1000) + 40;
    const genMs = Math.round((t.out / tps) * 1000);
    return {
      agoMin: t.agoMin,
      model: star.id,
      req_path: t.path,
      resp_content_type: t.ct,
      resp_status_code: 200,
      duration_ms: promptMs + genMs,
      has_capture: i < 3,
      tokens: {
        cache_tokens: t.cached,
        input_tokens: t.in,
        output_tokens: t.out,
        // A cached prefix is not re-processed, so prefill rate is reported over
        // the tokens actually run -- reporting it over the whole prompt is how
        // a cache hit turns into an imaginary 40k tok/s.
        prompt_per_second: t.in > t.cached ? Math.round(((t.in - t.cached) / promptMs) * 1000) : 0,
        tokens_per_second: Number((tps + ((i % 5) - 2) * 1.4).toFixed(2)),
        prompt_ms: promptMs,
        time_to_first_ms: promptMs + 60 + (i % 4) * 35,
      },
    };
  });

  // A generation and a synthesis in among the chat turns: one engine, several
  // modalities, which is the thing worth photographing. Neither reports tokens
  // -- an image is not measured in tok/s, and the row showing "-" for those
  // columns is what the real log does too.
  const noTokens = { cache_tokens: 0, input_tokens: 0, output_tokens: 0, prompt_per_second: 0, tokens_per_second: 0, prompt_ms: 0, time_to_first_ms: 0 };
  const media = [];
  if (imageModel) {
    media.push({ agoMin: 2.0, model: imageModel.id, req_path: "/v1/images/generations", resp_content_type: "application/json", resp_status_code: 200, duration_ms: 18_420, has_capture: false, tokens: noTokens });
  }
  if (speechModel) {
    media.push({ agoMin: 3.9, model: speechModel.id, req_path: "/v1/audio/speech", resp_content_type: "audio/wav", resp_status_code: 200, duration_ms: 2_140, has_capture: false, tokens: noTokens });
  }

  // Ids are assigned AFTER merging and sorting, never per-source. The activity
  // table orders by id, so hand-numbering the media rows sent a request from two
  // minutes ago to the top of a list of newer ones. Id order has to be time
  // order, which is what a real server-side counter would have given them.
  const rows = [...chat, ...media]
    .sort((a, b) => a.agoMin - b.agoMin)
    .map(({ agoMin, ...r }, i, all) => ({
      ...r,
      id: 1000 + all.length - i,
      timestamp: new Date(now - agoMin * 60_000).toISOString(),
    }));

  const ctx = star.ctx || 32768;
  const kvTokens = Math.round(ctx * 0.34);
  const served = rows.filter((r) => r.model === star.id);
  const backend = {
    model: star.id,
    timestamp: new Date(now).toISOString(),
    ok: true,
    kv_cache_usage_ratio: kvTokens / ctx,
    kv_cache_tokens: kvTokens,
    requests_processing: 0,
    requests_deferred: 0,
    prompt_tokens_total: served.reduce((a, r) => a + r.tokens.input_tokens, 0),
    tokens_predicted_total: served.reduce((a, r) => a + r.tokens.output_tokens, 0),
    n_decode_total: served.reduce((a, r) => a + r.tokens.output_tokens, 0),
    prompt_seconds_total: Number((served.reduce((a, r) => a + r.tokens.prompt_ms, 0) / 1000).toFixed(2)),
    predicted_seconds_total: Number(
      served.reduce((a, r) => a + r.tokens.output_tokens / tps, 0).toFixed(2),
    ),
    n_ctx: ctx,
    total_slots: 1,
    prompt_tokens: 0,
    prompt_tokens_seconds: 0,
    predicted_tokens_seconds: 0,
  };

  // Envelope order is the order the app would have received them in: the
  // catalog first (so the metrics that follow name a model it already knows),
  // then history, then the live gauges.
  const wrap = (type, data) => ({ type, data: JSON.stringify(data) });
  const envelopes = [
    wrap("modelStatus", doctored),
    ...(opts.logs ?? []),
    wrap("logData", { source: "upstream", data: upstreamLog(star, ctx) }),
    // Newest first: the store prepends each batch as it arrives.
    wrap("metrics", rows),
    wrap("backendMetrics", [backend]),
    wrap("inflight", { total: 0 }),
  ];

  return {
    model: star,
    envelopes,
    perf: doctorPerf(perf, star),
    captures: Object.fromEntries(
      rows.filter((r) => r.has_capture).map((r) => [r.id, capture(r, star)]),
    ),
  };
}

/**
 * Rewrites the recorded performance samples so the GPU reads as busy with the
 * starring model resident. Everything that describes the MACHINE -- adapter
 * name, total VRAM, core count, timestamps -- is left exactly as recorded; only
 * the load on it is synthesized.
 */
function doctorPerf(perf, star) {
  if (!perf) return perf;
  const resident = Math.round((star.estVramGB ?? 12) * 1024);
  const gpu = (perf.gpu_stats ?? []).map((s, i, arr) => {
    // Ramp the first few samples so the chart shows the model coming up rather
    // than a suspiciously flat line.
    const load = Math.min(1, (i + 1) / Math.max(1, Math.min(6, arr.length)));
    // max(), not baseline + resident: the sizer's estimate is a budget for the
    // whole card, system usage included, so adding the idle floor on top
    // over-commits it -- which the dashboard faithfully reported as -0.3G free.
    const used = Math.round(Math.max(s.mem_used_mb ?? 0, resident * load));
    return {
      ...s,
      mem_used_mb: used,
      mem_util_pct: s.mem_total_mb ? (used / s.mem_total_mb) * 100 : s.mem_util_pct,
      gpu_util_pct: Number((72 + ((i * 7) % 23)).toFixed(2)),
    };
  });
  return { ...perf, gpu_stats: gpu };
}

const b64 = (s) => Buffer.from(s, "utf8").toString("base64");

/** A request/response pair for the activity log's inspector. */
function capture(row, star) {
  const req = {
    model: star.id,
    messages: [
      { role: "system", content: "You are a helpful assistant." },
      { role: "user", content: "Summarise the changes in this diff and flag anything risky." },
    ],
    stream: row.resp_content_type === "text/event-stream",
    temperature: 0.7,
  };
  const resp = {
    id: `chatcmpl-${row.id}`,
    object: "chat.completion",
    model: star.id,
    choices: [
      {
        index: 0,
        message: { role: "assistant", content: "The diff moves theme resolution ahead of first paint and adds an opt-out attribute for in-app links. Nothing here changes behaviour outside the shell." },
        finish_reason: "stop",
      },
    ],
    usage: {
      prompt_tokens: row.tokens.input_tokens,
      completion_tokens: row.tokens.output_tokens,
      total_tokens: row.tokens.input_tokens + row.tokens.output_tokens,
    },
  };
  return {
    id: row.id,
    req_path: row.req_path,
    req_headers: { "content-type": "application/json", accept: "application/json" },
    req_body: b64(JSON.stringify(req, null, 2)),
    resp_headers: { "content-type": row.resp_content_type },
    resp_body: b64(JSON.stringify(resp, null, 2)),
  };
}

/** Plausible llama-server chatter, so the upstream log pane is not empty. */
function upstreamLog(star, ctx) {
  return [
    `load_tensors: offloading output layer to GPU`,
    `llama_context: n_ctx = ${ctx}`,
    `llama_context: KV self size = ${((ctx / 1024) * 0.11).toFixed(2)} MiB`,
    `main: server is listening on http://127.0.0.1:9310 - starting the main loop`,
    `srv  update_slots: all slots are idle`,
    `slot launch_slot_: id  0 | task 41 | processing task`,
    `slot update_slots: id  0 | task 41 | prompt done, n_tokens = 8214`,
    `slot      release: id  0 | task 41 | stop processing, truncated = 0`,
  ].join("\n");
}

/**
 * Installs the demo on a Playwright browser context.
 *
 * The EventSource replacement has to be an init script rather than a route: see
 * the note at the top of this file. The two fetch endpoints are routed normally.
 */
export async function installDemo(context, demo) {
  await context.addInitScript(
    ({ envelopes }) => {
      const Real = window.EventSource;
      // Only /api/events is faked; anything else still gets the real thing, so
      // this cannot quietly break a stream someone adds later.
      class DemoEventSource {
        constructor(url, init) {
          if (!String(url).includes("/api/events")) return new Real(url, init);
          this.url = String(url);
          this.readyState = 1; // OPEN, and it stays open: never erroring is the
          // whole point, or the app would paint itself as disconnected.
          this.onopen = null;
          this.onmessage = null;
          this.onerror = null;
          // Deferred, because the caller assigns those handlers on the line
          // AFTER `new EventSource(...)` -- firing in the constructor would
          // deliver every envelope to a null handler.
          setTimeout(() => {
            this.onopen?.(new Event("open"));
            for (const env of envelopes) this.onmessage?.({ data: JSON.stringify(env) });
          }, 0);
        }
        addEventListener() {}
        removeEventListener() {}
        close() {
          this.readyState = 2;
        }
      }
      DemoEventSource.CONNECTING = 0;
      DemoEventSource.OPEN = 1;
      DemoEventSource.CLOSED = 2;
      window.EventSource = DemoEventSource;
    },
    { envelopes: demo.envelopes },
  );

  // `**` after the path, because startPerfPolling's SECOND call onwards is
  // `/api/performance?after=<ts>` -- a pattern without it matches only the first
  // fetch, and every later poll went to the real instance, walking the status
  // rail's VRAM readout back down to the idle floor two seconds into the run.
  // The delta carries no samples: perf.ts ignores an empty `gpu_stats`, so the
  // doctored last sample stays the latest one for as long as we are shooting.
  await context.route("**/api/performance**", (route) => {
    const delta = route.request().url().includes("after=");
    const body = delta ? { ...demo.perf, gpu_stats: [], sys_stats: [] } : demo.perf;
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await context.route("**/api/captures/*", (route) => {
    const id = Number(new URL(route.request().url()).pathname.split("/").pop());
    const cap = demo.captures[id];
    if (!cap) return route.fulfill({ status: 404, body: "not found" });
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(cap) });
  });
}
