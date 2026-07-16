<script lang="ts">
  import { get } from "svelte/store";
  import { models, upstreamLogs, unloadSingleModel } from "../../stores/api";
  import { userPref } from "../../stores/prefs";
  import {
    imageSessions,
    activeImageChatId,
    generatingImageChatId,
    newImageChatId,
    deriveImageTitle,
    type ImageSession,
    type Turn,
  } from "../../stores/imageHistory";
  import { generateImage } from "../../lib/imageApi";
  import { generateSdImage, generateSdImg2Img } from "../../lib/sdApi";
  import { matchColorToRef } from "../../lib/colorMatch";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import MaskEditor from "./MaskEditor.svelte";
  import Select from "./Select.svelte";
  import Composer from "./Composer.svelte";
  import { autogrow } from "../../lib/autogrow";
  import { Image as ImageIcon, X, Download, Paperclip, Ban, Plus, Pencil, Save, Copy, Check, RefreshCw, ImageDown, Type, Paintbrush, Sparkles, Brush } from "lucide-svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import type { ImageApiMode } from "../../lib/types";

  // A conversational image tab: each user prompt becomes a turn, and the model
  // replies with an image. Follow-up prompts tweak the last image — Kontext gets
  // it as a reference edit (identity-preserving), other sdapi models re-run it
  // through img2img. First turn (no prior image) is a plain txt2img.
  // refs = the source/reference images fed into this turn (attachments, or the
  // prior reply reused as base), shown inside the user bubble like chat vision.
  // Turn / session types live in the store (imageHistory.ts); threads are saved
  // server-backed per user exactly like the chat tab.

  const selectedModelStore = userPref<string>("playground-image-model", "");
  // selectedSizeStore ("WxH") stays the single downstream source of truth; it's
  // now DERIVED from an aspect + long-edge pair (see the $effect below) so the UI
  // can offer the two knobs separately. Both persisted so they follow the user.
  const selectedSizeStore = userPref<string>("playground-image-size", "512x512");
  const aspectStore = userPref<string>("playground-image-aspect", "1:1");
  const longEdgeStore = userPref<string>("playground-image-long", "512");
  const apiModeStore = userPref<ImageApiMode>("playground-image-api-mode", "sdapi");
  const sdNegativePromptStore = userPref<string>("playground-sdapi-negative-prompt", "");
  const sdStepsStore = userPref<number>("playground-sdapi-steps", 20);
  const sdCfgScaleStore = userPref<number>("playground-sdapi-cfg-scale", 7);
  const sdSeedStore = userPref<number>("playground-sdapi-seed", -1);
  const sdSamplerStore = userPref<string>("playground-sdapi-sampler", "");
  const sdSchedulerStore = userPref<string>("playground-sdapi-scheduler", "");
  // Tweak strength for the img2img follow-up path (non-Kontext models).
  const sdDenoiseStore = userPref<number>("playground-sdapi-denoise", 0.6);
  // Luma tone-anchoring of reused sources back to the thread origin (fights
  // brightness drift over chained edits). Toggle to A/B it against the raw model.
  const sdToneAnchorStore = userPref<boolean>("playground-sdapi-tone-anchor", true);

  let prompt = $state("");
  // Images the user attached to the NEXT message (data URLs) — seed a thread from
  // an existing picture. When empty, follow-ups reuse the last reply's image.
  let attached = $state<string[]>([]);
  // Inpaint mask for the NEXT message (PNG data URL): white = regenerate, black =
  // keep. maskSource pins which base it was painted on so a stale mask (base
  // changed) is dropped at send. img2img path only — Kontext ignores it.
  let maskData = $state<string | null>(null);
  let maskSource = $state<string | null>(null);
  let showMask = $state(false);

  // Ensure a valid active thread exists (first run / persisted id gone). Mirrors
  // the chat tab's initChats. The list is hydrated server-side before mount.
  function initImageChats() {
    const sessions = get(imageSessions);
    let id = get(activeImageChatId);
    if (!sessions.some((s) => s.id === id)) {
      const recent = sessions.reduce<ImageSession | null>(
        (best, s) => (!best || s.updatedAt > best.updatedAt ? s : best),
        null,
      );
      id = recent ? recent.id : "";
      if (!id) {
        const s: ImageSession = { id: newImageChatId(), title: "New image", turns: [], updatedAt: Date.now() };
        imageSessions.set([s]);
        id = s.id;
      }
      activeImageChatId.set(id);
    }
  }
  initImageChats();

  // The turns of the active thread come from the store (single source of truth);
  // generation writes back by session id so a thread survives switching away.
  let activeSession = $derived($imageSessions.find((s) => s.id === $activeImageChatId));
  let turns = $derived(activeSession?.turns ?? []);

  // Session the generation loop is writing to (null = idle). Mirrored to the store
  // so the rail can flag the generating row. One thread generates at a time.
  let genId = $state<string | null>(null);
  let isGenerating = $derived(genId !== null);
  $effect(() => {
    generatingImageChatId.set(genId);
  });

  // --- store helpers: turns live in imageSessions, keyed by session id ---
  function sessionById(id: string): ImageSession | undefined {
    return get(imageSessions).find((s) => s.id === id);
  }
  function patchSession(id: string, fields: Partial<ImageSession>, bump = false) {
    imageSessions.update((ss) => {
      const i = ss.findIndex((s) => s.id === id);
      if (i === -1) return ss; // session deleted — don't resurrect it
      const copy = [...ss];
      copy[i] = { ...copy[i], ...fields, ...(bump ? { updatedAt: Date.now() } : {}) };
      return copy;
    });
  }
  function appendTurn(id: string, turn: Turn) {
    const s = sessionById(id);
    if (!s) return;
    const nextTurns = [...s.turns, turn];
    const title = s.titled ? s.title : deriveImageTitle(nextTurns);
    patchSession(id, { turns: nextTurns, title }, true);
  }
  function updateTurn(id: string, ti: number, patch: Partial<Turn>) {
    const s = sessionById(id);
    if (!s) return;
    patchSession(id, { turns: s.turns.map((t, i) => (i === ti ? { ...t, ...patch } : t)) });
  }
  function setTurns(id: string, next: Turn[], bump = false) {
    const s = sessionById(id);
    if (!s) return;
    const title = s.titled ? s.title : deriveImageTitle(next);
    patchSession(id, { turns: next, title }, bump);
  }

  let abortController = $state<AbortController | null>(null);
  let editingIdx = $state<number | null>(null);
  let editText = $state("");
  // Refs to each turn's rendered prompt span, so startEdit can capture its
  // actual width (see editWidth below).
  let promptEls: (HTMLElement | null)[] = $state([]);
  // A bare textarea has no intrinsic width from its content (only from `cols`,
  // default 20ch), so it collapses the shrink-to-fit user bubble down to ~5
  // words wide. Pin the textarea to the rendered prompt's width instead, so
  // the bubble stays the size it was.
  let editWidth = $state<number | null>(null);
  let showSettings = $state(false);
  let showNegative = $state(false);
  let fullscreenImg = $state<string | null>(null);
  let elapsed = $state(0);
  let step = $state(0);
  let totalSteps = $state(0);
  let secPerIt = $state(0);
  let stageLabel = $state("");
  let stagePhase = $state<"encode" | "cond" | "sample" | "decode" | null>(null);
  let threadEl = $state<HTMLDivElement | undefined>();
  let fileInput = $state<HTMLInputElement | undefined>();

  // Aspect ratio × long-edge → concrete WxH. The short edge is rounded to a
  // multiple of 64 (SD/VAE latent stride). One aspect + one size list beats a flat
  // grid of every WxH combo.
  const ASPECTS = [
    { value: "1:1", label: "Square 1:1", w: 1, h: 1 },
    { value: "4:3", label: "Landscape 4:3", w: 4, h: 3 },
    { value: "3:2", label: "Landscape 3:2", w: 3, h: 2 },
    { value: "16:9", label: "Wide 16:9", w: 16, h: 9 },
    { value: "3:4", label: "Portrait 3:4", w: 3, h: 4 },
    { value: "2:3", label: "Portrait 2:3", w: 2, h: 3 },
    { value: "9:16", label: "Tall 9:16", w: 9, h: 16 },
  ];
  const SIZE_TIERS = [512, 768, 1024, 1280, 1536, 2048];

  // Concrete [w,h] for an aspect id + long edge. Short edge snapped to /64.
  function aspectDims(aspectValue: string, longEdge: number): [number, number] {
    const a = ASPECTS.find((x) => x.value === aspectValue) ?? ASPECTS[0];
    if (a.w === a.h) return [longEdge, longEdge];
    const short = Math.max(64, Math.round((longEdge * Math.min(a.w, a.h)) / Math.max(a.w, a.h) / 64) * 64);
    return a.w > a.h ? [longEdge, short] : [short, longEdge];
  }
  const SAMPLER_OPTIONS = ["", "euler_a", "euler", "heun", "dpm2", "dpmpp2s_a", "dpmpp2m", "dpmpp2mv2", "ipndm", "ipndm_v", "lcm", "ddim_trailing", "tcd"].map(
    (v) => ({ value: v, label: v || "Default sampler" })
  );
  const SCHEDULER_OPTIONS = ["", "discrete", "karras", "exponential", "ays", "gits"].map(
    (v) => ({ value: v, label: v || "Auto for model" })
  );

  // Sensible per-model gen defaults, matched by id substring. Applied only when
  // the user switches models (not on reload) so manual tweaks survive a refresh.
  // ponytail: substring match, no backend "recommended params" field exists.
  // size/negative optional: SDXL-anime models need 1024 (512 duplicates) + their
  // booru quality-tag negative; distilled models leave both to the user's prefs.
  const SDXL_ANIME_NEG =
    "nsfw, lowres, (bad), text, error, fewer, extra, missing, worst quality, jpeg artifacts, low quality, watermark, unfinished, displeasing, oldest, early, chromatic aberration, signature, extra digits, artistic error, username, scan, [abstract]";
  // cfg here = sdapi cfg_scale (true CFG / txt_cfg); for flux-dev models keep it 1.0
  // and the DISTILLED guidance is baked server-side via autogen --guidance (the
  // /sdapi route has no per-request key for it). denoise = img2img strength.
  // maxDim = largest long edge this model handles; bigger tiers are greyed out in
  // the Size picker. Absent → DEFAULT_MAX_DIM (1536). 2048 is opt-in per model.
  const DEFAULT_MAX_DIM = 1536;
  const IMAGE_DEFAULTS: { match: string; steps: number; cfg: number; sampler: string; scheduler: string; size?: string; negative?: string; denoise?: number; maxDim?: number }[] = [
    { match: "z-image", steps: 10, cfg: 1.0, sampler: "euler", scheduler: "discrete" },
    // Kontext: surgical edit — low denoise so it doesn't redraw the whole scene.
    { match: "kontext", steps: 24, cfg: 1.0, sampler: "euler", scheduler: "discrete", denoise: 0.55 },
    // Qwen-Image-Edit Rapid: guidance-distilled (cfg MUST be 1.0 — cfg>1 burns to a
    // solid yellow frame). 8 steps undercooks; 20 is the sweet spot. Ref-image edit
    // (extra_images), so denoise unused.
    { match: "qwen-rapid", steps: 20, cfg: 1.0, sampler: "euler", scheduler: "discrete" },
    // Fill: inpaint — always fully regenerates the masked area (denoise 1.0).
    { match: "fill", steps: 20, cfg: 1.0, sampler: "euler", scheduler: "discrete", denoise: 1.0 },
    // AnimagineXL 3.1 / Illustrious SDXL-anime: Euler a, <30 steps, cfg 5-7, 1024.
    { match: "animagine", steps: 28, cfg: 7, sampler: "euler_a", scheduler: "discrete", size: "1024x1024", negative: SDXL_ANIME_NEG },
    { match: "illustrious", steps: 28, cfg: 7, sampler: "euler_a", scheduler: "discrete", size: "1024x1024", negative: SDXL_ANIME_NEG },
  ];
  function defaultsFor(id: string) {
    const l = id.toLowerCase();
    return IMAGE_DEFAULTS.find((d) => l.includes(d.match));
  }

  // Apply a preset model's safe gen defaults on FIRST load too, not only when the
  // model changes. Distilled models (Flux Kontext, Z-Image-Turbo) blow out to a
  // white image at the generic cfg=7; the persisted pref can carry that wrong
  // value in from another model, so re-assert the preset whenever the id differs
  // from what we last applied (null on mount → applies immediately). Non-preset
  // models (defaultsFor → undefined) keep the user's persisted values untouched.
  let lastModelForDefaults: string | null = null;
  $effect(() => {
    const id = $selectedModelStore;
    if (id === lastModelForDefaults) return;
    lastModelForDefaults = id;
    const d = defaultsFor(id);
    if (!d) return;
    $sdStepsStore = d.steps;
    $sdCfgScaleStore = d.cfg;
    $sdSamplerStore = d.sampler;
    $sdSchedulerStore = d.scheduler;
    if (d.size) {
      const [w, h] = d.size.split("x").map(Number);
      const r = w / h;
      $aspectStore = ASPECTS.reduce((best, a) =>
        Math.abs(a.w / a.h - r) < Math.abs(best.w / best.h - r) ? a : best
      ).value;
      $longEdgeStore = String(Math.max(w, h));
    }
    if (d.negative) $sdNegativePromptStore = d.negative;
    if (d.denoise !== undefined) $sdDenoiseStore = d.denoise;
  });

  // Elapsed tick so a slow (offloaded) generation looks alive.
  $effect(() => {
    if (!isGenerating) {
      elapsed = 0;
      return;
    }
    const start = Date.now();
    const id = setInterval(() => {
      elapsed = Math.floor((Date.now() - start) / 1000);
    }, 250);
    return () => clearInterval(id);
  });

  // Progress from sd-server's stdout (mirrored into upstreamLogs). A whole gen runs
  // several phases, each printing its own "N/M - Xs/it" bar: encode_first_stage
  // (VAE-encode the ref image), text-encode + weight streaming, the sampler, then
  // decode_first_stage (VAE-decode output). Only the sampler's total equals the
  // requested steps; the VAE bars have their own small tile counts. Naively taking
  // the newest N/M made the encode's "4/4" look like finished steps — so instead we
  // pick the live PHASE from the latest log marker and label the bar accordingly,
  // giving feedback during the slow encode/stream/decode stages too.
  $effect(() => {
    if (!isGenerating) {
      step = 0;
      totalSteps = 0;
      secPerIt = 0;
      stageLabel = "";
      stagePhase = null;
      return;
    }
    const tail = $upstreamLogs.slice(-6000);
    const expected = $sdStepsStore;

    // Live phase = the last marker present in the tail (they print in this order).
    const marks: [number, string, "encode" | "cond" | "sample" | "decode"][] = [
      [tail.lastIndexOf("EDIT mode"), "Encoding image…", "encode"],
      [tail.lastIndexOf("encode_first_stage completed"), "Encoding prompt…", "cond"],
      [
        Math.max(tail.lastIndexOf("get_learned_condition completed"), tail.lastIndexOf("generating image:")),
        "Sampling",
        "sample",
      ],
      [tail.lastIndexOf("decoding"), "Decoding…", "decode"],
    ];
    let cur: (typeof marks)[number] | null = null;
    for (const m of marks) if (m[0] >= 0 && (!cur || m[0] > cur[0])) cur = m;
    const phase = cur ? cur[2] : null;
    stageLabel = cur ? cur[1] : "Preparing…";
    stagePhase = phase;

    // Newest "N/M - Xs/it" bar. For sampling accept only total===steps (skip a stale
    // encode bar before the first sampler tick); for encode/decode take the VAE bar
    // (total!==steps); condition/prepare have no step bar → indeterminate.
    let picked: RegExpExecArray | null = null;
    if (phase === "sample" || phase === "encode" || phase === "decode") {
      const re = /(\d+)\/(\d+)\s*-\s*([\d.]+)s\/it/g;
      let m: RegExpExecArray | null;
      while ((m = re.exec(tail)) !== null) {
        const isSampler = +m[2] === expected;
        if (phase === "sample" ? isSampler : !isSampler) picked = m;
      }
    }
    if (picked) {
      step = +picked[1];
      totalSteps = +picked[2];
      secPerIt = +picked[3];
    } else {
      step = 0;
      totalSteps = 0;
      secPerIt = 0;
    }
  });

  let etaSec = $derived(totalSteps > 0 && secPerIt > 0 ? Math.round((totalSteps - step) * secPerIt) : 0);

  // Per-phase glyph for the in-flight bubble: read the source image, encode the
  // prompt, paint (sample), then develop (decode). null → the spinner fallback
  // (prepare / model load). All wear the reason-glow halo.
  let StageIcon = $derived(
    stagePhase === "encode"
      ? ImageDown
      : stagePhase === "cond"
        ? Type
        : stagePhase === "sample"
          ? Paintbrush
          : stagePhase === "decode"
            ? Sparkles
            : null,
  );

  // "90s" → "1m 30s"; sub-minute stays "45s". Image gens run minutes on an 8GB card.
  function fmtDur(s: number): string {
    if (s < 60) return `${s}s`;
    return `${Math.floor(s / 60)}m ${s % 60}s`;
  }

  let hasModels = $derived($models.some((m) => !m.unlisted));
  let isSdapi = $derived($apiModeStore === "sdapi");
  // Reference-edit models take the source as a ref (extra_images → ref_images),
  // NOT an img2img denoise base: Kontext, and Qwen-Image-Edit (incl. the distilled
  // "rapid" merges whose id drops the "edit" token, e.g. qwen-rapid-nsfw).
  let supportsRefImages = $derived(
    (() => {
      const l = $selectedModelStore.toLowerCase();
      return l.includes("kontext") || l.includes("qwen-image-edit") || l.includes("qwen-rapid");
    })()
  );
  let modelDefaults = $derived(defaultsFor($selectedModelStore));
  // Largest long edge the current model handles (tiers above are greyed out).
  let modelMax = $derived(modelDefaults?.maxDim ?? DEFAULT_MAX_DIM);
  let aspectOptions = $derived(ASPECTS.map((a) => ({ value: a.value, label: a.label })));
  // Size tiers for the chosen aspect, labelled with the concrete WxH; tiers over
  // the model's cap are disabled.
  let sizeOptions = $derived(
    SIZE_TIERS.map((L) => {
      const [w, h] = aspectDims($aspectStore, L);
      return { value: String(L), label: `${w}×${h}`, disabled: L > modelMax };
    })
  );
  // Aspect + long edge (clamped to the model cap) → the WxH every gen path reads.
  $effect(() => {
    const [w, h] = aspectDims($aspectStore, Math.min(Number($longEdgeStore) || 512, modelMax));
    $selectedSizeStore = `${w}x${h}`;
  });
  // The image the next turn tweaks: an attachment if present, else the last reply.
  let baseImage = $derived(
    attached[0] ?? [...turns].reverse().find((t) => t.images.length)?.images[0] ?? null
  );

  $effect(() => {
    playgroundStores.imageGenerating.set(isGenerating);
  });

  // Autoscroll the thread as turns/progress grow. stageLabel/totalSteps are deps
  // too: the in-flight bubble changes height when the stage text swaps and the
  // progress bar toggles, which would otherwise drift it into the bottom fade.
  $effect(() => {
    void turns.length;
    void isGenerating;
    void stageLabel;
    void totalSteps;
    if (threadEl) threadEl.scrollTop = threadEl.scrollHeight;
  });

  const stripB64 = (dataUrl: string) => dataUrl.replace(/^data:[^,]+,/, "");

  // Raw base64 for sd-server (extra_images / init_images want bytes, not a URL).
  // Fresh attachments/results are inline data: URLs; once persisted the server
  // rewrites them to /api/media/ paths (playground.go extractMedia), so a reused
  // source loaded from disk must be fetched back to bytes or it's sent as a path
  // and silently ignored.
  async function toB64(url: string): Promise<string> {
    if (url.startsWith("data:")) return stripB64(url);
    const blob = await (await fetch(url)).blob();
    const dataUrl = await new Promise<string>((res, rej) => {
      const fr = new FileReader();
      fr.onload = () => res(fr.result as string);
      fr.onerror = rej;
      fr.readAsDataURL(blob);
    });
    return stripB64(dataUrl);
  }

  async function genTxt2Img(promptText: string, refs: string[] | undefined, signal: AbortSignal): Promise<string[]> {
    const [w, h] = $selectedSizeStore.split("x").map(Number);
    const response = await generateSdImage(
      {
        model: $selectedModelStore,
        prompt: promptText,
        negative_prompt: $sdNegativePromptStore || undefined,
        width: w,
        height: h,
        steps: $sdStepsStore,
        cfg_scale: $sdCfgScaleStore,
        seed: $sdSeedStore,
        sampler_name: $sdSamplerStore || undefined,
        scheduler: $sdSchedulerStore || undefined,
        extra_images: refs && refs.length ? refs : undefined,
      },
      signal
    );
    return (response.images ?? []).map((img) => `data:image/png;base64,${img}`);
  }

  async function genImg2Img(promptText: string, initB64: string, mask: string | null, signal: AbortSignal): Promise<string[]> {
    const [w, h] = $selectedSizeStore.split("x").map(Number);
    const response = await generateSdImg2Img(
      {
        model: $selectedModelStore,
        prompt: promptText,
        negative_prompt: $sdNegativePromptStore || undefined,
        init_images: [initB64],
        denoising_strength: $sdDenoiseStore,
        width: w,
        height: h,
        steps: $sdStepsStore,
        cfg_scale: $sdCfgScaleStore,
        seed: $sdSeedStore,
        sampler_name: $sdSamplerStore || undefined,
        scheduler: $sdSchedulerStore || undefined,
        // Inpaint: only the white-painted region regenerates (invert 0 = as painted).
        ...(mask ? { mask: stripB64(mask), inpainting_mask_invert: 0 } : {}),
      },
      signal
    );
    return (response.images ?? []).map((img) => `data:image/png;base64,${img}`);
  }

  async function genOpenAi(promptText: string, signal: AbortSignal): Promise<string[]> {
    const response = await generateImage($selectedModelStore, promptText, $selectedSizeStore, signal);
    const d = response.data?.[0];
    if (!d) return [];
    if (d.b64_json) return [`data:image/png;base64,${d.b64_json}`];
    if (d.url) return [d.url];
    return [];
  }

  // The thread's pristine origin: the seed attachment of the first turn, else its
  // first generated image. Follow-up edits re-normalize their base back to this to
  // cancel the brightness/contrast drift that otherwise compounds every re-encode.
  function originOf(id: string): string | null {
    const t0 = sessionById(id)?.turns[0];
    return t0 ? (t0.refs[0] ?? t0.images[0] ?? null) : null;
  }

  // Dispatch a single generation from a prompt + the source images that feed it.
  // refs = attachments / the reused base (data URLs); empty = fresh txt2img.
  // origin = thread anchor for tone matching (null = nothing to match / this is it).
  async function generate(promptText: string, refs: string[], origin: string | null, mask: string | null, signal: AbortSignal): Promise<string[]> {
    if (!isSdapi) return genOpenAi(promptText, signal); // OpenAI route ignores sources
    const src = refs[0];
    if (!src) return genTxt2Img(promptText, undefined, signal);
    // Ref-edit models (Kontext, Qwen-Image-Edit) re-run off the previous output
    // each turn, and the model/VAE round-trip drifts brightness a little every
    // round → a chain darkens (or brightens) and compounds. Anchor the reused
    // base's BRIGHTNESS (mean only, matchContrast=false) back to the origin so the
    // drift becomes a one-time offset instead of compounding. Mean-only avoids the
    // contrast-stretch blowout full luma-matching caused. Skipped when src IS the
    // origin (first turn) or on any canvas failure.
    if (supportsRefImages) {
      let anchored = refs;
      if ($sdToneAnchorStore && origin && src !== origin) {
        try {
          anchored = [await matchColorToRef(src, origin, false), ...refs.slice(1)];
        } catch {
          /* keep the un-normalized base */
        }
      }
      return genTxt2Img(promptText, await Promise.all(anchored.map(toB64)), signal);
    }
    // img2img re-encodes the base into latent each turn → brightness/contrast
    // drift compounds. Anchor the base back to the origin's tone first (skipped
    // when src IS the origin or on any canvas failure — fall back raw).
    let base = src;
    if ($sdToneAnchorStore && origin && src !== origin) {
      try {
        base = await matchColorToRef(src, origin);
      } catch {
        /* keep the un-normalized base */
      }
    }
    return genImg2Img(promptText, await toB64(base), mask, signal);
  }

  // Run a turn already appended at index ti of session `id` and fold its result /
  // error back into the store. Writes by session id, so the reply lands even if
  // the user switched threads mid-generation. genId gates one turn at a time.
  async function runTurn(id: string, ti: number, promptText: string, refs: string[], mask: string | null, onAbort: () => void, prevTurns: Turn[]) {
    genId = id;
    abortController = new AbortController();
    try {
      const images = await generate(promptText, refs, originOf(id), mask, abortController.signal);
      updateTurn(id, ti, { images, secs: elapsed });
    } catch (err) {
      if (err instanceof Error && err.name === "AbortError") {
        // Restore the thread to exactly what it was before this run — for a fresh
        // send that drops the appended turn, for regenerate/edit it puts the
        // original turn (and its output) back rather than deleting it.
        patchSession(id, { turns: prevTurns });
        onAbort();
      } else {
        const msg = err instanceof Error ? err.message : "An error occurred";
        updateTurn(id, ti, { error: msg });
      }
    } finally {
      genId = null;
      abortController = null;
    }
  }

  async function send() {
    const promptText = prompt.trim();
    if (!$selectedModelStore || isGenerating || !promptText) return;
    const id = $activeImageChatId;
    if (!sessionById(id)) return;

    const base = baseImage;
    const wasAttached = attached;
    // Use the mask only if it was painted on this exact base (else it's stale).
    const useMask = !supportsRefImages && maskSource === base ? maskData : null;
    prompt = "";
    attached = [];
    maskData = null;
    maskSource = null;
    // Record what actually feeds this turn: attachments if present, else the
    // reused base image. OpenAI route ignores sources, so none there.
    const refs = !isSdapi ? [] : wasAttached.length ? wasAttached : base ? [base] : [];
    const prevTurns = sessionById(id)!.turns;
    const ti = prevTurns.length;
    appendTurn(id, { prompt: promptText, refs, images: [] });
    await runTurn(id, ti, promptText, refs, useMask, () => {
      prompt = promptText;
      attached = wasAttached;
      maskData = useMask;
      maskSource = useMask ? base : null;
    }, prevTurns);
  }

  // Edit a past prompt: rewrite it, drop that turn + everything after, re-run from
  // there reusing the same sources (mirrors the chat tab's edit-and-regenerate).
  async function saveEdit() {
    const idx = editingIdx;
    if (idx === null) return;
    const promptText = editText.trim();
    editingIdx = null;
    editText = "";
    if (isGenerating || !$selectedModelStore || !promptText) return;
    const id = $activeImageChatId;
    const s = sessionById(id);
    if (!s) return;
    const prevTurns = s.turns;
    const refs = prevTurns[idx].refs;
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: promptText, refs, images: [] }], true);
    await runTurn(id, idx, promptText, refs, null, () => {}, prevTurns);
  }

  // Re-run a turn with its same prompt + sources, dropping everything after it
  // (mirrors the chat tab's regenerate). A random seed yields a fresh image; a
  // pinned seed reproduces it.
  async function regenerate(idx: number) {
    if (isGenerating || !$selectedModelStore) return;
    const id = $activeImageChatId;
    const s = sessionById(id);
    const t = s?.turns[idx];
    if (!s || !t) return;
    const prevTurns = s.turns;
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: t.prompt, refs: t.refs, images: [] }], true);
    await runTurn(id, idx, t.prompt, t.refs, null, () => {}, prevTurns);
  }

  function startEdit(idx: number) {
    if (isGenerating) return;
    editingIdx = idx;
    editText = turns[idx].prompt;
    editWidth = promptEls[idx]?.clientWidth ?? null;
  }

  function cancelEdit() {
    editingIdx = null;
    editText = "";
  }

  function editKeyDown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void saveEdit();
    } else if (event.key === "Escape") {
      cancelEdit();
    }
  }

  // Stop = real stop. sd-server has no mid-image interrupt, so aborting the fetch
  // alone leaves the backend rendering. Unloading the model kills the process →
  // generation halts now. Cost: next gen cold-loads (~30-60s).
  function cancelGeneration() {
    abortController?.abort();
    const model = $selectedModelStore;
    if (model) unloadSingleModel(model).catch(() => {});
  }

  function newThread() {
    if (isGenerating) return;
    prompt = "";
    attached = [];
    const cur = sessionById($activeImageChatId);
    if (cur && cur.turns.length === 0) return; // already on a blank thread
    const s: ImageSession = { id: newImageChatId(), title: "New image", turns: [], updatedAt: Date.now() };
    imageSessions.update((ss) => [s, ...ss]);
    activeImageChatId.set(s.id);
  }

  function attachFiles(files: File[]) {
    maskData = null; // base changes → any painted mask is stale
    maskSource = null;
    for (const file of files) {
      const reader = new FileReader();
      reader.onload = () => (attached = [...attached, reader.result as string]);
      reader.readAsDataURL(file);
    }
  }

  function onAttachFiles(event: Event) {
    const input = event.target as HTMLInputElement;
    attachFiles(Array.from(input.files ?? []));
    input.value = "";
  }

  // Paste a screenshot / copied image straight into the composer, same as ChatInterface.
  function handlePaste(event: ClipboardEvent) {
    const items = event.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (const it of items) {
      if (it.kind === "file" && it.type.startsWith("image/")) {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (files.length === 0) return; // plain text → let the browser handle it
    event.preventDefault();
    attachFiles(files);
  }

  // Copy the rendered image to the clipboard as a PNG blob. copiedIdx flashes the
  // check on the turn that was copied. Silent no-op where the browser blocks
  // image clipboard writes.
  let copiedIdx = $state<number | null>(null);
  async function copyImage(dataUrl: string, ti: number) {
    try {
      const blob = await (await fetch(dataUrl)).blob();
      await navigator.clipboard.write([new ClipboardItem({ [blob.type]: blob })]);
      copiedIdx = ti;
      setTimeout(() => { if (copiedIdx === ti) copiedIdx = null; }, 1500);
    } catch {
      /* clipboard image write unsupported / denied */
    }
  }

  function downloadImage(img: string) {
    const link = document.createElement("a");
    link.href = img;
    link.download = `image-${Date.now()}.png`;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
  }

  function handleKeyDown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      send();
    }
  }
