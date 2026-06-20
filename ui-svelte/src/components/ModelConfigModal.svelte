<script lang="ts">
  import {
    getModelConfig,
    putModelOverride,
    resetModelOverride,
    estimatePlan,
    previewCmd,
    getSettings,
    type ModelConfig,
    type ModelOverride,
    type ModelVariant,
    type PlanEstimate,
  } from "../stores/api";
  import VramGauge from "./VramGauge.svelte";
  import { estimateSegments } from "../stores/vram";

  interface Props {
    modelId: string | null;
    open: boolean;
    onclose: () => void;
    /** Full model id of the row whose gear was clicked. Matched against the
     * resolved variant list to land on the right variant; the family base id
     * (no variant suffix) => the Default entry. */
    openForId?: string;
  }

  let { modelId, open, onclose, openForId = "" }: Props = $props();

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
  // Target VRAM + offload are sliders with an "auto" toggle (auto => inherit the
  // global target / let the sizer pick the offload). vramTarget/cpuOffload hold
  // the slider position; the *Auto flags decide whether it's sent.
  let vramTarget = $state<number>(0);
  let vramAuto = $state(true);
  let cpuOffload = $state<number>(0);
  let cpuAuto = $state(true);
  let globalTargetGB = $state(0); // global VRAM budget; slider ceiling for vramTarget
  let spec = $state("");
  // Boolean toggles. Stored as strings on the override ("" = default-on, "off" =
  // forced off); surfaced here as plain on/off checkboxes (auto state dropped).
  let reasoningOn = $state(true); // false => reasoningFmt "off"
  let flashOn = $state(true); // false => flashAttn "off"
  let mmapOn = $state(true); // false => mmap "off" (--no-mmap)
  let mlock = $state(false);
  let threads = $state<number | "">(""); // "" = global default
  let parallel = $state<number | "">(""); // "" = 1
  let ub = $state<number | "">(""); // "" = auto physical batch
  let aliasesText = $state("");
  let unlisted = $state(false);
  let skip = $state(false);
  let variants = $state<ModelVariant[]>([]);

  // Two-way launch-parameters box. cmdDraft is the editable command text. Form
  // edits re-render it from the backend (renderCmd); editing the box parses known
  // flags back into the form (parseCmd, on blur) and stashes anything autogen
  // doesn't model into extraArgs (passthrough, appended to the emitted command).
  let cmdDraft = $state("");
  let extraArgs = $state("");

  // Flags autogen always emits and OWNS (computed or fixed): ignored when parsing
  // the box so editing them never flips a form "auto" toggle or pins a value.
  const IGNORE_VALUE = new Set(["-m", "--port", "--host", "--spec-draft-n-max", "--dry-multiplier", "--dry-base", "--dry-allowed-length", "-c", "-ngl", "--n-cpu-moe", "-b"]);
  const IGNORE_BOOL = new Set(["--kv-unified", "--no-warmup", "--no-webui", "--jinja"]);

  // Parsed launch-flag bundle shared by the Default form and a variant. Booleans
  // are normalized to the form's on/off sense; computed flags are dropped.
  interface ParsedCmd {
    flashOn: boolean;
    mmapOn: boolean;
    mlock: boolean;
    kvInRam: boolean;
    reasoningOn: boolean;
    kvK: string;
    kvV: string;
    spec: string;
    threads: number | "";
    parallel: number | "";
    ub: number | "";
    extraArgs: string;
  }

  // Parse a launch command into form fields + extraArgs passthrough. Computed
  // flags (-c/-ngl/--n-cpu-moe) are owned by the sliders, so they're ignored here.
  function parseCmdFields(cmd: string): ParsedCmd {
    const toks = cmd.trim().split(/\s+/);
    let i = 0;
    while (i < toks.length && !toks[i].startsWith("-")) i++; // skip the exe
    const val = (): string => (i + 1 < toks.length && !toks[i + 1].startsWith("-") ? toks[++i] : "");
    let fa: string | null = null,
      ctk: string | null = null,
      ctv: string | null = null,
      t: string | null = null,
      par: string | null = null,
      u: string | null = null,
      sp: string | null = null,
      reason: string | null = null;
    let noMmap = false,
      mlockF = false,
      noKv = false;
    const extras: string[] = [];
    for (; i < toks.length; i++) {
      const tk = toks[i];
      switch (tk) {
        case "-fa": fa = val(); break;
        case "-ctk": ctk = val(); break;
        case "-ctv": ctv = val(); break;
        case "-t": t = val(); break;
        case "--parallel": par = val(); break;
        case "-ub": u = val(); break;
        case "--spec-type": sp = val(); break;
        case "--reasoning-format": reason = val(); break;
        case "--reasoning": if (val() === "off") reason = "off"; break;
        case "--no-mmap": noMmap = true; break;
        case "--mlock": mlockF = true; break;
        case "--no-kv-offload": noKv = true; break;
        default:
          if (IGNORE_VALUE.has(tk)) val();
          else if (IGNORE_BOOL.has(tk)) break;
          else {
            extras.push(tk);
            const v = i + 1 < toks.length && !toks[i + 1].startsWith("-") ? toks[++i] : "";
            if (v) extras.push(v);
          }
      }
    }
    return {
      flashOn: fa !== null ? fa !== "off" : false,
      mmapOn: !noMmap,
      mlock: mlockF,
      kvInRam: noKv,
      reasoningOn: reason !== "none" && reason !== "off",
      kvK: ctk ?? "",
      kvV: ctv ?? "",
      spec: sp ?? "",
      threads: t !== null ? Number(t) : "",
      parallel: par !== null ? Number(par) : "",
      ub: u !== null ? Number(u) : "",
      extraArgs: extras.join(" "),
    };
  }

  // Apply parsed flags to the Default form fields.
  function applyParsedToDefault(p: ParsedCmd) {
    flashOn = p.flashOn;
    mmapOn = p.mmapOn;
    mlock = p.mlock;
    kvInRam = p.kvInRam;
    reasoningOn = p.reasoningOn;
    kvK = p.kvK;
    kvV = p.kvV;
    spec = p.spec;
    threads = p.threads;
    parallel = p.parallel;
    ub = p.ub;
    extraArgs = p.extraArgs;
  }

  // Apply parsed flags to the selected variant (string on/off knobs mirror the
  // override encoding: "" = inherit/on, "off" = forced off).
  function applyParsedToVariant(v: ModelVariant, p: ParsedCmd) {
    v.flashAttn = p.flashOn ? "" : "off";
    v.mmap = p.mmapOn ? "" : "off";
    v.mlock = p.mlock;
    v.kvInRam = p.kvInRam;
    v.reasoningFmt = p.reasoningOn ? "" : "off";
    v.kvK = p.kvK;
    v.kvV = p.kvV;
    v.spec = p.spec;
    v.threads = p.threads === "" ? 0 : Number(p.threads);
    v.parallel = p.parallel === "" ? 0 : Number(p.parallel);
    v.ub = p.ub === "" ? 0 : Number(p.ub);
    v.extraArgs = p.extraArgs;
  }

  function onCmdInput(e: Event) {
    // Local-only while typing; the form fields (and thus the render effect) are
    // untouched, so the box isn't overwritten mid-keystroke.
    cmdDraft = (e.currentTarget as HTMLTextAreaElement).value;
  }
  function onCmdBlur() {
    // On blur, fold the edited command back into the active entry. Field changes
    // trigger the render effect, which re-renders the canonical command.
    const p = parseCmdFields(cmdDraft);
    if (selectedV) applyParsedToVariant(selectedV, p);
    else applyParsedToDefault(p);
  }

  // Re-render the launch command from the active entry (Default or the selected
  // variant), debounced. Skipped until the model config has loaded.
  let cmdTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const deps = [
      open, config, selectedVariant,
      ctx, ctxAuto, kvK, kvV, kvInRam, spec, reasoningOn, flashOn, mmapOn, mlock, threads, parallel, ub, vramTarget, vramAuto, cpuOffload, cpuAuto, extraArgs,
      selectedV?.ctx, selectedV?.kvK, selectedV?.kvV, selectedV?.kvInRam, selectedV?.spec,
      selectedV?.reasoningFmt, selectedV?.flashAttn, selectedV?.mmap, selectedV?.mlock,
      selectedV?.threads, selectedV?.parallel, selectedV?.ub, selectedV?.vramTargetGB,
      selectedV?.cpuOffload, selectedV?.ctxCheckpoints, selectedV?.dry, selectedV?.extraArgs,
    ];
    void deps;
    if (!open || !config || !modelId) return;
    const ov = selectedV ? variantToOverride(selectedV) : buildOverride();
    clearTimeout(cmdTimer);
    cmdTimer = setTimeout(async () => {
      try {
        cmdDraft = await previewCmd(modelId!, ov);
      } catch {
        /* leave the last good command in the box */
      }
    }, 150);
  });

  // Build a full ModelOverride from a variant so the launch-command preview and
  // estimate treat it as a standalone model (zero/empty fields still inherit at
  // the backend, but here they render as the generator defaults).
  function variantToOverride(v: ModelVariant): ModelOverride {
    return {
      ctx: v.ctx || 0,
      kvK: v.kvK ?? "",
      kvV: v.kvV ?? "",
      kvInRam: v.kvInRam ?? false,
      vramTargetGB: v.vramTargetGB || 0,
      cpuOffload: v.cpuOffload || 0,
      spec: v.spec ?? "",
      reasoningFmt: v.reasoningFmt ?? "",
      flashAttn: v.flashAttn ?? "",
      mmap: v.mmap ?? "",
      mlock: v.mlock ?? false,
      threads: v.threads || 0,
      parallel: v.parallel || 0,
      ub: v.ub || 0,
      extraArgs: v.extraArgs ?? "",
      aliases: v.aliases ?? [],
      unlisted: v.unlisted ?? false,
    };
  }

  // Live load-plan estimate for the current (unsaved) curated fields.
  let estimate = $state<PlanEstimate | null>(null);
  let estimateError = $state<string | null>(null);
  let estTimer: ReturnType<typeof setTimeout> | undefined;

  // Which entry the form edits: "" = the Default (base override, full field set);
  // a variant name = that variant (a subset of fields; the rest inherit Default).
  // Default is a pinned, non-deletable entry — opening any variant's gear lands
  // here with that variant selected.
  let selectedVariant = $state("");
  const selectedV = $derived(
    selectedVariant ? (variants.find((v) => v.name === selectedVariant) ?? null) : null,
  );

  const KV_OPTS = ["", "q8_0", "q4_0", "q5_1", "f16", "bf16"];

  // Slider ceiling = trained context length (fallback 32k). Floor 4k.
  const CTX_MIN = 4096;
  const maxCtx = $derived(config?.maxCtx && config.maxCtx > CTX_MIN ? config.maxCtx : 32768);
  // Offload slider ceiling = transformer block count (fallback 64).
  const maxOffload = $derived(config?.blockCount && config.blockCount > 0 ? config.blockCount : 64);
  // VRAM slider ceiling = the global budget (fallback 24 GB until settings load).
  const maxVram = $derived(globalTargetGB > 0 ? globalTargetGB : 24);

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

  // GPU layers as value/max (max = transformer blocks). -ngl 99 is the "all
  // layers" sentinel, so clamp to the block count; fall back to the raw value
  // when the block count is unknown.
  function nglDisplay(ngl: number, blocks: number): string {
    return blocks > 0 ? `${Math.min(ngl, blocks)}/${blocks}` : String(ngl);
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

  // Seed the structured form fields from a stored override (or autogen defaults
  // when null). Split out so "revert to auto" can re-seed without a refetch.
  function seedFromOverride(o: ModelOverride | null) {
    ctxAuto = !o?.ctx;
    // Slider seeds from the override, else the sizer's effective ctx, else 8k.
    ctx = o?.ctx || autoCtx || Math.min(8192, config?.maxCtx || 8192);
    kvK = o?.kvK ?? "";
    kvV = o?.kvV ?? "";
    kvInRam = o?.kvInRam ?? false;
    vramAuto = !o?.vramTargetGB;
    vramTarget = o?.vramTargetGB || globalTargetGB || 0;
    cpuAuto = !o?.cpuOffload;
    cpuOffload = o?.cpuOffload || 0;
    spec = o?.spec ?? "";
    reasoningOn = (o?.reasoningFmt ?? "") !== "off";
    flashOn = (o?.flashAttn ?? "") !== "off";
    mmapOn = (o?.mmap ?? "") !== "off";
    mlock = o?.mlock ?? false;
    threads = o?.threads ? o.threads : "";
    parallel = o?.parallel ? o.parallel : "";
    ub = o?.ub ? o.ub : "";
    extraArgs = o?.extraArgs ?? "";
    aliasesText = (o?.aliases ?? []).join(", ");
    unlisted = o?.unlisted ?? false;
    skip = o?.skip ?? false;
    variants = (o?.variants ?? []).map((v) => ({ ...v }));
  }

  async function load() {
    if (!modelId) return;
    loading = true;
    error = null;
    try {
      const cfg = await getModelConfig(modelId);
      config = cfg;
      // Global VRAM budget powers the target-VRAM slider ceiling + auto display.
      try {
        globalTargetGB = (await getSettings()).targetVramGB || 0;
      } catch {
        globalTargetGB = 0;
      }
      const o = cfg.override;
      autoCtx = parseCtx(cfg.cmd);
      cmdDraft = cfg.cmd; // render effect refreshes this to the canonical (${PORT}) form
      seedFromOverride(o);
      // Land on the clicked row's variant: the model id ends with "-<name>" for
      // a variant, or is the bare base for Default. Match the longest name so a
      // name that's a suffix of another doesn't win.
      let chosen = "";
      if (openForId) {
        for (const v of variants) {
          if (openForId.endsWith("-" + v.name) && v.name.length > chosen.length) chosen = v.name;
        }
      }
      selectedVariant = chosen;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  // Re-estimate (debounced) whenever a memory-affecting field changes while open.
  // Reads each dep synchronously so Svelte tracks them.
  $effect(() => {
    const deps = [
      open, config, selectedVariant,
      ctx, ctxAuto, kvK, kvV, kvInRam, spec, vramTarget, vramAuto, cpuOffload, cpuAuto,
      selectedV?.ctx, selectedV?.kvK, selectedV?.kvV, selectedV?.spec,
      selectedV?.vramTargetGB, selectedV?.ub, selectedV?.ctxCheckpoints,
      selectedV?.kvInRam, selectedV?.cpuOffload,
    ];
    void deps;
    if (!open || !config || !modelId) return;
    clearTimeout(estTimer);
    estTimer = setTimeout(runEstimate, 100);
  });

  async function runEstimate() {
    if (!modelId || !config) return;
    estimateError = null;
    try {
      // A variant carries its own full launch shape (ctx / kv / spec / vram /
      // offload / kv-in-ram / checkpoints). The Default entry uses the top-level
      // form fields. Zero/empty still inherits at the backend on save.
      const params = selectedV
        ? {
            ctx: selectedV.ctx ? Number(selectedV.ctx) : undefined,
            kvK: selectedV.kvK || undefined,
            kvV: selectedV.kvV || undefined,
            kvInRam: selectedV.kvInRam ?? false,
            spec: selectedV.spec || undefined,
            vram: selectedV.vramTargetGB ? Number(selectedV.vramTargetGB) : undefined,
            cpuOffload: selectedV.cpuOffload ? Number(selectedV.cpuOffload) : undefined,
            ctxCheckpoints: selectedV.ctxCheckpoints ?? undefined,
          }
        : {
            ctx: ctxAuto ? undefined : Number(ctx),
            kvK: kvK || undefined,
            kvV: kvV || undefined,
            kvInRam,
            spec: spec || undefined,
            vram: vramAuto ? undefined : Number(vramTarget),
            cpuOffload: cpuAuto ? undefined : Number(cpuOffload),
          };
      estimate = await estimatePlan(modelId, params);
    } catch (e) {
      estimateError = e instanceof Error ? e.message : String(e);
      estimate = null;
    }
  }

  // Wheel-to-adjust for numeric/range inputs: scrolling over the field nudges
  // its value by `step` and swallows the event so the modal body doesn't scroll
  // at the same time. Dispatches `input` so Svelte's bind:value picks it up.
  function wheelAdjust(node: HTMLInputElement) {
    function onwheel(e: WheelEvent) {
      if (node.disabled) return;
      e.preventDefault();
      const step = Number(node.step) || 1;
      const dir = e.deltaY < 0 ? 1 : -1;
      const cur = node.value === "" ? 0 : Number(node.value);
      const min = node.min !== "" ? Number(node.min) : -Infinity;
      const max = node.max !== "" ? Number(node.max) : Infinity;
      const next = Math.min(max, Math.max(min, cur + dir * step));
      node.value = String(next);
      node.dispatchEvent(new Event("input", { bubbles: true }));
    }
    node.addEventListener("wheel", onwheel, { passive: false });
    return { destroy: () => node.removeEventListener("wheel", onwheel) };
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
      vramTargetGB: vramAuto ? 0 : Number(vramTarget),
      cpuOffload: cpuAuto ? 0 : Number(cpuOffload),
      spec,
      reasoningFmt: reasoningOn ? "" : "off",
      flashAttn: flashOn ? "" : "off",
      mmap: mmapOn ? "" : "off",
      mlock,
      threads: threads === "" ? 0 : Number(threads),
      parallel: parallel === "" ? 0 : Number(parallel),
      ub: ub === "" ? 0 : Number(ub),
      extraArgs,
      aliases: parseAliases(aliasesText),
      unlisted,
      skip,
      variants,
    };
  }

  // Add a fresh variant (unique placeholder name) and select it for editing.
  function addVariantEntry() {
    let n = 1;
    let name = "variant";
    while (variants.some((v) => v.name.toLowerCase() === name.toLowerCase())) name = `variant${++n}`;
    variants = [
      ...variants,
      {
        name, ctx: 0, vramTargetGB: 0, kvK: "", kvV: "", spec: "", ub: 0,
        reasoningFmt: "", unlisted: false, aliases: [], ctxCheckpoints: null, dry: null,
        kvInRam: false, cpuOffload: 0, flashAttn: "", mmap: "", mlock: false,
        threads: 0, parallel: 0, extraArgs: "",
      },
    ];
    selectedVariant = name;
  }

  function removeVariantEntry(name: string) {
    variants = variants.filter((v) => v.name !== name);
    if (selectedVariant === name) selectedVariant = "";
  }

  // Variant ctx auto = 0 (sizer picks). Toggling seeds a sane starting value.
  function setVCtxAuto(auto: boolean) {
    if (selectedV) selectedV.ctx = auto ? 0 : Math.min(8192, maxCtx);
  }
  // Variant target-VRAM auto = 0 (inherit global). Seed to the global budget.
  function setVVramAuto(auto: boolean) {
    if (selectedV) selectedV.vramTargetGB = auto ? 0 : globalTargetGB || maxVram;
  }
  // Variant offload auto = 0 (sizer picks). Seed to 1 when pinned.
  function setVOffloadAuto(auto: boolean) {
    if (selectedV) selectedV.cpuOffload = auto ? 0 : 1;
  }
  // Variant checkpoints: undefined/null => inherit the model-wide default.
  function setVCheckpointsInherit(inherit: boolean) {
    if (selectedV) selectedV.ctxCheckpoints = inherit ? null : 0;
  }
  // Variant DRY: null/undefined => inherit (sampler default on); else explicit.
  function vDryValue(): string {
    if (!selectedV || selectedV.dry == null) return "inherit";
    return selectedV.dry ? "on" : "off";
  }
  function setVDry(val: string) {
    if (selectedV) selectedV.dry = val === "inherit" ? null : val === "on";
  }
  // Renaming the selected variant must move the selection pointer with it so the
  // derived `selectedV` keeps resolving to the same array element.
  function renameSelectedVariant(e: Event) {
    const name = (e.currentTarget as HTMLInputElement).value;
    if (selectedV) {
      selectedV.name = name;
      selectedVariant = name;
    }
  }
  function setVAliases(e: Event) {
    if (selectedV) selectedV.aliases = parseAliases((e.currentTarget as HTMLInputElement).value);
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

    <!-- Sticky live estimate: stays pinned above the scrolling form so the memory
         cost of the current tuning is always visible while editing. -->
    {#if config && !loading}
      <div class="px-4 py-2 border-b border-card-border bg-background/60 shrink-0">
        {#if estimateError}
          <p class="font-mono text-xs text-error">{estimateError}</p>
        {:else if estimate}
          <div class="flex items-start gap-3">
            <span class="font-mono text-[0.55rem] uppercase tracking-wider text-txtsecondary shrink-0 leading-tight pt-0.5">Est.<br />load</span>
            <div class="flex-1 min-w-0">
              <VramGauge
                usedMb={estimate.estVramGB * 1024}
                totalMb={estimate.targetVramGB * 1024}
                height="0.55rem"
                showLabel={true}
                showLegend={true}
                segments={estimateSegments(estimate, kvInRam)}
              />
            </div>
            <div class="flex gap-3 font-mono text-[0.7rem] tabular-nums shrink-0">
              <div class="text-center leading-tight">
                <div class="text-[0.5rem] uppercase tracking-wide text-txtsecondary">CTX</div>
                <div class="text-txtmain">{fmtCtx(estimate.ctx)}</div>
              </div>
              <div class="text-center leading-tight">
                <div class="text-[0.5rem] uppercase tracking-wide text-txtsecondary">RAM</div>
                <div class={estimate.ramExceeded ? "text-error" : "text-txtmain"}>
                  {estimate.estRamGB.toFixed(1)}{estimate.maxRamGB ? `/${estimate.maxRamGB.toFixed(0)}` : ""}G
                </div>
              </div>
              <div class="leading-tight">
                <div title="GPU layers"><span class="text-txtsecondary">GPU</span> {nglDisplay(estimate.ngl, config.blockCount)}</div>
                <div title="CPU-offloaded MoE layers"><span class="text-txtsecondary">MoE</span> {estimate.nCpuMoe}</div>
              </div>
            </div>
          </div>
          {#if estimate.ramExceeded}
            <p class="mt-1 font-mono text-[0.65rem] text-error">⚠ Estimated RAM exceeds the configured ceiling.</p>
          {/if}
        {:else}
          <p class="font-mono text-xs text-txtsecondary">Estimating…</p>
        {/if}
      </div>
    {/if}

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
        <!-- Entry selector: Default is a pinned, non-deletable entry; variants
             follow. Editing one shows its fields below — everything a variant
             doesn't set inherits from Default. -->
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            class="px-2.5 py-1 rounded text-xs font-mono border transition-colors {selectedVariant === ''
              ? 'bg-primary text-white border-primary'
              : 'border-card-border text-txtsecondary hover:text-txtmain'}"
            onclick={() => (selectedVariant = "")}
          >default</button>
          {#each variants as v (v.name)}
            <span
              class="inline-flex items-center rounded border overflow-hidden {selectedVariant === v.name
                ? 'border-primary'
                : 'border-card-border'}"
            >
              <button
                type="button"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedVariant === v.name
                  ? 'bg-primary text-white'
                  : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedVariant = v.name)}>{v.name || "(unnamed)"}</button>
              <button
                type="button"
                title="Remove variant"
                aria-label="Remove variant {v.name}"
                class="px-1.5 py-1 text-xs {selectedVariant === v.name ? 'bg-primary text-white' : 'text-txtsecondary'} hover:text-error"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <button
            type="button"
            class="px-2 py-1 rounded text-xs border border-dashed border-card-border text-txtsecondary hover:text-txtmain"
            onclick={addVariantEntry}>+ variant</button>
        </div>

        {#if selectedVariant === ""}
        <!-- Curated override fields, ordered most-tinkered (top) to least (bottom). -->
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
                use:wheelAdjust
                class="flex-1 disabled:opacity-40"
              />
              <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {fmtCtx(maxCtx)}</span>
            </div>
          </label>

          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Target VRAM
              {@render hint("How much VRAM to size this model against. Auto = the global target. Lower it to leave headroom for other apps (e.g. a game); the sizer recomputes the offload to fit.")}
              <span class="ml-auto font-mono text-txtmain">
                {vramAuto ? (globalTargetGB ? `auto · ${globalTargetGB.toFixed(1)} GB` : "auto") : `${Number(vramTarget).toFixed(1)} GB`}
              </span>
            </span>
            <div class="flex items-center gap-3">
              <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                <input type="checkbox" bind:checked={vramAuto} /> Auto
              </label>
              <input type="range" min="0" max={maxVram} step="0.5" bind:value={vramTarget} disabled={vramAuto} use:wheelAdjust class="flex-1 disabled:opacity-40" />
              <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {maxVram.toFixed(0)}G</span>
            </div>
          </label>

          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Offloaded layers
              {@render hint("Force how many layers run on the CPU, overriding the auto sizer. Auto = let the sizer pick. MoE models offload expert layers (--n-cpu-moe); dense models drop GPU layers. More offload = less VRAM, slower.")}
              <span class="ml-auto font-mono text-txtmain">{cpuAuto ? "auto" : `${cpuOffload}/${maxOffload}`}</span>
            </span>
            <div class="flex items-center gap-3">
              <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                <input type="checkbox" bind:checked={cpuAuto} /> Auto
              </label>
              <input type="range" min="0" max={maxOffload} step="1" bind:value={cpuOffload} disabled={cpuAuto} use:wheelAdjust class="flex-1 disabled:opacity-40" />
              <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {maxOffload}</span>
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
              Parallel slots
              {@render hint("--parallel. Number of concurrent request slots. Empty = 1. Each slot splits the context window, so raise context too if you increase this.")}
            </span>
            <input type="number" min="0" step="1" bind:value={parallel} use:wheelAdjust class="cfg-input" placeholder="1" />
          </label>

          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Threads
              {@render hint("-t. CPU threads for token generation. Empty = the global default. Mostly matters for CPU-offloaded layers.")}
            </span>
            <input type="number" min="0" step="1" bind:value={threads} use:wheelAdjust class="cfg-input" placeholder="global default" />
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Batch size
              {@render hint("-ub/-b physical batch. Empty = auto (1024, or 512 for ≥64k context). Larger = faster prompt processing, more VRAM.")}
            </span>
            <input type="number" min="0" step="64" bind:value={ub} use:wheelAdjust class="cfg-input" placeholder="auto" />
          </label>

          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Aliases (comma-separated)
              {@render hint("Extra names this model answers to in the /v1/models API (e.g. map gpt-4 to this model).")}
            </span>
            <input type="text" bind:value={aliasesText} class="cfg-input" placeholder="e.g. gpt-4, default" />
          </label>
        </div>

        <!-- Toggles: the on/off knobs, least-tinkered, grouped at the bottom. -->
        <div>
          <div class="font-mono text-[0.6rem] uppercase tracking-wider text-txtsecondary mb-2">Toggles</div>
          <div class="grid grid-cols-2 gap-x-4 gap-y-2">
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" bind:checked={reasoningOn} />
              <span class="text-txtsecondary flex items-center gap-1">
                Reasoning
                {@render hint("Chain-of-thought reasoning. On = llama.cpp auto-detects and exposes it (default). Off disables reasoning (--reasoning-format none).")}
              </span>
            </label>
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" bind:checked={kvInRam} />
              <span class="text-txtsecondary flex items-center gap-1">
                KV in RAM
                {@render hint("Keep the KV cache in system RAM instead of VRAM (--no-kv-offload). Frees VRAM at the cost of speed.")}
              </span>
            </label>
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" bind:checked={flashOn} />
              <span class="text-txtsecondary flex items-center gap-1">
                Flash attention
                {@render hint("-fa. On by default and required for a quantized KV cache (q8_0 etc.). Turn off only with an f16 KV cache.")}
              </span>
            </label>
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" bind:checked={mmapOn} />
              <span class="text-txtsecondary flex items-center gap-1">
                Memory-map (mmap)
                {@render hint("Memory-map weights from disk. On = default (the sizer still drops it for fully GPU-resident / expert-offloaded models). Off forces --no-mmap, copying weights into RAM.")}
              </span>
            </label>
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" bind:checked={mlock} />
              <span class="text-txtsecondary flex items-center gap-1">
                mlock
                {@render hint("--mlock. Lock the model in RAM so the OS never swaps it out. Needs enough free RAM for the whole model.")}
              </span>
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
        </div>

        <!-- Launch command (editable, two-way) — collapsed at bottom. Form edits
             re-render it; editing it (then blurring) folds known flags back into
             the form and keeps unknown ones as passthrough. -->
        <details class="group">
          <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
            Launch parameters {config.hasOverride ? "(custom)" : "(autogen default)"}
          </summary>
          <textarea
            value={cmdDraft}
            oninput={onCmdInput}
            onblur={onCmdBlur}
            spellcheck="false"
            rows="6"
            class="mt-2 w-full bg-background rounded border border-card-border p-3 text-xs font-mono whitespace-pre-wrap break-all resize-y text-txtmain"
          ></textarea>
          <p class="text-xs text-txtsecondary mt-1">
            Edits sync with the fields above on blur. Flags autogen doesn't model are kept verbatim;
            <code>-c</code>/<code>-ngl</code>/<code>--n-cpu-moe</code> stay sizer-controlled.
          </p>
          <p class="text-xs text-txtsecondary mt-1 font-mono break-all">{config.gguf}</p>
        </details>
        {:else if selectedV}
          {@const sv = selectedV}
          <p class="text-xs text-txtsecondary -mt-1">
            Editing variant <span class="font-mono text-txtmain">{config.id}-{sv.name || "(unnamed)"}</span>.
            Anything left unset inherits from <button type="button" class="underline hover:text-txtmain" onclick={() => (selectedVariant = "")}>Default</button>.
          </p>
          <div class="grid grid-cols-2 gap-3">
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Name (suffix)
                {@render hint("The variant's id suffix and listen-name. The model loads as <base-id>-<name>.")}
              </span>
              <input type="text" value={sv.name} oninput={renameSelectedVariant} class="cfg-input" placeholder="e.g. game, long, judge" />
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Target VRAM
                {@render hint("Size this variant against this VRAM budget. Auto = inherit the global target.")}
                <span class="ml-auto font-mono text-txtmain">{sv.vramTargetGB ? `${Number(sv.vramTargetGB).toFixed(1)} GB` : "auto"}</span>
              </span>
              <div class="flex items-center gap-3">
                <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                  <input type="checkbox" checked={!sv.vramTargetGB} onchange={(e) => setVVramAuto((e.currentTarget as HTMLInputElement).checked)} /> Auto
                </label>
                <input type="range" min="0" max={maxVram} step="0.5" value={sv.vramTargetGB || 0} oninput={(e) => (sv.vramTargetGB = Number((e.currentTarget as HTMLInputElement).value))} disabled={!sv.vramTargetGB} use:wheelAdjust class="flex-1 disabled:opacity-40" />
                <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {maxVram.toFixed(0)}G</span>
              </div>
            </label>

            <label class="flex flex-col gap-1 text-sm col-span-2">
              <span class="text-txtsecondary flex items-center gap-1">
                Context window
                {@render hint("Tokens this variant can attend to. Auto = the sizer picks to fit VRAM.")}
                <span class="ml-auto font-mono text-txtmain">{sv.ctx ? fmtCtx(sv.ctx) : "auto"}</span>
              </span>
              <div class="flex items-center gap-3">
                <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                  <input type="checkbox" checked={!sv.ctx} onchange={(e) => setVCtxAuto((e.currentTarget as HTMLInputElement).checked)} /> Auto
                </label>
                <input type="range" min={CTX_MIN} max={maxCtx} step="1024" value={sv.ctx || CTX_MIN} oninput={(e) => (sv.ctx = Number((e.currentTarget as HTMLInputElement).value))} disabled={!sv.ctx} use:wheelAdjust class="flex-1 disabled:opacity-40" />
                <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {fmtCtx(maxCtx)}</span>
              </div>
            </label>

            <label class="flex flex-col gap-1 text-sm col-span-2">
              <span class="text-txtsecondary flex items-center gap-1">
                Offloaded layers
                {@render hint("Force how many layers run on the CPU for this variant, overriding the auto sizer. Auto = inherit / let the sizer pick.")}
                <span class="ml-auto font-mono text-txtmain">{sv.cpuOffload ? `${sv.cpuOffload}/${maxOffload}` : "auto"}</span>
              </span>
              <div class="flex items-center gap-3">
                <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                  <input type="checkbox" checked={!sv.cpuOffload} onchange={(e) => setVOffloadAuto((e.currentTarget as HTMLInputElement).checked)} /> Auto
                </label>
                <input type="range" min="0" max={maxOffload} step="1" value={sv.cpuOffload || 0} oninput={(e) => (sv.cpuOffload = Number((e.currentTarget as HTMLInputElement).value))} disabled={!sv.cpuOffload} use:wheelAdjust class="flex-1 disabled:opacity-40" />
                <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {maxOffload}</span>
              </div>
            </label>

            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">KV cache K</span>
              <select bind:value={sv.kvK} class="cfg-input">{#each KV_OPTS as o}<option value={o}>{o === "" ? "inherit (q8_0)" : o}</option>{/each}</select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">KV cache V</span>
              <select bind:value={sv.kvV} class="cfg-input">{#each KV_OPTS as o}<option value={o}>{o === "" ? "inherit (q8_0)" : o}</option>{/each}</select>
            </label>

            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">Speculative</span>
              <select bind:value={sv.spec} class="cfg-input">{#each specOpts as o}<option value={o.value}>{o.label}</option>{/each}</select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Batch size
                {@render hint("-ub/-b physical batch for this variant. Empty / 0 = inherit.")}
              </span>
              <input type="number" min="0" step="64" bind:value={sv.ub} use:wheelAdjust class="cfg-input" placeholder="inherit" />
            </label>

            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Threads
                {@render hint("-t. CPU threads for this variant. Empty / 0 = inherit.")}
              </span>
              <input type="number" min="0" step="1" bind:value={sv.threads} use:wheelAdjust class="cfg-input" placeholder="inherit" />
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Parallel slots
                {@render hint("--parallel concurrent request slots for this variant. Empty / 0 = inherit (1).")}
              </span>
              <input type="number" min="0" step="1" bind:value={sv.parallel} use:wheelAdjust class="cfg-input" placeholder="inherit" />
            </label>

            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Context checkpoints
                {@render hint("--ctx-checkpoints. KV snapshots kept to restore a diverging prompt instead of reprocessing. Inherit = model-wide default (32). 0 disables and reserves no checkpoint VRAM (used by the judge variant).")}
              </span>
              <div class="flex items-center gap-2">
                <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                  <input type="checkbox" checked={sv.ctxCheckpoints == null} onchange={(e) => setVCheckpointsInherit((e.currentTarget as HTMLInputElement).checked)} /> Inherit
                </label>
                {#if sv.ctxCheckpoints != null}
                  <input type="number" min="0" step="1" bind:value={sv.ctxCheckpoints} use:wheelAdjust class="cfg-input flex-1" />
                {/if}
              </div>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                DRY sampler
                {@render hint("DRY repetition penalty. Inherit = model default (on). Off disables it for this variant (the judge variant turns it off).")}
              </span>
              <select value={vDryValue()} onchange={(e) => setVDry((e.currentTarget as HTMLSelectElement).value)} class="cfg-input">
                <option value="inherit">inherit (on)</option>
                <option value="on">on</option>
                <option value="off">off</option>
              </select>
            </label>

            <label class="flex flex-col gap-1 text-sm col-span-2">
              <span class="text-txtsecondary flex items-center gap-1">
                Aliases (comma-separated)
                {@render hint("Extra names this variant answers to in the /v1/models API.")}
              </span>
              <input type="text" value={(sv.aliases ?? []).join(", ")} oninput={setVAliases} class="cfg-input" placeholder="e.g. gpt-4" />
            </label>
          </div>

          <!-- Toggles: same knobs as Default, scoped to this variant. -->
          <div>
            <div class="font-mono text-[0.6rem] uppercase tracking-wider text-txtsecondary mb-2">Toggles</div>
            <div class="grid grid-cols-2 gap-x-4 gap-y-2">
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={sv.reasoningFmt !== "off"} onchange={(e) => (sv.reasoningFmt = (e.currentTarget as HTMLInputElement).checked ? "" : "off")} />
                <span class="text-txtsecondary flex items-center gap-1">
                  Reasoning
                  {@render hint("Chain-of-thought reasoning. Off disables it (--reasoning-format none) for this variant.")}
                </span>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" bind:checked={sv.kvInRam} />
                <span class="text-txtsecondary flex items-center gap-1">
                  KV in RAM
                  {@render hint("Keep this variant's KV cache in system RAM (--no-kv-offload). Frees VRAM at the cost of speed.")}
                </span>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={sv.flashAttn !== "off"} onchange={(e) => (sv.flashAttn = (e.currentTarget as HTMLInputElement).checked ? "" : "off")} />
                <span class="text-txtsecondary flex items-center gap-1">
                  Flash attention
                  {@render hint("-fa. On by default and required for a quantized KV cache. Turn off only with an f16 KV cache.")}
                </span>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={sv.mmap !== "off"} onchange={(e) => (sv.mmap = (e.currentTarget as HTMLInputElement).checked ? "" : "off")} />
                <span class="text-txtsecondary flex items-center gap-1">
                  Memory-map (mmap)
                  {@render hint("Memory-map weights from disk. Off forces --no-mmap, copying weights into RAM.")}
                </span>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" bind:checked={sv.mlock} />
                <span class="text-txtsecondary flex items-center gap-1">
                  mlock
                  {@render hint("--mlock. Lock the model in RAM so the OS never swaps it out.")}
                </span>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" bind:checked={sv.unlisted} />
                <span class="text-txtsecondary flex items-center gap-1">
                  Unlisted
                  {@render hint("Hide this variant from /v1/models, but keep it loadable by exact id.")}
                </span>
              </label>
            </div>
          </div>

          <!-- Launch command for this variant (two-way, same as Default). -->
          <details class="group">
            <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
              Launch parameters (variant)
            </summary>
            <textarea
              value={cmdDraft}
              oninput={onCmdInput}
              onblur={onCmdBlur}
              spellcheck="false"
              rows="6"
              class="mt-2 w-full bg-background rounded border border-card-border p-3 text-xs font-mono whitespace-pre-wrap break-all resize-y text-txtmain"
            ></textarea>
            <p class="text-xs text-txtsecondary mt-1">
              Edits sync with the fields above on blur. Flags autogen doesn't model are kept verbatim;
              <code>-c</code>/<code>-ngl</code>/<code>--n-cpu-moe</code> stay sizer-controlled.
            </p>
          </details>
        {/if}
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
