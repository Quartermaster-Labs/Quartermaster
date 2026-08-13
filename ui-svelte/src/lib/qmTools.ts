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
      "Read the live state of THIS running quartermaster. Returns compact formatted text. Pick one slice per call with `target` so you only pull what you need: 'status' (default) = what's loaded + a one-line VRAM/RAM summary; 'models' = EVERY installed model (not just loaded ones) with capabilities, context length and state, each with its variant count; 'loaded' = models running now with state + idle-TTL; 'vram' = live GPU/VRAM + system RAM; 'settings' = the global memory knobs; 'backends' = the backend registry: which executable (and managed build/version) each class runs, which is the ★ auto-pick, and whether any exe is missing from disk; 'estimate:<model id>' = a what-if load-plan sizing for that model — the chosen context, GPU/CPU layer split, estimated VRAM vs the budget and RAM vs the cap — optionally with `options` to try a tuning BEFORE you propose it with quartermaster_configure; 'fields' = the COMPLETE list of configuration fields quartermaster_configure can change, with types (call this before any change whose field name you are not sure of); 'logs' = the last lines of quartermaster's own log (model loads, swaps, evictions, spawn/health errors) — use it to diagnose a load failure, crash, or error the user hit; or a model id = that model's effective config (every set field, its named variant presets, AND each of its separate variant models — '<id>@ctx32768', '<id>@<backend>' — with the exact launch-flag deviations from the base command). Inspecting a variant id instead diffs it the other way, against its base. Use this before answering questions about the user's own setup or before suggesting/making config changes, so your answer matches reality.",
    parameters: {
      type: "object",
      properties: {
        target: {
          type: "string",
          description:
            "What to read: 'status' (or omit) for loaded + VRAM summary, 'models' (the whole installed catalog), 'loaded' (only what is running now), 'vram', 'settings', 'backends' (registry: exe/version per class), 'estimate:<model id>' (predict a load plan, pair it with `options`), 'fields' (every editable config field + its type), 'logs', or a model id for that model's config + variants (what each variant changes vs the base). One slice per call.",
        },
        options: {
          type: "object",
          description:
            "Only for target='estimate:<model id>': the what-if tuning to size. Anything you omit is re-derived by the sizer, NOT taken from the model's current config — so a bare estimate is the auto plan; pass actual=true for the placement that is really loaded. Keys: ctx (int), kvK/kvV ('q8_0','q4_0','f16'), spec (string), kvInRam (bool), ctxCheckpoints (int), checkpointMinStep (int), vram (number, GB budget to size against), cpuOffload (int layers on CPU), actual (bool). Nothing is changed by this call. Ignored for other targets.",
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
            "What to change: 'settings' = global memory knobs (dashboard); 'playground' = this user's own playground preferences; a model id = that model's per-model config; or '<model id>#<variant name>' = one existing named variant of that model.",
        },
        changes: {
          type: "object",
          description:
            "The fields to change (only these are touched; everything else is preserved). A model or variant target accepts EVERY knob the dashboard's per-model editor has — the full list with types comes from quartermaster_inspect target='fields'; call that whenever you are not certain a field exists, and never smuggle a flag into extraArgs when it has a field of its own. Unknown or wrong-typed fields are rejected with the correct name, not silently ignored. Common model fields: ctx (int), vramTargetGB (number), kvK/kvV ('q8_0','q4_0','f16'), cpuOffload (int layers on CPU), reasoningBudget (int), spec (draft/speculative string), chatTemplateFile (string .jinja path), extraArgs (string, only for flags with no field), unlisted/skip (bool). For target='settings': targetVramGB, vramOverheadGB, maxRamGB (numbers), ttlSec (int idle-unload seconds, 0=never). For target='playground': temperature (0-2), maxTokens (int), reasoningBudget (int, 0=unlimited), reasoning (bool), webSearch (bool), qmTools (bool), searxngUrl (string), searchMaxPerTurn (int), searchThrottleMs (int), searchDedupe (bool).",
        },
      },
      required: ["target", "changes"],
    },
  },
};