</script>

<div class="flex flex-col h-full">
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <ImageIcon class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No models configured. Add models to your configuration to generate images.</p>
    </div>
  {:else}
    <!-- Chat column — full-width so the pane scrolls; thread and composer are
         width-constrained and centered inside, matching the chat tab. -->
    <div class="flex-1 flex flex-col min-w-0 min-h-0 w-full">
      <!-- Thread -->
      <div bind:this={threadEl} class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b mb-4" use:scrollFade>
        <div class="w-full max-w-3xl mx-auto px-2 pt-4 flex flex-col gap-4 pb-8 {turns.length === 0 && !isGenerating ? 'h-full' : ''}">
          {#if turns.length === 0 && !isGenerating}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
              <ImageIcon class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Describe an image to start. Keep prompting to tweak it.</p>
            </div>
          {/if}

          {#each turns as t, ti (ti)}
            <!-- User prompt (right) — matches chat: black bubble, no avatar. Source
                 / reference images fed into this turn ride inside the bubble. -->
            <div class="flex justify-end">
              <div class="group relative max-w-[85%] rounded-2xl rounded-br-sm bg-[#141414] text-[#ededee] msg-tail-user px-3.5 py-2 flex flex-col gap-2">
                {#if t.refs.length}
                  <div class="flex flex-wrap gap-1.5">
                    {#each t.refs as ref, ri (ri)}
                      <button class="block rounded-lg overflow-hidden border border-white/15 cursor-zoom-in focus:outline-none" onclick={() => (fullscreenImg = ref)} aria-label="View reference image">
                        <img src={ref} alt="reference {ri + 1}" class="max-h-28 w-auto object-contain" />
                      </button>
                    {/each}
                  </div>
                {/if}
                {#if editingIdx === ti}
                  <div class="flex flex-col gap-2 min-w-[260px]">
                    <textarea
                      class="{editWidth ? '' : 'w-full'} px-2.5 py-1.5 rounded-lg bg-white/10 text-white text-[0.8125rem] resize-none overflow-hidden focus:outline-none focus:ring-2 focus:ring-white/40"
                      style={editWidth ? `width:${editWidth}px` : undefined}
                      rows="1"
                      bind:value={editText}
                      use:autogrow
                      onkeydown={editKeyDown}
                    ></textarea>
                    <div class="flex justify-end gap-1.5">
                      <button class="p-1.5 rounded hover:bg-white/20" onclick={cancelEdit} title="Cancel"><X class="w-4 h-4" /></button>
                      <button class="p-1.5 rounded hover:bg-white/20" onclick={saveEdit} title="Save & regenerate"><Save class="w-4 h-4" /></button>
                    </div>
                  </div>
                {:else}
                  <span class="text-[0.8125rem] leading-relaxed whitespace-pre-wrap pr-6" bind:this={promptEls[ti]}>{t.prompt}</span>
                  <button
                    class="absolute top-1.5 right-1.5 p-1 rounded-full opacity-0 group-hover:opacity-100 transition-all bg-white/10 text-white/70 hover:text-white hover:bg-white/25 disabled:hidden"
                    onclick={() => startEdit(ti)}
                    disabled={isGenerating}
                    title="Edit prompt"
                  >
                    <Pencil class="w-3 h-3" />
                  </button>
                {/if}
              </div>
            </div>
            <!-- Image reply (left) — matches chat: surface bubble, no avatar. -->
            <div class="flex flex-col items-start">
              <div class="relative group rounded-2xl rounded-bl-sm px-3 py-2 text-[0.8125rem] w-fit max-w-full sm:max-w-[60%]">
                {#if t.error}
                  <div class="text-red-500">{t.error}</div>
                {:else if t.images.length}
                  <div class="flex flex-wrap gap-2">
                    {#each t.images as img, ii (ii)}
                      <button class="block rounded-xl overflow-hidden border border-card-border bg-secondary cursor-zoom-in focus:outline-none" onclick={() => (fullscreenImg = img)} aria-label="View image fullscreen">
                        <img src={img} alt="generated {ti + 1}" class="max-h-56 w-auto object-contain" />
                      </button>
                    {/each}
                  </div>
                  <!-- Actions + timing, matching the chat tab's footer: divider, buttons
                       left, elapsed on the right. Acts on the first image (batch>1 isn't
                       exposed in the UI). -->
                  <div class="flex flex-wrap items-center gap-1 mt-2 pt-1 border-t border-card-border">
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary disabled:opacity-40"
                      onclick={() => regenerate(ti)}
                      disabled={isGenerating}
                      title="Regenerate"
                    >
                      <RefreshCw class="w-4 h-4" />
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
                      onclick={() => copyImage(t.images[0], ti)}
                      title={copiedIdx === ti ? "Copied!" : "Copy image"}
                    >
                      {#if copiedIdx === ti}<Check class="w-4 h-4 text-green-500" />{:else}<Copy class="w-4 h-4" />{/if}
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
                      onclick={() => downloadImage(t.images[0])}
                      title="Download"
                    >
                      <Download class="w-4 h-4" />
                    </button>
                    {#if t.secs != null}
                      <span class="ml-auto flex items-center self-center text-[0.6875rem] text-txtsecondary tabular-nums">{fmtDur(t.secs)}</span>
                    {/if}
                  </div>
                {:else if genId !== $activeImageChatId || ti !== turns.length - 1}
                  <div class="text-red-500">No image returned.</div>
                {:else}
                  <!-- In-flight: per-phase glowing icon + label, progress bar, then a
                       divider and the steps/time row (divider matches the finished
                       footer so the bubble keeps the same shape while generating). -->
                  <div class="flex flex-col gap-1.5 min-w-52">
                    <div class="flex items-center gap-2 text-txtsecondary">
                      {#if StageIcon}
                        <StageIcon class="w-4 h-4 reason-glow shrink-0" />
                      {:else}
                        <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                      {/if}
                      <span class="reason-shimmer-white font-medium">{stageLabel || "Generating…"}</span>
                    </div>
                    {#if totalSteps > 0}
                      <div class="h-1.5 w-full rounded bg-card-border overflow-hidden">
                        <div class="h-full bg-primary transition-all" style="width: {Math.round((step / totalSteps) * 100)}%"></div>
                      </div>
                    {/if}
                    <div class="flex items-center justify-between text-[0.6875rem] text-txtsecondary tabular-nums mt-1 pt-1 border-t border-card-border">
                      <span>{#if totalSteps > 0}{step}/{totalSteps} steps{/if}{#if etaSec > 0} · ~{fmtDur(etaSec)} left{/if}{#if totalSteps <= 0 && etaSec <= 0}&nbsp;{/if}</span>
                      <span>{fmtDur(elapsed)}</span>
                    </div>
                  </div>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </div>

      <!-- Composer — narrower than the thread, centered. -->
      {#snippet imageSettingsPanel()}
        <div class="flex flex-col gap-2">
          <div class="grid grid-cols-3 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">API</span>
              <Select
                bind:value={$apiModeStore}
                disabled={isGenerating}
                compact
                options={[
                  { value: "openai", label: "OpenAI" },
                  { value: "sdapi", label: "SDAPI" },
                ]}
              />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Aspect</span>
              <Select bind:value={$aspectStore} disabled={isGenerating} compact options={aspectOptions} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Size</span>
              <Select bind:value={$longEdgeStore} disabled={isGenerating} compact options={sizeOptions} />
            </div>
          </div>
          {#if isSdapi}
            <div class="grid grid-cols-3 gap-3">
              <div class="flex flex-col gap-1">
                <span class="text-xs uppercase tracking-wide text-txtsecondary">Steps</span>
                <input type="number" min="1" max="150" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdStepsStore} />
              </div>
              <div class="flex flex-col gap-1">
                <span class="text-xs uppercase tracking-wide text-txtsecondary">CFG</span>
                <input type="number" min="1" max="30" step="0.5" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdCfgScaleStore} />
              </div>
              <div class="flex flex-col gap-1">
                <span class="text-xs uppercase tracking-wide text-txtsecondary">Seed</span>
                <input type="number" min="-1" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdSeedStore} />
              </div>
            </div>
            {#if modelDefaults}
              <p class="text-xs text-txtsecondary -mt-1">Model default · {modelDefaults.steps} steps · cfg {modelDefaults.cfg} · {modelDefaults.sampler}</p>
            {/if}
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                Tweak strength · {$sdDenoiseStore.toFixed(2)}
                <span class="cursor-help opacity-60" title="How far each follow-up may stray from the previous image (non-Kontext models).">(?)</span>
              </span>
              <input type="range" min="0" max="1" step="0.05" class="w-full accent-primary" bind:value={$sdDenoiseStore} />
            </div>
            <div class="grid grid-cols-2 gap-3">
              <div class="flex flex-col gap-1">
                <span class="text-xs uppercase tracking-wide text-txtsecondary">Sampler</span>
                <Select bind:value={$sdSamplerStore} compact options={SAMPLER_OPTIONS} />
              </div>
              <div class="flex flex-col gap-1">
                <span class="text-xs uppercase tracking-wide text-txtsecondary">Scheduler</span>
                <Select bind:value={$sdSchedulerStore} compact options={SCHEDULER_OPTIONS} />
              </div>
            </div>
            <label class="flex items-center gap-2 cursor-pointer">
              <input type="checkbox" class="accent-primary" bind:checked={$sdToneAnchorStore} />
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                Tone anchor
                <span class="cursor-help opacity-60" title="Pin reused-source brightness to the thread's first image so chained edits don't drift darker/brighter. Off = raw model output.">(?)</span>
              </span>
            </label>
          {:else}
            <p class="text-xs text-txtsecondary">OpenAI image route generates fresh each turn — it can't tweak a previous image. Switch to SDAPI for the edit loop.</p>
          {/if}
        </div>
      {/snippet}

      {#snippet imageTopExtra()}
        {#if isSdapi && (showNegative || $sdNegativePromptStore)}
          <div class="flex items-start gap-2 pb-2 border-b border-card-border">
            <Ban class="w-3.5 h-3.5 mt-1.5 shrink-0 text-txtsecondary" />
            <textarea
              class="w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary min-h-[1.5rem] max-h-40 pretty-scroll"
              rows="1"
              placeholder="Negative — elements to avoid…"
              bind:value={$sdNegativePromptStore}
              disabled={isGenerating}
            ></textarea>
            <button
              class="mt-1 shrink-0 text-txtsecondary hover:text-txtmain transition-colors"
              onclick={() => { $sdNegativePromptStore = ""; showNegative = false; }}
              title="Remove negative prompt"
              aria-label="Remove negative prompt"
            ><X class="w-3.5 h-3.5" /></button>
          </div>
        {/if}
      {/snippet}

      {#snippet imageLeftButtons()}
        <button
          class="composer-icon-btn"
          onclick={() => fileInput?.click()}
          disabled={isGenerating}
          title={supportsRefImages ? "Attach reference image(s)" : "Attach a source image to edit"}
        >
          <Paperclip class="w-[1.125rem] h-[1.125rem]" />
        </button>
        {#if isSdapi && !supportsRefImages && baseImage}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors disabled:opacity-40 {maskData && maskSource === baseImage ? 'text-primary bg-secondary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
            onclick={() => (showMask = true)}
            disabled={isGenerating}
            title="Inpaint — mask a region to change (keeps the rest)"
          >
            <Brush class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
        {#if isSdapi && !(showNegative || $sdNegativePromptStore)}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
            onclick={() => (showNegative = true)}
            title="Add negative prompt"
          >
            <Ban class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
      {/snippet}

      {#snippet imageExtraRightButtons()}
        <button
          class="composer-icon-btn"
          onclick={newThread}
          disabled={isGenerating || turns.length === 0}
          title="New thread"
        >
          <Plus class="w-[1.125rem] h-[1.125rem]" />
        </button>
      {/snippet}

      <div class="shrink-0 relative w-full max-w-2xl mx-auto">
        {#if attached.length}
          <div class="flex flex-wrap gap-2 mb-2">
            {#each attached as img, i (i)}
              <div class="group relative w-14 h-14 rounded-lg overflow-hidden border border-card-border bg-secondary">
                <img src={img} alt="attachment {i + 1}" class="w-full h-full object-cover" />
                <button
                  class="absolute top-0 right-0 w-5 h-5 flex items-center justify-center bg-black/60 text-white rounded-bl opacity-0 group-hover:opacity-100 transition-opacity"
                  onclick={() => (attached = attached.filter((_, j) => j !== i))}
                  aria-label="Remove attachment {i + 1}"
                ><X class="w-3 h-3" /></button>
              </div>
            {/each}
          </div>
        {:else if baseImage && turns.length > 0}
          <p class="text-xs text-txtsecondary mb-2 px-2">{supportsRefImages ? "Editing the last image (reference)" : isSdapi ? "Editing the last image (img2img)" : "Fresh generation each turn"}</p>
        {/if}

        {#if maskData && maskSource === baseImage}
          <div class="flex items-center gap-2 mb-2 px-2 text-xs text-primary">
            <Brush class="w-3.5 h-3.5" />
            <span>Inpaint mask set — only the painted area changes</span>
            <button class="text-txtsecondary hover:text-txtmain" onclick={() => { maskData = null; maskSource = null; }}>clear</button>
          </div>
        {/if}

        <input type="file" accept="image/*" multiple class="hidden" bind:this={fileInput} onchange={onAttachFiles} />

        <Composer
          bind:value={prompt}
          placeholder={turns.length ? "Describe a change…" : "Describe the image you want…"}
          textareaDisabled={isGenerating}
          onKeydown={handleKeyDown}
          onPaste={handlePaste}
          bind:modelValue={$selectedModelStore}
          modelPlaceholder="Select an image model..."
          category="image"
          busy={isGenerating}
          onStop={cancelGeneration}
          stopTitle="Stop (unloads the model to interrupt)"
          bind:showSettings
          settingsTitle="Settings"
          topExtra={imageTopExtra}
          leftButtons={imageLeftButtons}
          extraRightButtons={imageExtraRightButtons}
          settingsPanel={imageSettingsPanel}
        />
      </div>
    </div>
  {/if}
</div>

{#if showMask && baseImage}
  <MaskEditor
    source={baseImage}
    initialMask={maskSource === baseImage ? maskData : null}
    onDone={(m) => { maskData = m; maskSource = m ? baseImage : null; showMask = false; }}
    onCancel={() => (showMask = false)}
  />
{/if}

<!-- Fullscreen viewer -->
{#if fullscreenImg}
  <div
    class="fixed inset-0 bg-black/90 z-50 flex items-center justify-center p-4"
    onclick={() => (fullscreenImg = null)}
    onkeydown={(e) => e.key === "Escape" && (fullscreenImg = null)}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
  >
    <button class="absolute top-4 right-4 text-white hover:text-gray-300 w-10 h-10 flex items-center justify-center rounded-full hover:bg-white/10 transition-colors" onclick={() => (fullscreenImg = null)} aria-label="Close">
      <X class="w-6 h-6" />
    </button>
    <img src={fullscreenImg} alt="fullscreen" class="max-w-full max-h-full object-contain" />
  </div>
{/if}

<style>
  /* Messenger-style bubble tail on the user prompt — matches the chat tab
     (ChatMessage.svelte .msg-tail-user). */
  .msg-tail-user::after {
    content: "";
    position: absolute;
    bottom: 0;
    right: -5px;
    width: 0;
    height: 0;
    border: 6px solid transparent;
    border-bottom: 0;
    border-left-color: #141414;
  }
</style>
