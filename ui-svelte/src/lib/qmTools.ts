import type { ToolDef } from "./types";

// The "quartermaster MCP": tools that let a playground chat model inspect and
// tune the running quartermaster instance it lives in. Advertised to the model
// like wiki/web-search, but dispatched SERVER-side (internal/server/turns_qm.go)
// against quartermaster's own loopback API — so the model reasons over the real
// running state (installed models, live VRAM, effective config) instead of
// guessing. Read is safe; the configure tool regenerates the config + hot-reloads
// (in place, no eviction). Deliberately NO load/unload: swapping a model would
// evict the very model answering the chat.
// Descriptions are kept tight on purpose: both tools are on by default, so every
// word here rides in the KV-stable prefix of every chat. Detail the model only
// needs sometimes (the field catalog) is fetched on demand via target='fields'.
export const QM_INSPECT_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "quartermaster_inspect",
    description:
      "Read the live state of this running quartermaster as short text, one slice per call. Call it before answering about the user's own setup or proposing a config change.",
    parameters: {
      type: "object",
      properties: {
        target: {
          type: "string",
          description:
            "'status' (default: loaded models + VRAM/RAM summary), 'models' (every installed model: capabilities, ctx, state, variant count), 'loaded' (running models + idle TTL), 'vram' (GPU memory + system RAM), 'settings' (global memory knobs), 'backends' (exe/version per backend class, the auto-pick, missing exes), 'fields' (every field quartermaster_configure accepts, with types), 'logs' (recent quartermaster log, for load failures and crashes), 'estimate:<model id>' (what-if load plan: ctx, GPU/CPU split, VRAM vs budget), or a model id (its effective config and variants as launch-flag deviations from the base; a variant id diffs against its base).",
        },
        options: {
          type: "object",
          description:
            "Only for 'estimate:<model id>': the tuning to size. Omitted keys are re-derived, not read from the current config; actual=true sizes the placement really loaded. Keys: ctx, kvK/kvV ('q8_0','q4_0','f16'), spec, kvInRam, ctxCheckpoints, checkpointMinStep, vram (GB), cpuOffload (layers), actual. Changes nothing.",
        },
        tail: {
          type: "integer",
          description: "Only for 'logs': how many recent lines (default 50, max 300).",
        },
        source: {
          type: "string",
          enum: ["proxy", "upstream", "all"],
          description:
            "Only for 'logs': 'proxy' (default, quartermaster's lifecycle/errors), 'upstream' (raw backend output, where crash reasons like alloc errors show), or 'all'.",
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
      "Change quartermaster's configuration. The user is shown a before/after diff and must accept it; this call blocks until they decide, so read the result rather than assuming it applied. Model and global changes hot-reload without evicting running models (a model's new args apply on its next load); playground prefs apply on the user's next page reload. Cannot load or unload models.",
    parameters: {
      type: "object",
      properties: {
        target: {
          type: "string",
          description:
            "'settings' (global memory knobs), 'playground' (this user's playground prefs), a model id, or '<model id>#<variant name>' for one existing named variant.",
        },
        changes: {
          type: "object",
          description:
            "Only the fields to change; everything else is kept. Field names and types come from quartermaster_inspect target='fields' - check there when unsure instead of guessing or putting a flag in extraArgs. Unknown fields are rejected with the right name. 'playground' fields (not in that list): temperature (0-2), maxTokens (int), reasoningBudget (int, 0=unlimited), reasoning, webSearch, qmTools, searchDedupe (bool), searxngUrl (string), searchMaxPerTurn, searchThrottleMs (int).",
        },
      },
      required: ["target", "changes"],
    },
  },
};
