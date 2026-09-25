<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { get } from "svelte/store";
  import { models, upstreamLogs } from "../../stores/api";
  import { userPref } from "../../stores/prefs";
  import { selectedTabStore } from "../../stores/playground";
  import {
    videoSessions,
    activeVideoChatId,
    generatingVideoChatId,
    newVideoChatId,
    deriveVideoTitle,
    type VideoSession,
    type Turn,
  } from "../../stores/videoHistory";
  import {
    startVideoJob,
    awaitVideoJob,
    cancelVideoJob,
    videoSrc,
    isPlayable,
    type VideoJob,
  } from "../../lib/videoApi";
  import { fetchSdLoras } from "../../lib/sdApi";
  import type { SdApiLora, SdApiLoraRef } from "../../lib/types";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { vramTotals } from "../../stores/perf";
  import Select from "./Select.svelte";
  import Composer from "./Composer.svelte";
  import { autogrow } from "../../lib/autogrow";
  import { Film, X, Download, Ban, Plus, Pencil, Save, RefreshCw, Type, Paintbrush, Sparkles, Maximize2, ChevronsRight, ImagePlus, FlagTriangleRight, Loader2, Wand2, Undo2 } from "lucide-svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import { parseSdProgress } from "./imageGen";
  import { enhancePrompt } from "../../lib/promptEnhance";
  import {
    ASPECTS,
    tierDims,
    tierLabel,
    nearestTier,
    fitTier,
    nearestAspect,
    SAMPLER_OPTIONS,
    SCHEDULER_OPTIONS,
    VIDEO_TIERS,
    VIDEO_DEFAULT_MAX_DIM,
    clipLabel,
    supportsFrameRefs,
    vramWarning,
    frameOptionsFor,
    fpsOptionsFor,
    snapFrames,
    videoDefaultsFor,
    videoSettingsFor,
    videoStatusLabel,
    fmtDur,
  } from "./videoGen";

  // The Video tab: the Images tab's thread model with a clip in place of the
  // picture. Deliberately a close sibling of ImageInterface.svelte (same store
  // shape, same bubbles, same composer chrome) so the two stay recognisable.
  //
  // What is genuinely different, and why:
  //
  //   - The request is ASYNCHRONOUS. POST /sdcpp/v1/vid_gen returns a job id in
  //     milliseconds and the render runs for minutes behind it, so a turn holds
  //     a jobId and this component polls. The server keeps the model pinned for
  //     the life of the job (internal/server/videojobs.go).
  //   - STOP IS A REAL CANCEL. The Images tab has to unload the model to
  //     interrupt a render; here the job API has a cancel route, so a stop costs
  //     nothing (the model stays warm). sd.cpp may still refuse once the sampler
  //     has started, which is why the poll is aborted either way.
  //   - NO EDIT LOOP. There is no video img2img/ref path in this build, so there
  //     are no attachments, no mask, no style ref: every turn is a fresh render.

  const selectedModelStore = userPref<string>("playground-video-model", "");
  const selectedSizeStore = userPref<string>("playground-video-size", "640x384");
  const aspectStore = userPref<string>("playground-video-aspect", "16:9");
  // Keyed apart from the retired "playground-video-long" pref on purpose: that
  // one held a LONG edge, and reading a stored 640 as a 640p TIER would silently
  // promote an existing user from 640x384 to 1136x640 on first load.
  const tierStore = userPref<string>("playground-video-tier", "480");
  const negativePromptStore = userPref<string>("playground-video-negative", "");
  const stepsStore = userPref<number>("playground-video-steps", 20);
  const cfgScaleStore = userPref<number>("playground-video-cfg", 1);
  const seedStore = userPref<number>("playground-video-seed", -1);
  const framesStore = userPref<string>("playground-video-frames", "25");
  const fpsStore = userPref<string>("playground-video-fps", "24");
  const samplerStore = userPref<string>("playground-video-sampler", "");
  const schedulerStore = userPref<string>("playground-video-scheduler", "");
  // Selected LoRAs, keyed per model then per LoRA `path` -> strength. Keyed by
  // `path` (what sd-server resolves against --lora-model-dir) and not by the
  // display `name`, which the backend rejects as an invalid lora path. Per model
  // because a LoRA is trained against one base and must not leak across a switch.
  const videoLoraStore = userPref<Record<string, Record<string, number>>>("playground-video-loras", {});

  let prompt = $state("");
  let promptEl = $state<HTMLTextAreaElement>();

  // Auto-grow the composer textarea by content. Guard scrollHeight === 0 (this
  // tab is display:none at mount) exactly as the Images tab does.
  $effect(() => {
    prompt;
    $selectedTabStore;
    if (promptEl) {
      promptEl.style.height = "auto";
      if (promptEl.scrollHeight > 0) promptEl.style.height = Math.min(promptEl.scrollHeight, 480) + "px";
    }
  });

  function initVideoChats() {
    const sessions = get(videoSessions);
    let id = get(activeVideoChatId);
    if (!sessions.some((s) => s.id === id)) {
      const recent = sessions.reduce<VideoSession | null>(
        (best, s) => (!best || s.updatedAt > best.updatedAt ? s : best),
        null,
      );
      id = recent ? recent.id : "";
      if (!id) {
        const s: VideoSession = { id: newVideoChatId(), title: "New video", turns: [], updatedAt: Date.now() };
        videoSessions.set([s]);
        id = s.id;
      }
      activeVideoChatId.set(id);
    }
  }
  initVideoChats();

  let activeSession = $derived($videoSessions.find((s) => s.id === $activeVideoChatId));
  let turns = $derived(activeSession?.turns ?? []);

  let genId = $state<string | null>(null);
  let isGenerating = $derived(genId !== null);
  $effect(() => {
    generatingVideoChatId.set(genId);
  });

  // --- store helpers: turns live in videoSessions, keyed by session id ---
  function sessionById(id: string): VideoSession | undefined {
    return get(videoSessions).find((s) => s.id === id);
  }
  function patchSession(id: string, fields: Partial<VideoSession>, bump = false) {
    videoSessions.update((ss) => {
      const i = ss.findIndex((s) => s.id === id);
      if (i === -1) return ss; // session deleted - don't resurrect it
      const copy = [...ss];
      copy[i] = { ...copy[i], ...fields, ...(bump ? { updatedAt: Date.now() } : {}) };
      return copy;
    });
  }
  function appendTurn(id: string, turn: Turn) {
    const s = sessionById(id);
    if (!s) return;
    const nextTurns = [...s.turns, turn];
    const title = s.titled ? s.title : deriveVideoTitle(nextTurns);
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
    const title = s.titled ? s.title : deriveVideoTitle(next);
    patchSession(id, { turns: next, title }, bump);
  }

  let abortController = $state<AbortController | null>(null);
  // The job this component is currently polling. Kept out of the turn until it
  // finishes so a cancel has something to address without a store round-trip.
  let liveJobId = $state<string | null>(null);
  let jobStatus = $state<string>("");
  let queuePos = $state(0);
  let editingIdx = $state<number | null>(null);
  let editText = $state("");
  let promptEls: (HTMLElement | null)[] = $state([]);
  let editWidth = $state<number | null>(null);
  let showSettings = $state(false);
  let showNegative = $state(false);
  let fullscreenVid = $state<string | null>(null);
  let elapsed = $state(0);
  let step = $state(0);
  let totalSteps = $state(0);
  let secPerIt = $state(0);
  let stageLabel = $state("");
  let stagePhase = $state<"encode" | "cond" | "sample" | "decode" | null>(null);
  let threadEl = $state<HTMLDivElement | undefined>();

  // Switching models resets the settings panel to that model's defaults, same
  // reasoning as the Images tab: MiniMax-H3 conditions at cfg 1.0 and the
  // generic 5 gives mush, and a frame count carried over from another family is
  // silently realigned to a clip length the user did not ask for.
  const defaultsModelStore = userPref<string>("playground-video-defaults-model", "");
  $effect(() => {
    const id = $selectedModelStore;
    if (!id || id === $defaultsModelStore) return;
    $defaultsModelStore = id;
    const d = videoSettingsFor(id, $models.find((m) => m.id === id)?.genDefaults);
    $stepsStore = d.steps;
    $cfgScaleStore = d.cfg;
    $samplerStore = d.sampler;
    $schedulerStore = d.scheduler;
    $negativePromptStore = d.negative;
    $framesStore = String(d.frames);
    $fpsStore = String(d.fps);
    if (d.size) {
      const [w, h] = d.size.split("x").map(Number);
      $aspectStore = nearestAspect(w / h);
      $tierStore = String(nearestTier(Math.min(w, h)));
    }
  });

  // Elapsed tick. Load-bearing here, not decoration: the job API has no progress
  // field, so on a cold model (or while queued) this counter is the ONLY sign
  // the render is alive.
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

  // Step progress, parsed out of sd-server's stdout the same way the Images tab
  // does. The job document cannot supply it (see lib/videoApi.ts), but the
  // backend still prints its sampler bar, and that IS mirrored into upstreamLogs.
  $effect(() => {
    if (!isGenerating) {
      step = 0;
      totalSteps = 0;
      secPerIt = 0;
      stageLabel = "";
      stagePhase = null;
      return;
    }
    const p = parseSdProgress($upstreamLogs.slice(-6000), $stepsStore);
    stagePhase = p.phase;
    step = p.step;
    totalSteps = p.totalSteps;
    secPerIt = p.secPerIt;
    // While the backend has printed nothing yet, the job's own status is more
    // honest than "Preparing...": it distinguishes queued-behind-another-job
    // from a model that is still loading.
    stageLabel = p.phase ? p.label : videoStatusLabel(jobStatus, queuePos);
  });

  let etaSec = $derived(totalSteps > 0 && secPerIt > 0 ? Math.round((totalSteps - step) * secPerIt) : 0);

  let StageIcon = $derived(
    stagePhase === "cond" ? Type : stagePhase === "sample" ? Paintbrush : stagePhase === "decode" ? Sparkles : null,
  );

  let hasModels = $derived($models.some((m) => !m.unlisted));
  let modelGen = $derived($models.find((m) => m.id === $selectedModelStore)?.genDefaults);
  let modelPreset = $derived(videoDefaultsFor($selectedModelStore));
  let modelDefaults = $derived(
    modelPreset || modelGen ? videoSettingsFor($selectedModelStore, modelGen) : undefined
  );
  // Ceiling on the Size picker, FLOORED by the size the model actually launches
  // with. A UI cap must never silently clamp a backend default: without this
  // floor a model launched at 1360x768 was clamped to the generic 960 max and
  // rendered 960x512, while the picker's label still said 1360.
  let modelMax = $derived(
    Math.max(modelPreset?.maxDim ?? VIDEO_DEFAULT_MAX_DIM, modelGen?.width ?? 0, modelGen?.height ?? 0)
  );
  // First/last frame conditioning. Held as data: URLs so the thumbnails can show
  // them directly, and stripped to raw base64 at send time (sd-server wants
  // bytes, not a URL) exactly as the Images tab does for its references.
  let firstFrame = $state<string | null>(null);
  let lastFrame = $state<string | null>(null);
  let firstInput = $state<HTMLInputElement | null>(null);
  let lastInput = $state<HTMLInputElement | null>(null);
  let frameRefError = $state("");
  let frameRefs = $derived(supportsFrameRefs($selectedModelStore));

  // An END frame alone is meaningless: sd.cpp's --end-img help calls it
  // "required by flf2v", i.e. it pairs WITH a start frame. So dropping the start
  // drops the end with it, rather than leaving a chip that would be sent and
  // ignored.
  $effect(() => {
    if (!firstFrame && lastFrame) lastFrame = null;
  });

  // Prompt enhancement: the Images tab's rewrite button, wired to the video
  // directions. A first frame set means the render conditions on an image, so
  // the edit enhancer is the one that knows what to do with it; otherwise the
  // text enhancer. Either half alone covers both directions, since dropping the
  // button on a model that clearly has an enhancer reads as a bug, and the
  // rewrite is reviewable anyway.
  let enhancing = $state(false);
  // Its own slot rather than frameRefError: that one is cleared by the next
  // frame pick, and a failed rewrite should stay on screen until it is read.
  let enhanceError = $state("");
  // The pre-rewrite prompt, kept so one click undoes the rewrite. Cleared as
  // soon as the user edits the box themselves or sends, because after that
  // "revert" would throw away work rather than undo a machine edit.
  let preEnhance = $state<string | null>(null);
  // What the enhancer produced, so the revert offer can tell an untouched
  // rewrite from one the user has since edited.
  let enhancedText = $state<string | null>(null);
  // The aspect the enhancer moved the framing to, and what it was before. A
  // structured enhancer returns the ratio it wrote the prompt FOR, so applying
  // it keeps the two agreeing; but framing is a control the user sets by hand,
  // so a silent change is a control moving on its own. Shown, and reverted with
  // the prompt, so the whole rewrite undoes as one action.
  let enhancedAspect = $state<string | null>(null);
  let preEnhanceAspect = $state<string | null>(null);
  $effect(() => {
    if (preEnhance !== null && prompt !== enhancedText) {
      preEnhance = null;
      enhancedText = null;
      // Deliberately NOT reverting the aspect here. Editing the rewritten text
      // is accepting the rewrite and continuing from it, so the framing it was
      // written for should stay; only an explicit revert puts it back.
      enhancedAspect = null;
      preEnhanceAspect = null;
    }
  });

  // The rewrite model this video model opts into, resolved server-side. Absent
  // => the button does not render at all, rather than rendering disabled: an
  // enhancer is opt-in per model and most models will never have one.
  //
  // A model may name one per DIRECTION (Qwen ships PE-T2I and PE-I2I, which are
  // not interchangeable), so the pick follows the mode the render itself will
  // use: a first frame set means this is an image-to-video render.
  let enhancerPair = $derived($models.find((m) => m.id === $selectedModelStore));
  let enhancer = $derived(
    firstFrame
      ? (enhancerPair?.promptEnhancerEdit ?? enhancerPair?.promptEnhancer)
      : (enhancerPair?.promptEnhancer ?? enhancerPair?.promptEnhancerEdit),
  );

  // Nearest supported aspect to a free-form "W:H" the enhancer may answer with.
  // It can return a ratio the picker has no entry for, and refusing those would
  // drop the framing on exactly the layouts the field exists to describe.
  function snapAspect(ratio: string): string | null {
    const [w, h] = ratio.split(":").map((n) => Number(n.trim()));
    if (!(w > 0) || !(h > 0)) return null;
    return nearestAspect(w / h);
  }

  async function runEnhance() {
    if (!enhancer || enhancing || isGenerating) return;
    enhancing = true;
    enhanceError = "";
    try {
      // The frames are what this render conditions on, so a vision enhancer
      // gets to see them. A text-only rewriter ignores the list entirely.
      const refs = enhancer.vision
        ? [firstFrame, lastFrame].filter((x): x is string => !!x)
        : [];
      // Refs may be /api/media/ paths from a reloaded turn rather than data
      // URLs; enhancePrompt resolves them, the same way the render path's
      // toDataUrl does.
      const r = await enhancePrompt(enhancer, prompt, refs);
      prompt = r.prompt;
      enhancedText = r.prompt;
      preEnhance = r.original;
      // ratioFollow means "match an input frame", which the render already
      // does, so there is nothing to set and nothing to announce.
      const snapped = r.ratio && !r.ratioFollow ? snapAspect(r.ratio) : null;
      if (snapped && snapped !== $aspectStore) {
        preEnhanceAspect = $aspectStore;
        enhancedAspect = snapped;
        $aspectStore = snapped;
      }
    } catch (e) {
      enhanceError = e instanceof Error ? e.message : String(e);
    } finally {
      enhancing = false;
    }
  }

  function revertEnhance() {
    if (preEnhance === null) return;
    prompt = preEnhance;
    preEnhance = null;
    enhancedText = null;
    if (preEnhanceAspect !== null) $aspectStore = preEnhanceAspect;
    enhancedAspect = null;
    preEnhanceAspect = null;
  }

  // A frame reference as bytes the backend can decode.
  //
  // A fresh pick is already a data: URL, but once the turn is persisted the
  // server's extractMedia rewrites it to a /api/media/ path to keep the session
  // JSON small. Regenerating such a turn would otherwise post a PATH as
  // init_image, which sd-server drops without complaint: the render succeeds
  // and quietly ignores the conditioning.
  async function toDataUrl(url: string): Promise<string> {
    if (url.startsWith("data:")) return url;
    const blob = await (await fetch(url)).blob();
    return await new Promise<string>((res, rej) => {
      const fr = new FileReader();
      fr.onload = () => res(fr.result as string);
      fr.onerror = rej;
      fr.readAsDataURL(blob);
    });
  }

  function pickFrame(event: Event, which: "first" | "last") {
    const input = event.target as HTMLInputElement;
    const file = input.files?.[0];
    if (file) {
      const reader = new FileReader();
      reader.onload = () => {
        const url = reader.result as string;
        if (which === "first") firstFrame = url;
        else lastFrame = url;
        frameRefError = "";
      };
      reader.readAsDataURL(file);
    }
    input.value = "";
  }

  /**
   * The final frame of a rendered clip, as a data: URL.
   *
   * Done in the browser rather than the backend because the clip is already
   * here: a fresh render arrives as a data: URL and a saved one as a same-origin
   * /api/media path, so neither taints the canvas and no round trip is needed.
   */
  async function lastFrameOf(src: string): Promise<string> {
    const v = document.createElement("video");
    v.src = src;
    v.muted = true;
    v.playsInline = true;
    await new Promise<void>((res, rej) => {
      v.onloadeddata = () => res();
      v.onerror = () => rej(new Error("Could not read that clip."));
    });
    // A frame short of the end on purpose: seeking to exactly duration lands
    // past the last sample on some decoders, which never fires seeked and would
    // hang this promise forever.
    await new Promise<void>((res, rej) => {
      v.onseeked = () => res();
      v.onerror = () => rej(new Error("Could not seek that clip."));
      v.currentTime = Math.max(0, (v.duration || 0) - 1 / 30);
    });
    const c = document.createElement("canvas");
    c.width = v.videoWidth;
    c.height = v.videoHeight;
    const ctx = c.getContext("2d");
    if (!ctx || !c.width || !c.height) throw new Error("Could not decode that clip's frames.");
    ctx.drawImage(v, 0, 0);
    return c.toDataURL("image/png");
  }

  // Chain one clip onto the next: its last frame becomes the next render's first
  // frame. Clears any END frame, which belonged to the render just finished and
  // would otherwise pull the continuation back to where it started.
  async function continueFrom(src: string) {
    try {
      firstFrame = await lastFrameOf(src);
      lastFrame = null;
      frameRefError = "";
    } catch (e) {
      frameRefError = e instanceof Error ? e.message : String(e);
    }
  }

  // The card's real size, for the feasibility warning. The playground app never
  // polls /api/performance (admin-only; App.svelte), so here this is normally the
  // 24GB fallback, which can only change the COLOUR of a row, never whether it
  // can be picked.
  let vramGB = $derived(($vramTotals?.totalMb ?? 0) / 1024 || 24);

  let aspectOptions = $derived(ASPECTS.map((a) => ({ value: a.value, label: a.label })));
  // Every tier is listed; the ones this install cannot reach are disabled rather
  // than hidden, so the ceiling is visible instead of mysterious. No off-grid
  // fallback rung is needed any more: a model's launched size is adopted as the
  // NEAREST TIER, so the bound value is always one of these.
  let sizeOptions = $derived(
    VIDEO_TIERS.map((t) => {
      const [w, h] = tierDims($aspectStore, t, $selectedModelStore);
      const warn = vramWarning(w, h, Number($framesStore) || 1, vramGB, $selectedModelStore);
      return {
        value: String(t),
        label: tierLabel($aspectStore, t, $selectedModelStore),
        disabled: Math.max(w, h) > modelMax,
        warn: !!warn,
        title: warn || undefined,
      };
    })
  );
  $effect(() => {
    // Fit a whole tier rather than clamping the pixels: clamping mid-calculation
    // is what once rendered 960x512 under a label reading 1360.
    const t = fitTier($aspectStore, Number($tierStore) || 480, $selectedModelStore, modelMax);
    const [w, h] = tierDims($aspectStore, t, $selectedModelStore);
    $selectedSizeStore = `${w}x${h}`;
  });
  // Both pickers are family-scoped: H3 aligns frames to 17k+5 and is hard-wired
  // to 24 fps, LTX aligns DOWN to 8k+1 and runs to its specified 20s (481f at
  // 24 fps), everything else is 4n+1 and free. The snap effect exists because
  // the stores are PERSISTED prefs, so a value picked under one family survives
  // a switch to another and would otherwise leave the Select showing blank.
  let frameOptions = $derived(frameOptionsFor($selectedModelStore));
  // Length rungs, priced against the CURRENT canvas: the two knobs multiply, so
  // which lengths are affordable changes every time the size does.
  let lengthOptions = $derived(
    frameOptions.map((f) => {
      const [w, h] = $selectedSizeStore.split("x").map(Number);
      const warn = vramWarning(w || 640, h || 384, f, vramGB, $selectedModelStore);
      return {
        value: String(f),
        label: clipLabel(f, Number($fpsStore) || 1),
        warn: !!warn,
        title: warn || undefined,
      };
    })
  );
  let fpsOptions = $derived(fpsOptionsFor($selectedModelStore));
  $effect(() => {
    if (!frameOptions.includes(Number($framesStore))) {
      $framesStore = String(snapFrames(Number($framesStore), $selectedModelStore));
    }
    if (!fpsOptions.includes(Number($fpsStore))) $fpsStore = String(fpsOptions[0]);
  });

  // Available LoRAs for the selected model, from GET /sdapi/v1/loras. sd-server
  // lists whatever sits in its --lora-model-dir, which autogen points at the
  // model gguf's own folder, so a turbo LoRA dropped next to the checkpoint is
  // zero-config. NOT fetched on model change: that route is model-dispatched, so
  // listing would swap a multi-gigabyte video model in. The user asks for it.
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
      // Drop saved selections the backend no longer lists (a deleted file, or a
      // row the model-file filter now strips), which would fail the render.
      const valid = new Set(loraList.map((l) => l.path));
      const saved = $videoLoraStore[model] ?? {};
      const kept = Object.fromEntries(Object.entries(saved).filter(([p]) => valid.has(p)));
      if (Object.keys(kept).length !== Object.keys(saved).length) {
        $videoLoraStore = { ...$videoLoraStore, [model]: kept };
      }
    } catch (e) {
      loraError = e instanceof Error ? e.message : String(e);
    } finally {
      loraLoading = false;
    }
  }

  // The refs sent with a render. multiplier 0 means "not applied", so it drops.
  let activeLoras = $derived.by<SdApiLoraRef[]>(() =>
    Object.entries($videoLoraStore[$selectedModelStore] ?? {})
      .filter(([, mult]) => mult !== 0)
      .map(([path, multiplier]) => ({ path, multiplier }))
  );

  function setLoraStrength(path: string, multiplier: number) {
    const model = $selectedModelStore;
    const forModel = { ...($videoLoraStore[model] ?? {}) };
    if (multiplier === 0) delete forModel[path];
    else forModel[path] = multiplier;
    $videoLoraStore = { ...$videoLoraStore, [model]: forModel };
  }

  $effect(() => {
    playgroundStores.videoGenerating.set(isGenerating);
  });

  $effect(() => {
    void turns.length;
    void isGenerating;
    void stageLabel;
    void totalSteps;
    if (threadEl) threadEl.scrollTop = threadEl.scrollHeight;
  });

  // One render, start to finish. Returns the playable data: URL, or throws with
  // whatever the backend said went wrong.
  async function generate(promptText: string, refs: string[], signal: AbortSignal): Promise<{ src: string; job: VideoJob }> {
    const [w, h] = $selectedSizeStore.split("x").map(Number);
    // refs[0] is always the start frame and refs[1] the optional end frame: an
    // end frame cannot exist without a start, so the position is unambiguous.
    // Resolved back to bytes here rather than at pick time because a turn
    // reloaded from disk carries /api/media/ paths, and a path sent as
    // init_image is not an error, it is dropped in silence.
    const initRef = frameRefs && refs[0] ? await toDataUrl(refs[0]) : undefined;
    const endRef = frameRefs && refs[1] ? await toDataUrl(refs[1]) : undefined;
    const job = await startVideoJob(
      {
        model: $selectedModelStore,
        prompt: promptText,
        negative_prompt: $negativePromptStore || undefined,
        width: w,
        height: h,
        video_frames: snapFrames(Number($framesStore), $selectedModelStore),
        fps: Number($fpsStore),
        seed: $seedStore,
        lora: activeLoras.length ? activeLoras : undefined,
        // Sent as the data: URL, unstripped: sd-server's own client posts
        // `init_image: <dataUrl>` to this route.
        init_image: initRef,
        end_image: endRef,
        sample_params: {
          sample_steps: $stepsStore,
          sample_method: $samplerStore || undefined,
          scheduler: $schedulerStore || undefined,
          guidance: { txt_cfg: $cfgScaleStore },
        },
      },
      signal,
    );
    liveJobId = job.id;
    jobStatus = job.status;
    queuePos = job.queue_position ?? 0;
    const done = await awaitVideoJob(
      job.id,
      (j) => {
        jobStatus = j.status;
        queuePos = j.queue_position ?? 0;
      },
      signal,
    );
    if (done.status !== "completed") {
      throw new Error(done.error?.message || `Render ${done.status}`);
    }
    if (!isPlayable(done.result)) {
      // A clip in a container no browser plays is a config problem, not a
      // transient failure: say which format came back rather than mounting a
      // <video> that shows a silent black box.
      throw new Error(
        `Backend returned ${done.result?.output_format || done.result?.mime_type || "an unknown format"}, which the browser cannot play. Set --output-format to webm or mp4 on this model.`,
      );
    }
    return { src: videoSrc(done.result), job: done };
  }

  async function runTurn(id: string, ti: number, promptText: string, refs: string[], onAbort: () => void, prevTurns: Turn[]) {
    genId = id;
    abortController = new AbortController();
    try {
      const { src, job } = await generate(promptText, refs, abortController.signal);
      updateTurn(id, ti, {
        videos: [src],
        secs: elapsed,
        jobId: job.id,
        frames: job.result?.frame_count,
        fps: job.result?.fps,
      });
    } catch (err) {
      if (err instanceof Error && err.name === "AbortError") {
        patchSession(id, { turns: prevTurns });
        onAbort();
      } else {
        const msg = err instanceof Error ? err.message : "An error occurred";
        updateTurn(id, ti, { error: msg });
      }
    } finally {
      genId = null;
      abortController = null;
      liveJobId = null;
      jobStatus = "";
      queuePos = 0;
    }
  }

  async function send() {
    const promptText = prompt.trim();
    if (!$selectedModelStore || isGenerating || !promptText) return;
    const id = $activeVideoChatId;
    if (!sessionById(id)) return;
    prompt = "";
    // The frames are CONSUMED by the send, like the prompt text: they move out
    // of the composer and into the turn that used them. Leaving them in the
    // composer would silently re-condition the next render on a frame belonging
    // to the previous one, which is the opposite of what chaining wants.
    const refs = frameRefs && firstFrame ? (lastFrame ? [firstFrame, lastFrame] : [firstFrame]) : [];
    firstFrame = null;
    lastFrame = null;
    const prevTurns = sessionById(id)!.turns;
    const ti = prevTurns.length;
    appendTurn(id, { prompt: promptText, refs, videos: [], model: $selectedModelStore });
    await runTurn(id, ti, promptText, refs, () => {
      prompt = promptText;
      // Put them back if the send was abandoned, so an aborted render does not
      // cost the user their picks.
      firstFrame = refs[0] ?? null;
      lastFrame = refs[1] ?? null;
    }, prevTurns);
  }

  async function saveEdit() {
    const idx = editingIdx;
    if (idx === null) return;
    const promptText = editText.trim();
    editingIdx = null;
    editText = "";
    if (isGenerating || !$selectedModelStore || !promptText) return;
    const id = $activeVideoChatId;
    const s = sessionById(id);
    if (!s) return;
    const prevTurns = s.turns;
    // Frame conditioning rides along with the edit: changing the words should
    // not quietly change what the clip starts from.
    const refs = prevTurns[idx].refs;
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: promptText, refs, videos: [], model: $selectedModelStore }], true);
    await runTurn(id, idx, promptText, refs, () => {}, prevTurns);
  }

  async function regenerate(idx: number) {
    if (isGenerating || !$selectedModelStore) return;
    const id = $activeVideoChatId;
    const s = sessionById(id);
    const t = s?.turns[idx];
    if (!s || !t) return;
    const prevTurns = s.turns;
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: t.prompt, refs: t.refs, videos: [], model: $selectedModelStore }], true);
    await runTurn(id, idx, t.prompt, t.refs, () => {}, prevTurns);
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

  // Stop: cancel the JOB, then stop polling. Unlike the Images tab this does not
  // unload the model - the backend owns an interruptible queue here, so the next
  // render still starts warm. sd.cpp refuses a cancel once the sampler is
  // running; the poll is dropped either way, and the orphaned render releases
  // its own lease when the server's watcher sees it finish.
  function cancelGeneration() {
    const id = liveJobId;
    if (id) cancelVideoJob(id);
    abortController?.abort();
  }

  function newThread() {
    if (isGenerating) return;
    prompt = "";
    const cur = sessionById($activeVideoChatId);
    if (cur && cur.turns.length === 0) return; // already on a blank thread
    const s: VideoSession = { id: newVideoChatId(), title: "New video", turns: [], updatedAt: Date.now() };
    videoSessions.update((ss) => [s, ...ss]);
    activeVideoChatId.set(s.id);
  }

  function downloadVideo(src: string, t: Turn) {
    const link = document.createElement("a");
    link.href = src;
    // Extension from the data: URL's own mime, so a webm is not saved as .mp4.
    const mime = src.startsWith("data:") ? src.slice(5, src.indexOf(";")) : "";
    const ext = mime.includes("webm") ? "webm" : mime.includes("mp4") ? "mp4" : src.split(".").pop() || "mp4";
    link.download = `video-${t.jobId || Date.now()}.${ext}`;
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

<div class="relative flex flex-col h-full">
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <Film class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No models configured. Add models to your configuration to generate video.</p>
    </div>
  {:else}
    <div class="flex-1 flex flex-col min-w-0 min-h-0 w-full">
      <!-- Thread -->
      <div bind:this={threadEl} class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b mb-2" use:scrollFade>
        <div class="w-full max-w-3xl mx-auto px-2 pt-4 flex flex-col gap-4 pb-2 {turns.length === 0 && !isGenerating ? 'h-full' : ''}">
          {#if turns.length === 0 && !isGenerating}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
              <Film class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Describe a scene to start. Each prompt renders a fresh clip.</p>
            </div>
          {/if}

          {#each turns as t, ti (ti)}
            <!-- User prompt (right) - same bubble as the Images tab. -->
            <div class="flex justify-end">
              <div class="group relative max-w-[85%] rounded-2xl rounded-br-none bg-[#141414] text-[#ededee] px-3.5 py-2 flex flex-col gap-2">
                <!-- The frames this render was conditioned on, inside the bubble
                     with the prompt: they were part of the message, so this is
                     where they belong once it is sent. -->
                {#if t.refs.length}
                  <div class="flex flex-wrap gap-1.5">
                    {#each t.refs as ref, ri (ri)}
                      <div class="relative rounded-lg overflow-hidden border border-white/15">
                        <img src={ref} alt={ri === 0 ? "start frame" : "end frame"} class="max-h-28 w-auto object-contain" />
                        <span class="absolute bottom-0 inset-x-0 bg-black/60 text-white text-[0.5625rem] text-center leading-tight">
                          {ri === 0 ? "Start" : "End"}
                        </span>
                      </div>
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
            <!-- Video reply (left). -->
            <div class="flex flex-col items-start">
              {#if t.model}
                <span class="flex items-center gap-1 mb-1 px-3 text-[0.6875rem] font-medium text-txtsecondary">
                  <Sparkles class="w-3 h-3 shrink-0" />{t.model}
                </span>
              {/if}
              <div class="relative group rounded-2xl rounded-bl-sm px-3 py-2 text-[0.8125rem] w-fit max-w-full sm:max-w-[60%]">
                {#if t.error}
                  <div class="text-error">{t.error}</div>
                {:else if t.videos.length}
                  <!-- The clip itself: native controls, loop on, and
                       preload="metadata" so a thread of saved clips does not
                       pull every byte back through /api/media at once. -->
                  <div class="rounded-xl overflow-hidden border border-card-border bg-secondary">
                    <!-- svelte-ignore a11y_media_has_caption -->
                    <video
                      src={t.videos[0]}
                      class="max-h-72 w-auto max-w-full"
                      controls
                      loop
                      playsinline
                      preload="metadata"
                    ></video>
                  </div>
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
                      onclick={() => downloadVideo(t.videos[0], t)}
                      use:tip={"Download"}
                    >
                      <Download class="w-4 h-4" />
                    </button>
                    <button
                      class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
                      onclick={() => (fullscreenVid = t.videos[0])}
                      use:tip={"View large"}
                    >
                      <Maximize2 class="w-4 h-4" />
                    </button>
                    {#if frameRefs}
                      <button
                        class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary disabled:opacity-40"
                        onclick={() => continueFrom(t.videos[0])}
                        disabled={isGenerating}
                        use:tip={"Continue from here: load this clip's last frame as the next render's first frame"}
                      >
                        <ChevronsRight class="w-4 h-4" />
                      </button>
                    {/if}
                    {#if t.frames}
                      <span class="flex items-center self-center text-[0.6875rem] text-txtsecondary tabular-nums">
                        {t.frames}f{#if t.fps} @ {t.fps}fps{/if}
                      </span>
                    {/if}
                    {#if t.secs != null}
                      <span class="ml-auto flex items-center self-center text-[0.6875rem] text-txtsecondary tabular-nums">{fmtDur(t.secs)}</span>
                    {/if}
                  </div>
                {:else if genId !== $activeVideoChatId || ti !== turns.length - 1}
                  <div class="text-error">No video returned.</div>
                {:else}
                  <!-- In-flight. The bar only appears once sd-server prints a
                       sampler line; until then the status label plus the elapsed
                       counter carry the whole signal. -->
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

      <!-- Composer -->
      {#snippet videoSettingsPanel()}
        <div class="flex flex-col gap-2">
          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Aspect</span>
              <Select bind:value={$aspectStore} disabled={isGenerating} compact options={aspectOptions} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Size</span>
              <Select bind:value={$tierStore} disabled={isGenerating} compact options={sizeOptions} />
            </div>
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                Length
                <span class="cursor-help opacity-60" use:tip={"How long the clip plays. Seconds are frames divided by fps, so the rungs are not round numbers: the backend's grid is defined in FRAMES (17k+5 for MiniMax-H3, 4n+1 for the rest) and it rounds anything off-grid UP, which is why only exact values are offered. Time and VRAM both scale with length."}>(?)</span>
              </span>
              <Select
                bind:value={$framesStore}
                disabled={isGenerating}
                compact
                options={lengthOptions}
              />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">FPS</span>
              <Select
                bind:value={$fpsStore}
                disabled={isGenerating}
                compact
                options={fpsOptions.map((f) => ({ value: String(f), label: String(f) }))}
              />
            </div>
          </div>
          <div class="grid grid-cols-3 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Steps</span>
              <input type="number" min="1" max="150" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$stepsStore} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                CFG
                <span class="cursor-help opacity-60" use:tip={"Guidance. MiniMax-H3 is conditioned at 1.0 and washes out above it; Wan wants about 5. Nothing rejects a bad value, it just renders badly."}>(?)</span>
              </span>
              <input type="number" min="1" max="30" step="0.5" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$cfgScaleStore} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Seed</span>
              <input type="number" min="-1" class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$seedStore} />
            </div>
          </div>
          {#if modelDefaults}
            <p class="text-xs text-txtsecondary -mt-1">
              Model default · {modelDefaults.steps} steps · cfg {modelDefaults.cfg} · {modelDefaults.frames}f @ {modelDefaults.fps}fps{modelDefaults.size ? ` · ${modelDefaults.size}` : ""}
            </p>
          {/if}
          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Sampler</span>
              <Select bind:value={$samplerStore} compact options={SAMPLER_OPTIONS} />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">Scheduler</span>
              <Select bind:value={$schedulerStore} compact options={SCHEDULER_OPTIONS} />
            </div>
          </div>
          <!-- LoRAs. The list comes from the backend's --lora-model-dir, so it
               needs the model loaded: fetched on demand, never automatically.
               A turbo LoRA here is what makes a 4-step render correct, which is
               why Steps stays at the base model's 20 until one is selected. -->
          <div class="flex flex-col gap-1 pt-1 border-t border-card-border">
            <div class="flex items-center justify-between">
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                LoRAs
                <span class="cursor-help opacity-60" use:tip={"Adapters found next to the model file. Listing them loads the model. A turbo LoRA (4 or 8 step) also needs Steps lowered to match."}>(?)</span>
              </span>
              <button
                class="text-xs text-primary hover:underline disabled:opacity-50"
                onclick={loadLoras}
                disabled={loraLoading || !$selectedModelStore}
              >{loraLoading ? "Loading…" : loraListModel === $selectedModelStore ? "Refresh" : "Load list"}</button>
            </div>
            {#if loraError}
              <p class="text-xs text-error">{loraError}</p>
            {:else if loraListModel === $selectedModelStore && loraList.length === 0}
              <p class="text-xs text-txtsecondary">No LoRAs in this model's folder.</p>
            {:else if loraListModel === $selectedModelStore}
              {#each loraList as lora (lora.path)}
                {@const strength = $videoLoraStore[$selectedModelStore]?.[lora.path] ?? 0}
                <div class="flex items-center gap-2">
                  <input
                    type="checkbox"
                    class="accent-primary"
                    checked={strength !== 0}
                    disabled={isGenerating}
                    onchange={(e) => setLoraStrength(lora.path, (e.currentTarget as HTMLInputElement).checked ? 1 : 0)}
                  />
                  <span class="text-xs truncate flex-1" use:tip={lora.path}>{lora.name}</span>
                  <input
                    type="number"
                    min="-2"
                    max="2"
                    step="0.05"
                    class="w-16 px-1.5 py-0.5 text-xs rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary disabled:opacity-40"
                    disabled={strength === 0 || isGenerating}
                    value={strength}
                    onchange={(e) => setLoraStrength(lora.path, Number((e.currentTarget as HTMLInputElement).value))}
                  />
                </div>
              {/each}
            {:else if activeLoras.length}
              <p class="text-xs text-txtsecondary">{activeLoras.map((l) => `${l.path} @ ${l.multiplier}`).join(", ")}</p>
            {/if}
          </div>
        </div>
      {/snippet}

      {#snippet videoTopExtra()}
        {#if showNegative || $negativePromptStore}
          <div class="flex items-start gap-2 pb-2 border-b border-card-border">
            <Ban class="w-3.5 h-3.5 mt-1.5 shrink-0 text-txtsecondary" />
            <textarea
              class="w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary min-h-[1.5rem] max-h-40 pretty-scroll"
              rows="1"
              placeholder="Negative - elements to avoid…"
              bind:value={$negativePromptStore}
              disabled={isGenerating}
            ></textarea>
            <button
              class="mt-1 shrink-0 text-txtsecondary hover:text-txtmain transition-colors"
              onclick={() => { $negativePromptStore = ""; showNegative = false; }}
              use:tip={"Remove negative prompt"}
              aria-label="Remove negative prompt"
            ><X class="w-3.5 h-3.5" /></button>
          </div>
        {/if}
      {/snippet}

      {#snippet videoLeftButtons()}
        {#if enhancer}
          <button
            class="composer-icon-btn"
            onclick={runEnhance}
            disabled={enhancing || isGenerating || !prompt.trim()}
            use:tip={isGenerating
              ? "Wait for this render to finish: the enhancer is a separate model, and starting it now would make it queue behind the video model."
              : `Enhance the prompt with ${enhancer.name}${firstFrame ? " (first-frame rewrite)" : " (text-to-video rewrite)"}${enhancer.vision && firstFrame ? ", which reads the reference frame" : ""}. Rewrites the box, so you can read and edit it before rendering.`}
          >
            {#if enhancing}
              <Loader2 class="w-[1.125rem] h-[1.125rem] animate-spin" />
            {:else}
              <Wand2 class="w-[1.125rem] h-[1.125rem]" />
            {/if}
          </button>
          {#if preEnhance !== null}
            <button
              class="composer-icon-btn"
              onclick={revertEnhance}
              disabled={enhancing}
              use:tip={preEnhanceAspect !== null
                ? `Revert to the prompt you wrote, and the aspect ratio back to ${preEnhanceAspect}`
                : "Revert to the prompt you wrote"}
            >
              <Undo2 class="w-[1.125rem] h-[1.125rem]" />
            </button>
          {/if}
        {/if}
        <!-- Frame conditioning lives in the COMPOSER, not the settings panel:
             these are per-message inputs like an attachment, not a setting that
             persists across renders. Shown only for checkpoints that condition
             on frames, since a t2v model drops the fields rather than erroring,
             which would leave a paperclip that silently does nothing. -->
        {#if frameRefs}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors disabled:opacity-40 {firstFrame ? 'text-primary bg-secondary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
            onclick={() => firstInput?.click()}
            disabled={isGenerating}
            use:tip={"Start frame - the image the clip animates from"}
          >
            <ImagePlus class="w-[1.125rem] h-[1.125rem]" />
          </button>
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors disabled:opacity-40 {lastFrame ? 'text-primary bg-secondary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
            onclick={() => lastInput?.click()}
            disabled={isGenerating || !firstFrame}
            use:tip={firstFrame
              ? "End frame - the clip travels from the start frame to this one"
              : "End frame needs a start frame first - on its own there is nothing for the clip to travel from"}
          >
            <FlagTriangleRight class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
        {#if !(showNegative || $negativePromptStore)}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
            onclick={() => (showNegative = true)}
            use:tip={"Add negative prompt"}
          >
            <Ban class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
      {/snippet}

      {#snippet videoExtraRightButtons()}
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
        {#if frameRefs && (firstFrame || lastFrame)}
          <div class="flex flex-wrap items-center gap-2 mb-2">
            {#each [{ key: "first", src: firstFrame, label: "Start" }, { key: "last", src: lastFrame, label: "End" }] as slot (slot.key)}
              {#if slot.src}
                <div class="group relative w-14 h-14 rounded-lg overflow-hidden border border-card-border bg-secondary">
                  <img src={slot.src} alt="{slot.label} frame" class="w-full h-full object-cover" />
                  <span class="absolute bottom-0 inset-x-0 bg-black/60 text-white text-[0.5625rem] text-center leading-tight">{slot.label}</span>
                  <button
                    class="absolute top-0 right-0 w-5 h-5 flex items-center justify-center bg-black/60 text-white rounded-bl opacity-0 group-hover:opacity-100 transition-opacity"
                    onclick={() => (slot.key === "first" ? (firstFrame = null) : (lastFrame = null))}
                    aria-label="Remove {slot.label.toLowerCase()} frame"
                  ><X class="w-3 h-3" /></button>
                </div>
              {/if}
            {/each}
            <span class="text-xs text-txtsecondary">
              {lastFrame ? "Travelling from the start frame to the end frame" : "Animating from the start frame"}
            </span>
          </div>
        {/if}
        {#if frameRefError}
          <p class="text-xs text-error mb-2 px-2">{frameRefError}</p>
        {/if}

        {#if enhanceError}
          <div class="mb-2 p-2 bg-error/10 text-error rounded text-sm flex items-start gap-2">
            <span class="flex-1">{enhanceError}</span>
            <button class="shrink-0 opacity-70 hover:opacity-100" onclick={() => (enhanceError = "")} aria-label="Dismiss">
              <X class="w-3.5 h-3.5" />
            </button>
          </div>
        {/if}

        <!-- A control moved on its own, so it says so. Without this the aspect
             picker silently disagrees with what the user last set it to. -->
        {#if enhancedAspect}
          <div class="mb-2 p-2 bg-surface-2 border border-card-border text-txtsecondary rounded text-sm flex items-start gap-2">
            <span class="flex-1">
              {enhancer?.name ?? "The enhancer"} wrote this prompt for <strong class="text-txtmain">{enhancedAspect}</strong>, so the aspect ratio was changed to match.
            </span>
            <button class="shrink-0 opacity-70 hover:opacity-100" onclick={() => (enhancedAspect = null)} aria-label="Dismiss">
              <X class="w-3.5 h-3.5" />
            </button>
          </div>
        {/if}

        <input type="file" accept="image/*" class="hidden" bind:this={firstInput} onchange={(e) => pickFrame(e, "first")} />
        <input type="file" accept="image/*" class="hidden" bind:this={lastInput} onchange={(e) => pickFrame(e, "last")} />
        <Composer
          bind:value={prompt}
          bind:textareaEl={promptEl}
          placeholder={turns.length ? "Describe another scene…" : "Describe the video you want…"}
          textareaDisabled={isGenerating}
          onKeydown={handleKeyDown}
          bind:modelValue={$selectedModelStore}
          modelPlaceholder="Select a video model..."
          category="video"
          busy={isGenerating}
          onStop={cancelGeneration}
          stopTitle="Cancel this render (model stays loaded)"
          bind:showSettings
          settingsTitle="Settings"
          topExtra={videoTopExtra}
          leftButtons={videoLeftButtons}
          extraRightButtons={videoExtraRightButtons}
          settingsPanel={videoSettingsPanel}
        />
      </div>
    </div>
  {/if}
</div>

<!-- Fullscreen viewer -->
{#if fullscreenVid}
  <div
    class="fixed inset-0 bg-black/90 z-50 flex items-center justify-center p-4"
    onclick={() => (fullscreenVid = null)}
    onkeydown={(e) => e.key === "Escape" && (fullscreenVid = null)}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
  >
    <button class="absolute top-4 right-4 text-white hover:text-gray-300 w-10 h-10 flex items-center justify-center rounded-full hover:bg-white/10 transition-colors" onclick={() => (fullscreenVid = null)} aria-label="Close">
      <X class="w-6 h-6" />
    </button>
    <!-- svelte-ignore a11y_media_has_caption -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <video
      src={fullscreenVid}
      class="max-w-full max-h-full object-contain"
      controls
      autoplay
      loop
      playsinline
      onclick={(e) => e.stopPropagation()}
    ></video>
  </div>
{/if}
