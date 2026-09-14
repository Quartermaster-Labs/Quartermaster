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
  import { playgroundStores } from "../../stores/playgroundActivity";
  import Select from "./Select.svelte";
  import Composer from "./Composer.svelte";
  import { autogrow } from "../../lib/autogrow";
  import { Film, X, Download, Ban, Plus, Pencil, Save, RefreshCw, Type, Paintbrush, Sparkles, Maximize2 } from "lucide-svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import { parseSdProgress } from "./imageGen";
  import {
    ASPECTS,
    aspectDims,
    SAMPLER_OPTIONS,
    SCHEDULER_OPTIONS,
    VIDEO_SIZE_TIERS,
    VIDEO_DEFAULT_MAX_DIM,
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
  const longEdgeStore = userPref<string>("playground-video-long", "640");
  const negativePromptStore = userPref<string>("playground-video-negative", "");
  const stepsStore = userPref<number>("playground-video-steps", 20);
  const cfgScaleStore = userPref<number>("playground-video-cfg", 1);
  const seedStore = userPref<number>("playground-video-seed", -1);
  const framesStore = userPref<string>("playground-video-frames", "25");
  const fpsStore = userPref<string>("playground-video-fps", "24");
  const samplerStore = userPref<string>("playground-video-sampler", "");
  const schedulerStore = userPref<string>("playground-video-scheduler", "");

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
      const r = w / h;
      $aspectStore = ASPECTS.reduce((best, a) =>
        Math.abs(a.w / a.h - r) < Math.abs(best.w / best.h - r) ? a : best
      ).value;
      $longEdgeStore = String(Math.max(w, h));
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
  let modelMax = $derived(modelPreset?.maxDim ?? VIDEO_DEFAULT_MAX_DIM);
  let aspectOptions = $derived(ASPECTS.map((a) => ({ value: a.value, label: a.label })));
  let sizeOptions = $derived(
    VIDEO_SIZE_TIERS.map((L) => {
      const [w, h] = aspectDims($aspectStore, L);
      return { value: String(L), label: `${w}x${h}`, disabled: L > modelMax };
    })
  );
  $effect(() => {
    const [w, h] = aspectDims($aspectStore, Math.min(Number($longEdgeStore) || 640, modelMax));
    $selectedSizeStore = `${w}x${h}`;
  });
  // Clip length in seconds, shown next to the frame picker: frames alone say
  // nothing about how long the result plays, and the two knobs interact.
  let clipSeconds = $derived((Number($framesStore) || 1) / Math.max(1, Number($fpsStore) || 1));

  // Both pickers are family-scoped: H3 aligns frames to 17k+5 and is hard-wired
  // to 24 fps, everything else is 4n+1 and free. The snap effect exists because
  // the stores are PERSISTED prefs, so a value picked under one family survives
  // a switch to another and would otherwise leave the Select showing blank.
  let frameOptions = $derived(frameOptionsFor($selectedModelStore));
  let fpsOptions = $derived(fpsOptionsFor($selectedModelStore));
  $effect(() => {
    if (!frameOptions.includes(Number($framesStore))) {
      $framesStore = String(snapFrames(Number($framesStore), $selectedModelStore));
    }
    if (!fpsOptions.includes(Number($fpsStore))) $fpsStore = String(fpsOptions[0]);
  });

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
  async function generate(promptText: string, signal: AbortSignal): Promise<{ src: string; job: VideoJob }> {
    const [w, h] = $selectedSizeStore.split("x").map(Number);
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

  async function runTurn(id: string, ti: number, promptText: string, onAbort: () => void, prevTurns: Turn[]) {
    genId = id;
    abortController = new AbortController();
    try {
      const { src, job } = await generate(promptText, abortController.signal);
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
    const prevTurns = sessionById(id)!.turns;
    const ti = prevTurns.length;
    appendTurn(id, { prompt: promptText, refs: [], videos: [], model: $selectedModelStore });
    await runTurn(id, ti, promptText, () => { prompt = promptText; }, prevTurns);
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
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: promptText, refs: [], videos: [], model: $selectedModelStore }], true);
    await runTurn(id, idx, promptText, () => {}, prevTurns);
  }

  async function regenerate(idx: number) {
    if (isGenerating || !$selectedModelStore) return;
    const id = $activeVideoChatId;
    const s = sessionById(id);
    const t = s?.turns[idx];
    if (!s || !t) return;
    const prevTurns = s.turns;
    setTurns(id, [...prevTurns.slice(0, idx), { prompt: t.prompt, refs: [], videos: [], model: $selectedModelStore }], true);
    await runTurn(id, idx, t.prompt, () => {}, prevTurns);
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
                  <div class="text-red-500">{t.error}</div>
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
                  <div class="text-red-500">No video returned.</div>
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
              <Select bind:value={$longEdgeStore} disabled={isGenerating} compact options={sizeOptions} />
            </div>
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary flex items-center gap-1">
                Frames
                <span class="cursor-help opacity-60" use:tip={"Frames rendered. The backend rounds this UP onto the model family grid (17k+5 for MiniMax-H3, 4n+1 for the rest), so only exact values are offered. Time and VRAM both scale with it."}>(?)</span>
              </span>
              <Select
                bind:value={$framesStore}
                disabled={isGenerating}
                compact
                options={frameOptions.map((f) => ({ value: String(f), label: String(f) }))}
              />
            </div>
            <div class="flex flex-col gap-1">
              <span class="text-xs uppercase tracking-wide text-txtsecondary">FPS · {clipSeconds.toFixed(1)}s</span>
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
                <span class="cursor-help opacity-60" use:tip={"Guidance. Distilled models (MiniMax-H3) require 1.0 - the backend refuses to sample above it."}>(?)</span>
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
