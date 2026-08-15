<script lang="ts">
  import {
    getModelConfig,
    putModelOverride,
    putModelDisplayName,
    deleteModelDisplayName,
    putDefaultVariants,
    resetModelOverride,
    estimatePlan,
    previewCmd,
    getSettings,
    pickFileOfKind,
    type ModelConfig,
    type ModelOverride,
    type ModelVariant,
    type PlanEstimate,
    models,
  } from "../stores/api";
  import { get } from "svelte/store";
  import { FolderOpen } from "lucide-svelte";
  import VramGauge from "./VramGauge.svelte";
  import { estimateSegments } from "../stores/vram";
  import { backendClass, engineLabel } from "../lib/backends";
  import {
    IMG_SAMPLERS,
    fmtCtx,
    genDefaultKv,
    genDefaultSpec,
    hoistChatTemplate,
    nglDisplay,
    noNoMmap,
    parseCmdFields,
    parseCtx,
    parseImageCmdFields,
    specHas,
    specToggle,
    type ParsedCmd,
    type ParsedImg,
  } from "./modelCmdForm";

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
  let saved = $state(false);
  let error = $state<string | null>(null);
  let savedTimer: ReturnType<typeof setTimeout> | undefined;
  let config = $state<ModelConfig | null>(null);

  // Click-to-edit display name (advertised alias -> real id; cascades to variants).
  let editingName = $state(false);
  let nameDraft = $state("");
  // Focus on mount instead of the `autofocus` attribute (a11y_autofocus): the
  // input only exists while editing, so mounting is the moment to focus.
  function focusOnMount(node: HTMLInputElement) {
    node.focus();
    node.select();
  }
  async function commitName() {
    if (!modelId || !config) return;
    editingName = false;
    const next = nameDraft.trim();
    const cur = config.displayName ?? "";
    if (next === cur) return;
    saving = true;
    error = null;
    try {
      if (next === "") await deleteModelDisplayName(modelId);
      else await putModelDisplayName(modelId, next);
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

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
  // mmap inherit default: a blank ("") mmap override inherits the sizer's
  // placement choice, which shows as --no-mmap in the base launch args for
  // GPU-resident / expert-offloaded models. Used to render blank mmap checkboxes
  // (Default + variant tabs) truthfully instead of always-on.
  // ponytail: base cmd is the best available proxy — per-variant cmds aren't
  // fetched, so a variant with different offload than base can read stale.
  const mmapInheritOn = $derived(!/(?:^|\s)--no-mmap(?:\s|$)/.test(config?.cmd ?? ""));
  // Per-variant launch cmds, keyed by variant name, fetched at load() (each
  // variant is its own served id "<base>-<name>"). Lets a blank variant mmap
  // checkbox read that variant's own placement default instead of the base's.
  let variantCmds = $state<Record<string, string>>({});
  // True when a variant's mmap checkbox should show checked: explicit override
  // wins; blank inherits from the variant's own cmd, falling back to the base
  // until that fetch lands.
  function variantMmapOn(v: ModelVariant): boolean {
    if (v.mmap) return v.mmap !== "off";
    const c = variantCmds[v.name];
    return c != null ? noNoMmap(c) : mmapInheritOn;
  }
  let mlock = $state(false);
  let threads = $state<number | "">(""); // "" = global default
  let parallel = $state<number | "">(""); // "" = 1
  let ub = $state<number | "">(""); // "" = auto physical batch
  // DRY sampler (Default). dryOn drives on/off; values "" => generator default
  // (0.8 / 1.75 / 3).
  let dryOn = $state(false);
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
  // Advanced / power-user knobs (Default tab), one object to keep load/save compact.
  // Numeric fields hold "" (=inherit/omit) or a number; tri-states hold ""/"on"/"off".
  type AdvKnobs = {
    threadsBatch: number | ""; prio: number | ""; directIo: boolean; noOpOffload: boolean; noRepack: boolean;
    kvKDraft: string; kvVDraft: string; cacheReuse: number | ""; cacheRamMB: number | ""; cacheIdleSlots: string;
    swaFull: boolean; checkpointMinStep: number | ""; contextShift: string; specDraftNMin: number | "";
    slotPromptSimilarity: number | ""; ropeScaling: string; ropeScale: number | ""; ropeFreqBase: number | "";
    yarnOrigCtx: number | ""; splitMode: string; tensorSplit: string; mainGpu: number | ""; overrideTensor: string;
    chatTemplateFile: string;
  };
  function blankAdv(): AdvKnobs {
    return {
      threadsBatch: "", prio: "", directIo: false, noOpOffload: false, noRepack: false,
      kvKDraft: "", kvVDraft: "", cacheReuse: "", cacheRamMB: "", cacheIdleSlots: "",
      swaFull: false, checkpointMinStep: "", contextShift: "", specDraftNMin: "",
      slotPromptSimilarity: "", ropeScaling: "", ropeScale: "", ropeFreqBase: "",
      yarnOrigCtx: "", splitMode: "", tensorSplit: "", mainGpu: "", overrideTensor: "",
      chatTemplateFile: "",
    };
  }
  let adv = $state<AdvKnobs>(blankAdv());
  // adv object <-> ModelOverride's advanced fields.
  function advFromOverride(o: ModelOverride | null): AdvKnobs {
    return {
      threadsBatch: o?.threadsBatch || "", prio: o?.prio || "", directIo: o?.directIo ?? false,
      noOpOffload: o?.noOpOffload ?? false, noRepack: o?.noRepack ?? false,
      kvKDraft: o?.kvKDraft ?? "", kvVDraft: o?.kvVDraft ?? "",
      cacheReuse: o?.cacheReuse || "", cacheRamMB: o?.cacheRamMB || "", cacheIdleSlots: o?.cacheIdleSlots ?? "",
      swaFull: o?.swaFull ?? false, checkpointMinStep: o?.checkpointMinStep || "", contextShift: o?.contextShift ?? "",
      specDraftNMin: o?.specDraftNMin || "", slotPromptSimilarity: o?.slotPromptSimilarity || "",
      ropeScaling: o?.ropeScaling ?? "", ropeScale: o?.ropeScale || "", ropeFreqBase: o?.ropeFreqBase || "",
      yarnOrigCtx: o?.yarnOrigCtx || "", splitMode: o?.splitMode ?? "", tensorSplit: o?.tensorSplit ?? "",
      mainGpu: o?.mainGpu || "", overrideTensor: o?.overrideTensor ?? "",
      chatTemplateFile: o?.chatTemplateFile ?? "",
    };
  }
  function advToOverride(): Partial<ModelOverride> {
    const n = (v: number | "") => (v === "" ? 0 : Number(v));
    return {
      threadsBatch: n(adv.threadsBatch), prio: n(adv.prio), directIo: adv.directIo,
      noOpOffload: adv.noOpOffload, noRepack: adv.noRepack, kvKDraft: adv.kvKDraft, kvVDraft: adv.kvVDraft,
      cacheReuse: n(adv.cacheReuse), cacheRamMB: n(adv.cacheRamMB), cacheIdleSlots: adv.cacheIdleSlots,
      swaFull: adv.swaFull, checkpointMinStep: n(adv.checkpointMinStep), contextShift: adv.contextShift,
      specDraftNMin: n(adv.specDraftNMin), slotPromptSimilarity: n(adv.slotPromptSimilarity),
      ropeScaling: adv.ropeScaling, ropeScale: n(adv.ropeScale), ropeFreqBase: n(adv.ropeFreqBase),
      yarnOrigCtx: n(adv.yarnOrigCtx), splitMode: adv.splitMode, tensorSplit: adv.tensorSplit,
      mainGpu: n(adv.mainGpu), overrideTensor: adv.overrideTensor,
      chatTemplateFile: adv.chatTemplateFile.trim(),
    };
  }
  // Native open-file dialog for the chat-template path (the dialog opens on the
  // server host — the operator's own machine for a local install). Returns null
  // on cancel or on a platform with no picker, leaving the text field alone.
  async function browseChatTemplate(apply: (path: string) => void): Promise<void> {
    try {
      const picked = await pickFileOfKind("template");
      if (picked) apply(picked);
    } catch {
      // picker failed — the field stays typable, no need to nag
    }
  }

  let unlisted = $state(false);
  let skip = $state(false);
  // Opt this model into on-disk slot KV persistence (--slot-save-path). On by
  // default so the global slot-cache toggle (Dashboard) alone enables it; only
  // takes effect when that master switch is also on. Uncheck to opt this model out.
  let slotCacheOn = $state(true);
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

  // --- Backend selection (per-model) ---
  // backend = the registry entry id ("" => auto-pick the class default). vllm
  // knobs apply only when the selected backend's kind is "vllm".
  let backend = $state("");
  let vllmGpuUtil = $state<number | "">("");
  let vllmTensorParallel = $state<number | "">("");
  let vllmTokenizer = $state("");

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
  let vaeOnCpu = $state(""); // "" gpu (default) | "on" cpu
  let vaeTiling = $state(""); // "" on | "off"
  let diffusionFa = $state(""); // "" on | "off"
  // Generation defaults baked into the launch cmd; "" => sd-server default.
  let defaultSteps = $state<number | "">("");
  let defaultCfg = $state<number | "">("");
  let defaultSampler = $state("");
  let defaultWidth = $state<number | "">("");
  let defaultHeight = $state<number | "">("");

  const imageMode = $derived(config?.isImage ?? false);
  // Qwen3-TTS talker (tts-server): minimal form, no KV/ctx/spec/estimate. The
  // talker + codec are tiny and fully resident; voice/temperature are per-request.
  const audioMode = $derived(config?.isAudio ?? false);
  // SAM segmentation (sam3_server): minimal form, no KV/ctx/spec/estimate. Only
  // launch knobs are the backend pick + extraArgs + listing toggles; box/point
  // prompts are per-request (/v1/segment). CPU vs GPU is auto (VRAM-aware) or
  // pinned via extraArgs --no-gpu.
  const samMode = $derived(config?.isSam ?? false);
  // Which speech engine this model runs on. The two are not interchangeable
  // (TTS.cpp reads Kokoro/Parler/Orpheus ggufs, qwentts.cpp reads a talker +
  // paired codec), so the form blurb must not claim the wrong one.
  const ttscppMode = $derived(audioMode && /(^|\s)--model-path(\s|=)/.test(config?.cmd ?? ""));

  // Backends compatible with this model, the selected one's kind, and whether it
  // is vllm (drives which knobs apply). Empty registry => no picker shown.
  // The server names the class (it knows which emitter ran); the form flags are
  // only a fallback for an older backend, and they can't tell TTS from ASR.
  const modelClass = $derived(
    config?.class || (imageMode ? "image" : audioMode ? "tts" : samMode ? "segment" : "llm"),
  );
  const classBackends = $derived((config?.backends ?? []).filter((b) => backendClass(b.kind) === modelClass));
  const selectedKind = $derived(classBackends.find((b) => b.id === backend)?.kind ?? "");
  const isVllm = $derived(selectedKind === "vllm");


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
    adv.chatTemplateFile = p.chatTemplateFile;
  }

  function applyImageParsedToDefault(p: ParsedImg) {
    vaePath = p.vaePath; clipLPath = p.clipLPath; clipGPath = p.clipGPath;
    t5Path = p.t5Path; textEncoderPath = p.textEncoderPath;
    offloadToCpu = p.offloadToCpu; teOnCpu = p.teOnCpu; vaeOnCpu = p.vaeOnCpu;
    vaeTiling = p.vaeTiling; diffusionFa = p.diffusionFa;
    defaultSteps = p.defaultSteps; defaultCfg = p.defaultCfg; defaultSampler = p.defaultSampler;
    defaultWidth = p.defaultWidth; defaultHeight = p.defaultHeight;
    threads = p.threads;
    extraArgs = p.extraArgs;
  }

  // Apply parsed flags to the selected variant (string on/off knobs mirror the
  // override encoding: "" = inherit/on, "off" = forced off).
  function applyParsedToVariant(v: ModelVariant, p: ParsedCmd) {
    // A variant is standalone, so the box renders generator-default base + the
    // variant's fields. Model-specific flags (-ngl/-c/--n-cpu-moe/-m...) are
    // skipped by IGNORE_VALUE, so nothing model-bound leaks. The only fields that
    // would wrongly bake into a fleet-wide variant are kv and spec at their
    // generator defaults (the model's own kv / draft-mtp|draft-dflash|ngram-mod): capture those
    // as a delta vs the generator default so an unchanged value stays "inherit" ("").
    const genSpec = genDefaultSpec(config);
    v.flashAttn = p.flashOn ? "" : "off";
    // mmap is tri-state ("" inherits the placement default, which is --no-mmap for
    // fully-offloaded models). The checkbox is binary, so checked forces "on" to
    // stay authoritative over that default. See save handler + inline variant editor.
    v.mmap = p.mmapOn ? "on" : "off";
    v.mlock = p.mlock;
    v.kvInRam = p.kvInRam;
    v.reasoningFmt = p.reasoningOn ? "" : "off";
    const genKv = genDefaultKv(config);
    v.kvK = p.kvK === genKv ? "" : p.kvK;
    v.kvV = p.kvV === genKv ? "" : p.kvV;
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
    // The variant box renders the inherited model-wide template too — capture it
    // as a delta so an untouched value stays "inherit" ("") instead of pinning.
    v.chatTemplateFile = p.chatTemplateFile === adv.chatTemplateFile.trim() ? "" : p.chatTemplateFile;
  }

  function onCmdInput(e: Event) {
    // Local-only while typing; the form fields (and thus the render effect) are
    // untouched, so the box isn't overwritten mid-keystroke.
    cmdDraft = (e.currentTarget as HTMLTextAreaElement).value;
  }
  function onCmdBlur() {
    // On blur, fold the edited command back into the active entry. Field changes
    // trigger the render effect, which re-renders the canonical command.
    if (imageMode) {
      // Image box is Default-only (no variant box), sd-server flag set.
      applyImageParsedToDefault(parseImageCmdFields(cmdDraft));
      return;
    }
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
      vaePath, clipLPath, clipGPath, t5Path, textEncoderPath, offloadToCpu, teOnCpu, vaeOnCpu, vaeTiling, diffusionFa,
      defaultSteps, defaultCfg, defaultSampler, defaultWidth, defaultHeight,
      selectedV?.ctx, selectedV?.kvK, selectedV?.kvV, selectedV?.kvInRam, selectedV?.spec,
      selectedV?.reasoningFmt, selectedV?.flashAttn, selectedV?.mmap, selectedV?.mlock,
      selectedV?.threads, selectedV?.parallel, selectedV?.ub, selectedV?.vramTargetGB,
      selectedV?.cpuOffload, selectedV?.ctxCheckpoints, selectedV?.dry, selectedV?.extraArgs, selectedV?.preserveThinking,
      selectedV?.dryMultiplier, selectedV?.dryBase, selectedV?.dryAllowedLength,
      selectedV?.specDraftNMax, selectedV?.specDefault, selectedV?.specNgramSizeN, selectedV?.specNgramSizeM, selectedV?.specNgramMinHits,
      // Advanced knobs (Default via adv, variant via selectedV) — deep-read so any
      // nested change re-renders the launch-command preview.
      JSON.stringify(adv), JSON.stringify(selectedV),
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
  // render. unlisted/variants stay variant-local (never inherited).
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
        vaeOnCpu: v.vaeOnCpu || base.vaeOnCpu,
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
      slotCache: v.slotCache ?? base.slotCache ?? true,
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
      // Advanced knobs: variant value wins, else inherit the base (Default tab).
      threadsBatch: v.threadsBatch || base.threadsBatch || 0,
      prio: v.prio || base.prio || 0,
      directIo: v.directIo ?? base.directIo ?? false,
      noOpOffload: v.noOpOffload ?? base.noOpOffload ?? false,
      noRepack: v.noRepack ?? base.noRepack ?? false,
      kvKDraft: v.kvKDraft || base.kvKDraft || "",
      kvVDraft: v.kvVDraft || base.kvVDraft || "",
      cacheReuse: v.cacheReuse || base.cacheReuse || 0,
      cacheRamMB: v.cacheRamMB || base.cacheRamMB || 0,
      cacheIdleSlots: v.cacheIdleSlots || base.cacheIdleSlots || "",
      swaFull: v.swaFull ?? base.swaFull ?? false,
      checkpointMinStep: v.checkpointMinStep || base.checkpointMinStep || 0,
      contextShift: v.contextShift || base.contextShift || "",
      specDraftNMin: v.specDraftNMin || base.specDraftNMin || 0,
      slotPromptSimilarity: v.slotPromptSimilarity || base.slotPromptSimilarity || 0,
      ropeScaling: v.ropeScaling || base.ropeScaling || "",
      ropeScale: v.ropeScale || base.ropeScale || 0,
      ropeFreqBase: v.ropeFreqBase || base.ropeFreqBase || 0,
      yarnOrigCtx: v.yarnOrigCtx || base.yarnOrigCtx || 0,
      splitMode: v.splitMode || base.splitMode || "",
      tensorSplit: v.tensorSplit || base.tensorSplit || "",
      mainGpu: v.mainGpu || base.mainGpu || 0,
      overrideTensor: v.overrideTensor || base.overrideTensor || "",
      chatTemplateFile: v.chatTemplateFile || base.chatTemplateFile || "",
      ctxCheckpoints: v.ctxCheckpoints ?? null,
      // variant-local: never inherited from the base.
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

  // Baseline of the memory-affecting fields as they were SEEDED (saved config).
  // The estimate pins the layer split to the running argv only while the form
  // still matches what's loaded; once any of these is edited the preview is a
  // what-if and must re-derive placement, or the pin freezes -ngl at whatever
  // the spawn guard picked and a ctx/kv change silently reports the old split
  // (e.g. "GPU 62/65" with 1.6GB of the budget unspent). Plain lets, not $state:
  // only runEstimate reads them, and making them reactive would re-trigger it.
  let memBaseline: string | null = null;
  let baselineVariant = "";

  // The exact set the re-estimate effect below watches, serialized. Anything
  // added to those deps that can change the load plan belongs here too.
  function memKey(): string {
    return JSON.stringify([
      ctx, ctxAuto, kvK, kvV, kvInRam, spec, vramTarget, vramAuto, cpuOffload, cpuAuto, ctxCheckpoints,
      // ub scales the compute buffer, -cms scales each checkpoint's KV term and
      // rope scaling decides the ctx ceiling: all three move the estimate.
      ub, adv.checkpointMinStep, adv.ropeScaling,
      selectedV?.ctx, selectedV?.kvK, selectedV?.kvV, selectedV?.spec,
      selectedV?.vramTargetGB, selectedV?.ub, selectedV?.ctxCheckpoints,
      selectedV?.kvInRam, selectedV?.cpuOffload,
      selectedV?.checkpointMinStep, selectedV?.ropeScaling,
    ]);
  }

  // Which entry the form edits: "" = the Default (base override, full field set);
  // a variant name = that variant (a subset of fields; the rest inherit Default).
  // Default is a pinned, non-deletable entry — opening any variant's gear lands
  // here with that variant selected.
  // Selection is keyed by object REFERENCE, not name: a variant's name can be
  // momentarily empty while the user retypes its suffix, and an empty name would
  // otherwise collide with the Default tab's "" sentinel and snap the editor back
  // to Default. null = Default tab.
  let selectedV = $state<ModelVariant | null>(null);
  const selectedVariant = $derived(selectedV?.name ?? "");
  // The selected tab is a fleet-wide default variant (game) => edits save globally.
  const selectedIsDefault = $derived(
    !!selectedV && defaultVariants.includes(selectedV),
  );

  // llama.cpp's kv_cache_types, minus iq4_nl (no flash-attention KV kernel).
  const KV_OPTS = ["", "f32", "f16", "bf16", "q8_0", "q5_1", "q5_0", "q4_1", "q4_0"];

  // Slider ceiling = trained context length (fallback 32k). Floor 4k.
  const CTX_MIN = 4096;
  const nativeCtx = $derived(config?.maxCtx && config.maxCtx > CTX_MIN ? config.maxCtx : 32768);
  // RoPE scaling is what makes a ctx past the trained length meaningful rather
  // than garbage, so it's the one knob that lifts the slider's ceiling (the Go
  // sizer applies the same rule and derives --rope-scale from the chosen ctx).
  // Cap at 4x: past that even YaRN degrades badly.
  const ropeOn = $derived(adv.ropeScaling === "yarn" || adv.ropeScaling === "linear");
  const maxCtx = $derived(ropeOn ? nativeCtx * 4 : nativeCtx);
  // Where the trained length sits on the (extended) slider track, as a %.
  const nativePct = $derived(
    maxCtx > nativeCtx ? ((nativeCtx - CTX_MIN) / (maxCtx - CTX_MIN)) * 100 : 100,
  );
  const ctxOverNative = $derived(!ctxAuto && ctx > nativeCtx);

  // Toggling extension off must also pull the ctx back under the ceiling — the
  // sizer would clamp it anyway, and a slider showing 256k on a 128k model that
  // launches at 128k is a lie.
  function toggleRope(on: boolean) {
    adv.ropeScaling = on ? "yarn" : "";
    if (!on) {
      adv.ropeScale = "";
      if (ctx > nativeCtx) ctx = nativeCtx;
    }
  }
  // Offload slider ceiling = transformer block count (fallback 64).
  const maxOffload = $derived(config?.blockCount && config.blockCount > 0 ? config.blockCount : 64);
  // VRAM slider ceiling = the global budget (fallback 24 GB until settings load).
  const maxVram = $derived(globalTargetGB > 0 ? globalTargetGB : 24);

  // Speculative backends are chainable (e.g. draft-mtp + ngram-map-k4v), so spec
  // is a "+"-joined list. draft-mtp/draft-dflash are only offered when the model
  // has a paired draft (MTP layers/sidecar, or a *-dflash-*.gguf sidecar).
  const specBackends = $derived([
    ...(config?.isMTP ? ["draft-mtp"] : []),
    ...(config?.isDflash ? ["draft-dflash"] : []),
    "ngram-mod",
    "ngram-map-k4v",
  ]);

  function activeSpecs(s: string | undefined): string[] {
    const raw = (s ?? "").split("+").filter(Boolean);
    if (raw.length === 0) return genDefaultSpec(config).split("+");
    if (raw.includes("none")) return [];
    return raw;
  }
  const effSpecs = $derived(activeSpecs(spec)); // Default tab
  // Variant tab: own spec list, else the generator default (standalone — does NOT
  // inherit the Default tab's spec).
  const vEffSpecs = $derived(activeSpecs(selectedV?.spec));

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
    // Fields are about to be (re)seeded from storage, so the next estimate
    // re-takes the baseline instead of reading the seed itself as an edit.
    memBaseline = null;
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
    backend = o?.backend ?? "";
    vllmGpuUtil = o?.vllmGpuUtil ? o.vllmGpuUtil : "";
    vllmTensorParallel = o?.vllmTensorParallel ? o.vllmTensorParallel : "";
    vllmTokenizer = o?.vllmTokenizer ?? "";
    reasoningOn = (o?.reasoningFmt ?? "") !== "off";
    reasoningBudget = o?.reasoningBudget ? o.reasoningBudget : "";
    preserveThinking = o?.preserveThinking ?? false;
    flashOn = (o?.flashAttn ?? "") !== "off";
    // mmap blank (inherit) => reflect the placement default the sizer actually
    // emitted (--no-mmap for GPU-resident/expert-offloaded models), read from the
    // launch args; an explicit on/off override wins over that.
    mmapOn =
      (o?.mmap ?? "") === "" ? mmapInheritOn : o?.mmap !== "off";
    mlock = o?.mlock ?? false;
    threads = o?.threads ? o.threads : "";
    parallel = o?.parallel ? o.parallel : "";
    ub = o?.ub ? o.ub : "";
    dryOn = o?.dry ?? false; // null/undefined => off (fleet default)
    dryMultiplier = o?.dryMultiplier ? o.dryMultiplier : "";
    dryBase = o?.dryBase ? o.dryBase : "";
    dryAllowedLength = o?.dryAllowedLength ? o.dryAllowedLength : "";
    specDraftNMax = o?.specDraftNMax ? o.specDraftNMax : "";
    specDefault = o?.specDefault ?? false;
    specNgramSizeN = o?.specNgramSizeN ? o.specNgramSizeN : "";
    specNgramSizeM = o?.specNgramSizeM ? o.specNgramSizeM : "";
    specNgramMinHits = o?.specNgramMinHits ? o.specNgramMinHits : "";
    adv = advFromOverride(o);
    extraArgs = o?.extraArgs ?? "";
    // A template set through extraArgs (qm-tools, hand-edited sidecar) belongs in
    // the advanced field — otherwise it renders nowhere in the form and the first
    // launch-box blur silently drops it.
    {
      const h = hoistChatTemplate(extraArgs);
      if (h.path) {
        extraArgs = h.extra;
        if (!adv.chatTemplateFile) adv.chatTemplateFile = h.path;
      }
    }
    unlisted = o?.unlisted ?? false;
    skip = o?.skip ?? false;
    slotCacheOn = o?.slotCache ?? true;
    ctxCheckpoints = o?.ctxCheckpoints ?? null;
    variants = (o?.variants ?? []).map((v) => {
      const c = { ...v };
      const h = hoistChatTemplate(c.extraArgs ?? "");
      if (h.path) {
        c.extraArgs = h.extra;
        if (!c.chatTemplateFile) c.chatTemplateFile = h.path;
      }
      return c;
    });
    ctxTiers = (o?.ctxVariants ?? []).map((n) => blankVariant(fmtCtx(n), n));
    // Image fields (no-op for llama models — left at "").
    vaePath = o?.vaePath ?? "";
    clipLPath = o?.clipLPath ?? "";
    clipGPath = o?.clipGPath ?? "";
    t5Path = o?.t5Path ?? "";
    textEncoderPath = o?.textEncoderPath ?? "";
    offloadToCpu = o?.offloadToCpu ?? "";
    teOnCpu = o?.teOnCpu ?? "";
    vaeOnCpu = o?.vaeOnCpu ?? "";
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
      reasoningFmt: "", unlisted: false, ctxCheckpoints: null, dry: null, preserveThinking: null,
      slotCache: null,
      kvInRam: false, cpuOffload: 0, flashAttn: "", mmap: "", mlock: false,
      threads: 0, parallel: 0, extraArgs: "", chatTemplateFile: "",
      dryMultiplier: 0, dryBase: 0, dryAllowedLength: 0,
      specDraftNMax: 0, specDefault: false, specNgramSizeN: 0, specNgramSizeM: 0, specNgramMinHits: 0,
      vaePath: "", clipLPath: "", clipGPath: "", t5Path: "", textEncoderPath: "",
      offloadToCpu: "", teOnCpu: "", vaeOnCpu: "", vaeTiling: "", diffusionFa: "",
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
    // Select the ARRAY's element, not the raw literal: $state wraps elements in a
    // proxy, so `selectedV = nv` never `===` the rendered tab and nothing lights up.
    selectedV = variants[variants.length - 1];
  }

  // True when a ctx tier carries nothing but its ctx value, so it round-trips as
  // a compact ctxVariants int instead of a full named variant.
  function ctxTierIsPure(v: ModelVariant): boolean {
    return (
      !v.vramTargetGB && !v.kvK && !v.kvV && !v.spec && !v.ub &&
      !v.reasoningFmt && !v.unlisted &&
      v.ctxCheckpoints == null && v.dry == null && v.preserveThinking == null && v.slotCache == null && !v.kvInRam && !v.cpuOffload &&
      !v.flashAttn && !v.mmap && !v.mlock && !v.threads && !v.parallel && !v.extraArgs && !v.chatTemplateFile &&
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
      // Vision twin: models shipping an mmproj projector get an auto-generated
      // "<id>-vision" profile. Surface it as an editable "vision" variant tab —
      // generate.go merges a "vision" variant back into that twin in place. Seed
      // a blank (listed, matching the auto default) when none is saved yet.
      if (get(models).some((m) => m.id === `${modelId}-vision`) && !variants.some((v) => v.name === "vision")) {
        const nv = blankVariant("vision", 0);
        variants = [...variants, nv];
      }
      // Land on the clicked row's variant: the model id ends with "-<name>" for
      // a variant/tier, or is the bare base for Default. Match the longest name so a
      // name that's a suffix of another doesn't win.
      let chosen = "";
      if (openForId) {
        for (const v of [...variants, ...ctxTiers, ...defaultVariants]) {
          if (openForId.endsWith("-" + v.name) && v.name.length > chosen.length) chosen = v.name;
        }
      }
      selectedV = chosen
        ? ([...variants, ...ctxTiers, ...defaultVariants].find((v) => v.name === chosen) ?? null)
        : null;
      // Fetch each variant's own launch cmd so blank mmap checkboxes reflect that
      // variant's placement (not the base's). Best-effort, parallel, non-blocking.
      const idSet = new Set(get(models).map((m) => m.id));
      const cmds: Record<string, string> = {};
      await Promise.all(
        [...variants, ...ctxTiers, ...defaultVariants].map(async (v) => {
          const vid = `${modelId}-${v.name}`;
          if (!idSet.has(vid)) return;
          try {
            cmds[v.name] = (await getModelConfig(vid)).cmd;
          } catch {
            /* leave blank => base fallback */
          }
        }),
      );
      variantCmds = cmds;
    } catch (e) {
      // Drop the previous model's config: `config` is what the whole body renders
      // from, so keeping it on a failed load showed the LAST model's settings under
      // this model's title — and a save then wrote those values to the wrong gguf.
      config = null;
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
      // ub scales the compute buffer, -cms scales each checkpoint's KV term and
      // rope scaling decides the ctx ceiling: all three move the estimate.
      ub, adv.checkpointMinStep, adv.ropeScaling,
      selectedV?.ctx, selectedV?.kvK, selectedV?.kvV, selectedV?.spec,
      selectedV?.vramTargetGB, selectedV?.ub, selectedV?.ctxCheckpoints,
      selectedV?.kvInRam, selectedV?.cpuOffload,
      selectedV?.checkpointMinStep, selectedV?.ropeScaling,
    ];
    void deps;
    // Diffusion/TTS/vllm sizing isn't modeled by the llama sizer; skip the estimate.
    if (!open || !config || !modelId || imageMode || audioMode || samMode || isVllm) return;
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
      // The model we actually estimate against. A variant with its OWN served model
      // (the reserved "vision" twin) estimates against that twin so its real cmd —
      // e.g. --mmproj — drives the plan (else mmprojGB is 0 and the bar drops the
      // Vision projector segment). Opening the base model's vision tab passes the
      // BASE id here, so `${modelId}-vision` resolves the twin; opening the config
      // from the dashboard passes the twin's OWN id as modelId, where that suffix
      // wouldn't exist (double "-vision") and we fall back to modelId (already the
      // twin). Both land on the twin id.
      const selV = selectedV;
      const estId =
        selV && get(models).some((m) => m.id === `${modelId}-${selV.name}`)
          ? `${modelId}-${selV.name}`
          : modelId;
      // Pin the GPU/CPU layer split to the ACTUAL running argv (post spawn-time
      // offload guard) when the model we're estimating (estId) is itself loaded, so
      // the preview matches the staging area instead of re-deriving a rosier -ngl
      // against the budget. Gate on estId — NOT modelId — so it fires whether the
      // config was opened from the dashboard (modelId already the twin) or the base
      // model's variant tab (estId resolves the twin). Suppressed only by a manual
      // cpu-offload (variant field or model-wide), which is a genuine what-if.
      const manualOffload = selectedV ? !!selectedV.cpuOffload || !cpuAuto : !cpuAuto;
      // Re-baseline on the first estimate after a seed and on every variant
      // switch (each tab carries its own saved fields), then treat any later
      // difference as an edit.
      const key = memKey();
      if (memBaseline === null || selectedVariant !== baselineVariant) {
        memBaseline = key;
        baselineVariant = selectedVariant;
      }
      const edited = key !== memBaseline;
      const actual =
        !manualOffload && !edited && get(models).some((m) => m.id === estId && m.state === "ready");
      const params = selectedV
        ? {
            ctx: selectedV.ctx ? Number(selectedV.ctx) : ctxAuto ? undefined : Number(ctx),
            kvK: selectedV.kvK || kvK || undefined,
            kvV: selectedV.kvV || kvV || undefined,
            kvInRam: selectedV.kvInRam ?? kvInRam,
            // Send the RESOLVED spec (blank => the generator default, e.g. baked
            // draft-mtp) so the server charges the draft's VRAM — matching what
            // generate bakes. A raw blank would drop to undefined and under-report.
            spec: vEffSpecs.length ? vEffSpecs.join("+") : "none",
            vram: selectedV.vramTargetGB ? Number(selectedV.vramTargetGB) : vramAuto ? undefined : Number(vramTarget),
            cpuOffload: selectedV.cpuOffload ? Number(selectedV.cpuOffload) : cpuAuto ? undefined : Number(cpuOffload),
            ctxCheckpoints: selectedV.ctxCheckpoints ?? undefined,
            // Variant fields inherit the Default when blank, exactly as the
            // generator merges them — send the resolved value or the preview
            // sizes a launch shape neither entry has.
            checkpointMinStep: Number(selectedV.checkpointMinStep || adv.checkpointMinStep) || undefined,
            ub: Number(selectedV.ub || ub) || undefined,
            ropeScaling: selectedV.ropeScaling || adv.ropeScaling || undefined,
            actual,
          }
        : {
            ctx: ctxAuto ? undefined : Number(ctx),
            kvK: kvK || undefined,
            kvV: kvV || undefined,
            kvInRam,
            // Resolved spec (blank => generator default, e.g. baked draft-mtp) so
            // the default tab charges the drafter's VRAM like the variant tabs do.
            spec: effSpecs.length ? effSpecs.join("+") : "none",
            vram: vramAuto ? undefined : Number(vramTarget),
            cpuOffload: cpuAuto ? undefined : Number(cpuOffload),
            ctxCheckpoints: ctxCheckpoints ?? undefined,
            checkpointMinStep: Number(adv.checkpointMinStep) || undefined,
            ub: Number(ub) || undefined,
            ropeScaling: adv.ropeScaling || undefined,
            actual,
          };
      estimate = await estimatePlan(estId, params);
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

  function buildOverride(): ModelOverride {
    return {
      ctx: ctxAuto ? 0 : Number(ctx),
      kvK,
      kvV,
      kvInRam,
      vramTargetGB: vramAuto ? 0 : Number(vramTarget),
      cpuOffload: cpuAuto ? 0 : Number(cpuOffload),
      backend,
      vllmGpuUtil: vllmGpuUtil === "" ? 0 : Number(vllmGpuUtil),
      vllmTensorParallel: vllmTensorParallel === "" ? 0 : Number(vllmTensorParallel),
      vllmTokenizer,
      spec,
      reasoningFmt: reasoningOn ? "" : "off",
      reasoningBudget: reasoningBudget === "" ? 0 : Number(reasoningBudget),
      preserveThinking: reasoningOn && preserveThinking,
      flashAttn: flashOn ? "" : "off",
      mmap: mmapOn ? "on" : "off",
      mlock,
      threads: threads === "" ? 0 : Number(threads),
      parallel: parallel === "" ? 0 : Number(parallel),
      ub: ub === "" ? 0 : Number(ub),
      dry: dryOn, // explicit on/off (fleet default is off, so on must be explicit)
      dryMultiplier: dryMultiplier === "" ? 0 : Number(dryMultiplier),
      dryBase: dryBase === "" ? 0 : Number(dryBase),
      dryAllowedLength: dryAllowedLength === "" ? 0 : Number(dryAllowedLength),
      specDraftNMax: specDraftNMax === "" ? 0 : Number(specDraftNMax),
      specDefault,
      specNgramSizeN: specNgramSizeN === "" ? 0 : Number(specNgramSizeN),
      specNgramSizeM: specNgramSizeM === "" ? 0 : Number(specNgramSizeM),
      specNgramMinHits: specNgramMinHits === "" ? 0 : Number(specNgramMinHits),
      ...advToOverride(),
      extraArgs,
      unlisted,
      skip,
      slotCache: slotCacheOn,
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
      vaeOnCpu,
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
      // MTP/DFlash-paired models default a new variant to the matching draft spec
      // even when the model-wide spec is disabled/empty — no reason to leave
      // speculative speed on the table.
      kvK: o.kvK ?? "", kvV: o.kvV ?? "", kvInRam: o.kvInRam ?? false,
      spec: (config?.isMTP || config?.isDflash) && (!o.spec || o.spec === "none") ? genDefaultSpec(config) : (o.spec ?? ""),
      reasoningFmt: o.reasoningFmt ?? "",
      preserveThinking: o.preserveThinking ? null : false,
      flashAttn: o.flashAttn ?? "", mmap: o.mmap ?? "", mlock: o.mlock ?? false,
      threads: o.threads ?? 0, parallel: o.parallel ?? 0, ub: o.ub ?? 0,
      dry: o.dry ?? null,
      dryMultiplier: o.dryMultiplier ?? 0, dryBase: o.dryBase ?? 0, dryAllowedLength: o.dryAllowedLength ?? 0,
      specDraftNMax: o.specDraftNMax ?? 0, specDefault: o.specDefault ?? false,
      specNgramSizeN: o.specNgramSizeN ?? 0, specNgramSizeM: o.specNgramSizeM ?? 0, specNgramMinHits: o.specNgramMinHits ?? 0,
      extraArgs: o.extraArgs ?? "", unlisted: false, ctxCheckpoints: o.ctxCheckpoints ?? null,
      // Snapshot the Default tab's advanced knobs too (variant then drifts freely).
      ...advToOverride(),
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
    // Select the proxied array element (see addImageVariantEntry).
    selectedV = variants[variants.length - 1];
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
    selectedV = defaultVariants[defaultVariants.length - 1];
  }

  // Remove a tab from whichever bucket holds it (per-model variant, ctx tier, or
  // fleet-wide default variant). Fleet-wide removals save globally.
  function removeVariantEntry(name: string) {
    if (!confirm(`Delete variant "${name}"? This cannot be undone until you save.`)) return;
    variants = variants.filter((v) => v.name !== name);
    ctxTiers = ctxTiers.filter((v) => v.name !== name);
    defaultVariants = defaultVariants.filter((v) => v.name !== name);
    if (selectedV?.name === name) selectedV = null;
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
  // Variant slot-cache: null/undefined => inherit the model-wide flag; else explicit.
  function vSlotCacheValue(): string {
    if (!selectedV || selectedV.slotCache == null) return "inherit";
    return selectedV.slotCache ? "on" : "off";
  }
  function setVSlotCache(val: string) {
    if (selectedV) selectedV.slotCache = val === "inherit" ? null : val === "on";
  }
  // Renaming the selected variant must move the selection pointer with it so the
  // derived `selectedV` keeps resolving to the same array element.
  function renameSelectedVariant(e: Event) {
    // Selection follows the object ref, so an empty name no longer drops us to
    // Default — just write it through.
    if (selectedV) selectedV.name = (e.currentTarget as HTMLInputElement).value;
  }
  // A variant's "inherit by zero" number field: show blank for 0 so the
  // placeholder (the inherited value) surfaces instead of a literal "0".
  function vnum(n: number | null | undefined): string {
    return n ? String(n) : "";
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
      // Stay open — the live reload applies in the background. Re-seed from the
      // regenerated config so the modal reflects what actually got saved.
      await load();
      saved = true;
      clearTimeout(savedTimer);
      savedTimer = setTimeout(() => (saved = false), 2000);
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
        {#if config}
          {@const suffix = selectedVariant && !config.id.endsWith(`-${selectedVariant}`) ? `-${selectedVariant}` : ""}
          {#if editingName}
            <input
              class="text-base font-mono font-normal bg-background border border-card-border rounded px-1 py-0.5 text-txtmain w-64"
              bind:value={nameDraft}
              onblur={commitName}
              onkeydown={(e) => { if (e.key === "Enter") commitName(); else if (e.key === "Escape") editingName = false; }}
              placeholder={config.id}
              use:focusOnMount
            />
          {:else}
            <button
              type="button"
              class="text-base font-mono font-normal text-txtsecondary hover:text-txtmain hover:underline decoration-dotted"
              title="Click to rename (advertised name; real id still routes, cascades to variants)"
              onclick={() => { nameDraft = config?.displayName ?? ""; editingName = true; }}
            >{(config.displayName || config.id) + suffix}</button>
          {/if}
        {/if}
      </h2>
      <button onclick={() => dialogEl?.close()} class="text-txtsecondary hover:text-txtmain text-2xl leading-none">&times;</button>
    </div>

    <!-- Sticky live estimate: stays pinned above the scrolling form so the memory
         cost of the current tuning is always visible while editing. -->
    {#if config && !loading && !imageMode && !audioMode && !samMode && !isVllm}
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
        {#if classBackends.length > 0}
          <div class="flex items-center gap-2">
            <span class="text-txtsecondary text-sm">Backend</span>
            {@render hint("Which inference backend serves this model. Auto uses the ★ default for the model's class (Settings → Backends). Switching backend kind changes which knobs apply.")}
            <select bind:value={backend} class="cfg-input ml-auto w-56">
              <option value="">Auto (default)</option>
              {#each classBackends as b (b.id)}
                <option value={b.id}>{b.name || engineLabel(b.kind)}{b.default ? " ★" : ""} ({engineLabel(b.kind)})</option>
              {/each}
            </select>
          </div>
          {#if isVllm}
            <div class="rounded border border-card-border p-3 space-y-2">
              <p class="text-xs text-txtsecondary">vLLM backend - llama.cpp knobs below are ignored. Context sets <span class="font-mono">--max-model-len</span>; blank sizes it against the VRAM budget.</p>
              <label class="flex items-center gap-2 text-sm">
                <span>GPU memory utilization</span>
                {@render hint("--gpu-memory-utilization: fraction of each GPU vLLM may fill (weights + KV + activations). Blank derives it from the VRAM budget and the card's size.")}
                <input type="number" min="0.1" max="1" step="0.05" bind:value={vllmGpuUtil} class="cfg-input w-24 ml-auto" placeholder="0.90" />
              </label>
              <label class="flex items-center gap-2 text-sm">
                <span>Tensor parallel size</span>
                {@render hint("--tensor-parallel-size: shard the model across N GPUs. Default 1 (single GPU).")}
                <input type="number" min="1" step="1" bind:value={vllmTensorParallel} class="cfg-input w-24 ml-auto" placeholder="1" />
              </label>
              <label class="flex items-center gap-2 text-sm">
                <span>Tokenizer</span>
                {@render hint("--tokenizer: the base model's tokenizer (Hugging Face repo id or a local path). vLLM recommends this over the one converted out of the GGUF, which is slow and unstable. Blank omits the flag - it is never guessed, since the discovered model only knows its local folder name.")}
                <input type="text" bind:value={vllmTokenizer} class="cfg-input flex-1 ml-auto" placeholder="Qwen/Qwen3-8B" />
              </label>
            </div>
          {/if}
        {/if}
        {#if imageMode}
        <!-- Image (diffusion / sd-server) form. Mirrors the Default tab's design
             but with diffusion-relevant knobs: external component paths, placement,
             and per-model generation defaults. No KV/ctx/spec; no estimate. Variants
             are generation presets (steps/cfg/size) inheriting the model's paths. -->
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            class="px-2.5 py-1 rounded text-xs font-mono border transition-colors {selectedV === null
              ? 'bg-primary text-white border-primary'
              : 'border-card-border text-txtsecondary hover:text-txtmain'}"
            onclick={() => (selectedV = null)}
          >default</button>
          {#each variants as v (v.name)}
            <span class="inline-flex items-center rounded border overflow-hidden {selectedV === v ? 'border-primary' : 'border-card-border'}">
              <button
                type="button"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedV === v ? 'bg-primary text-white' : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedV = v)}>{v.name || "(unnamed)"}</button>
              <button
                type="button"
                title="Remove preset"
                aria-label="Remove preset {v.name}"
                class="px-1.5 py-1 text-xs {selectedV === v ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <button
            type="button"
            title="Add a generation preset (steps / cfg / size), inheriting this model's paths"
            class="px-2.5 py-1 rounded text-xs font-semibold border border-dashed border-info text-info hover:bg-info hover:text-white transition-colors"
            onclick={addImageVariantEntry}>+ preset</button>
        </div>

        {#if selectedV === null}
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
              <input type="checkbox" checked={vaeOnCpu === "on"} onchange={(e) => (vaeOnCpu = (e.currentTarget as HTMLInputElement).checked ? "on" : "")} />
              <span class="text-txtsecondary flex items-center gap-1">
                VAE on CPU
                {@render hint("--backend vae=cpu. Force the VAE decoder onto the CPU (off by default - it decodes on the GPU). Turn on if the GPU VAE outputs a blank/white image (a bf16 VAE whitens on some backends); costs decode speed.")}
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

        <!-- Launch command (editable, two-way). Form edits re-render it; editing the
             box parses sd-server flags back into the fields (on blur), unknown flags
             stashed into extraArgs. Mirrors the LLM tab. -->
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
            Two-way: known sd-server flags parse back into the fields on blur; anything else lands in <code>extraArgs</code>.
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
        {:else if audioMode}
        <!-- Audio (Qwen3-TTS / qwentts.cpp tts-server) form. No KV/ctx/spec/estimate:
             the talker + paired codec are tiny and fully resident. Voice, temperature,
             top_k etc. are per-request (/v1/audio/speech), not launch flags — so the
             only launch knobs are extraArgs passthrough + listing toggles. No variants
             (base/customvoice/voicedesign ship as separate ggufs = separate models). -->
        <div class="grid grid-cols-2 gap-3">
          <p class="col-span-2 text-xs text-txtsecondary">
            {#if ttscppMode}
              Served by TTS.cpp <code>tts-server</code> (OpenAI <code>/v1/audio/speech</code>).
              Self-contained gguf, CPU only; voice is chosen per request.
            {:else}
              Served by qwentts.cpp <code>tts-server</code> (OpenAI <code>/v1/audio/speech</code>).
              The talker loads with its paired codec gguf; voice is chosen per request.
            {/if}
          </p>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Extra args
              {@render hint("Appended verbatim to the tts-server command, for flags autogen doesn't model.")}
            </span>
            <input type="text" bind:value={extraArgs} class="cfg-input" placeholder="e.g. --temperature 0.7" spellcheck="false" />
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

        <details class="group">
          <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
            Launch parameters {config.hasOverride ? "(custom)" : "(autogen default)"}
          </summary>
          <textarea value={cmdDraft} readonly spellcheck="false" rows="4" class="mt-2 w-full bg-background rounded border border-card-border p-3 text-xs font-mono whitespace-pre-wrap break-all resize-y text-txtmain opacity-90"></textarea>
          <p class="text-xs text-txtsecondary mt-1 font-mono break-all">{config.gguf}</p>
        </details>
        {:else if samMode}
        <!-- SAM segmentation (sam3.cpp tts-server sibling: sam3_server) form. No
             KV/ctx/spec/estimate - SAM models are tiny and fully resident. Box/
             point prompts are per-request (/v1/segment), not launch flags. CPU vs
             GPU placement is auto (VRAM-aware, coexists with the loaded model);
             pin it with extraArgs --no-gpu. Only launch knobs are the backend pick
             (above) + extraArgs passthrough + listing toggles. No variants. -->
        <div class="grid grid-cols-2 gap-3">
          <p class="col-span-2 text-xs text-txtsecondary">
            Served by <code>sam3_server</code> (<code>POST /v1/segment</code>) with box / point
            prompts. Runs alongside the loaded model - auto-placed on CPU when VRAM is tight
            (pin with <code>--no-gpu</code> below). Used from the image playground's AI-select tool.
          </p>
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Extra args
              {@render hint("Appended verbatim to the sam3_server command, for flags autogen doesn't model. Use --no-gpu to force CPU regardless of the auto placement.")}
            </span>
            <input type="text" bind:value={extraArgs} class="cfg-input" placeholder="e.g. --no-gpu" spellcheck="false" />
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

        <details class="group">
          <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
            Launch parameters {config.hasOverride ? "(custom)" : "(autogen default)"}
          </summary>
          <textarea value={cmdDraft} readonly spellcheck="false" rows="4" class="mt-2 w-full bg-background rounded border border-card-border p-3 text-xs font-mono whitespace-pre-wrap break-all resize-y text-txtmain opacity-90"></textarea>
          <p class="text-xs text-txtsecondary mt-1 font-mono break-all">{config.gguf}</p>
        </details>
        {:else}
        <!-- Entry selector: Default is a pinned, non-deletable entry; variants
             follow. Editing one shows its fields below - everything a variant
             doesn't set inherits from Default. -->
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            class="px-2.5 py-1 rounded text-xs font-mono border transition-colors {selectedV === null
              ? 'bg-primary text-white border-primary'
              : 'border-card-border text-txtsecondary hover:text-txtmain'}"
            onclick={() => (selectedV = null)}
          >default</button>
          {#each variants as v (v.name)}
            <span
              class="inline-flex items-center rounded border overflow-hidden {selectedV === v
                ? 'border-primary'
                : 'border-card-border'}"
            >
              <button
                type="button"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedV === v
                  ? 'bg-primary text-white'
                  : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedV = v)}>{v.name || "(unnamed)"}</button>
              <button
                type="button"
                title="Remove variant"
                aria-label="Remove variant {v.name}"
                class="px-1.5 py-1 text-xs {selectedV === v ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <!-- Ctx tiers (32k/64k…): per-model, removable like variants. -->
          {#each ctxTiers as v (v.name)}
            <span
              class="inline-flex items-center rounded border overflow-hidden {selectedV === v
                ? 'border-primary'
                : 'border-card-border'}"
            >
              <button
                type="button"
                title="Context tier"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedV === v
                  ? 'bg-primary text-white'
                  : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedV = v)}>{v.name || "(unnamed)"}</button>
              <button
                type="button"
                title="Remove ctx tier"
                aria-label="Remove ctx tier {v.name}"
                class="px-1.5 py-1 text-xs {selectedV === v ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
                onclick={() => removeVariantEntry(v.name)}>×</button>
            </span>
          {/each}
          <!-- Fleet-wide default variants (e.g. game): shared by every model.
               Adding/removing/editing one writes them globally on save. -->
          {#each defaultVariants as v (v.name)}
            <span
              class="inline-flex items-center rounded border overflow-hidden {selectedV === v
                ? 'border-primary'
                : 'border-card-border'}"
            >
              <button
                type="button"
                title="Fleet-wide variant (shared by all models)"
                class="px-2.5 py-1 text-xs font-mono transition-colors {selectedV === v
                  ? 'bg-primary text-white'
                  : 'text-txtsecondary hover:text-txtmain'}"
                onclick={() => (selectedV = v)}>{v.name || "(unnamed)"} <span class="opacity-60">⊕</span></button>
              <button
                type="button"
                title="Remove fleet-wide variant"
                aria-label="Remove fleet-wide variant {v.name}"
                class="px-1.5 py-1 text-xs {selectedV === v ? 'bg-primary text-white hover:bg-black/25' : 'text-txtsecondary hover:text-error'}"
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

        {#if selectedV === null}
        <!-- Curated override fields, ordered most-tinkered (top) to least (bottom). -->
        <div class="grid grid-cols-2 gap-3">
          <label class="flex flex-col gap-1 text-sm col-span-2">
            <span class="text-txtsecondary flex items-center gap-1">
              Context window
              {@render hint("Tokens the model can attend to. Auto = the size the autogen sizer picked to fit free VRAM (shown). Slider ranges 4k to the model's trained max, or 4x that with RoPE extension on.")}
              <span class="ml-auto font-mono {ctxOverNative ? 'text-warning' : 'text-txtmain'}">
                {ctxAuto ? (autoCtx ? `auto · ${fmtCtx(autoCtx)}` : "auto") : fmtCtx(ctx)}
                {#if ctxOverNative}<span class="text-xs"> · {(ctx / nativeCtx).toFixed(2)}x native</span>{/if}
              </span>
            </span>
            <div class="flex items-center gap-3">
              <label class="flex items-center gap-1.5 text-xs text-txtsecondary whitespace-nowrap">
                <input type="checkbox" bind:checked={ctxAuto} /> Auto
              </label>
              <!-- The track carries a tick at the trained length; everything right
                   of it only works because RoPE is being stretched. -->
              <div class="relative flex-1 flex items-center">
                {#if maxCtx > nativeCtx}
                  <div
                    class="pointer-events-none absolute top-0 bottom-0 w-px bg-warning/70"
                    style="left: {nativePct}%"
                  ></div>
                {/if}
                <input
                  type="range"
                  min={CTX_MIN}
                  max={maxCtx}
                  step="1024"
                  bind:value={ctx}
                  disabled={ctxAuto}
                  use:wheelAdjust
                  class="w-full disabled:opacity-40 {ctxOverNative ? 'accent-warning' : ''}"
                />
              </div>
              <label
                class="flex items-center gap-1.5 text-xs whitespace-nowrap {ropeOn ? 'text-warning' : 'text-txtsecondary'}"
                title="Extend past the model's trained {fmtCtx(nativeCtx)} context with YaRN RoPE scaling (--rope-scaling yarn). The scale factor is derived from the ctx you pick. Quality degrades the further past native you go."
              >
                <input
                  type="checkbox"
                  checked={ropeOn}
                  onchange={(e) => toggleRope(e.currentTarget.checked)}
                /> RoPE
              </label>
              <span class="text-xs text-txtsecondary font-mono whitespace-nowrap">max {fmtCtx(maxCtx)}</span>
            </div>
            {#if ctxOverNative}
              <span class="text-xs text-warning">
                Past the trained {fmtCtx(nativeCtx)} - YaRN scaling at {(Math.ceil((ctx / nativeCtx) * 2) / 2).toFixed(1)}x. Long-range recall degrades; verify before relying on it.
              </span>
            {/if}
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
              {@render hint("Quantization of the attention key cache. Lower bits = less VRAM, but quantized KV costs long-context recall well before it shows in perplexity. Default is f16, dropping to q8_0 only when f16 cannot reach the minimum context in the VRAM budget.")}
            </span>
            <select bind:value={kvK} class="cfg-input">
              {#each KV_OPTS as o}<option value={o}>{o === "" ? "default (auto)" : o}</option>{/each}
            </select>
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              KV cache V
              {@render hint("Quantization of the attention value cache. Must match K for flash-attention. Same default as K.")}
            </span>
            <select bind:value={kvV} class="cfg-input">
              {#each KV_OPTS as o}<option value={o}>{o === "" ? "default (auto)" : o}</option>{/each}
            </select>
          </label>

          <div class="flex flex-col gap-1 text-sm">
            <span class="text-txtsecondary flex items-center gap-1">
              Speculative
              {@render hint("Speculative decoding backends. Chainable (e.g. draft-mtp + ngram-map-k4v). None checked = generator default; draft-mtp needs a model with MTP layers, draft-dflash needs a paired *-dflash-*.gguf sidecar.")}
            </span>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
              {#each specBackends as b}
                <label class="flex items-center gap-1"><input type="checkbox" checked={effSpecs.includes(b)} onchange={(e) => (spec = specToggle(spec, b, e.currentTarget.checked))} />{b}</label>
              {/each}
              <label class="flex items-center gap-1"><input type="checkbox" checked={specHas(spec, "none")} onchange={(e) => (spec = specToggle(spec, "none", e.currentTarget.checked))} />none</label>
            </div>
          </div>

          {#if effSpecs.includes("draft-mtp") || effSpecs.includes("draft-dflash")}
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Draft n-max
                {@render hint(`--spec-draft-n-max. Max draft tokens proposed per step. Empty = ${effSpecs.includes("draft-dflash") ? "5 (draft-dflash)" : "2 (draft-mtp)"}.`)}
              </span>
              <input type="number" min="0" step="1" bind:value={specDraftNMax} use:wheelAdjust class="cfg-input" placeholder={effSpecs.includes("draft-dflash") ? "5" : "2"} />
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
              {@render hint("--dry-* repetition penalty. Off by default. Multiplier / base / allowed-length: empty = 0.8 / 1.75 / 3.")}
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
                {@render hint("Memory-map weights from disk. Reflects the sizer's placement default: OFF (--no-mmap) when fully GPU-resident / expert-offloaded, ON when weights sit on CPU. Toggle to force either way.")}
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
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" bind:checked={slotCacheOn} />
              <span class="text-txtsecondary flex items-center gap-1">
                Save KV cache to disk
                {@render hint("Persist this conversation's KV cache to disk so a long chat survives being evicted from the slot, and is restored instead of reprocessed. Needs the global slot-cache toggle on (Dashboard).")}
              </span>
            </label>
          </div>
        </div>

        <!-- Advanced / power-user llama-server knobs. Collapsed; every field
             inherits/omits unless set. Same knobs available per-variant below. -->
        <details class="group">
          <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
            Advanced
          </summary>
          <div class="mt-2 grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Batch threads {@render hint("-tb. CPU threads for prompt/batch processing. Empty = same as -t.")}</span>
              <input type="number" min="0" step="1" bind:value={adv.threadsBatch} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="auto" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Priority {@render hint("--prio. 0 normal, 1 medium, 2 high, 3 realtime.")}</span>
              <input type="number" min="0" max="3" step="1" bind:value={adv.prio} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="0" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Cache reuse {@render hint("--cache-reuse N. Min chunk reused from the prompt cache via KV-shifting. 0 = off.")}</span>
              <input type="number" min="0" step="64" bind:value={adv.cacheReuse} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="off" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Cache RAM (MiB) {@render hint("-cram. Max prompt-cache size in MiB. Empty = llama default (8192).")}</span>
              <input type="number" min="0" step="512" bind:value={adv.cacheRamMB} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="8192" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Checkpoint spacing {@render hint("-cms. Min tokens between context checkpoints. Empty = llama default (8192).")}</span>
              <input type="number" min="0" step="512" bind:value={adv.checkpointMinStep} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="8192" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Spec draft n-min {@render hint("--spec-draft-n-min. Minimum draft tokens per speculative step. 0 = default.")}</span>
              <input type="number" min="0" step="1" bind:value={adv.specDraftNMin} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="0" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Slot match {@render hint("-sps. Prompt-similarity threshold (0..1) to reuse a slot. 0 = omit.")}</span>
              <input type="number" min="0" max="1" step="0.05" bind:value={adv.slotPromptSimilarity} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="off" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Main GPU {@render hint("-mg. Primary GPU index. 0 = GPU 0.")}</span>
              <input type="number" min="0" step="1" bind:value={adv.mainGpu} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="0" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Draft KV-K {@render hint("-ctkd. Draft/spec model K cache type. Empty = f16.")}</span>
              <input type="text" bind:value={adv.kvKDraft} class="cfg-input w-20 ml-auto" placeholder="f16" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Draft KV-V {@render hint("-ctvd. Draft/spec model V cache type. Empty = f16.")}</span>
              <input type="text" bind:value={adv.kvVDraft} class="cfg-input w-20 ml-auto" placeholder="f16" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Idle-slot cache {@render hint("--cache-idle-slots. Save idle slots to the prompt cache. inherit = llama default.")}</span>
              <select bind:value={adv.cacheIdleSlots} class="cfg-input ml-auto"><option value="">inherit</option><option value="on">on</option><option value="off">off</option></select>
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Context shift {@render hint("--context-shift. Slide the window on overflow. inherit = llama default (off).")}</span>
              <select bind:value={adv.contextShift} class="cfg-input ml-auto"><option value="">inherit</option><option value="on">on</option><option value="off">off</option></select>
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">RoPE scaling {@render hint("--rope-scaling. Context-extension method. auto = from model.")}</span>
              <select bind:value={adv.ropeScaling} class="cfg-input ml-auto"><option value="">auto</option><option value="none">none</option><option value="linear">linear</option><option value="yarn">yarn</option></select>
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Split mode {@render hint("-sm. Multi-GPU split strategy. auto = from model.")}</span>
              <select bind:value={adv.splitMode} class="cfg-input ml-auto"><option value="">auto</option><option value="none">none</option><option value="layer">layer</option><option value="row">row</option><option value="tensor">tensor</option></select>
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">RoPE scale {@render hint("--rope-scale. Context scaling factor (expand ctx by N). 0 = omit.")}</span>
              <input type="number" min="0" step="0.5" bind:value={adv.ropeScale} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="auto" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">RoPE freq base {@render hint("--rope-freq-base. NTK base frequency. 0 = from model.")}</span>
              <input type="number" min="0" step="10000" bind:value={adv.ropeFreqBase} use:wheelAdjust class="cfg-input w-24 ml-auto" placeholder="auto" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">YaRN orig ctx {@render hint("--yarn-orig-ctx. Model's original training context. 0 = from model.")}</span>
              <input type="number" min="0" step="1024" bind:value={adv.yarnOrigCtx} use:wheelAdjust class="cfg-input w-24 ml-auto" placeholder="auto" />
            </label>
            <label class="flex items-center gap-2">
              <span class="text-txtsecondary flex items-center gap-1">Tensor split {@render hint("-ts. Per-GPU proportion, comma list e.g. 3,1. Empty = omit.")}</span>
              <input type="text" bind:value={adv.tensorSplit} class="cfg-input w-24 ml-auto" placeholder="3,1" />
            </label>
            <label class="flex items-center gap-2 col-span-2">
              <span class="text-txtsecondary flex items-center gap-1 shrink-0">Override tensor {@render hint("-ot. Manual tensor→buffer placement pattern, e.g. exps=CPU. Empty = omit.")}</span>
              <input type="text" bind:value={adv.overrideTensor} class="cfg-input flex-1 ml-auto font-mono" placeholder="regex=BUFFER" />
            </label>
            <label class="flex items-center gap-2 col-span-2">
              <span class="text-txtsecondary flex items-center gap-1 shrink-0">Chat template file {@render hint("--chat-template-file. Path to a .jinja chat template replacing the gguf's baked-in one - use a vendor-fixed template (e.g. Gemma, Qwen) without rebuilding the gguf. Empty = the baked-in template (or quartermaster's built-in Qwen 3.5/3.6 fix).")}</span>
              <input type="text" bind:value={adv.chatTemplateFile} class="cfg-input flex-1 ml-auto font-mono" placeholder="D:/LLM/Models/templates/gemma4.jinja" spellcheck="false" />
              <button
                type="button" title="Browse for a .jinja template" aria-label="Browse for a chat template file"
                class="shrink-0 p-1.5 rounded border border-transparent text-txtsecondary hover:text-primary hover:border-primary transition-colors"
                onclick={() => browseChatTemplate((p) => (adv.chatTemplateFile = p))}
              ><FolderOpen size={14} /></button>
            </label>
            <label class="flex items-center gap-2">
              <input type="checkbox" bind:checked={adv.directIo} />
              <span class="text-txtsecondary flex items-center gap-1">Direct I/O {@render hint("-dio. DirectIO for faster cold model load where supported.")}</span>
            </label>
            <label class="flex items-center gap-2">
              <input type="checkbox" bind:checked={adv.swaFull} />
              <span class="text-txtsecondary flex items-center gap-1">Full SWA cache {@render hint("--swa-full. Keep the full sliding-window KV cache (Gemma etc.).")}</span>
            </label>
            <label class="flex items-center gap-2">
              <input type="checkbox" bind:checked={adv.noOpOffload} />
              <span class="text-txtsecondary flex items-center gap-1">No op-offload {@render hint("--no-op-offload. Keep host tensor ops on the CPU.")}</span>
            </label>
            <label class="flex items-center gap-2">
              <input type="checkbox" bind:checked={adv.noRepack} />
              <span class="text-txtsecondary flex items-center gap-1">No repack {@render hint("--no-repack. Disable weight repacking at load.")}</span>
            </label>
          </div>
        </details>

        <!-- Launch command (editable, two-way) - collapsed at bottom. Form edits
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
            Anything left unset inherits from <button type="button" class="underline hover:text-txtmain" onclick={() => (selectedV = null)}>Default</button>.
          </p>
          {#if selectedIsDefault}
            <p class="text-xs text-warning bg-warning/10 border border-warning/30 rounded px-2 py-1.5">
              ⚠ Fleet-wide variant - shared by <strong>every</strong> model. Saving rewrites it globally, not just for {config.id}.
            </p>
          {/if}
          <div class="grid grid-cols-2 gap-3">
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Name (suffix)
                {@render hint("The variant's id suffix and listen-name. The model loads as <base-id>-<name>.")}
              </span>
              {#if sv.name === "vision"}
                <input type="text" value="vision" readonly class="cfg-input opacity-70" title="Reserved: the auto-generated vision twin that loads the mmproj image projector. Tune its ctx/VRAM/visibility here; uncheck Unlisted to surface it in the model picker." />
              {:else}
                <input type="text" value={sv.name} oninput={renameSelectedVariant} class="cfg-input" placeholder="e.g. game, long, judge" />
              {/if}
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

            {#if vEffSpecs.includes("draft-mtp") || vEffSpecs.includes("draft-dflash")}
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-txtsecondary flex items-center gap-1">
                  Draft n-max
                  {@render hint(`--spec-draft-n-max for this variant. Empty / 0 = inherit (${specDraftNMax !== "" && Number(specDraftNMax) > 0 ? specDraftNMax : vEffSpecs.includes("draft-dflash") ? "5" : "2"}).`)}
                </span>
                <input type="number" min="0" step="1" value={vnum(sv.specDraftNMax)} oninput={(e) => (sv.specDraftNMax = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input" placeholder={`inherit (${specDraftNMax !== "" && Number(specDraftNMax) > 0 ? specDraftNMax : vEffSpecs.includes("draft-dflash") ? "5" : "2"})`} />
              </label>
            {/if}
            {#if vEffSpecs.includes("ngram-map-k4v")}
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-txtsecondary flex items-center gap-1">
                  ngram size-n / size-m
                  {@render hint("--spec-ngram-map-k4v-size-n / -size-m for this variant. Empty / 0 = inherit.")}
                </span>
                <div class="flex items-end gap-2">
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">size-n<input type="number" min="0" step="1" value={vnum(sv.specNgramSizeN)} oninput={(e) => (sv.specNgramSizeN = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="inherit" /></span>
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">size-m<input type="number" min="0" step="1" value={vnum(sv.specNgramSizeM)} oninput={(e) => (sv.specNgramSizeM = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="inherit" /></span>
                </div>
              </label>
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-txtsecondary flex items-center gap-1">
                  ngram min-hits
                  {@render hint("--spec-ngram-map-k4v-min-hits for this variant. Empty / 0 = inherit.")}
                </span>
                <input type="number" min="0" step="1" value={vnum(sv.specNgramMinHits)} oninput={(e) => (sv.specNgramMinHits = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input" placeholder="inherit" />
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
              <input type="number" min="0" step="64" value={vnum(sv.ub)} oninput={(e) => (sv.ub = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input" placeholder={ub === "" ? "inherit (auto)" : `inherit (${ub})`} />
            </label>

            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Threads
                {@render hint("-t. CPU threads for this variant. Empty / 0 = inherit.")}
              </span>
              <input type="number" min="0" step="1" value={vnum(sv.threads)} oninput={(e) => (sv.threads = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input" placeholder={threads === "" ? "inherit (global)" : `inherit (${threads})`} />
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-txtsecondary flex items-center gap-1">
                Parallel slots
                {@render hint("--parallel concurrent request slots for this variant. Empty / 0 = inherit (1).")}
              </span>
              <input type="number" min="0" step="1" value={vnum(sv.parallel)} oninput={(e) => (sv.parallel = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input" placeholder={parallel === "" ? "inherit (1)" : `inherit (${parallel})`} />
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
                <option value="inherit">inherit (off)</option>
                <option value="on">on</option>
                <option value="off">off</option>
              </select>
              {#if vDryValue() !== "off"}
                <div class="flex items-end gap-2">
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">multiplier<input type="number" min="0" step="0.05" value={vnum(sv.dryMultiplier)} oninput={(e) => (sv.dryMultiplier = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="0.8" /></span>
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">base<input type="number" min="0" step="0.05" value={vnum(sv.dryBase)} oninput={(e) => (sv.dryBase = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="1.75" /></span>
                  <span class="flex flex-col gap-0.5 flex-1 min-w-0 text-xs text-txtsecondary">allowed-len<input type="number" min="0" step="1" value={vnum(sv.dryAllowedLength)} oninput={(e) => (sv.dryAllowedLength = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-full min-w-0" placeholder="3" /></span>
                </div>
              {/if}
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
                <input type="checkbox" checked={variantMmapOn(sv)} onchange={(e) => (sv.mmap = (e.currentTarget as HTMLInputElement).checked ? "on" : "off")} />
                <span class="text-txtsecondary flex items-center gap-1">
                  Memory-map (mmap)
                  {@render hint("Memory-map weights from disk. Blank inherits this variant's placement default (--no-mmap when GPU-resident). Off forces --no-mmap, copying weights into RAM.")}
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
              <label class="flex items-center gap-2 text-sm col-span-2">
                <span class="text-txtsecondary flex items-center gap-1 shrink-0">
                  Save KV cache to disk
                  {@render hint("Persist this variant's KV cache to disk so a long chat survives slot eviction and is restored instead of reprocessed. inherit = use the Default tab's setting. Needs the global slot-cache toggle on (Dashboard).")}
                </span>
                <select value={vSlotCacheValue()} onchange={(e) => setVSlotCache((e.currentTarget as HTMLSelectElement).value)} class="cfg-input ml-auto">
                  <option value="inherit">inherit</option>
                  <option value="on">on</option>
                  <option value="off">off</option>
                </select>
              </label>
            </div>
          </div>

          <!-- Advanced knobs for this variant; empty/unset inherits Default. -->
          <details class="group">
            <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
              Advanced
            </summary>
            <div class="mt-2 grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Batch threads {@render hint("-tb. CPU threads for prompt/batch processing.")}</span>
                <input type="number" min="0" step="1" value={vnum(sv.threadsBatch)} oninput={(e) => (sv.threadsBatch = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Priority {@render hint("--prio. 0 normal, 1 medium, 2 high, 3 realtime.")}</span>
                <input type="number" min="0" max="3" step="1" value={vnum(sv.prio)} oninput={(e) => (sv.prio = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Cache reuse {@render hint("--cache-reuse N. Prefix KV-shift reuse. 0 = off.")}</span>
                <input type="number" min="0" step="64" value={vnum(sv.cacheReuse)} oninput={(e) => (sv.cacheReuse = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Cache RAM (MiB) {@render hint("-cram. Prompt-cache size MiB.")}</span>
                <input type="number" min="0" step="512" value={vnum(sv.cacheRamMB)} oninput={(e) => (sv.cacheRamMB = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Checkpoint spacing {@render hint("-cms. Min tokens between context checkpoints.")}</span>
                <input type="number" min="0" step="512" value={vnum(sv.checkpointMinStep)} oninput={(e) => (sv.checkpointMinStep = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Spec draft n-min {@render hint("--spec-draft-n-min. Min draft tokens per step.")}</span>
                <input type="number" min="0" step="1" value={vnum(sv.specDraftNMin)} oninput={(e) => (sv.specDraftNMin = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Slot match {@render hint("-sps. Slot prompt-similarity threshold (0..1).")}</span>
                <input type="number" min="0" max="1" step="0.05" value={vnum(sv.slotPromptSimilarity)} oninput={(e) => (sv.slotPromptSimilarity = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Main GPU {@render hint("-mg. Primary GPU index.")}</span>
                <input type="number" min="0" step="1" value={vnum(sv.mainGpu)} oninput={(e) => (sv.mainGpu = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Draft KV-K {@render hint("-ctkd. Draft/spec K cache type.")}</span>
                <input type="text" value={sv.kvKDraft ?? ""} oninput={(e) => (sv.kvKDraft = (e.currentTarget as HTMLInputElement).value)} class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Draft KV-V {@render hint("-ctvd. Draft/spec V cache type.")}</span>
                <input type="text" value={sv.kvVDraft ?? ""} oninput={(e) => (sv.kvVDraft = (e.currentTarget as HTMLInputElement).value)} class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Idle-slot cache {@render hint("--cache-idle-slots. inherit = Default's value.")}</span>
                <select value={sv.cacheIdleSlots ?? ""} onchange={(e) => (sv.cacheIdleSlots = (e.currentTarget as HTMLSelectElement).value)} class="cfg-input ml-auto"><option value="">inherit</option><option value="on">on</option><option value="off">off</option></select>
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Context shift {@render hint("--context-shift. inherit = Default's value.")}</span>
                <select value={sv.contextShift ?? ""} onchange={(e) => (sv.contextShift = (e.currentTarget as HTMLSelectElement).value)} class="cfg-input ml-auto"><option value="">inherit</option><option value="on">on</option><option value="off">off</option></select>
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">RoPE scaling {@render hint("--rope-scaling. Context-extension method.")}</span>
                <select value={sv.ropeScaling ?? ""} onchange={(e) => (sv.ropeScaling = (e.currentTarget as HTMLSelectElement).value)} class="cfg-input ml-auto"><option value="">inherit</option><option value="none">none</option><option value="linear">linear</option><option value="yarn">yarn</option></select>
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Split mode {@render hint("-sm. Multi-GPU split strategy.")}</span>
                <select value={sv.splitMode ?? ""} onchange={(e) => (sv.splitMode = (e.currentTarget as HTMLSelectElement).value)} class="cfg-input ml-auto"><option value="">inherit</option><option value="none">none</option><option value="layer">layer</option><option value="row">row</option><option value="tensor">tensor</option></select>
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">RoPE scale {@render hint("--rope-scale. Ctx scaling factor. 0 = inherit.")}</span>
                <input type="number" min="0" step="0.5" value={vnum(sv.ropeScale)} oninput={(e) => (sv.ropeScale = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-20 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">RoPE freq base {@render hint("--rope-freq-base. NTK base frequency.")}</span>
                <input type="number" min="0" step="10000" value={vnum(sv.ropeFreqBase)} oninput={(e) => (sv.ropeFreqBase = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-24 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">YaRN orig ctx {@render hint("--yarn-orig-ctx. Model's original training context.")}</span>
                <input type="number" min="0" step="1024" value={vnum(sv.yarnOrigCtx)} oninput={(e) => (sv.yarnOrigCtx = Number((e.currentTarget as HTMLInputElement).value))} use:wheelAdjust class="cfg-input w-24 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2">
                <span class="text-txtsecondary flex items-center gap-1">Tensor split {@render hint("-ts. Per-GPU proportion, e.g. 3,1.")}</span>
                <input type="text" value={sv.tensorSplit ?? ""} oninput={(e) => (sv.tensorSplit = (e.currentTarget as HTMLInputElement).value)} class="cfg-input w-24 ml-auto" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2 col-span-2">
                <span class="text-txtsecondary flex items-center gap-1 shrink-0">Override tensor {@render hint("-ot. Manual tensor→buffer placement, e.g. exps=CPU.")}</span>
                <input type="text" value={sv.overrideTensor ?? ""} oninput={(e) => (sv.overrideTensor = (e.currentTarget as HTMLInputElement).value)} class="cfg-input flex-1 ml-auto font-mono" placeholder="inherit" />
              </label>
              <label class="flex items-center gap-2 col-span-2">
                <span class="text-txtsecondary flex items-center gap-1 shrink-0">Chat template file {@render hint("--chat-template-file. Path to a .jinja chat template replacing the gguf's baked-in one. Empty = inherit the model-wide value.")}</span>
                <input type="text" value={sv.chatTemplateFile ?? ""} oninput={(e) => (sv.chatTemplateFile = (e.currentTarget as HTMLInputElement).value)} class="cfg-input flex-1 ml-auto font-mono" placeholder="inherit" spellcheck="false" />
                <button
                  type="button" title="Browse for a .jinja template" aria-label="Browse for a chat template file"
                  class="shrink-0 p-1.5 rounded border border-transparent text-txtsecondary hover:text-primary hover:border-primary transition-colors"
                  onclick={() => browseChatTemplate((p) => (sv.chatTemplateFile = p))}
                ><FolderOpen size={14} /></button>
              </label>
              <label class="flex items-center gap-2">
                <input type="checkbox" checked={!!sv.directIo} onchange={(e) => (sv.directIo = (e.currentTarget as HTMLInputElement).checked)} />
                <span class="text-txtsecondary flex items-center gap-1">Direct I/O {@render hint("-dio. Faster cold model load where supported.")}</span>
              </label>
              <label class="flex items-center gap-2">
                <input type="checkbox" checked={!!sv.swaFull} onchange={(e) => (sv.swaFull = (e.currentTarget as HTMLInputElement).checked)} />
                <span class="text-txtsecondary flex items-center gap-1">Full SWA cache {@render hint("--swa-full. Full sliding-window KV cache.")}</span>
              </label>
              <label class="flex items-center gap-2">
                <input type="checkbox" checked={!!sv.noOpOffload} onchange={(e) => (sv.noOpOffload = (e.currentTarget as HTMLInputElement).checked)} />
                <span class="text-txtsecondary flex items-center gap-1">No op-offload {@render hint("--no-op-offload. Keep host tensor ops on CPU.")}</span>
              </label>
              <label class="flex items-center gap-2">
                <input type="checkbox" checked={!!sv.noRepack} onchange={(e) => (sv.noRepack = (e.currentTarget as HTMLInputElement).checked)} />
                <span class="text-txtsecondary flex items-center gap-1">No repack {@render hint("--no-repack. Disable weight repacking at load.")}</span>
              </label>
            </div>
          </details>

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
        <button onclick={() => dialogEl?.close()} class="btn btn--sm">Close</button>
        <button onclick={save} class="btn btn--sm btn--primary !text-white" disabled={saving || loading}>
          {saving ? "Saving…" : saved ? "Saved ✓" : "Save & reload"}
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
