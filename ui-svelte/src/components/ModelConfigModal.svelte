<script lang="ts">
  import {
    getModelConfig,
    putModelOverride,
    resetModelOverride,
    estimatePlan,
    type ModelConfig,
    type ModelOverride,
    type ModelVariant,
    type PlanEstimate,
  } from "../stores/api";
  import VramGauge from "./VramGauge.svelte";

  interface Props {
    modelId: string | null;
    open: boolean;
    onclose: () => void;
  }

  let { modelId, open, onclose }: Props = $props();

  let dialogEl: HTMLDialogElement | undefined = $state();

  let loading = $state(false);
  let saving = $state(false);
  let error = $state<string | null>(null);
  let config = $state<ModelConfig | null>(null);

  // Editable form state (mirrors ModelOverride; "" / 0 means inherit default).
  let ctx = $state<number>(8192);
  let ctxAuto = $state(true);
  let autoCtx = $state(0); // effective ctx the autogen sizer picked (parsed from -c in the launch cmd)
  let kvK = $state("");
  let kvV = $state("");
  let kvInRam = $state(false);
  let spec = $state("");
  let reasoningFmt = $state("");
  let aliasesText = $state("");
  let unlisted = $state(false);
  let skip = $state(false);
  let variants = $state<ModelVariant[]>([]);

  // Live load-plan estimate for the current (unsaved) curated fields.
  let estimate = $state<PlanEstimate | null>(null);
  let estimateError = $state<string | null>(null);
  let estTimer: ReturnType<typeof setTimeout> | undefined;

  // New-variant draft.
  let vName = $state("");
  let vCtx = $state<number | "">("");
  let vVram = $state<number | "">("");
  let vKvK = $state("");
  let vKvV = $state("");
  let vSpec = $state("");
  let vReasoning = $state("");
  let vUnlisted = $state(false);

  const KV_OPTS = ["", "q8_0", "q4_0", "q5_1", "f16", "bf16"];
  const REASON_OPTS = [
    { value: "", label: "default (none)" },
    { value: "auto", label: "auto" },
  ];

  // Slider ceiling = trained context length (fallback 32k). Floor 4k.
  const CTX_MIN = 4096;
  const maxCtx = $derived(config?.maxCtx && config.maxCtx > CTX_MIN ? config.maxCtx : 32768);

  // Speculative options: draft-mtp only offered when the model has MTP layers.
  // MTP models auto-default to draft-mtp (matches generator); others default to ngram-mod.
  const specOpts = $derived([
    { value: "", label: config?.isMTP ? "default (draft-mtp)" : "default (ngram-mod)" },
    { value: "none", label: "none (disable)" },
    ...(config?.isMTP ? [{ value: "draft-mtp", label: "draft-mtp" }] : []),
    ...(config?.isMTP ? [{ value: "ngram-mod", label: "ngram-mod" }] : []),
  ]);

  function fmtCtx(n: number): string {
    return n % 1024 === 0 ? `${n / 1024}k` : `${n}`;
  }

  // Effective context the autogen sizer baked into the launch command (-c N).
  function parseCtx(cmd: string): number {
    const m = cmd.match(/(?:^|\s)-c\s+(\d+)/);
    return m ? Number(m[1]) : 0;
  }

  $effect(() => {
    if (open && dialogEl) {
      dialogEl.showModal();
      void load();
    } else if (!open && dialogEl) {
      dialogEl.close();
    }
  });

  async function load() {
    if (!modelId) return;
    loading = true;
    error = null;
    try {
      const cfg = await getModelConfig(modelId);
      config = cfg;
      const o = cfg.override;
      autoCtx = parseCtx(cfg.cmd);
      ctxAuto = !o?.ctx;
      // Slider seeds from the override, else the sizer's effective ctx, else 8k.
      ctx = o?.ctx || autoCtx || Math.min(8192, cfg.maxCtx || 8192);
      kvK = o?.kvK ?? "";
      kvV = o?.kvV ?? "";
      kvInRam = o?.kvInRam ?? false;
      spec = o?.spec ?? "";
      reasoningFmt = o?.reasoningFmt ?? "";
      aliasesText = (o?.aliases ?? []).join(", ");
      unlisted = o?.unlisted ?? false;
      skip = o?.skip ?? false;
      variants = (o?.variants ?? []).map((v) => ({ ...v }));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  // Re-estimate (debounced) whenever a memory-affecting field changes while open.
  // Reads each dep synchronously so Svelte tracks them.
  $effect(() => {
    const deps = [open, config, ctx, ctxAuto, kvK, kvV, kvInRam, spec];
    void deps;
    if (!open || !config || !modelId) return;
    clearTimeout(estTimer);
    estTimer = setTimeout(runEstimate, 100);
  });

  async function runEstimate() {
    if (!modelId || !config) return;
    estimateError = null;
    try {
      estimate = await estimatePlan(modelId, {
        ctx: ctxAuto ? undefined : Number(ctx),
        kvK: kvK || undefined,
        kvV: kvV || undefined,
        kvInRam,
        spec: spec || undefined,
      });
    } catch (e) {
      estimateError = e instanceof Error ? e.message : String(e);
      estimate = null;
    }
  }

  function parseAliases(s: string): string[] {
    return s
      .split(",")
      .map((a) => a.trim())
      .filter(Boolean);
  }

  function buildOverride(): ModelOverride {
    return {
      ctx: ctxAuto ? 0 : Number(ctx),
      kvK,
      kvV,
      kvInRam,
      spec,
      reasoningFmt,
      aliases: parseAliases(aliasesText),
      unlisted,
      skip,
      variants,
    };
  }

  function addVariant() {
    const name = vName.trim();
    if (!name) return;
    const v: ModelVariant = {
      name,
      ctx: vCtx === "" ? 0 : Number(vCtx),
      vramTargetGB: vVram === "" ? 0 : Number(vVram),
      kvK: vKvK,
      kvV: vKvV,
      spec: vSpec,
      reasoningFmt: vReasoning,
      unlisted: vUnlisted,
    };
    const idx = variants.findIndex((x) => x.name.toLowerCase() === name.toLowerCase());
    if (idx >= 0) variants[idx] = v;
    else variants = [...variants, v];
    // reset draft
    vName = "";
    vCtx = "";
    vVram = "";
    vKvK = "";
    vKvV = "";
    vSpec = "";
    vReasoning = "";
    vUnlisted = false;
  }

  function removeVariant(name: string) {
    variants = variants.filter((v) => v.name !== name);
  }

  async function save() {
    if (!modelId) return;
    saving = true;
    error = null;
    try {
      await putModelOverride(modelId, buildOverride());
      onclose();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  async function reset() {
    if (!modelId) return;
    if (!confirm("Reset this model to autogen defaults? Removes all custom params and variants.")) return;
    saving = true;
    error = null;
    try {
      await resetModelOverride(modelId);
      onclose();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }
</script>

<dialog
  bind:this={dialogEl}
  onclose={onclose}
  class="bg-surface text-txtmain rounded-lg shadow-xl max-w-[640px] w-full max-h-[90vh] p-0 backdrop:bg-black/50 m-auto"
>
  <div class="flex flex-col max-h-[90vh]">
    <div class="flex justify-between items-center p-4 border-b border-card-border">
      <h2 class="text-xl font-bold pb-0">
        Model parameters
        {#if config}<span class="text-base font-mono font-normal text-txtsecondary">{config.id}</span>{/if}
      </h2>
      <button onclick={() => dialogEl?.close()} class="text-txtsecondary hover:text-txtmain text-2xl leading-none">&times;</button>
    </div>

    <div class="overflow-y-auto flex-1 p-4 space-y-4">
      {#if loading}
        <p class="text-txtsecondary">Loading…</p>
      {:else if error}
        <p class="text-red-500 text-sm font-mono whitespace-pre-wrap">{error}</p>
      {/if}

      {#snippet hint(text: string)}
        <span class="hint" title={text} aria-label={text}>?</span>
      {/snippet}

      {#if config}
        <!-- Curated override fields -->
        <div class="grid grid-cols-2 gap-3">
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Context window
              {@render hint("Tokens the model can attend to. Auto = the size the autogen sizer picked to fit free VRAM (shown). Slider ranges 4k to the model's trained max.")}
              <span class="ml-auto font-mono text-txtmain">
                {ctxAuto ? (autoCtx ? `auto · ${fmtCtx(autoCtx)}` : "auto") : fmtCtx(ctx)}
              </span>
            </span>
            <div class="flex items-center gap-3">
              <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                <input type="checkbox" bind:checked={ctxAuto} /> Auto
              </label>
              <input
                type="range"
                min={CTX_MIN}
                max={maxCtx}
                step="1024"
                bind:value={ctx}
                disabled={ctxAuto}
                class="flex-1 disabled:opacity-40"
              />
              <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {fmtCtx(maxCtx)}</span>
            </div>
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              KV cache K
              {@render hint("Quantization of the attention key cache. Lower bits = less VRAM, slightly less accuracy. q8_0 is the default.")}
            </span>
            <select bind:value={kvK} class="cfg-input">
              {#each KV_OPTS as o}<option value={o}>{o === "" ? "default (q8_0)" : o}</option>{/each}
            </select>
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              KV cache V
              {@render hint("Quantization of the attention value cache. Must match K for flash-attention. q8_0 is the default.")}
            </span>
            <select bind:value={kvV} class="cfg-input">
              {#each KV_OPTS as o}<option value={o}>{o === "" ? "default (q8_0)" : o}</option>{/each}
            </select>
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Speculative
              {@render hint("Speculative decoding to speed up generation. ngram-mod is the default; draft-mtp needs a model with MTP layers.")}
            </span>
            <select bind:value={spec} class="cfg-input">
              {#each specOpts as o}<option value={o.value}>{o.label}</option>{/each}
            </select>
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Reasoning format
              {@render hint("How the model's chain-of-thought is parsed/exposed. 'auto' lets llama.cpp detect it; default emits none.")}
            </span>
            <select bind:value={reasoningFmt} class="cfg-input">
              {#each REASON_OPTS as o}<option value={o.value}>{o.label}</option>{/each}
            </select>
          </label>
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" bind:checked={kvInRam} />
            <span class="text-txtsecondary flex items-center gap-1">
              KV in RAM
              {@render hint("Keep the KV cache in system RAM instead of VRAM (--no-kv-offload). Frees VRAM at the cost of speed.")}
            </span>
          </label>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Aliases (comma-separated)
              {@render hint("Extra names this model answers to in the /v1/models API (e.g. map gpt-4 to this model).")}
            </span>
            <input type="text" bind:value={aliasesText} class="cfg-input" placeholder="e.g. gpt-4, default" />
          </label>
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" bind:checked={unlisted} />
            <span class="text-txtsecondary flex items-center gap-1">
              Unlisted
              {@render hint("Hide from /v1/models listings, but still loadable by exact id.")}
            </span>
          </label>
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" bind:checked={skip} />
            <span class="text-txtsecondary flex items-center gap-1">
              Skip (don't emit)
              {@render hint("Exclude this model from the generated config entirely.")}
            </span>
          </label>
        </div>

        <!-- Live memory estimate for the unsaved tuning above -->
        <div class="rounded border border-card-border bg-background p-3">
          <div class="flex items-center justify-between mb-2">
            <span class="font-mono text-xs uppercase tracking-wider text-txtsecondary">Estimated load</span>
          </div>
          {#if estimateError}
            <p class="font-mono text-xs text-error">{estimateError}</p>
          {:else if estimate}
            <VramGauge usedMb={estimate.estVramGB * 1024} totalMb={estimate.targetVramGB * 1024} height="0.6rem" />
            <div class="mt-3 grid grid-cols-4 gap-2 font-mono text-xs">
              <div>
                <div class="text-txtsecondary uppercase tracking-wide">Ctx</div>
                <div class="text-txtmain tabular-nums">{fmtCtx(estimate.ctx)}</div>
              </div>
              <div>
                <div class="text-txtsecondary uppercase tracking-wide">RAM</div>
                <div class="text-txtmain tabular-nums {estimate.ramExceeded ? 'text-error' : ''}">
                  {estimate.estRamGB.toFixed(1)}{estimate.maxRamGB ? `/${estimate.maxRamGB.toFixed(0)}` : ""}G
                </div>
              </div>
              <div>
                <div class="text-txtsecondary uppercase tracking-wide">-ngl</div>
                <div class="text-txtmain tabular-nums">{estimate.ngl}</div>
              </div>
              <div>
                <div class="text-txtsecondary uppercase tracking-wide">cpu-moe</div>
                <div class="text-txtmain tabular-nums">{estimate.nCpuMoe}</div>
              </div>
            </div>
            {#if estimate.ramExceeded}
              <p class="mt-2 font-mono text-xs text-error">⚠ Estimated RAM exceeds the configured ceiling.</p>
            {/if}
          {:else}
            <p class="font-mono text-xs text-txtsecondary">—</p>
          {/if}
        </div>

        <!-- Variants -->
        <details class="group">
          <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
            Custom variants ({variants.length})
          </summary>
          <div class="mt-2 space-y-1">
            {#each variants as v (v.name)}
              <div class="flex items-center justify-between bg-background rounded border border-card-border px-3 py-1.5 text-sm">
                <span class="font-mono">{config.id}-{v.name}
                  <span class="text-txtsecondary">· ctx {v.ctx || "auto"}{v.vramTargetGB ? ` · ${v.vramTargetGB}GB` : ""}{v.unlisted ? " · unlisted" : ""}</span>
                </span>
                <button class="text-txtsecondary hover:text-red-500" onclick={() => removeVariant(v.name)}>Remove</button>
              </div>
            {/each}
            {#if variants.length === 0}
              <p class="text-xs text-txtsecondary">No custom variants. Add one below.</p>
            {/if}
          </div>

          <div class="mt-3 bg-background rounded border border-card-border p-3 grid grid-cols-2 gap-2">
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">Name (suffix)</span>
              <input type="text" bind:value={vName} class="cfg-input" placeholder="e.g. game, long" />
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">Context (0 = auto)</span>
              <input type="number" min="0" step="1024" bind:value={vCtx} class="cfg-input" placeholder="auto" />
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">VRAM target GB (0 = default)</span>
              <input type="number" min="0" step="0.5" bind:value={vVram} class="cfg-input" placeholder="default" />
            </label>
            <label class="flex items-center gap-2 text-sm mt-5">
              <input type="checkbox" bind:checked={vUnlisted} /> <span class="text-txtsecondary">Unlisted</span>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">KV K</span>
              <select bind:value={vKvK} class="cfg-input">{#each KV_OPTS as o}<option value={o}>{o === "" ? "inherit" : o}</option>{/each}</select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">KV V</span>
              <select bind:value={vKvV} class="cfg-input">{#each KV_OPTS as o}<option value={o}>{o === "" ? "inherit" : o}</option>{/each}</select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">Speculative</span>
              <select bind:value={vSpec} class="cfg-input">{#each specOpts as o}<option value={o.value}>{o.label}</option>{/each}</select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">Reasoning</span>
              <select bind:value={vReasoning} class="cfg-input">{#each REASON_OPTS as o}<option value={o.value}>{o.label}</option>{/each}</select>
            </label>
            <div class="col-span-2 flex justify-end">
              <button class="btn btn--sm" onclick={addVariant} disabled={!vName.trim()}>Add / update variant</button>
            </div>
          </div>
        </details>

        <!-- Launch command (read-only) — collapsed at bottom -->
        <details class="group">
          <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
            Launch parameters {config.hasOverride ? "(custom)" : "(autogen default)"}
          </summary>
          <div class="mt-2 bg-background rounded border border-card-border overflow-auto max-h-48">
            <pre class="p-3 text-xs font-mono whitespace-pre-wrap break-all">{config.cmd}</pre>
          </div>
          <p class="text-xs text-txtsecondary mt-1 font-mono break-all">{config.gguf}</p>
        </details>
      {/if}
    </div>

    <div class="p-4 border-t border-card-border flex justify-between items-center">
      <button onclick={reset} class="btn btn--sm" disabled={saving || !config?.hasOverride}>Reset to default</button>
      <div class="flex gap-2">
        <button onclick={() => dialogEl?.close()} class="btn btn--sm">Cancel</button>
        <button onclick={save} class="btn btn--sm btn--primary" disabled={saving || loading}>
          {saving ? "Saving…" : "Save & reload"}
        </button>
      </div>
    </div>
  </div>
</dialog>

<style>
  .cfg-input {
    padding: 4px 8px;
    border-radius: 4px;
    background: var(--color-background);
    border: 1px solid var(--color-card-border);
    color: var(--color-txtmain);
    font-size: 0.85rem;
  }
  .hint {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 14px;
    height: 14px;
    border-radius: 9999px;
    border: 1px solid var(--color-card-border);
    color: var(--color-txtsecondary);
    font-size: 0.65rem;
    line-height: 1;
    cursor: help;
    user-select: none;
  }
  .hint:hover {
    color: var(--color-txtmain);
    border-color: var(--color-txtmain);
  }
</style>
