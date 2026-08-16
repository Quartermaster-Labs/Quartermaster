<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { get } from "svelte/store";
  import { models, upstreamLogs, unloadSingleModel } from "../../stores/api";
  import { userPref } from "../../stores/prefs";
  import { selectedTabStore } from "../../stores/playground";
  import {
    imageSessions,
    activeImageChatId,
    generatingImageChatId,
    newImageChatId,
    deriveImageTitle,
    type ImageSession,
    type Turn,
  } from "../../stores/imageHistory";
  import { generateImage, upscaleImage } from "../../lib/imageApi";
  import { generateSdImage, generateSdImg2Img, fetchSdLoras } from "../../lib/sdApi";
  import { matchColorToRef } from "../../lib/colorMatch";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import MaskEditor from "./MaskEditor.svelte";
  import Select from "./Select.svelte";
  import Composer from "./Composer.svelte";
  import { autogrow } from "../../lib/autogrow";
  import { Image as ImageIcon, X, Download, Paperclip, Ban, Plus, Pencil, Save, Copy, Check, RefreshCw, ImageDown, Type, Paintbrush, Sparkles, Brush, Palette, Reply, Maximize2, Loader2 } from "lucide-svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import type { ImageApiMode, SdApiLora, SdApiLoraRef } from "../../lib/types";
  import { ASPECTS, SIZE_TIERS, aspectDims, SAMPLER_OPTIONS, SCHEDULER_OPTIONS, DEFAULT_MAX_DIM, MAX_BATCH, defaultsFor, settingsFor, parseSdProgress, fmtDur } from "./imageGen";

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
  // Batch: how many images one prompt renders (sdapi `batch_size` → sd.cpp
  // batch_count). sd.cpp renders them one after another, incrementing the seed
  // per image — time is linear in the count. A pinned seed still reproduces
  // image 1; images 2..N are seed+1.. .
  const sdBatchStore = userPref<number>("playground-sdapi-batch", 1);
  const sdSamplerStore = userPref<string>("playground-sdapi-sampler", "");
  const sdSchedulerStore = userPref<string>("playground-sdapi-scheduler", "");
  // Tweak strength for the img2img follow-up path (non-Kontext models).
  const sdDenoiseStore = userPref<number>("playground-sdapi-denoise", 0.6);
  // Luma tone-anchoring of reused sources back to the thread origin (fights
  // brightness drift over chained edits). Toggle to A/B it against the raw model.
  const sdToneAnchorStore = userPref<boolean>("playground-sdapi-tone-anchor", true);
  // Keep the source image's native resolution on img2img (edit in place, don't
  // resize to the selected size). Falls back to selected size if dims unreadable.
  const sdKeepResStore = userPref<boolean>("playground-sdapi-keep-res", true);
  // Selected LoRAs, keyed per model then per LoRA `path` → strength. The key is
  // the list entry's `path` (what sd-server resolves against --lora-model-dir),
  // NOT its display `name`: a bare name is rejected with "invalid lora path".
  // LoRAs are
  // trained against one base checkpoint, so a selection must not leak across
  // models when the picker switches.
  const sdLoraStore = userPref<Record<string, Record<string, number>>>("playground-sdapi-loras", {});

  // Available LoRAs for the selected model, from GET /sdapi/v1/loras (sd-server
  // lists whatever sits in its --lora-model-dir, which autogen points at the
  // model's own folder by default). NOT fetched on model change: that route is
  // model-dispatched, so listing would swap the model in — the user asks for it.
  let loraList = $state<SdApiLora[]>([]);
  let loraListModel = $state("");
  let loraLoading = $state(false);
  let loraError = $state("");

  async function loadLoras() {
    const model = $selectedModelStore;
    if (!model) return;
    loraLoading = true;
    loraError = "";
    try {
      loraList = await fetchSdLoras(model);
      loraListModel = model;
      // Drop selections that aren't a listed `path` — chiefly entries saved by
      // the earlier name-keyed build, which the backend rejects outright.
      const valid = new Set(loraList.map((l) => l.path));
      const saved = $sdLoraStore[model] ?? {};
      const kept = Object.fromEntries(Object.entries(saved).filter(([p]) => valid.has(p)));
      if (Object.keys(kept).length !== Object.keys(saved).length) {
        $sdLoraStore = { ...$sdLoraStore, [model]: kept };
      }
    } catch (e) {
      loraError = e instanceof Error ? e.message : String(e);
    } finally {
      loraLoading = false;
    }
  }

  // The per-request refs sent with a generation. sd-server resolves `path`
  // against --lora-model-dir; multiplier 0 means "not applied", so it's dropped.
  let activeLoras = $derived.by<SdApiLoraRef[]>(() =>
    Object.entries($sdLoraStore[$selectedModelStore] ?? {})
      .filter(([, mult]) => mult !== 0)
      .map(([path, multiplier]) => ({ path, multiplier }))
  );

  function setLoraStrength(path: string, multiplier: number) {
    const model = $selectedModelStore;
    const forModel = { ...($sdLoraStore[model] ?? {}) };
    if (multiplier === 0) delete forModel[path];
    else forModel[path] = multiplier;
    $sdLoraStore = { ...$sdLoraStore, [model]: forModel };
  }

  let prompt = $state("");
  let promptEl = $state<HTMLTextAreaElement>();

  // Auto-grow the composer textarea by content, same as the chat tab. Guard
  // scrollHeight === 0 (this tab is display:none at mount) — otherwise the height
  // locks at 0px and never recovers, leaving an invisible textarea. CSS
  // min-h-[3rem]/max-h-[30rem] on Composer bound the range.
  $effect(() => {
    prompt;
    $selectedTabStore; // re-run when this tab becomes visible again
    if (promptEl) {
      promptEl.style.height = "auto";
      if (promptEl.scrollHeight > 0) promptEl.style.height = Math.min(promptEl.scrollHeight, 480) + "px";
    }
  });

  // Images the user attached to the NEXT message (data URLs) — seed a thread from
  // an existing picture. When empty, follow-ups reuse the last reply's image.
  let attached = $state<string[]>([]);
  // Inpaint mask for the NEXT message (PNG data URL): white = regenerate, black =
  // keep. maskSource pins which base it was painted on so a stale mask (base
  // changed) is dropped at send. img2img path only — Kontext ignores it.
  let maskData = $state<string | null>(null);
  let maskSource = $state<string | null>(null);
  let showMask = $state(false);
  // Style-transfer reference (data URL) for the NEXT message: appended as the LAST
  // ref image and scaffolds the prompt ("apply the style of the last reference").
  // Ref-edit models only (Qwen-Image-Edit multi-ref / Kontext); ignored elsewhere.
  let styleRef = $state<string | null>(null);
  let styleInput = $state<HTMLInputElement | undefined>();
  // A segmentation-capable model (SAM) unlocks the AI-select tools (box/point/
  // lasso) inside the inpaint MaskEditor — same mask output, loaded on demand via
  // /v1/segment. "" = brush-only.
  let segmentModel = $derived($models.find((m) => m.capabilities?.segmentation)?.id ?? "");

  // A preview of the base image with the masked region tinted, so the pending
  // mask (above the composer) and the sent turn show WHAT will be regenerated.
  let pendingMaskPreview = $state<string | null>(null);
  $effect(() => {
    const b = baseImage;
    const m = maskData;
    if (!b || !m || maskSource !== b) {
      pendingMaskPreview = null;
      return;
    }
    let cancelled = false;
    buildMaskOverlay(b, m).then((u) => { if (!cancelled) pendingMaskPreview = u; });
    return () => { cancelled = true; };
  });

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
  // Per-turn pick inside a batch (turn index → image index). Which image the
  // turn's action row acts on; view-only state, not persisted with the thread.
  let pickedImg = $state<Record<number, number>>({});
  let elapsed = $state(0);
  let step = $state(0);
  let totalSteps = $state(0);
  let secPerIt = $state(0);
  let stageLabel = $state("");
  let stagePhase = $state<"encode" | "cond" | "sample" | "decode" | null>(null);
  let threadEl = $state<HTMLDivElement | undefined>();
  let fileInput = $state<HTMLInputElement | undefined>();

  // Switching models resets the settings panel to that model's defaults — every
  // field, including the ones a preset omits (they fall back to GENERIC_DEFAULTS
  // via settingsFor). Carrying the previous model's values over silently
  // mis-renders: distilled models (Flux Kontext, Z-Image-Turbo, qwen-rapid) burn
  // out at the generic cfg=7, and the SDXL-anime booru negative leaks into
  // everything downstream of it.
  // Which model the panel was last reset for is PERSISTED, so a page reload
  // doesn't wipe hand-tuned values — only an actual model switch does.
  const defaultsModelStore = userPref<string>("playground-image-defaults-model", "");
  $effect(() => {
    const id = $selectedModelStore;
    if (!id || id === $defaultsModelStore) return;
    $defaultsModelStore = id;
    const d = settingsFor(id);
    $sdStepsStore = d.steps;
    $sdCfgScaleStore = d.cfg;
    $sdSamplerStore = d.sampler;
    $sdSchedulerStore = d.scheduler;
    $sdNegativePromptStore = d.negative;
    $sdDenoiseStore = d.denoise;
    // Aspect/long-edge are the user's framing choice, so only a preset that
    // names a size (SDXL-anime needs 1024 — 512 duplicates subjects) moves them.
    if (d.size) {
      const [w, h] = d.size.split("x").map(Number);
      const r = w / h;
      $aspectStore = ASPECTS.reduce((best, a) =>
        Math.abs(a.w / a.h - r) < Math.abs(best.w / best.h - r) ? a : best
      ).value;
      $longEdgeStore = String(Math.max(w, h));
    }
  });

  // Batch picks are indexed by turn position, which means something else in
  // another thread — drop them on a thread switch.
  $effect(() => {
    void $activeImageChatId;
    pickedImg = {};
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

  // Live gen progress, parsed out of sd-server stdout (see parseSdProgress).
  $effect(() => {
    if (!isGenerating) {
      step = 0;
      totalSteps = 0;
      secPerIt = 0;
      stageLabel = "";
      stagePhase = null;
      return;
    }
    const p = parseSdProgress($upstreamLogs.slice(-6000), $sdStepsStore);
    stageLabel = p.label;
    stagePhase = p.phase;
    step = p.step;
    totalSteps = p.totalSteps;
    secPerIt = p.secPerIt;
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
  // Clamp the persisted batch pref — a hand-edited pref (or an old value) must not
  // queue 200 renders behind one prompt.
  let batchCount = $derived(Math.min(MAX_BATCH, Math.max(1, Math.round($sdBatchStore || 1))));
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
  // When set, the next turn ignores the last reply and runs a fresh txt2img (a new
  // image in the same thread instead of an img2img edit). Cleared once the user
  // picks a base again (attach / reply) or after the send.
  let skipBase = $state(false);
  // The image the next turn tweaks: an attachment if present, else the last reply
  // (unless the user opted out via skipBase).
  let baseImage = $derived(
    attached[0] ?? (skipBase ? null : [...turns].reverse().find((t) => t.images.length)?.images[0]) ?? null
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

  // Decode an image's native pixel dims (for keep-resolution edits), snapped to
  // the model's latent grid (multiple of 64). Ref-edit models reflow the WHOLE
  // frame when output dims don't align to the VAE/patch grid, so an off-grid
  // native size (e.g. 1023×769) causes a full redraw instead of a local edit.
  // Accepts a data: URL or a same-origin media path.
  function imgDims(url: string): Promise<[number, number]> {
    return new Promise((res, rej) => {
      const im = new Image();
      im.onload = () => {
        let w = im.naturalWidth;
        let h = im.naturalHeight;
        // Clamp the long edge to the model cap so a big source (phone photo)
        // doesn't balloon the gen canvas → slow / VRAM spill. Preserve aspect.
        const long = Math.max(w, h);
        if (long > modelMax) {
          const s = modelMax / long;
          w = Math.round(w * s);
          h = Math.round(h * s);
        }
        const snap = (n: number) => Math.max(64, Math.round(n / 64) * 64);
        res([snap(w), snap(h)]);
      };
      im.onerror = rej;
      im.src = url;
    });
  }

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

  async function genTxt2Img(promptText: string, refs: string[] | undefined, signal: AbortSignal, dims?: [number, number]): Promise<string[]> {
    const [w, h] = dims ?? $selectedSizeStore.split("x").map(Number);
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
        batch_size: batchCount > 1 ? batchCount : undefined,
        sampler_name: $sdSamplerStore || undefined,
        scheduler: $sdSchedulerStore || undefined,
        extra_images: refs && refs.length ? refs : undefined,
        lora: activeLoras.length ? activeLoras : undefined,
      },
      signal
    );
    return (response.images ?? []).map((img) => `data:image/png;base64,${img}`);
  }

  async function genImg2Img(promptText: string, initB64: string, mask: string | null, signal: AbortSignal): Promise<string[]> {
    let [w, h] = $selectedSizeStore.split("x").map(Number);
    if ($sdKeepResStore) {
      try {
        [w, h] = await imgDims(`data:image/png;base64,${initB64}`);
      } catch {
        /* unreadable — fall back to selected size */
      }
    }
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
        batch_size: batchCount > 1 ? batchCount : undefined,
        sampler_name: $sdSamplerStore || undefined,
        scheduler: $sdSchedulerStore || undefined,
        lora: activeLoras.length ? activeLoras : undefined,
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
    // A painted mask forces the img2img+mask (inpaint) route below even for
    // ref-edit models — sd.cpp honors the mask there (unmasked region preserved),
    // which the extra_images ref path can't do (it redraws the whole frame).
    if (supportsRefImages && !mask) {
      let anchored = refs;
      if ($sdToneAnchorStore && origin && src !== origin) {
        try {
          anchored = [await matchColorToRef(src, origin, false), ...refs.slice(1)];
        } catch {
          /* keep the un-normalized base */
        }
      }
      let dims: [number, number] | undefined;
      if ($sdKeepResStore) {
        try {
          dims = await imgDims(src);
        } catch {
          /* unreadable — fall back to selected size */
        }
      }
      return genTxt2Img(promptText, await Promise.all(anchored.map(toB64)), signal, dims);
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

  function loadImg(src: string): Promise<HTMLImageElement> {
    return new Promise((resolve, reject) => {
      const im = new Image();
      im.onload = () => resolve(im);
      im.onerror = () => reject(new Error("image load failed"));
      im.src = src;
    });
  }

  // Composite the base image with its white-on-black mask, tinting the masked
  // region pink (matching the MaskEditor's on-canvas mask) so the user sees
  // exactly what region will be regenerated. Returns a PNG data URL at the base's
  // natural resolution.
  async function buildMaskOverlay(baseUrl: string, maskUrl: string): Promise<string> {
    const [b, m] = await Promise.all([loadImg(baseUrl), loadImg(maskUrl)]);
    const w = b.naturalWidth;
    const h = b.naturalHeight;
    const c = document.createElement("canvas");
    c.width = w;
    c.height = h;
    const x = c.getContext("2d")!;
    x.drawImage(b, 0, 0, w, h);
    const t = document.createElement("canvas");
    t.width = w;
    t.height = h;
    const tx = t.getContext("2d")!;
    tx.drawImage(m, 0, 0, w, h);
    const d = tx.getImageData(0, 0, w, h);
    const px = d.data;
    for (let i = 0; i < px.length; i += 4) {
      if (px[i] > 127) {
        px[i] = 236; px[i + 1] = 72; px[i + 2] = 153; px[i + 3] = 150; // #ec4899 pink-500 @ ~0.6
      } else {
        px[i + 3] = 0;
      }
    }
    tx.putImageData(d, 0, 0);
    x.drawImage(t, 0, 0);
    return c.toDataURL("image/png");
  }

  async function send() {
    const promptText = prompt.trim();
    if (!$selectedModelStore || isGenerating || !promptText) return;
    const id = $activeImageChatId;
    if (!sessionById(id)) return;

    const base = baseImage;
    const wasAttached = attached;
    // Style transfer needs the second-image ref slot, so it's ref-edit only.
    const useStyle = supportsRefImages ? styleRef : null;
    // Use the mask only if it was painted on this exact base (else it's stale).
    // A style ref forces the whole-frame ref path, so drop any pending mask.
    const useMask = !useStyle && maskSource === base ? maskData : null;
    prompt = "";
    attached = [];
    skipBase = false;
    maskData = null;
    maskSource = null;
    styleRef = null;
    // Record what actually feeds this turn: attachments if present, else the
    // reused base image. A style ref rides last (the scaffold points at it).
    // OpenAI route ignores sources, so none there.
    const contentRefs = !isSdapi ? [] : wasAttached.length ? wasAttached : base ? [base] : [];
    const refs = useStyle ? [...contentRefs, useStyle] : contentRefs;
    // Prepend the style instruction so the model applies the last ref's look to
    // the rest. Stored into the turn so regenerate/edit reproduce it verbatim.
    const sentPrompt = useStyle
      ? `Apply the artistic style, color palette, brushwork, and texture of the final reference image to the other image, keeping its content and composition. ${promptText}`.trim()
      : promptText;
    // Composite base + mask now so the sent turn shows the region that changed.
    const maskPreview = useMask && base ? await buildMaskOverlay(base, useMask) : undefined;
    const prevTurns = sessionById(id)!.turns;
    const ti = prevTurns.length;
    appendTurn(id, { prompt: sentPrompt, refs, images: [], maskPreview, model: $selectedModelStore });
    await runTurn(id, ti, sentPrompt, refs, useMask, () => {
      prompt = promptText;
      attached = wasAttached;
      maskData = useMask;
      maskSource = useMask ? base : null;
      styleRef = useStyle;
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
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: promptText, refs, images: [], model: $selectedModelStore }], true);
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
    // Label with the model that will ACTUALLY run this — generate() always uses
    // the current picker, so keeping the turn's old id after a model switch
    // credits the new image to the wrong model.
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: t.prompt, refs: t.refs, images: [], model: $selectedModelStore }], true);
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
    skipBase = false; // picking a base overrides the fresh-gen opt-out
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

  function onAttachStyle(event: Event) {
    const input = event.target as HTMLInputElement;
    const file = input.files?.[0];
    if (file) {
      const reader = new FileReader();
      reader.onload = () => (styleRef = reader.result as string);
      reader.readAsDataURL(file);
    }
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

  // Reply to a past image: attach it as the source/reference for the next prompt.
  // baseImage reads attached[0] first, so the next turn edits THIS image instead
  // of the latest reply. Clears any stale mask.
  function replyWithImage(img: string) {
    if (isGenerating) return;
    skipBase = false;
    attached = [img];
    maskData = null;
    maskSource = null;
  }

  // Upscale a finished image via the server's standalone ESRGAN runner and post
  // the result as a NEW turn below (source kept as the turn's ref). One at a time
  // (the server serializes runs too, to avoid stacking GPU memory).
  // Which image is upscaling (null = idle). Keyed by source so a message-image
  // button ("m<turn>") and a composer-attachment button ("a<idx>") don't share a
  // spinner. One at a time (the server serializes runs too).
  let upscaling = $state<string | null>(null);
  async function runUpscale(img: string, key: string) {
    if (upscaling !== null || isGenerating) return;
    const id = $activeImageChatId;
    if (!sessionById(id)) return;
    upscaling = key;
    const t0 = performance.now();
    try {
      // A saved image is a /api/media/<hash> URL (server splits inline base64 out
      // on save), not a data URL — fetch it back to bytes before sending.
      const b64 = await toB64(img);
      const result = await upscaleImage(b64, 4);
      appendTurn(id, { prompt: "Upscaled ×4", refs: [img], images: [result], model: "upscale", secs: Math.round((performance.now() - t0) / 1000) });
    } catch (e) {
      appendTurn(id, { prompt: "Upscaled ×4", refs: [img], images: [], model: "upscale", error: e instanceof Error ? e.message : String(e) });
    } finally {
      upscaling = null;
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
            <!-- picked = which image of a batch the reply/copy/download/upscale
                 actions act on; clamped so a shorter regenerated turn can't act
                 on a stale index. -->
            {@const picked = Math.min(pickedImg[ti] ?? 0, Math.max(0, t.images.length - 1))}
            <!-- User prompt (right) — matches chat: black bubble, no avatar. Source
                 / reference images fed into this turn ride inside the bubble. -->
            <div class="flex justify-end">
              <div class="group relative max-w-[85%] rounded-2xl rounded-br-sm bg-[#141414] text-[#ededee] msg-tail-user px-3.5 py-2 flex flex-col gap-2">
                {#if t.maskPreview}
                  <!-- Inpaint mask: base with the regenerated region highlighted
                       (replaces the plain reference — it IS the base image). -->
                  <button class="block self-start rounded-lg overflow-hidden border border-white/15 cursor-zoom-in focus:outline-none" onclick={() => (fullscreenImg = t.maskPreview ?? null)} aria-label="View inpaint mask">
                    <img src={t.maskPreview} alt="inpaint mask" class="max-h-28 w-auto object-contain" />
                  </button>
                {:else if t.refs.length}
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
                      <button class="p-1.5 rounded hover:bg-white/20" onclick={cancelEdit} use:tip={"Cancel"}><X class="w-4 h-4" /></button>
                      <button class="p-1.5 rounded hover:bg-white/20" onclick={saveEdit} use:tip={"Save & regenerate"}><Save class="w-4 h-4" /></button>
                    </div>
                  </div>
                {:else}
                  <span class="text-[0.8125rem] leading-relaxed whitespace-pre-wrap pr-6" bind:this={promptEls[ti]}>{t.prompt}</span>
                  <button
                    class="absolute top-1.5 right-1.5 p-1 rounded-full opacity-0 group-hover:opacity-100 transition-all bg-white/10 text-white/70 hover:text-white hover:bg-white/25 disabled:hidden"
                    onclick={() => startEdit(ti)}
                    disabled={isGenerating}
                    use:tip={"Edit prompt"}
                  >
                    <Pencil class="w-3 h-3" />
                  </button>
                {/if}
              </div>
            </div>
            <!-- Image reply (left) — matches chat: surface bubble, no avatar. -->
            <div class="flex flex-col items-start">
              {#if t.model}
                <span class="flex items-center gap-1 mb-1 px-3 text-[0.6875rem] font-medium text-txtsecondary">
                  <Sparkles class="w-3 h-3 shrink-0" />{t.model}
                </span>
              {/if}
              <div class="relative group rounded-2xl rounded-bl-sm px-3 py-2 text-[0.8125rem] w-fit max-w-full sm:max-w-[60%]">
                {#if t.images.length && !isGenerating}
                  <button
                    class="absolute top-1/2 left-full ml-2 -translate-y-1/2 z-10 p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary opacity-0 group-hover:opacity-100 transition-opacity"
                    onclick={() => replyWithImage(t.images[picked])}
                    use:tip={"Reply - use this image as the source/reference"}
                  >
                    <Reply class="w-4 h-4" />
                  </button>
                {/if}
                {#if t.error}
                  <div class="text-red-500">{t.error}</div>
                {:else if t.images.length}
                  <div class="flex flex-wrap gap-2">
                    {#each t.images as img, ii (ii)}
                      <div class="relative">
                        <button class="block rounded-xl overflow-hidden border {t.images.length > 1 && ii === picked ? 'border-primary' : 'border-card-border'} bg-secondary cursor-zoom-in focus:outline-none" onclick={() => (fullscreenImg = img)} aria-label="View image fullscreen">
                          <img src={img} alt="generated {ti + 1}" class="max-h-56 w-auto object-contain" />
                        </button>
                        {#if t.images.length > 1}
                          <!-- Batch picker. A separate badge, not the thumbnail itself:
                               clicking the image already means zoom, and stealing that
                               would break the single-image case. -->
                          <button
                            class="absolute top-1 left-1 min-w-5 px-1 py-0.5 rounded text-[0.625rem] font-medium tabular-nums {ii === picked ? 'bg-primary text-white' : 'bg-black/50 text-white/80 hover:bg-black/70'}"
                            onclick={() => (pickedImg[ti] = ii)}
                            use:tip={"Use this one for the actions below"}
                          >{ii + 1}</button>
                        {/if}
                      </div>
                    {/each}
                  </div>
                  <!-- Actions + timing, matching the chat tab's footer: divider, buttons
                       left, elapsed on the right. Acts on the picked image of the batch
                       (the first one unless a badge was clicked). -->
                  <div class="flex flex-wrap items-center gap-1 mt-2 pt-1 border-t border-card-border">
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary disabled:opacity-40"
                      onclick={() => regenerate(ti)}
                      disabled={isGenerating}
                      use:tip={"Regenerate"}
                    >
                      <RefreshCw class="w-4 h-4" />
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
                      onclick={() => copyImage(t.images[picked], ti)}
                      use:tip={copiedIdx === ti ? "Copied!" : "Copy image"}
                    >
                      {#if copiedIdx === ti}<Check class="w-4 h-4 text-green-500" />{:else}<Copy class="w-4 h-4" />{/if}
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
                      onclick={() => downloadImage(t.images[picked])}
                      use:tip={"Download"}
                    >
                      <Download class="w-4 h-4" />
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary disabled:opacity-40"
                      onclick={() => runUpscale(t.images[picked], "m" + ti)}
                      disabled={upscaling !== null || isGenerating}
                      use:tip={"Upscale ×4"}
                    >
                      {#if upscaling === "m" + ti}<Loader2 class="w-4 h-4 animate-spin" />{:else}<Maximize2 class="w-4 h-4" />{/if}
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
                      {#if batchCount > 1}
                        <!-- The step bar restarts once per batch image; say so, or a
                             bar going back to 0/N looks like a stall/restart. -->
                        <span class="text-[0.6875rem] tabular-nums">×{batchCount}</span>
                      {/if}
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
            <div class="grid grid-cols-2 gap-3">
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
              <div class="flex flex-col gap-1">
                <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                  Batch
                  <span class="cursor-help opacity-60" use:tip={`Images rendered per prompt (max ${MAX_BATCH}). They render one after another - N images take N× the time - and the seed increments per image, so a pinned seed reproduces the first one.`}>(?)</span>
                </span>
                <input type="number" min="1" max={MAX_BATCH} step="1" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$sdBatchStore} />
              </div>
            </div>
            {#if modelDefaults}
              <p class="text-xs text-txtsecondary -mt-1">Model default · {modelDefaults.steps} steps · cfg {modelDefaults.cfg} · {modelDefaults.sampler}</p>
            {/if}
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                Tweak strength · {$sdDenoiseStore.toFixed(2)}
                <span class="cursor-help opacity-60" use:tip={"How far each follow-up may stray from the previous image (non-Kontext models)."}>(?)</span>
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
                <span class="cursor-help opacity-60" use:tip={"Pin reused-source brightness to the thread's first image so chained edits don't drift darker/brighter. Off = raw model output."}>(?)</span>
              </span>
            </label>
            <label class="flex items-center gap-2 cursor-pointer">
              <input type="checkbox" class="accent-primary" bind:checked={$sdKeepResStore} />
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                Keep resolution
                <span class="cursor-help opacity-60" use:tip={"Edit at the source image's native size instead of resizing to the selected size."}>(?)</span>
              </span>
            </label>
            <!-- LoRAs. The list comes from the backend's --lora-model-dir, so it
                 needs the model loaded — fetched on demand, never automatically. -->
            <div class="flex flex-col gap-1 pt-1 border-t border-card-border">
              <div class="flex items-center justify-between">
                <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                  LoRAs
                  <span class="cursor-help opacity-60" use:tip={"Adapters found next to the model file. Listing them loads the model."}>(?)</span>
                </span>
                <button
                  class="text-xs text-primary hover:underline disabled:opacity-50"
                  onclick={loadLoras}
                  disabled={loraLoading || !$selectedModelStore}
                >{loraLoading ? "Loading…" : loraListModel === $selectedModelStore ? "Refresh" : "Load list"}</button>
              </div>
              {#if loraError}
                <p class="text-xs text-red-500">{loraError}</p>
              {:else if loraListModel === $selectedModelStore && loraList.length === 0}
                <p class="text-xs text-txtsecondary">No LoRAs in this model's folder.</p>
              {:else if loraListModel === $selectedModelStore}
                {#each loraList as lora (lora.path)}
                  {@const strength = $sdLoraStore[$selectedModelStore]?.[lora.path] ?? 0}
                  <div class="flex items-center gap-2">
                    <input
                      type="checkbox"
                      class="accent-primary"
                      checked={strength !== 0}
                      onchange={(e) => setLoraStrength(lora.path, (e.currentTarget as HTMLInputElement).checked ? 1 : 0)}
                    />
                    <span class="text-xs truncate flex-1" use:tip={lora.path}>{lora.name}</span>
                    <input
                      type="number"
                      min="-2"
                      max="2"
                      step="0.05"
                      class="w-16 px-1.5 py-0.5 text-xs rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary disabled:opacity-40"
                      disabled={strength === 0}
                      value={strength}
                      onchange={(e) => setLoraStrength(lora.path, Number((e.currentTarget as HTMLInputElement).value))}
                    />
                  </div>
                {/each}
              {:else if activeLoras.length}
                <p class="text-xs text-txtsecondary">{activeLoras.map((l) => `${l.path} @ ${l.multiplier}`).join(", ")}</p>
              {/if}
            </div>
          {:else}
            <p class="text-xs text-txtsecondary">OpenAI image route generates fresh each turn - it can't tweak a previous image. Switch to SDAPI for the edit loop.</p>
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
              placeholder="Negative - elements to avoid…"
              bind:value={$sdNegativePromptStore}
              disabled={isGenerating}
            ></textarea>
            <button
              class="mt-1 shrink-0 text-txtsecondary hover:text-txtmain transition-colors"
              onclick={() => { $sdNegativePromptStore = ""; showNegative = false; }}
              use:tip={"Remove negative prompt"}
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
          use:tip={supportsRefImages ? "Attach reference image(s)" : "Attach a source image to edit"}
        >
          <Paperclip class="w-[1.125rem] h-[1.125rem]" />
        </button>
        {#if isSdapi && baseImage}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors disabled:opacity-40 {maskData && maskSource === baseImage ? 'text-primary bg-secondary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
            onclick={() => (showMask = true)}
            disabled={isGenerating}
            use:tip={segmentModel ? "Inpaint - mask a region to change (brush or AI select)" : "Inpaint - mask a region to change (keeps the rest)"}
          >
            <Brush class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
        {#if isSdapi && supportsRefImages}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors disabled:opacity-40 {styleRef ? 'text-primary bg-secondary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
            onclick={() => styleInput?.click()}
            disabled={isGenerating}
            use:tip={"Style transfer - apply the look of a reference image to the edit"}
          >
            <Palette class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
        {#if isSdapi && !(showNegative || $sdNegativePromptStore)}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
            onclick={() => (showNegative = true)}
            use:tip={"Add negative prompt"}
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
          use:tip={"New thread"}
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
                <button
                  class="absolute top-0 left-0 w-5 h-5 flex items-center justify-center bg-black/60 text-white rounded-br opacity-0 group-hover:opacity-100 transition-opacity disabled:opacity-40 disabled:cursor-default"
                  onclick={() => runUpscale(img, "a" + i)}
                  disabled={upscaling !== null || isGenerating}
                  use:tip={"Upscale ×4"}
                >
                  {#if upscaling === "a" + i}<Loader2 class="w-3 h-3 animate-spin" />{:else}<Maximize2 class="w-3 h-3" />{/if}
                </button>
              </div>
            {/each}
          </div>
        {:else if baseImage && turns.length > 0}
          <p class="flex items-center gap-1.5 text-xs text-txtsecondary mb-2 px-2">
            <span>{supportsRefImages ? "Editing the last image (reference)" : isSdapi ? "Editing the last image (img2img)" : "Fresh generation each turn"}</span>
            {#if isSdapi}
              <button class="hover:text-txtmain transition-colors" onclick={() => (skipBase = true)} use:tip={"New image instead - don't edit the last one"}>
                <X class="w-3.5 h-3.5" />
              </button>
            {/if}
          </p>
        {:else if skipBase && turns.length > 0 && isSdapi}
          <p class="flex items-center gap-1.5 text-xs text-txtsecondary mb-2 px-2">
            <span>New image - not editing the last one</span>
            <button class="hover:text-txtmain transition-colors" onclick={() => (skipBase = false)} use:tip={"Edit the last image instead"}>
              <RefreshCw class="w-3.5 h-3.5" />
            </button>
          </p>
        {/if}

        {#if maskData && maskSource === baseImage}
          <div class="flex items-center gap-2.5 mb-2 px-2">
            {#if pendingMaskPreview}
              <button
                class="block rounded-lg overflow-hidden border border-card-border shrink-0 cursor-zoom-in focus:outline-none"
                onclick={() => (fullscreenImg = pendingMaskPreview)}
                aria-label="View inpaint mask"
              >
                <img src={pendingMaskPreview} alt="inpaint mask preview" class="h-14 w-auto object-contain" />
              </button>
            {/if}
            <div class="flex items-center gap-2 text-xs text-primary">
              <Brush class="w-3.5 h-3.5" />
              <span>Inpaint mask set - only the highlighted area changes</span>
              <button class="text-txtsecondary hover:text-txtmain" onclick={() => { maskData = null; maskSource = null; }}>clear</button>
            </div>
          </div>
        {/if}

        {#if styleRef && supportsRefImages}
          <div class="flex items-center gap-2.5 mb-2 px-2">
            <div class="relative w-14 h-14 rounded-lg overflow-hidden border border-primary bg-secondary shrink-0">
              <img src={styleRef} alt="style reference" class="w-full h-full object-cover" />
            </div>
            <div class="flex items-center gap-2 text-xs text-primary">
              <Palette class="w-3.5 h-3.5" />
              <span>Style reference set - its look is applied to the edit</span>
              <button class="text-txtsecondary hover:text-txtmain" onclick={() => (styleRef = null)}>clear</button>
            </div>
          </div>
        {/if}

        <input type="file" accept="image/*" multiple class="hidden" bind:this={fileInput} onchange={onAttachFiles} />
        <input type="file" accept="image/*" class="hidden" bind:this={styleInput} onchange={onAttachStyle} />

        <Composer
          bind:value={prompt}
          bind:textareaEl={promptEl}
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
    model={segmentModel}
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
