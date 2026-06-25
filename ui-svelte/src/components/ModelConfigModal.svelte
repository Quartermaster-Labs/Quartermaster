<script lang="ts">
  import {
    getModelConfig,
    putModelOverride,
    putDefaultVariants,
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
  let preserveThinking = $state(false); // keep prior-turn <think> in history (needs reasoning on)
  let reasoningBudget = $state<number | "">(""); // --reasoning-budget token cap; "" => no cap
  let flashOn = $state(true); // false => flashAttn "off"
  let mmapOn = $state(true); // false => mmap "off" (--no-mmap)
  let mlock = $state(false);
  let threads = $state<number | "">(""); // "" = global default
  let parallel = $state<number | "">(""); // "" = 1
  let ub = $state<number | "">(""); // "" = auto physical batch
  // DRY sampler (Default). dryOn drives on/off; values "" => generator default
  // (0.8 / 1.75 / 3).
  let dryOn = $state(true);
  let dryMultiplier = $state<number | "">("");
  let dryBase = $state<number | "">("");
  let dryAllowedLength = $state<number | "">("");
  // Speculative-decode sub-knobs (Default), emitted per spec backend; "" / false
  // => omit (llama-server default).
  let specDraftNMax = $state<number | "">(""); // draft-mtp; "" => 2
  let specDefault = $state(false);
  let specNgramSizeN = $state<number | "">("");
  let specNgramSizeM = $state<number | "">("");
  let specNgramMinHits = $state<number | "">("");
  let aliasesText = $state("");
  let unlisted = $state(false);
  let skip = $state(false);
  // Model-wide --ctx-checkpoints default; null => auto (sizer/llama default),
  // explicit (incl. 0) pins it. Variants inherit this unless they set their own.
  let ctxCheckpoints = $state<number | null>(null);
  let variants = $state<ModelVariant[]>([]);
  // Per-model ctx tiers (32k/64k…), seeded from override.ctxVariants ints as
  // editable variant entries. On save, tiers that only set ctx collapse back to
  // ctxVariants ints; tiers given extra knobs promote to named variants.
  let ctxTiers = $state<ModelVariant[]>([]);
  // Fleet-wide default variants (e.g. game) shared by every model. Editable here
  // but saved globally; a snapshot detects edits so we only PUT when changed.
  let defaultVariants = $state<ModelVariant[]>([]);
  let origDefaultVariants = $state("");

  // Two-way launch-parameters box. cmdDraft is the editable command text. Form
  // edits re-render it from the backend (renderCmd); editing the box parses known
  // flags back into the form (parseCmd, on blur) and stashes anything autogen
  // doesn't model into extraArgs (passthrough, appended to the emitted command).
  let cmdDraft = $state("");
  let extraArgs = $state("");

  // --- Image (diffusion / sd-server) fields. Only used when config.isImage. ---
  // Component paths (external VAE + text encoders); "" => omit the flag.
  let vaePath = $state("");
  let clipLPath = $state("");
  let clipGPath = $state("");
  let t5Path = $state("");
  let textEncoderPath = $state("");
  // Placement: offload is tri-state (auto/on/off); the rest default-on ("" => on,
  // "off" => omit the flag), surfaced as plain on/off checkboxes.
  let offloadToCpu = $state(""); // "" auto | "on" | "off"
  let teOnCpu = $state(""); // "" on | "off"
  let vaeTiling = $state(""); // "" on | "off"
  let diffusionFa = $state(""); // "" on | "off"
  // Generation defaults baked into the launch cmd; "" => sd-server default.
  let defaultSteps = $state<number | "">("");
  let defaultCfg = $state<number | "">("");
  let defaultSampler = $state("");
  let defaultWidth = $state<number | "">("");
  let defaultHeight = $state<number | "">("");

  const imageMode = $derived(config?.isImage ?? false);
  // sd.cpp sampling methods (mirrors the playground's SAMPLER_OPTIONS).
  const IMG_SAMPLERS = ["", "euler_a", "euler", "heun", "dpm2", "dpmpp2s_a", "dpmpp2m", "dpmpp2mv2", "ipndm", "ipndm_v", "lcm", "ddim_trailing", "tcd"];

  // Flags autogen always emits and OWNS (computed or fixed): ignored when parsing
  // the box so editing them never flips a form "auto" toggle or pins a value.
  // Value-flags owned by other controls (sliders / toggles / sizer), swallowed
  // when parsing so they never bleed into extraArgs and double-emit:
  //   -c/-ngl/--n-cpu-moe/-b  sizer; --ctx-checkpoints  its own field;
  //   --chat-template-kwargs  the preserve-thinking toggle; -md  draft path.
  const IGNORE_VALUE = new Set(["-m", "--port", "--host", "-c", "-ngl", "--n-cpu-moe", "-b", "--ctx-checkpoints", "--chat-template-kwargs", "-md"]);
  const IGNORE_BOOL = new Set(["--kv-unified", "--no-warmup", "--no-webui", "--jinja"]);

  // Parsed launch-flag bundle shared by the Default form and a variant. Booleans
  // are normalized to the form's on/off sense; computed flags are dropped.
  interface ParsedCmd {
    flashOn: boolean;
    mmapOn: boolean;
    mlock: boolean;
    kvInRam: boolean;
    reasoningOn: boolean;
    reasoningBudget: number | "";
    kvK: string;
    kvV: string;
    spec: string;
    threads: number | "";
    parallel: number | "";
    ub: number | "";
    extraArgs: string;
    // DRY: presence of any --dry-* flag => on; absence => off. Values "" => default.
    dryOn: boolean;
    dryMultiplier: number | "";
    dryBase: number | "";
    dryAllowedLength: number | "";
    // Speculative sub-knobs (value "" / false => omit).
    specDraftNMax: number | "";
    specDefault: boolean;
    specNgramSizeN: number | "";
    specNgramSizeM: number | "";
    specNgramMinHits: number | "";
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
      reason: string | null = null,
      rBudget: string | null = null;
    let noMmap = false,
      mlockF = false,
      noKv = false,
      specDef = false;
    let dMult: string | null = null,
      dBase: string | null = null,
      dAllow: string | null = null,
      sNMax: string | null = null,
      sNgN: string | null = null,
      sNgM: string | null = null,
      sNgHits: string | null = null;
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
        case "--spec-type": { const t = val(); sp = sp ? `${sp}+${t}` : t; break; } // chained backends accumulate
        case "--reasoning-format": reason = val(); break;
        case "--reasoning-budget": rBudget = val(); break;
        case "--reasoning": if (val() === "off") reason = "off"; break;
        case "--no-mmap": noMmap = true; break;
        case "--mlock": mlockF = true; break;
        case "--no-kv-offload": noKv = true; break;
        case "--dry-multiplier": dMult = val(); break;
        case "--dry-base": dBase = val(); break;
        case "--dry-allowed-length": dAllow = val(); break;
        case "--spec-draft-n-max": sNMax = val(); break;
        case "--spec-default": specDef = true; break;
        case "--spec-ngram-map-k4v-size-n": sNgN = val(); break;
        case "--spec-ngram-map-k4v-size-m": sNgM = val(); break;
        case "--spec-ngram-map-k4v-min-hits": sNgHits = val(); break;
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
      reasoningBudget: rBudget !== null && rBudget !== "" ? Number(rBudget) : "",
      kvK: ctk ?? "",
      kvV: ctv ?? "",
      spec: sp ?? "",
      threads: t !== null ? Number(t) : "",
      parallel: par !== null ? Number(par) : "",
      ub: u !== null ? Number(u) : "",
      extraArgs: extras.join(" "),
      // DRY is on iff any --dry-* flag survived in the box.
      dryOn: dMult !== null || dBase !== null || dAllow !== null,
      dryMultiplier: dMult !== null && dMult !== "" ? Number(dMult) : "",
      dryBase: dBase !== null && dBase !== "" ? Number(dBase) : "",
      dryAllowedLength: dAllow !== null && dAllow !== "" ? Number(dAllow) : "",
      specDraftNMax: sNMax !== null && sNMax !== "" ? Number(sNMax) : "",
      specDefault: specDef,
      specNgramSizeN: sNgN !== null && sNgN !== "" ? Number(sNgN) : "",
      specNgramSizeM: sNgM !== null && sNgM !== "" ? Number(sNgM) : "",
      specNgramMinHits: sNgHits !== null && sNgHits !== "" ? Number(sNgHits) : "",
    };
  }

  // Apply parsed flags to the Default form fields.
  function applyParsedToDefault(p: ParsedCmd) {
    flashOn = p.flashOn;
    mmapOn = p.mmapOn;
    mlock = p.mlock;
    kvInRam = p.kvInRam;
    reasoningOn = p.reasoningOn;
    reasoningBudget = p.reasoningBudget;
    kvK = p.kvK;
    kvV = p.kvV;
    spec = p.spec;
    threads = p.threads;
    parallel = p.parallel;
    ub = p.ub;
    dryOn = p.dryOn;
    dryMultiplier = p.dryMultiplier;
    dryBase = p.dryBase;
    dryAllowedLength = p.dryAllowedLength;
    specDraftNMax = p.specDraftNMax;
    specDefault = p.specDefault;
    specNgramSizeN = p.specNgramSizeN;
    specNgramSizeM = p.specNgramSizeM;
    specNgramMinHits = p.specNgramMinHits;
    extraArgs = p.extraArgs;
  }

  // Apply parsed flags to the selected variant (string on/off knobs mirror the
  // override encoding: "" = inherit/on, "off" = forced off).
  function applyParsedToVariant(v: ModelVariant, p: ParsedCmd) {
    // A variant is standalone, so the box renders generator-default base + the
    // variant's fields. Model-specific flags (-ngl/-c/--n-cpu-moe/-m...) are
    // skipped by IGNORE_VALUE, so nothing model-bound leaks. The only fields that
    // would wrongly bake into a fleet-wide variant are kv and spec at their
    // generator defaults (q8_0 / draft-mtp|ngram-mod): capture those as a delta vs
    // the generator default so an unchanged value stays "inherit" ("").
    const genSpec = config?.isMTP ? "draft-mtp" : "ngram-mod";
    v.flashAttn = p.flashOn ? "" : "off";
    v.mmap = p.mmapOn ? "" : "off";
    v.mlock = p.mlock;
    v.kvInRam = p.kvInRam;
    v.reasoningFmt = p.reasoningOn ? "" : "off";
    v.kvK = p.kvK === "q8_0" ? "" : p.kvK;
    v.kvV = p.kvV === "q8_0" ? "" : p.kvV;
    v.spec = p.spec === genSpec ? "" : p.spec;
    v.threads = p.threads === "" ? 0 : Number(p.threads);
    v.parallel = p.parallel === "" ? 0 : Number(p.parallel);
    v.ub = p.ub === "" ? 0 : Number(p.ub);
    // Box edit is explicit: any --dry-* present => on, none => off (loses inherit).
    v.dry = p.dryOn;
    v.dryMultiplier = p.dryMultiplier === "" ? 0 : Number(p.dryMultiplier);
    v.dryBase = p.dryBase === "" ? 0 : Number(p.dryBase);
    v.dryAllowedLength = p.dryAllowedLength === "" ? 0 : Number(p.dryAllowedLength);
    v.specDraftNMax = p.specDraftNMax === "" ? 0 : Number(p.specDraftNMax);
    v.specDefault = p.specDefault;
    v.specNgramSizeN = p.specNgramSizeN === "" ? 0 : Number(p.specNgramSizeN);
    v.specNgramSizeM = p.specNgramSizeM === "" ? 0 : Number(p.specNgramSizeM);
    v.specNgramMinHits = p.specNgramMinHits === "" ? 0 : Number(p.specNgramMinHits);
    v.extraArgs = p.extraArgs.trim();
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
      ctx, ctxAuto, kvK, kvV, kvInRam, spec, reasoningOn, reasoningBudget, preserveThinking, flashOn, mmapOn, mlock, threads, parallel, ub, vramTarget, vramAuto, cpuOffload, cpuAuto, extraArgs, ctxCheckpoints,
      dryOn, dryMultiplier, dryBase, dryAllowedLength, specDraftNMax, specDefault, specNgramSizeN, specNgramSizeM, specNgramMinHits,
      vaePath, clipLPath, clipGPath, t5Path, textEncoderPath, offloadToCpu, teOnCpu, vaeTiling, diffusionFa,
      defaultSteps, defaultCfg, defaultSampler, defaultWidth, defaultHeight,
      selectedV?.ctx, selectedV?.kvK, selectedV?.kvV, selectedV?.kvInRam, selectedV?.spec,
      selectedV?.reasoningFmt, selectedV?.flashAttn, selectedV?.mmap, selectedV?.mlock,
      selectedV?.threads, selectedV?.parallel, selectedV?.ub, selectedV?.vramTargetGB,
      selectedV?.cpuOffload, selectedV?.ctxCheckpoints, selectedV?.dry, selectedV?.extraArgs, selectedV?.preserveThinking,
      selectedV?.dryMultiplier, selectedV?.dryBase, selectedV?.dryAllowedLength,
      selectedV?.specDraftNMax, selectedV?.specDefault, selectedV?.specNgramSizeN, selectedV?.specNgramSizeM, selectedV?.specNgramMinHits,
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

  // A named variant INHERITS the model-wide override (the Default tab) and layers
  // its own non-blank fields on top — same as the generate path. So the preview
  // override is the base merged with the variant's set fields, NOT a standalone
  // render. aliases/unlisted/variants stay variant-local (never inherited).
  function variantToOverride(v: ModelVariant): ModelOverride {
    // Image variants inherit the model-wide base (component paths + placement) and
    // override only their preset; preview merges base + variant so the cmd is real.
    if (imageMode) {
      const base = buildOverride();
      return {
        ...base,
        vaePath: v.vaePath || base.vaePath,
        clipLPath: v.clipLPath || base.clipLPath,
        clipGPath: v.clipGPath || base.clipGPath,
        t5Path: v.t5Path || base.t5Path,
        textEncoderPath: v.textEncoderPath || base.textEncoderPath,
        offloadToCpu: v.offloadToCpu || base.offloadToCpu,
        teOnCpu: v.teOnCpu || base.teOnCpu,
        vaeTiling: v.vaeTiling || base.vaeTiling,
        diffusionFa: v.diffusionFa || base.diffusionFa,
        vramTargetGB: v.vramTargetGB || base.vramTargetGB,
        threads: v.threads || base.threads,
        defaultSteps: v.defaultSteps || base.defaultSteps,
        defaultCfg: v.defaultCfg || base.defaultCfg,
        defaultSampler: v.defaultSampler || base.defaultSampler,
        defaultWidth: v.defaultWidth || base.defaultWidth,
        defaultHeight: v.defaultHeight || base.defaultHeight,
        extraArgs: v.extraArgs || base.extraArgs,
        aliases: v.aliases ?? [],
        unlisted: v.unlisted ?? false,
        variants: [],
      };
    }
    const base = buildOverride();
    return {
      ...base,
      ctx: v.ctx || base.ctx || 0,
      kvK: v.kvK || base.kvK || "",
      kvV: v.kvV || base.kvV || "",
      kvInRam: v.kvInRam ?? base.kvInRam ?? false,
      vramTargetGB: v.vramTargetGB || base.vramTargetGB || 0,
      cpuOffload: v.cpuOffload || base.cpuOffload || 0,
      spec: v.spec || base.spec || "",
      reasoningFmt: v.reasoningFmt || base.reasoningFmt || "",
      // preserve-thinking defaults on for reasoning variants; off when reasoning off.
      preserveThinking: v.reasoningFmt !== "off" && (v.preserveThinking ?? true),
      flashAttn: v.flashAttn || base.flashAttn || "",
      mmap: v.mmap || base.mmap || "",
      mlock: v.mlock ?? base.mlock ?? false,
      threads: v.threads || base.threads || 0,
      parallel: v.parallel || base.parallel || 0,
      ub: v.ub || base.ub || 0,
      extraArgs: v.extraArgs || base.extraArgs || "",
      dry: v.dry ?? base.dry ?? null,
      dryMultiplier: v.dryMultiplier || base.dryMultiplier || 0,
      dryBase: v.dryBase || base.dryBase || 0,
      dryAllowedLength: v.dryAllowedLength || base.dryAllowedLength || 0,
      specDraftNMax: v.specDraftNMax || base.specDraftNMax || 0,
      specDefault: v.specDefault || base.specDefault || false,
      specNgramSizeN: v.specNgramSizeN || base.specNgramSizeN || 0,
      specNgramSizeM: v.specNgramSizeM || base.specNgramSizeM || 0,
      specNgramMinHits: v.specNgramMinHits || base.specNgramMinHits || 0,
      ctxCheckpoints: v.ctxCheckpoints ?? null,
      // variant-local: never inherited from the base.
      aliases: v.aliases ?? [],
      unlisted: v.unlisted ?? false,
      skip: false,
      // single-variant preview: don't fan nested variants/tiers back in.
      variants: [],
      ctxVariants: [],
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
    selectedVariant
      ? ([...variants, ...ctxTiers, ...defaultVariants].find((v) => v.name === selectedVariant) ?? null)
      : null,
  );
  // The selected tab is a fleet-wide default variant (game) => edits save globally.
  const selectedIsDefault = $derived(
    !!selectedVariant && defaultVariants.some((v) => v.name === selectedVariant),
  );

  const KV_OPTS = ["", "q8_0", "q4_0", "q5_1", "f16", "bf16"];

  // Slider ceiling = trained context length (fallback 32k). Floor 4k.
  const CTX_MIN = 4096;
  const maxCtx = $derived(config?.maxCtx && config.maxCtx > CTX_MIN ? config.maxCtx : 32768);
  // Offload slider ceiling = transformer block count (fallback 64).
  const maxOffload = $derived(config?.blockCount && config.blockCount > 0 ? config.blockCount : 64);
  // VRAM slider ceiling = the global budget (fallback 24 GB until settings load).
  const maxVram = $derived(globalTargetGB > 0 ? globalTargetGB : 24);

  // Speculative backends are chainable (e.g. draft-mtp + ngram-map-k4v), so spec
  // is a "+"-joined list. draft-mtp is only offered when the model has MTP layers.
  const specBackends = $derived([
    ...(config?.isMTP ? ["draft-mtp"] : []),
    "ngram-mod",
    "ngram-map-k4v",
  ]);

  // Does spec list s contain backend b?
  function specHas(s: string | undefined, b: string): boolean {
    return (s ?? "").split("+").includes(b);
  }
  // Toggle backend b in the "+"-joined list s. "none" is exclusive (clears the
  // rest); checking a real backend clears "none".
  function specToggle(s: string | undefined, b: string, on: boolean): string {
    if (b === "none") return on ? "none" : "";
    let parts = (s ?? "").split("+").filter(Boolean).filter((x) => x !== "none" && x !== b);
    if (on) parts.push(b);
    // Unchecking the last backend means "off" — store explicit "none" rather
    // than "" (empty would fall back to the MTP/ngram auto-default at emit).
    if (!on && parts.length === 0) return "none";
    return parts.join("+");
  }
  // Resolved active backends (""/unset => the generator default) so the form can
  // show only the sub-knobs the chosen backends actually emit.
  function activeSpecs(s: string | undefined): string[] {
    const raw = (s ?? "").split("+").filter(Boolean);
    if (raw.length === 0) return [config?.isMTP ? "draft-mtp" : "ngram-mod"];
    if (raw.includes("none")) return [];
    return raw;
  }
  const effSpecs = $derived(activeSpecs(spec)); // Default tab
  // Variant tab: own spec list, else the generator default (standalone — does NOT
  // inherit the Default tab's spec).
  const vEffSpecs = $derived(activeSpecs(selectedV?.spec));

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
    reasoningBudget = o?.reasoningBudget ? o.reasoningBudget : "";
    preserveThinking = o?.preserveThinking ?? false;
    flashOn = (o?.flashAttn ?? "") !== "off";
    mmapOn = (o?.mmap ?? "") !== "off";
    mlock = o?.mlock ?? false;
    threads = o?.threads ? o.threads : "";
    parallel = o?.parallel ? o.parallel : "";
    ub = o?.ub ? o.ub : "";
    dryOn = o?.dry ?? true; // null/undefined => on
    dryMultiplier = o?.dryMultiplier ? o.dryMultiplier : "";
    dryBase = o?.dryBase ? o.dryBase : "";
    dryAllowedLength = o?.dryAllowedLength ? o.dryAllowedLength : "";
    specDraftNMax = o?.specDraftNMax ? o.specDraftNMax : "";
    specDefault = o?.specDefault ?? false;
    specNgramSizeN = o?.specNgramSizeN ? o.specNgramSizeN : "";
    specNgramSizeM = o?.specNgramSizeM ? o.specNgramSizeM : "";
    specNgramMinHits = o?.specNgramMinHits ? o.specNgramMinHits : "";
    extraArgs = o?.extraArgs ?? "";
    aliasesText = (o?.aliases ?? []).join(", ");
    unlisted = o?.unlisted ?? false;
    skip = o?.skip ?? false;
    ctxCheckpoints = o?.ctxCheckpoints ?? null;
    variants = (o?.variants ?? []).map((v) => ({ ...v }));
    ctxTiers = (o?.ctxVariants ?? []).map((n) => blankVariant(fmtCtx(n), n));
    // Image fields (no-op for llama models — left at "").
    vaePath = o?.vaePath ?? "";
    clipLPath = o?.clipLPath ?? "";
    clipGPath = o?.clipGPath ?? "";
    t5Path = o?.t5Path ?? "";
    textEncoderPath = o?.textEncoderPath ?? "";
    offloadToCpu = o?.offloadToCpu ?? "";
    teOnCpu = o?.teOnCpu ?? "";
    vaeTiling = o?.vaeTiling ?? "";
    diffusionFa = o?.diffusionFa ?? "";
    defaultSteps = o?.defaultSteps ? o.defaultSteps : "";
    defaultCfg = o?.defaultCfg ? o.defaultCfg : "";
    defaultSampler = o?.defaultSampler ?? "";
    defaultWidth = o?.defaultWidth ? o.defaultWidth : "";
    defaultHeight = o?.defaultHeight ? o.defaultHeight : "";
  }

  // A ModelVariant with every field at its inherit/default value except name+ctx.
  // Used to render a ctx tier (or a fresh variant) through the variant editor.
  function blankVariant(name: string, ctx: number): ModelVariant {
    return {
      name, ctx, vramTargetGB: 0, kvK: "", kvV: "", spec: "", ub: 0,
      reasoningFmt: "", unlisted: false, aliases: [], ctxCheckpoints: null, dry: null, preserveThinking: null,
      kvInRam: false, cpuOffload: 0, flashAttn: "", mmap: "", mlock: false,
      threads: 0, parallel: 0, extraArgs: "",
      dryMultiplier: 0, dryBase: 0, dryAllowedLength: 0,
      specDraftNMax: 0, specDefault: false, specNgramSizeN: 0, specNgramSizeM: 0, specNgramMinHits: 0,
      vaePath: "", clipLPath: "", clipGPath: "", t5Path: "", textEncoderPath: "",
      offloadToCpu: "", teOnCpu: "", vaeTiling: "", diffusionFa: "",
      defaultSteps: 0, defaultCfg: 0, defaultSampler: "", defaultWidth: 0, defaultHeight: 0,
    };
  }

  // Add a fresh image variant (inherits the model's paths/placement; overrides
  // only its generation preset) and select it.
  function addImageVariantEntry() {
    let n = 1;
    let name = "preset";
    const taken = (nm: string) => variants.some((v) => v.name.toLowerCase() === nm.toLowerCase());
    while (taken(name)) name = `preset${++n}`;
    variants = [...variants, blankVariant(name, 0)];
    selectedVariant = name;
  }

  // True when a ctx tier carries nothing but its ctx value, so it round-trips as
  // a compact ctxVariants int instead of a full named variant.
  function ctxTierIsPure(v: ModelVariant): boolean {
    return (
      !v.vramTargetGB && !v.kvK && !v.kvV && !v.spec && !v.ub &&
      !v.reasoningFmt && !v.unlisted && (v.aliases?.length ?? 0) === 0 &&
      v.ctxCheckpoints == null && v.dry == null && v.preserveThinking == null && !v.kvInRam && !v.cpuOffload &&
      !v.flashAttn && !v.mmap && !v.mlock && !v.threads && !v.parallel && !v.extraArgs &&
      !v.dryMultiplier && !v.dryBase && !v.dryAllowedLength &&
      !v.specDraftNMax && !v.specDefault && !v.specNgramSizeN && !v.specNgramSizeM && !v.specNgramMinHits
    );
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
      defaultVariants = (cfg.defaultVariants ?? []).map((v) => ({ ...v }));
      origDefaultVariants = JSON.stringify(defaultVariants);
      // Land on the clicked row's variant: the model id ends with "-<name>" for
      // a variant/tier, or is the bare base for Default. Match the longest name so a
      // name that's a suffix of another doesn't win.
      let chosen = "";
      if (openForId) {
        for (const v of [...variants, ...ctxTiers, ...defaultVariants]) {
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
      ctx, ctxAuto, kvK, kvV, kvInRam, spec, vramTarget, vramAuto, cpuOffload, cpuAuto, ctxCheckpoints,
      selectedV?.ctx, selectedV?.kvK, selectedV?.kvV, selectedV?.spec,
      selectedV?.vramTargetGB, selectedV?.ub, selectedV?.ctxCheckpoints,
      selectedV?.kvInRam, selectedV?.cpuOffload,
    ];
    void deps;
    // Diffusion sizing isn't modeled by the llama sizer; skip the estimate.
    if (!open || !config || !modelId || imageMode) return;
    clearTimeout(estTimer);
    estTimer = setTimeout(runEstimate, 100);
  });

  async function runEstimate() {
    if (!modelId || !config) return;
    estimateError = null;
    try {
      // A variant INHERITS the model-wide override (Default form fields) and
      // overrides with its own non-blank fields — same as the generate path. So a
      // blank kv/spec/vram falls back to the Default values, not the bare backend
      // default, keeping the preview estimate in step with the emitted config.
      const params = selectedV
        ? {
            ctx: selectedV.ctx ? Number(selectedV.ctx) : ctxAuto ? undefined : Number(ctx),
            kvK: selectedV.kvK || kvK || undefined,
            kvV: selectedV.kvV || kvV || undefined,
            kvInRam: selectedV.kvInRam ?? kvInRam,
            spec: selectedV.spec || spec || undefined,
            vram: selectedV.vramTargetGB ? Number(selectedV.vramTargetGB) : vramAuto ? undefined : Number(vramTarget),
            cpuOffload: selectedV.cpuOffload ? Number(selectedV.cpuOffload) : cpuAuto ? undefined : Number(cpuOffload),
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
            ctxCheckpoints: ctxCheckpoints ?? undefined,
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
      // Only adjust when the field is focused; otherwise let the page scroll.
      if (document.activeElement !== node) return;
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
      reasoningBudget: reasoningBudget === "" ? 0 : Number(reasoningBudget),
      preserveThinking: reasoningOn && preserveThinking,
      flashAttn: flashOn ? "" : "off",
      mmap: mmapOn ? "" : "off",
      mlock,
      threads: threads === "" ? 0 : Number(threads),
      parallel: parallel === "" ? 0 : Number(parallel),
      ub: ub === "" ? 0 : Number(ub),
      dry: dryOn ? null : false, // on => inherit/default-on; off => explicit false
      dryMultiplier: dryMultiplier === "" ? 0 : Number(dryMultiplier),
      dryBase: dryBase === "" ? 0 : Number(dryBase),
      dryAllowedLength: dryAllowedLength === "" ? 0 : Number(dryAllowedLength),
      specDraftNMax: specDraftNMax === "" ? 0 : Number(specDraftNMax),
      specDefault,
      specNgramSizeN: specNgramSizeN === "" ? 0 : Number(specNgramSizeN),
      specNgramSizeM: specNgramSizeM === "" ? 0 : Number(specNgramSizeM),
      specNgramMinHits: specNgramMinHits === "" ? 0 : Number(specNgramMinHits),
      extraArgs,
      aliases: parseAliases(aliasesText),
      unlisted,
      skip,
      ctxCheckpoints,
      // ctx tiers with nothing but a ctx stay compact ints; any with extra knobs
      // promote to named variants alongside the explicit ones.
      ctxVariants: ctxTiers.filter(ctxTierIsPure).map((v) => v.ctx ?? 0).filter((n) => n > 0),
      variants: [...variants, ...ctxTiers.filter((v) => !ctxTierIsPure(v))],
      // Image fields (zero/empty for llama models => emit nothing).
      vaePath,
      clipLPath,
      clipGPath,
      t5Path,
      textEncoderPath,
      offloadToCpu,
      teOnCpu,
      vaeTiling,
      diffusionFa,
      defaultSteps: defaultSteps === "" ? 0 : Number(defaultSteps),
      defaultCfg: defaultCfg === "" ? 0 : Number(defaultCfg),
      defaultSampler,
      defaultWidth: defaultWidth === "" ? 0 : Number(defaultWidth),
      defaultHeight: defaultHeight === "" ? 0 : Number(defaultHeight),
    };
  }

  // Snapshot the current Default tab into a standalone variant. A new per-model
  // variant copies the Default's launch knobs ONCE at creation (so it starts
  // matching what the model launches with); afterwards it is fully independent —
  // later Default edits don't touch it. Sizing fields (ctx/vram/cpuOffload) stay
  // auto so the variant sizes itself. Model-specific, so only used for per-model
  // variants, never fleet-wide ones.
  function variantFromDefault(name: string): ModelVariant {
    const o = buildOverride();
    return {
      name, ctx: 0, vramTargetGB: 0, cpuOffload: 0,
      kvK: o.kvK ?? "", kvV: o.kvV ?? "", kvInRam: o.kvInRam ?? false, spec: o.spec ?? "",
      reasoningFmt: o.reasoningFmt ?? "",
      preserveThinking: o.preserveThinking ? null : false,
      flashAttn: o.flashAttn ?? "", mmap: o.mmap ?? "", mlock: o.mlock ?? false,
      threads: o.threads ?? 0, parallel: o.parallel ?? 0, ub: o.ub ?? 0,
      dry: o.dry ?? null,
      dryMultiplier: o.dryMultiplier ?? 0, dryBase: o.dryBase ?? 0, dryAllowedLength: o.dryAllowedLength ?? 0,
      specDraftNMax: o.specDraftNMax ?? 0, specDefault: o.specDefault ?? false,
      specNgramSizeN: o.specNgramSizeN ?? 0, specNgramSizeM: o.specNgramSizeM ?? 0, specNgramMinHits: o.specNgramMinHits ?? 0,
      extraArgs: o.extraArgs ?? "", unlisted: false, aliases: [], ctxCheckpoints: o.ctxCheckpoints ?? null,
    };
  }

  // Add a fresh variant (unique placeholder name) and select it for editing.
  function addVariantEntry() {
    let n = 1;
    let name = "variant";
    const taken = (nm: string) =>
      [...variants, ...ctxTiers, ...defaultVariants].some((v) => v.name.toLowerCase() === nm.toLowerCase());
    while (taken(name)) name = `variant${++n}`;
    variants = [...variants, variantFromDefault(name)];
    selectedVariant = name;
  }

  // Add a fresh fleet-wide variant (shared by every model) and select it. Saved
  // globally via putDefaultVariants; the snapshot compare in save() detects it.
  function addDefaultVariantEntry() {
    let n = 1;
    let name = "fleet";
    const taken = (nm: string) =>
      [...variants, ...ctxTiers, ...defaultVariants].some((v) => v.name.toLowerCase() === nm.toLowerCase());
    while (taken(name)) name = `fleet${++n}`;
    // Seed from the current Default (spec, kv, engine knobs) like a per-model
    // variant: it inherits at creation, then drifts independently (standalone).
    defaultVariants = [...defaultVariants, variantFromDefault(name)];
    selectedVariant = name;
  }

  // Remove a tab from whichever bucket holds it (per-model variant, ctx tier, or
  // fleet-wide default variant). Fleet-wide removals save globally.
  function removeVariantEntry(name: string) {
    variants = variants.filter((v) => v.name !== name);
    ctxTiers = ctxTiers.filter((v) => v.name !== name);
    defaultVariants = defaultVariants.filter((v) => v.name !== name);
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
      // Fleet-wide default variants saved separately (global) only when edited.
      if (JSON.stringify(defaultVariants) !== origDefaultVariants) {
        await putDefaultVariants(defaultVariants);
      }
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
        {#if config}<span class="text-base font-mono font-normal text-txtsecondary">{config.id}{selectedVariant && !config.id.endsWith(`-${selectedVariant}`) ? `-${selectedVariant}` : ""}</span>{/if}
      </h2>
      <button onclick={() => dialogEl?.close()} class="text-txtsecondary hover:text-txtmain text-2xl leading-none">&times;</button>
    </div>

    <!-- Sticky live estimate: stays pinned above the scrolling form so the memory
         cost of the current tuning is always visible while editing. -->
    {#if config && !loading && !imageMode}
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

    <div class="overflow-y-auto flex-1 p-4 space-y-4 pretty-scroll">
      {#if loading}
        <p class="text-txtsecondary">Loading…</p>
      {:else if error}
        <p class="text-red-500 text-sm font-mono whitespace-pre-wrap">{error}</p>
      {/if}

      {#snippet hint(text: string)}
        <span class="hint" title={text} aria-label={text}>?</span>
      {/snippet}

      {#if config}
        {#if imageMode}
        <!-- Image (diffusion / sd-server) form. Mirrors the Default tab's design
             but with diffusion-relevant knobs: external component paths, placement,
             and per-model generation defaults. No KV/ctx/spec; no estimate. Variants
             are generation presets (steps/cfg/size) inheriting the model's paths. -->
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            class="px-2.5 py-1 rounded text-xs font-mono border transition-colors {selectedVariant === ''
              ? 'bg-primary text-white border-primary'
              : 'border-card-border text-txtsecondary hover:text-txtmain'}"
            onclick={() => (selectedVariant = "")}
          >default</button>
          {#each variants as v (v.name)}
            <span class="inline-flex items-center rounded border overflow-hidden {selectedVariant === v.name ? 'border-primary' : 'border-card-border'}">
              <button
                type="button"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedVariant === v.name ? 'bg-primary text-white' : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedVariant = v.name)}>{v.name || "(unnamed)"}</button>
              <button
                type="button"
                title="Remove preset"
                aria-label="Remove preset {v.name}"
                class="px-1.5 py-1 text-xs {selectedVariant === v.name ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <button
            type="button"
            title="Add a generation preset (steps / cfg / size), inheriting this model's paths"
            class="px-2.5 py-1 rounded text-xs font-semibold border border-dashed border-info text-info hover:bg-info hover:text-white transition-colors"
            onclick={addImageVariantEntry}>+ preset</button>
        </div>

        {#if selectedVariant === ""}
        <div class="grid grid-cols-2 gap-3">
          <div class="col-span-2 font-mono text-[0.6rem] uppercase tracking-wider text-txtsecondary">Component paths</div>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              VAE
              {@render hint("--vae. External VAE file (decodes the latent to pixels). A diffusion-only GGUF needs this supplied separately.")}
            </span>
            <input type="text" bind:value={vaePath} class="cfg-input" placeholder="e.g. /models/ae.safetensors" spellcheck="false" />
          </label>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Text encoder (LLM)
              {@render hint("--llm. Text-encoder model for Z-Image / Lumina-family diffusion (e.g. a Qwen3 GGUF). Use this OR the CLIP/T5 encoders below, per the model family.")}
            </span>
            <input type="text" bind:value={textEncoderPath} class="cfg-input" placeholder="e.g. /models/qwen3-4b-q8_0.gguf" spellcheck="false" />
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              CLIP-L
              {@render hint("--clip_l. CLIP-L text encoder (SD/SDXL/Flux).")}
            </span>
            <input type="text" bind:value={clipLPath} class="cfg-input" placeholder="clip_l.safetensors" spellcheck="false" />
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              CLIP-G
              {@render hint("--clip_g. CLIP-G text encoder (SDXL).")}
            </span>
            <input type="text" bind:value={clipGPath} class="cfg-input" placeholder="clip_g.safetensors" spellcheck="false" />
          </label>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              T5-XXL
              {@render hint("--t5xxl. T5-XXL text encoder (Flux / SD3).")}
            </span>
            <input type="text" bind:value={t5Path} class="cfg-input" placeholder="t5xxl.safetensors" spellcheck="false" />
          </label>

          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Target VRAM
              {@render hint("How much VRAM to size this model against (--max-vram). Auto = the global target. sd.cpp graph-cuts to fit it; lower it to leave headroom for other apps.")}
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

          <div class="col-span-2 font-mono text-[0.6rem] uppercase tracking-wider text-txtsecondary mt-1">Generation defaults</div>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Steps
              {@render hint("--steps. Default sampling steps when a request omits it. Turbo/LCM models need few (e.g. 8); standard models 20-30. Empty = sd-server default.")}
            </span>
            <input type="number" min="0" step="1" bind:value={defaultSteps} use:wheelAdjust class="cfg-input" placeholder="default" />
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              CFG scale
              {@render hint("--cfg-scale. Prompt-adherence strength. Turbo/distilled models REQUIRE 1.0 (higher blurs output); standard models ~7. Empty = sd-server default.")}
            </span>
            <input type="number" min="0" step="0.5" bind:value={defaultCfg} use:wheelAdjust class="cfg-input" placeholder="default" />
          </label>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Sampler
              {@render hint("--sampling-method. Default sampling method when a request omits it. Empty = sd-server default.")}
            </span>
            <select bind:value={defaultSampler} class="cfg-input">
              {#each IMG_SAMPLERS as o}<option value={o}>{o === "" ? "default" : o}</option>{/each}
            </select>
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Default width
              {@render hint("--width. Default image width in px when a request omits it. Empty = sd-server default (512).")}
            </span>
            <input type="number" min="0" step="64" bind:value={defaultWidth} use:wheelAdjust class="cfg-input" placeholder="default" />
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Default height
              {@render hint("--height. Default image height in px when a request omits it. Empty = sd-server default (512).")}
            </span>
            <input type="number" min="0" step="64" bind:value={defaultHeight} use:wheelAdjust class="cfg-input" placeholder="default" />
          </label>

          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              CPU offload
              {@render hint("--offload-to-cpu. Page the diffusion weights to RAM (loaded to VRAM on use) to fit a tight budget. Auto = the sizer offloads when weights + compute don't fit the target.")}
            </span>
            <select bind:value={offloadToCpu} class="cfg-input">
              <option value="">auto (sizer decides)</option>
              <option value="on">on (force offload)</option>
              <option value="off">off (keep on GPU)</option>
            </select>
          </label>

          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Threads
              {@render hint("-t. CPU threads. Empty = the global default. Matters for the CPU-side text encoder / offloaded weights.")}
            </span>
            <input type="number" min="0" step="1" bind:value={threads} use:wheelAdjust class="cfg-input" placeholder="global default" />
          </label>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Aliases (comma-separated)
              {@render hint("Extra names this model answers to in the /v1/models API (e.g. map dall-e-3 to this model).")}
            </span>
            <input type="text" bind:value={aliasesText} class="cfg-input" placeholder="e.g. dall-e-3, gpt-image-1" />
          </label>
        </div>

        <!-- Toggles: the on/off knobs, grouped at the bottom (mirrors the LLM tab). -->
        <div>
          <div class="font-mono text-[0.6rem] uppercase tracking-wider text-txtsecondary mb-2">Toggles</div>
          <div class="grid grid-cols-2 gap-x-4 gap-y-2">
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={teOnCpu !== "off"} onchange={(e) => (teOnCpu = (e.currentTarget as HTMLInputElement).checked ? "" : "off")} />
              <span class="text-txtsecondary flex items-center gap-1">
                Text encoder on CPU
                {@render hint("--backend te=cpu. Run the text encoder on the CPU (on by default). It runs once per generation, so it's the cheapest component to keep off the GPU. Turn off only if you have VRAM headroom.")}
              </span>
            </label>
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={vaeTiling !== "off"} onchange={(e) => (vaeTiling = (e.currentTarget as HTMLInputElement).checked ? "" : "off")} />
              <span class="text-txtsecondary flex items-center gap-1">
                VAE tiling
                {@render hint("--vae-tiling. Tile the VAE decode to cap its VRAM spike (on by default). Decoding a full latent whole can OOM on a tight card. Quality is steps/cfg, not this.")}
              </span>
            </label>
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={diffusionFa !== "off"} onchange={(e) => (diffusionFa = (e.currentTarget as HTMLInputElement).checked ? "" : "off")} />
              <span class="text-txtsecondary flex items-center gap-1">
                Diffusion flash-attn
                {@render hint("--diffusion-fa. Flash attention for the diffusion model (on by default). Near-free VRAM saver.")}
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

        <!-- Launch command (read-only preview; re-renders live from the fields). The
             image cmd has no two-way parse-back — use the fields above + extraArgs. -->
        <details class="group">
          <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
            Launch parameters {config.hasOverride ? "(custom)" : "(autogen default)"}
          </summary>
          <textarea
            value={cmdDraft}
            readonly
            spellcheck="false"
            rows="6"
            class="mt-2 w-full bg-background rounded border border-card-border p-3 text-xs font-mono whitespace-pre-wrap break-all resize-y text-txtmain opacity-90"
          ></textarea>
          <p class="text-xs text-txtsecondary mt-1">
            Re-renders from the fields above. For flags not modelled here, add them in <code>extraArgs</code> via the generate file.
          </p>
          <p class="text-xs text-txtsecondary mt-1 font-mono break-all">{config.gguf}</p>
        </details>
        {:else if selectedV}
          {@const sv = selectedV}
          <p class="text-xs text-txtsecondary -mt-1">
            Editing preset <span class="font-mono text-txtmain">{config.id}-{sv.name || "(unnamed)"}</span>.
            Inherits this model's component paths + placement; set only what differs.
          </p>
          <div class="grid grid-cols-2 gap-3">
            <label class="flex flex-col gap-1 text-sm col-span-2">
              <span class="text-txtsecondary flex items-center gap-1">
                Name (suffix)
                {@render hint("The preset's id suffix. Loads as <base-id>-<name>.")}
              </span>
              <input type="text" value={sv.name} oninput={renameSelectedVariant} class="cfg-input" placeholder="e.g. fast, quality, hd" />
            </label>

            <div class="col-span-2 font-mono text-[0.6rem] uppercase tracking-wider text-txtsecondary mt-1">Generation defaults</div>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">Steps{@render hint("--steps for this preset. Empty / 0 = inherit the model default.")}</span>
              <input type="number" min="0" step="1" bind:value={sv.defaultSteps} use:wheelAdjust class="cfg-input" placeholder="inherit" />
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">CFG scale{@render hint("--cfg-scale for this preset. Empty / 0 = inherit. Turbo/distilled need 1.0.")}</span>
              <input type="number" min="0" step="0.5" bind:value={sv.defaultCfg} use:wheelAdjust class="cfg-input" placeholder="inherit" />
            </label>
            <label class="flex flex-col gap-1 text-sm col-span-2">
              <span class="text-txtsecondary flex items-center gap-1">Sampler{@render hint("--sampling-method for this preset. 'inherit' = the model default.")}</span>
              <select bind:value={sv.defaultSampler} class="cfg-input">
                {#each IMG_SAMPLERS as o}<option value={o}>{o === "" ? "inherit" : o}</option>{/each}
              </select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">Width{@render hint("--width for this preset. Empty / 0 = inherit.")}</span>
              <input type="number" min="0" step="64" bind:value={sv.defaultWidth} use:wheelAdjust class="cfg-input" placeholder="inherit" />
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">Height{@render hint("--height for this preset. Empty / 0 = inherit.")}</span>
              <input type="number" min="0" step="64" bind:value={sv.defaultHeight} use:wheelAdjust class="cfg-input" placeholder="inherit" />
            </label>

            <label class="flex flex-col gap-1 text-sm col-span-2">
              <span class="text-txtsecondary flex items-center gap-1">
                Target VRAM
                {@render hint("Size this preset against this VRAM budget (--max-vram). Auto = inherit the model/global target.")}
                <span class="ml-auto font-mono text-txtmain">{sv.vramTargetGB ? `${Number(sv.vramTargetGB).toFixed(1)} GB` : "inherit"}</span>
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
                CPU offload
                {@render hint("--offload-to-cpu for this preset. Inherit = use the model's setting.")}
              </span>
              <select bind:value={sv.offloadToCpu} class="cfg-input">
                <option value="">inherit</option>
                <option value="on">on (force offload)</option>
                <option value="off">off (keep on GPU)</option>
              </select>
            </label>
            <label class="flex flex-col gap-1 text-sm col-span-2">
              <span class="text-txtsecondary flex items-center gap-1">
                Aliases (comma-separated)
                {@render hint("Extra names this preset answers to in the /v1/models API.")}
              </span>
              <input type="text" value={(sv.aliases ?? []).join(", ")} oninput={setVAliases} class="cfg-input" placeholder="e.g. dall-e-3-hd" />
            </label>
            <label class="flex items-center gap-2 text-sm col-span-2">
              <input type="checkbox" bind:checked={sv.unlisted} />
              <span class="text-txtsecondary flex items-center gap-1">
                Unlisted
                {@render hint("Hide this preset from /v1/models, but keep it loadable by exact id.")}
              </span>
            </label>
          </div>

          <details class="group">
            <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
              Launch parameters (preset)
            </summary>
            <textarea value={cmdDraft} readonly spellcheck="false" rows="6" class="mt-2 w-full bg-background rounded border border-card-border p-3 text-xs font-mono whitespace-pre-wrap break-all resize-y text-txtmain opacity-90"></textarea>
            <p class="text-xs text-txtsecondary mt-1">Re-renders from the fields above; empty fields inherit the model default.</p>
          </details>
        {/if}
        {:else}
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
                class="px-1.5 py-1 text-xs {selectedVariant === v.name ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <!-- Ctx tiers (32k/64k…): per-model, removable like variants. -->
          {#each ctxTiers as v (v.name)}
            <span
              class="inline-flex items-center rounded border overflow-hidden {selectedVariant === v.name
                ? 'border-primary'
                : 'border-card-border'}"
            >
              <button
                type="button"
                title="Context tier"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedVariant === v.name
                  ? 'bg-primary text-white'
                  : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedVariant = v.name)}>{v.name || "(unnamed)"}</button>
              <button
                type="button"
                title="Remove ctx tier"
                aria-label="Remove ctx tier {v.name}"
                class="px-1.5 py-1 text-xs {selectedVariant === v.name ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <!-- Fleet-wide default variants (e.g. game): shared by every model.
               Adding/removing/editing one writes them globally on save. -->
          {#each defaultVariants as v (v.name)}
            <span
              class="inline-flex items-center rounded border overflow-hidden {selectedVariant === v.name
                ? 'border-primary'
                : 'border-card-border'}"
            >
              <button
                type="button"
                title="Fleet-wide variant (shared by all models)"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedVariant === v.name
                  ? 'bg-primary text-white'
                  : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedVariant = v.name)}>{v.name || "(unnamed)"} <span class="opacity-60">⊕</span></button>
              <button
                type="button"
                title="Remove fleet-wide variant"
                aria-label="Remove fleet-wide variant {v.name}"
                class="px-1.5 py-1 text-xs {selectedVariant === v.name ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <button
            type="button"
            title="Add a per-model variant (only this model)"
            class="px-2.5 py-1 rounded text-xs font-semibold border border-dashed border-info text-info hover:bg-info hover:text-white transition-colors"
            onclick={addVariantEntry}>+ variant</button>
          <button
            type="button"
            title="Add a fleet-wide variant shared by every model (e.g. a 32k ctx tier or a low-VRAM coexistence variant)"
            class="px-2.5 py-1 rounded text-xs font-semibold border border-dashed border-success text-success hover:bg-success hover:text-white transition-colors"
            onclick={addDefaultVariantEntry}>+ fleet variant ⊕</button>
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

          <div class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Speculative
              {@render hint("Speculative decoding backends. Chainable (e.g. draft-mtp + ngram-map-k4v). None checked = generator default; draft-mtp needs a model with MTP layers.")}
            </span>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
              {#each specBackends as b}
                <label class="flex items-center gap-1"><input type="checkbox" checked={effSpecs.includes(b)} onchange={(e) => (spec = specToggle(spec, b, e.currentTarget.checked))} />{b}</label>
              {/each}
              <label class="flex items-center gap-1"><input type="checkbox" checked={specHas(spec, "none")} onchange={(e) => (spec = specToggle(spec, "none", e.currentTarget.checked))} />none</label>
            </div>
          </div>

          {#if effSpecs.includes("draft-mtp")}
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Draft n-max
                {@render hint("--spec-draft-n-max. Max draft tokens proposed per step (draft-mtp). Empty = 2.")}
              </span>
              <input type="number" min="0" step="1" bind:value={specDraftNMax} use:wheelAdjust class="cfg-input" placeholder="2" />
            </label>
          {/if}
          {#if effSpecs.includes("ngram-map-k4v")}
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                ngram size-n / size-m
                {@render hint("--spec-ngram-map-k4v-size-n / -size-m. ngram map dimensions. Empty = llama-server default.")}
              </span>
              <div class="flex items-end gap-2">
                <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">size-n<input type="number" min="0" step="1" bind:value={specNgramSizeN} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="n" /></span>
                <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">size-m<input type="number" min="0" step="1" bind:value={specNgramSizeM} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="m" /></span>
              </div>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                ngram min-hits
                {@render hint("--spec-ngram-map-k4v-min-hits. Min ngram hits before drafting. Empty = default.")}
              </span>
              <input type="number" min="0" step="1" bind:value={specNgramMinHits} use:wheelAdjust class="cfg-input" placeholder="default" />
            </label>
            <label class="flex items-center gap-2 text-sm self-end">
              <input type="checkbox" bind:checked={specDefault} />
              <span class="text-txtsecondary flex items-center gap-1">
                spec-default
                {@render hint("--spec-default. Apply llama-server's built-in default speculative parameters.")}
              </span>
            </label>
          {/if}

          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              <input type="checkbox" bind:checked={dryOn} />
              DRY sampler
              {@render hint("--dry-* repetition penalty. On by default. Multiplier / base / allowed-length: empty = 0.8 / 1.75 / 3.")}
            </span>
            {#if dryOn}
              <div class="flex items-end gap-2">
                <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">multiplier<input type="number" min="0" step="0.05" bind:value={dryMultiplier} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="0.8" /></span>
                <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">base<input type="number" min="0" step="0.05" bind:value={dryBase} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="1.75" /></span>
                <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">allowed-len<input type="number" min="0" step="1" bind:value={dryAllowedLength} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="3" /></span>
              </div>
            {/if}
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

          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Context checkpoints
              {@render hint("--ctx-checkpoints. KV snapshots kept to restore a diverging prompt instead of reprocessing. Auto = the sizer's pick (llama default 32). 0 disables and reserves no checkpoint VRAM. Variants inherit this unless they set their own.")}
            </span>
            <div class="flex items-center gap-2">
              <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                <input type="checkbox" checked={ctxCheckpoints == null} onchange={(e) => (ctxCheckpoints = (e.currentTarget as HTMLInputElement).checked ? null : 0)} /> Auto
              </label>
              {#if ctxCheckpoints != null}
                <input type="number" min="0" step="1" bind:value={ctxCheckpoints} use:wheelAdjust class="cfg-input flex-1" />
              {/if}
            </div>
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
            <label class="flex items-center gap-2 text-sm" class:opacity-40={!reasoningOn}>
              <input type="checkbox" bind:checked={preserveThinking} disabled={!reasoningOn} />
              <span class="text-txtsecondary flex items-center gap-1">
                Preserve thinking
                {@render hint("Keep prior-turn <think> blocks in chat history instead of stripping them (Qwen3.6+ via --chat-template-kwargs preserve_thinking). Avoids reasoning amnesia in multi-turn/agentic loops. Needs reasoning on, and the client must send reasoning_content back.")}
              </span>
            </label>
            <label class="flex items-center gap-2 text-sm" class:opacity-40={!reasoningOn}>
              <span class="text-txtsecondary flex items-center gap-1">
                Reasoning budget
                {@render hint("--reasoning-budget. Max thinking tokens before the model is forced to answer. Empty = no cap. Needs reasoning on.")}
              </span>
              <input type="number" min="0" step="1000" bind:value={reasoningBudget} use:wheelAdjust disabled={!reasoningOn} class="cfg-input w-24 ml-auto" placeholder="none" />
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
            Editing variant <span class="font-mono text-txtmain">{config.id}{config.id.endsWith(`-${sv.name}`) ? "" : `-${sv.name || "(unnamed)"}`}</span>.
            Anything left unset inherits from <button type="button" class="underline hover:text-txtmain" onclick={() => (selectedVariant = "")}>Default</button>.
          </p>
          {#if selectedIsDefault}
            <p class="text-xs text-warning bg-warning/10 border border-warning/30 rounded px-2 py-1.5">
              ⚠ Fleet-wide variant — shared by <strong>every</strong> model. Saving rewrites it globally, not just for {config.id}.
            </p>
          {/if}
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
              <select bind:value={sv.kvK} class="cfg-input">{#each KV_OPTS as o}<option value={o}>{o === "" ? "inherit" : o}</option>{/each}</select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">KV cache V</span>
              <select bind:value={sv.kvV} class="cfg-input">{#each KV_OPTS as o}<option value={o}>{o === "" ? "inherit" : o}</option>{/each}</select>
            </label>

            <div class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary">Speculative</span>
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                {#each specBackends as b}
                  <label class="flex items-center gap-1"><input type="checkbox" checked={vEffSpecs.includes(b)} onchange={(e) => (sv.spec = specToggle(sv.spec, b, e.currentTarget.checked))} />{b}</label>
                {/each}
                <label class="flex items-center gap-1"><input type="checkbox" checked={specHas(sv.spec, "none")} onchange={(e) => (sv.spec = specToggle(sv.spec, "none", e.currentTarget.checked))} />none</label>
              </div>
            </div>

            {#if vEffSpecs.includes("draft-mtp")}
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-txtsecondary flex items-center gap-1">
                  Draft n-max
                  {@render hint("--spec-draft-n-max for this variant. Empty / 0 = inherit (2).")}
                </span>
                <input type="number" min="0" step="1" bind:value={sv.specDraftNMax} use:wheelAdjust class="cfg-input" placeholder="inherit" />
              </label>
            {/if}
            {#if vEffSpecs.includes("ngram-map-k4v")}
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-txtsecondary flex items-center gap-1">
                  ngram size-n / size-m
                  {@render hint("--spec-ngram-map-k4v-size-n / -size-m for this variant. Empty / 0 = inherit.")}
                </span>
                <div class="flex items-end gap-2">
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">size-n<input type="number" min="0" step="1" bind:value={sv.specNgramSizeN} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="n" /></span>
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">size-m<input type="number" min="0" step="1" bind:value={sv.specNgramSizeM} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="m" /></span>
                </div>
              </label>
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-txtsecondary flex items-center gap-1">
                  ngram min-hits
                  {@render hint("--spec-ngram-map-k4v-min-hits for this variant. Empty / 0 = inherit.")}
                </span>
                <input type="number" min="0" step="1" bind:value={sv.specNgramMinHits} use:wheelAdjust class="cfg-input" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2 text-sm self-end">
                <input type="checkbox" bind:checked={sv.specDefault} />
                <span class="text-txtsecondary flex items-center gap-1">
                  spec-default
                  {@render hint("--spec-default for this variant.")}
                </span>
              </label>
            {/if}
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
              {#if vDryValue() !== "off"}
                <div class="flex items-end gap-2">
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">multiplier<input type="number" min="0" step="0.05" bind:value={sv.dryMultiplier} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="0.8" /></span>
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">base<input type="number" min="0" step="0.05" bind:value={sv.dryBase} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="1.75" /></span>
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">allowed-len<input type="number" min="0" step="1" bind:value={sv.dryAllowedLength} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="3" /></span>
                </div>
              {/if}
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
                <input type="checkbox" checked={sv.preserveThinking !== false} disabled={sv.reasoningFmt === "off"} onchange={(e) => (sv.preserveThinking = (e.currentTarget as HTMLInputElement).checked ? null : false)} />
                <span class="text-txtsecondary flex items-center gap-1">
                  Preserve thinking
                  {@render hint("Keep prior-turn <think> blocks in chat history (Qwen3.6+). On by default for this variant; needs reasoning on.")}
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
      {/if}
    </div>

    <div class="p-4 border-t border-card-border flex justify-between items-center">
      <button onclick={reset} class="btn btn--sm" disabled={saving || !config?.hasOverride}>Reset to default</button>
      <div class="flex gap-2">
        <button onclick={() => dialogEl?.close()} class="btn btn--sm">Cancel</button>
        <button onclick={save} class="btn btn--sm btn--primary !text-white" disabled={saving || loading}>
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
