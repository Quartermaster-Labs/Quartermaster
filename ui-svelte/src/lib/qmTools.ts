import type { ToolDef } from "./types";

// The "quartermaster MCP": tools that let a playground chat model inspect and
// tune the running quartermaster instance it lives in. Advertised to the model
// like wiki/web-search, but dispatched SERVER-side (internal/server/turns_qm.go)
// against quartermaster's own loopback API — so the model reasons over the real
// running state (installed models, live VRAM, effective config) instead of
// guessing. Read is safe; the configure tool regenerates the config + hot-reloads
// (in place, no eviction). Deliberately NO load/unload: swapping a model would
// evict the very model answering the chat.
export const QM_INSPECT_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "quartermaster_inspect",
    description:
      "Read the live state of THIS running quartermaster. Returns compact formatted text. Pick one slice per call with `target` so you only pull what you need: 'status' (default) = what's loaded + a one-line VRAM/RAM summary; 'models' = installed models with capabilities + context length; 'loaded' = models running now with state + idle-TTL; 'vram' = live GPU/VRAM + system RAM; 'settings' = the global memory knobs; 'logs' = the last lines of quartermaster's own log (model loads, swaps, evictions, spawn/health errors) — use it to diagnose a load failure, crash, or error the user hit; or a model id = that model's effective config (ctx, KV, offload, variants). Use this before answering questions about the user's own setup or before suggesting/making config changes, so your answer matches reality.",
    parameters: {
      type: "object",
      properties: {
        target: {
          type: "string",
          description:
            "What to read: 'status' (or omit) for loaded + VRAM summary, 'models', 'loaded', 'vram', 'settings', 'logs', or a model id for that model's config. One slice per call.",
        },
        tail: {
          type: "integer",
          description:
            "Only for target='logs': how many of the most recent log lines to return (default 50, max 300). Ignored for other targets.",
        },
        source: {
          type: "string",
          enum: ["proxy", "upstream", "all"],
          description:
            "Only for target='logs': which log. 'proxy' (default) = quartermaster's own lifecycle/error log, and does NOT include the currently-answering model's token-by-token decode noise. 'upstream' = the raw backend (llama-server/sd-server) output — noisier, but where a crash reason (CUDA/Vulkan alloc error) shows. 'all' = both combined.",
        },
      },
    },
  },
};

export const QM_CONFIGURE_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "quartermaster_configure",
    description:
      "Change quartermaster's configuration. The user must approve every change: they are shown a before/after diff and this call blocks until they accept or deny it (or it times out) — nothing is applied without an explicit accept, so never assume it succeeded, read the tool result. Global/model changes regenerate the config and hot-reload in place (running models are NOT evicted; a changed model's new launch args apply on its next load); playground changes just save the user's own prefs (apply on their next page reload). ALWAYS quartermaster_inspect first so you know the current values, then send only the fields you want to change. Cannot load/unload models.",
    parameters: {
      type: "object",
      properties: {
        target: {
          type: "string",
          description:
            "What to change: 'settings' = global memory knobs (dashboard); 'playground' = this user's own playground preferences; or a model id = that model's per-model config.",
        },
        changes: {
          type: "object",
          description:
            "The fields to change (only these are touched; everything else is preserved). For target='settings': targetVramGB (number), vramOverheadGB (number), maxRamGB (number), ttlSec (integer idle-unload seconds, 0=never). For target='playground': temperature (0-2), maxTokens (int), reasoningBudget (int, 0=unlimited), reasoning (bool), webSearch (bool), qmTools (bool), searxngUrl (string), searchMaxPerTurn (int), searchThrottleMs (int), searchDedupe (bool). For a model target: ctx (integer), vramTargetGB (number), kvK/kvV (quant strings e.g. 'q8_0','q4_0','f16'), cpuOffload (integer layers pinned to CPU), reasoningBudget (integer), spec (draft/speculative spec string), extraArgs (string), unlisted (bool), skip (bool). Numeric fields must be > 0 where the setting requires it.",
        },
      },
      required: ["target", "changes"],
    },
  },
};
