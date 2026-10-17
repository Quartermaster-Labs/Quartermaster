<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import Toggle from "../Toggle.svelte";
  import Select from "../Select.svelte";
  import { get } from "svelte/store";
  import { models } from "../../stores/api";
  import { userPref } from "../../stores/prefs";
  import {
    musicSessions,
    activeMusicChatId,
    generatingMusicChatId,
    newMusicChatId,
    deriveMusicTitle,
    type MusicSession,
    type MusicTask,
    type Turn,
  } from "../../stores/musicHistory";
  import { generateMusic, editMusic, separateAudio, uploadMedia, fetchBlob, toWav, wavSeconds, type EditTask } from "../../lib/musicApi";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { matchesCapabilities } from "../../lib/modelUtils";
  import ModelSelector from "./ModelSelector.svelte";
  import {
    Music,
    Volume2,
    VolumeX,
    Download,
    RefreshCw,
    Upload,
    Square,
    MoreVertical,
    Trash2,
    Play,
    Dice5,
    Lock,
    ChevronDown,
    Wand2,
    Layers,
    SlidersHorizontal,
    X,
  } from "lucide-svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import AudioPlayer from "./AudioPlayer.svelte";
  import PaneHeader from "./PaneHeader.svelte";
  import { newMusicChat } from "../../lib/playgroundThreads";

  // Music studio, laid out like the Speech studio: a settings panel on the left,
  // the takes grid on the right with the prompt composer under it.
  //
  // Three modes, one per route family (lib/musicApi.ts):
  //   create  text to music (ACE-Step, MiniMax Music 3)
  //   edit    repaint a window / complete / cover an existing clip (ACE-Step)
  //   stems   split a clip into four stems (HTDemucs)
  // Each mode has its own model pick, filtered by capability, so switching modes
  // never leaves a model selected that the route would refuse.
  //
  // Every audio field a take holds is a /api/media ref (see stores/musicHistory.ts):
  // results are uploaded once as they land, and the thread only carries refs.

  type Mode = "create" | "edit" | "stems";
  const modeStore = userPref<Mode>("playground-music-mode", "create");
  // playground-music-model is also what the dashboard's "open in playground"
  // writes (PlaygroundApp TAB_MODEL_PREF), so it can arrive holding a separator.
  const genModelStore = userPref<string>("playground-music-model", "");
  const editModelStore = userPref<string>("playground-music-edit-model", "");
  const stemsModelStore = userPref<string>("playground-music-stems-model", "");
  const autoPlayStore = userPref<boolean>("playground-music-autoplay", false);
  const volumeStore = userPref<number>("playground-music-volume", 1);

  // Form fields that are worth keeping across reloads. The prompt and lyrics
  // are the song; the rest is how the user likes to work.
  const lyricsStore = userPref<string>("playground-music-lyrics", "");
  const durationStore = userPref<string>("playground-music-duration", "30");
  const languageStore = userPref<string>("playground-music-language", "");
  const stepsStore = userPref<string>("playground-music-steps", "");
  const guidanceStore = userPref<string>("playground-music-guidance", "");
  const bpmStore = userPref<string>("playground-music-bpm", "");
  const keyStore = userPref<string>("playground-music-key", "");
  const timeSigStore = userPref<string>("playground-music-timesig", "");
  const negativeStore = userPref<string>("playground-music-negative", "");
  const cotStore = userPref<string>("playground-music-cot", "");

  const COT_OPTIONS = [
    { value: "", label: "model (full)" },
    { value: "full", label: "full" },
    { value: "melody", label: "melody" },
    { value: "off", label: "off" },
  ];

  const CAP: Record<Mode, string> = { create: "music_generation", edit: "music_edit", stems: "audio_separation" };
  const STORE: Record<Mode, typeof genModelStore> = { create: genModelStore, edit: editModelStore, stems: stemsModelStore };

  let listed = $derived($models.filter((m) => !m.unlisted));
  let modelsFor = $derived({
    create: listed.filter((m) => matchesCapabilities(m, [CAP.create])),
    edit: listed.filter((m) => matchesCapabilities(m, [CAP.edit])),
    stems: listed.filter((m) => matchesCapabilities(m, [CAP.stems])),
  });
  let hasAny = $derived(modelsFor.create.length + modelsFor.edit.length + modelsFor.stems.length > 0);

  // A launch link that named a separator (or an editor) lands in the generation
  // pick; move it to the mode that can run it instead of leaving a model there
  // the generation route would refuse.
  $effect(() => {
    const id = $genModelStore;
    if (!id || !listed.length) return;
    const m = listed.find((x) => x.id === id);
    if (!m || matchesCapabilities(m, [CAP.create])) return;
    if (matchesCapabilities(m, [CAP.stems])) {
      stemsModelStore.set(id);
      modeStore.set("stems");
    }
    genModelStore.set("");
  });

  // Keep each mode's pick valid: an empty or vanished id falls back to the
  // first model that can serve the mode.
  $effect(() => {
    for (const mode of ["create", "edit", "stems"] as Mode[]) {
      const store = STORE[mode];
      const cur = get(store);
      const pool = modelsFor[mode];
      if (pool.length && !pool.some((m) => m.id === cur)) store.set(pool[0].id);
    }
  });

  // A mode with no model is hidden; fall back to one that has.
  let modes = $derived((["create", "edit", "stems"] as Mode[]).filter((m) => modelsFor[m].length > 0));
  $effect(() => {
    if (modes.length && !modes.includes($modeStore)) modeStore.set(modes[0]);
  });
  let mode = $derived($modeStore);
  let modelStore = $derived(STORE[mode]);
  let model = $derived(mode === "create" ? $genModelStore : mode === "edit" ? $editModelStore : $stemsModelStore);

  // MiniMax Music 3 requires lyrics and takes none of ACE-Step's metadata
  // options. YuE2 takes none of them either, and has no length: the song is as
  // long as its lyrics, so a duration is a 400. The server keys this off the
  // generated family, which the catalog does not ship; the id is the next best
  // thing, and the server's 400 is the backstop if a rename ever fools it.
  let isMiniMax = $derived(/minimax/i.test(model));
  let isYue = $derived(/yue/i.test(model));
  let isAce = $derived(mode !== "stems" && !isMiniMax && !isYue);

  // --- source clip (edit + stems) -------------------------------------------
  // `url` is what the player plays: a /api/media ref for a take, an object URL
  // for a file the user picked. `ref` is set once the clip is on the server.
  type Source = { url: string; ref?: string; label: string; blob: Blob; secs: number | null };
  let source = $state<Source | null>(null);
  let sourceError = $state("");
  let sourceBusy = $state(false);

  function dropSource() {
    if (source && !source.ref) URL.revokeObjectURL(source.url);
    source = null;
    sourceError = "";
  }

  async function useTakeAsSource(ref: string, label: string, to: Mode) {
    sourceError = "";
    sourceBusy = true;
    try {
      const blob = await fetchBlob(ref);
      if (source && !source.ref) URL.revokeObjectURL(source.url);
      source = { url: ref, ref, label, blob, secs: await wavSeconds(blob) };
      resetWindow();
      modeStore.set(to);
    } catch (e) {
      sourceError = e instanceof Error ? e.message : "Could not read that take";
    } finally {
      sourceBusy = false;
    }
  }

  async function onSourceFile(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    sourceError = "";
    sourceBusy = true;
    try {
      const blob = await toWav(file);
      if (source && !source.ref) URL.revokeObjectURL(source.url);
      source = { url: URL.createObjectURL(blob), label: file.name, blob, secs: await wavSeconds(blob) };
      resetWindow();
    } catch {
      sourceError = "The browser could not decode that file. Use a WAV, MP3, FLAC or OGG clip.";
    } finally {
      sourceBusy = false;
    }
  }

  // The thread keeps refs only, so a picked file goes up before the take that
  // reads it is written. A re-upload of the same bytes is a no-op server-side
  // that also refreshes the file's GC grace.
  async function sourceRef(src: Source): Promise<string> {
    if (src.ref) return src.ref;
    src.ref = await uploadMedia(src.blob);
    return src.ref;
  }

  // --- edit settings ----------------------------------------------------------
  let editTask = $state<EditTask>("repaint");
  let winStart = $state(0);
  let winEnd = $state(10);
  let repaintMode = $state("balanced");
  let repaintStrength = $state(0.5);
  let coverStrength = $state(1);

  function resetWindow() {
    const d = source?.secs ?? 0;
    winStart = d > 0 ? Math.round(d * 0.25 * 10) / 10 : 0;
    winEnd = d > 0 ? Math.round(d * 0.5 * 10) / 10 : 10;
  }

  // Drag across the bar to pick the repaint window.
  let trackEl = $state<HTMLDivElement | null>(null);
  let dragFrom: number | null = null;
  function trackSecs(clientX: number): number {
    const d = source?.secs ?? 0;
    if (!trackEl || d <= 0) return 0;
    const r = trackEl.getBoundingClientRect();
    return Math.round(Math.min(1, Math.max(0, (clientX - r.left) / r.width)) * d * 10) / 10;
  }
  function onTrackDown(e: PointerEvent) {
    if (!source?.secs) return;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    dragFrom = trackSecs(e.clientX);
    winStart = dragFrom;
    winEnd = dragFrom;
  }
  function onTrackMove(e: PointerEvent) {
    if (dragFrom === null) return;
    const t = trackSecs(e.clientX);
    winStart = Math.min(dragFrom, t);
    winEnd = Math.max(dragFrom, t);
  }
  function onTrackUp() {
    dragFrom = null;
    // A click with no drag leaves an empty window; give it a few seconds.
    if (winEnd - winStart < 0.5) winEnd = Math.min(source?.secs ?? winStart + 5, winStart + 5);
  }
  let windowValid = $derived(winStart >= 0 && winEnd > winStart && (!source?.secs || winStart < source.secs));

  // --- seed -----------------------------------------------------------------
  // Empty = random each take. The server echoes the seed it used, so any take
  // can be pinned afterwards from its card.
  let seedText = $state("");
  let seedLocked = $derived(seedText.trim() !== "");
  function parsedSeed(): number | undefined {
    const n = Number(seedText.trim());
    return seedText.trim() && Number.isInteger(n) && n >= 0 ? n : undefined;
  }
  let seedInvalid = $derived(seedLocked && parsedSeed() === undefined);

  let showAdvanced = $state(false);

  function num(s: string): number | undefined {
    const n = Number(s.trim());
    return s.trim() && Number.isFinite(n) ? n : undefined;
  }

  // Collected from the form at press time. Each family gets only its own
  // options: the server whitelists per family and answers an unknown key with
  // a 400.
  function familyOptions(): Record<string, string | number> {
    const o: Record<string, string | number> = {};
    if (isYue && get(cotStore)) o.cot = get(cotStore);
    if (!isAce) return o;
    const bpm = num(get(bpmStore));
    if (bpm !== undefined) o.bpm = Math.round(bpm);
    if (get(keyStore).trim()) o.keyscale = get(keyStore).trim();
    if (get(timeSigStore).trim()) o.timesignature = get(timeSigStore).trim();
    if (get(negativeStore).trim()) o.negative_prompt = get(negativeStore).trim();
    return o;
  }

  // --- threads --------------------------------------------------------------
  function initMusicChats() {
    const sessions = get(musicSessions);
    let id = get(activeMusicChatId);
    if (!sessions.some((s) => s.id === id)) {
      const recent = sessions.reduce<MusicSession | null>((best, s) => (!best || s.updatedAt > best.updatedAt ? s : best), null);
      id = recent ? recent.id : "";
      if (!id) {
        const s: MusicSession = { id: newMusicChatId(), title: "New music", turns: [], updatedAt: Date.now() };
        musicSessions.set([s]);
        id = s.id;
      }
      activeMusicChatId.set(id);
    }
  }
  initMusicChats();

  let activeSession = $derived($musicSessions.find((s) => s.id === $activeMusicChatId));
  let turns = $derived(activeSession?.turns ?? []);

  const PAGE_SIZE = 8;
  let page = $state(1);
  let ordered = $derived(turns.map((t, ti) => ({ t, ti })).reverse());
  let pageCount = $derived(Math.max(1, Math.ceil(ordered.length / PAGE_SIZE)));
  let pagedTurns = $derived(ordered.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE));
  $effect(() => {
    void turns.length;
    void $activeMusicChatId;
    page = 1;
  });
  $effect(() => {
    if (page > pageCount) page = pageCount;
  });

  // In-flight take, by session AND index: takes are a library here, so a new
  // one is always appended and nothing after it is dropped.
  let gen = $state<{ id: string; ti: number } | null>(null);
  let isGenerating = $derived(gen !== null);
  $effect(() => {
    generatingMusicChatId.set(gen?.id ?? null);
  });
  $effect(() => {
    playgroundStores.musicGenerating.set(isGenerating);
  });

  function sessionById(id: string): MusicSession | undefined {
    return get(musicSessions).find((s) => s.id === id);
  }
  function patchSession(id: string, fields: Partial<MusicSession>, bump = false) {
    musicSessions.update((ss) => {
      const i = ss.findIndex((s) => s.id === id);
      if (i === -1) return ss;
      const copy = [...ss];
      copy[i] = { ...copy[i], ...fields, ...(bump ? { updatedAt: Date.now() } : {}) };
      return copy;
    });
  }
  function appendTurn(id: string, turn: Turn): number {
    const s = sessionById(id);
    if (!s) return -1;
    const next = [...s.turns, turn];
    patchSession(id, { turns: next, title: s.titled ? s.title : deriveMusicTitle(next) }, true);
    return next.length - 1;
  }
  function updateTurn(id: string, ti: number, patch: Partial<Turn>) {
    const s = sessionById(id);
    if (!s) return;
    patchSession(id, { turns: s.turns.map((t, i) => (i === ti ? { ...t, ...patch } : t)) });
  }
  function removeTurn(id: string, ti: number, bump = false) {
    const s = sessionById(id);
    if (!s) return;
    const next = s.turns.filter((_, i) => i !== ti);
    patchSession(id, { turns: next, title: s.titled ? s.title : deriveMusicTitle(next) }, bump);
  }

  let prompt = $state("");
  let promptEl = $state<HTMLTextAreaElement | null>(null);
  $effect(() => {
    void prompt;
    const el = promptEl;
    if (!el) return;
    el.style.height = "auto";
    if (el.scrollHeight > 0) el.style.height = Math.min(el.scrollHeight, Math.floor(window.innerHeight * 0.4)) + "px";
  });

  let abortController: AbortController | null = null;
  let elapsed = $state(0);
  let menuIdx = $state<number | null>(null);
  // The take that should start playing once its player has loaded.
  let autoPlayRef = $state<string | null>(null);

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

  function fmtDur(s: number): string {
    if (s < 60) return `${s}s`;
    return `${Math.floor(s / 60)}m ${s % 60}s`;
  }
  function fmtClock(s: number): string {
    return `${Math.floor(s / 60)}:${(Math.round(s * 10) / 10 % 60).toFixed(1).padStart(4, "0")}`;
  }

  // Run one take: append it, call the route, upload what came back, and write
  // refs into the take by session id (the user may switch threads meanwhile).
  // Aborting removes the take, as the Speech tab does.
  async function runTake(turn: Turn, job: (signal: AbortSignal) => Promise<Partial<Turn>>, onAbort?: () => void) {
    const id = $activeMusicChatId;
    if (!sessionById(id) || isGenerating) return;
    const ti = appendTurn(id, turn);
    if (ti < 0) return;
    gen = { id, ti };
    abortController = new AbortController();
    const started = Date.now();
    try {
      const patch = await job(abortController.signal);
      updateTurn(id, ti, { ...patch, secs: (Date.now() - started) / 1000 });
      if (get(autoPlayStore) && id === get(activeMusicChatId)) autoPlayRef = patch.audio ?? patch.stems?.[0]?.audio ?? null;
    } catch (err) {
      if (err instanceof Error && err.name === "AbortError") {
        removeTurn(id, ti);
        onAbort?.();
      } else {
        updateTurn(id, ti, { error: err instanceof Error ? err.message : "An error occurred" });
      }
    } finally {
      gen = null;
      abortController = null;
    }
  }

  // Build a take from the form as it stands now.
  function formTurn(task: MusicTask, text: string, src?: string, srcLabel?: string): Turn {
    const t: Turn = { task, model, prompt: text };
    if (task === "separate") return { ...t, source: src, sourceLabel: srcLabel };
    const lyrics = get(lyricsStore);
    if (lyrics.trim()) t.lyrics = lyrics;
    const dur = num(get(durationStore));
    if (!isYue && dur !== undefined && dur > 0 && (task === "generate" || task === "complete")) t.duration = dur;
    const seed = parsedSeed();
    if (seed !== undefined) t.seed = seed;
    const steps = num(get(stepsStore));
    if (steps !== undefined) t.steps = Math.round(steps);
    const guidance = num(get(guidanceStore));
    if (guidance !== undefined) t.guidance = guidance;
    const options = familyOptions();
    if (task === "cover") options.audio_cover_strength = coverStrength;
    if (Object.keys(options).length) t.options = options;
    if (src) {
      t.source = src;
      t.sourceLabel = srcLabel;
    }
    if (task === "repaint") {
      t.start = winStart;
      t.end = winEnd;
      t.repaintMode = repaintMode;
      t.strength = repaintStrength;
    }
    if (task === "cover") t.strength = coverStrength;
    return t;
  }

  // The request a take describes. Shared by a fresh take and a regenerate, so
  // "again" is exactly the take's own settings with a new seed.
  async function execute(t: Turn, signal: AbortSignal): Promise<Partial<Turn>> {
    const language = get(languageStore).trim() || undefined;
    if (t.task === "separate") {
      const stems = await separateAudio(t.model, await fetchBlob(t.source!), signal);
      if (!stems.length) throw new Error("The separator returned no stems");
      const refs = await Promise.all(stems.map(async (s) => ({ name: s.name, audio: await uploadMedia(s.blob) })));
      return { stems: refs };
    }
    const base = {
      model: t.model,
      prompt: t.prompt,
      lyrics: t.lyrics ?? "",
      duration: t.duration,
      seed: t.seed,
      steps: t.steps,
      guidance: t.guidance,
      language,
      options: t.options,
    };
    const res =
      t.task === "generate"
        ? await generateMusic(base, signal)
        : await editMusic(
            {
              ...base,
              task: t.task,
              audio: await fetchBlob(t.source!),
              start: t.task === "repaint" ? t.start : undefined,
              end: t.task === "repaint" ? t.end : undefined,
              repaintMode: t.task === "repaint" ? t.repaintMode : undefined,
              repaintStrength: t.task === "repaint" ? t.strength : undefined,
            },
            signal,
          );
    return { audio: await uploadMedia(res.blob), seed: res.seed ?? t.seed };
  }

  let canRun = $derived.by(() => {
    if (!model || isGenerating || sourceBusy) return false;
    if (mode === "stems") return !!source;
    if (seedInvalid) return false;
    if (isMiniMax && !$lyricsStore.trim()) return false;
    if (mode === "create") return !!prompt.trim();
    if (!source || !prompt.trim()) return false;
    return editTask !== "repaint" || windowValid;
  });

  let blockReason = $derived.by(() => {
    if (!model) return "Pick a model";
    if (mode === "stems") return source ? "" : "Pick a source clip on the left";
    if (mode === "edit" && !source) return "Pick a source clip on the left";
    if (isMiniMax && !$lyricsStore.trim()) return "MiniMax Music 3 needs lyrics";
    if (seedInvalid) return "The seed must be a whole number";
    if (mode === "edit" && editTask === "repaint" && !windowValid) return "The repaint window is empty";
    return "";
  });

  async function send() {
    if (!canRun) return;
    const text = prompt.trim();
    if (mode === "create") {
      prompt = "";
      await runTake(formTurn("generate", text), (sig) => execute(sessionLast(), sig), () => (prompt = text));
      return;
    }
    const src = source!;
    let ref: string;
    try {
      ref = await sourceRef(src);
    } catch (e) {
      sourceError = e instanceof Error ? e.message : "Upload failed";
      return;
    }
    if (mode === "stems") {
      await runTake(formTurn("separate", "", ref, src.label), (sig) => execute(sessionLast(), sig));
      return;
    }
    prompt = "";
    await runTake(formTurn(editTask, text, ref, src.label), (sig) => execute(sessionLast(), sig), () => (prompt = text));
  }

  // The take runTake just appended: execute() reads the stored take rather than
  // the form, so a regenerate and a fresh take go through the same path.
  function sessionLast(): Turn {
    const s = sessionById(gen!.id)!;
    return s.turns[gen!.ti];
  }

  // Same settings, new seed: a take is appended, never replaced.
  async function regenerate(ti: number) {
    const t = turns[ti];
    if (!t || isGenerating) return;
    const again: Turn = { ...t, seed: undefined, audio: undefined, stems: undefined, error: undefined, secs: undefined };
    await runTake(again, (sig) => execute(sessionLast(), sig));
  }

  // Load a take's settings back into the form, seed included, so it can be
  // varied from there.
  function reuseSettings(t: Turn) {
    if (t.task === "separate") return;
    prompt = t.prompt;
    lyricsStore.set(t.lyrics ?? "");
    if (t.duration !== undefined) durationStore.set(String(t.duration));
    seedText = t.seed !== undefined ? String(t.seed) : "";
    stepsStore.set(t.steps !== undefined ? String(t.steps) : "");
    guidanceStore.set(t.guidance !== undefined ? String(t.guidance) : "");
    const o = t.options ?? {};
    bpmStore.set(o.bpm !== undefined ? String(o.bpm) : "");
    keyStore.set(typeof o.keyscale === "string" ? o.keyscale : "");
    timeSigStore.set(o.timesignature !== undefined ? String(o.timesignature) : "");
    negativeStore.set(typeof o.negative_prompt === "string" ? o.negative_prompt : "");
    cotStore.set(typeof o.cot === "string" ? o.cot : "");
    if (t.task !== "generate") editTask = t.task;
    if (t.task === "repaint") {
      winStart = t.start ?? winStart;
      winEnd = t.end ?? winEnd;
      repaintMode = t.repaintMode ?? repaintMode;
      repaintStrength = t.strength ?? repaintStrength;
    }
    if (t.task === "cover" && t.strength !== undefined) coverStrength = t.strength;
    const m: Mode = t.task === "generate" ? "create" : "edit";
    if (modelsFor[m].some((x) => x.id === t.model)) STORE[m].set(t.model);
    modeStore.set(m);
    if (t.source && t.task !== "generate") void useTakeAsSource(t.source, t.sourceLabel || "source", m);
  }

  function cancelGeneration() {
    abortController?.abort();
  }

  function deleteTurn(ti: number) {
    if (isGenerating) return;
    removeTurn($activeMusicChatId, ti, true);
  }

  function takeLabel(ti: number): string {
    return `Take ${ti + 1}`;
  }

  function download(url: string, name: string) {
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }
  function fileStem(t: Turn, ti: number): string {
    const base = (t.prompt || t.sourceLabel || "music").replace(/[^\w\- ]+/g, "").trim().slice(0, 40).replace(/\s+/g, "-");
    return `${base || "music"}-take${ti + 1}${t.seed !== undefined ? `-s${t.seed}` : ""}`;
  }

  const TASK_LABEL: Record<MusicTask, string> = {
    generate: "Song",
    repaint: "Repaint",
    complete: "Complete",
    cover: "Cover",
    separate: "Stems",
  };

  function handleKeyDown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void send();
    }
  }

  const inputCls = "w-full rounded border border-card-border bg-background px-2 py-1 text-xs focus:outline-none focus:border-primary";
  const labelCls = "text-micro font-medium uppercase tracking-wide text-txtsecondary";
</script>

<div class="flex flex-col h-full">
  <PaneHeader
    title={activeSession?.title || "New music"}
    meta={turns.length ? `${turns.length} take${turns.length === 1 ? "" : "s"}` : ""}
    updatedAt={turns.length ? activeSession?.updatedAt : undefined}
    newLabel="New"
    onNew={newMusicChat}
  />
  {#if !hasAny}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <Music class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No music models. Download ACE-Step, MiniMax Music 3 or HTDemucs from the model browser to use this tab.</p>
    </div>
  {:else}
    <div class="flex-1 min-h-0 flex">
      <!-- LEFT: mode + settings. -->
      <aside class="w-[19.375rem] shrink-0 flex flex-col min-h-0 bg-rail border-r border-card-border-inner">
        <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner shrink-0">
          <div class="seg w-full" role="group" aria-label="Mode">
            {#each modes as m (m)}
              <button class="flex-1" aria-pressed={mode === m} onclick={() => modeStore.set(m)}>
                {m === "create" ? "Create" : m === "edit" ? "Edit" : "Stems"}
              </button>
            {/each}
          </div>
        </div>

        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll flex flex-col gap-3.5 p-4">
          {#if mode !== "create"}
            <!-- Source clip: a take (from its card menu) or a file. -->
            <div class="flex flex-col gap-1.5">
              <div class="flex items-center gap-2">
                <span class={labelCls}>Source</span>
                {#if source}
                  <button class="icon-btn ml-auto !w-6 !h-6" onclick={dropSource} use:tip={"Clear the source"} aria-label="Clear source">
                    <X class="w-3.5 h-3.5" />
                  </button>
                {/if}
              </div>
              {#if source}
                <AudioPlayer src={source.url} volume={$volumeStore} label={source.label} wide />
                {#if source.secs}
                  <span class="text-micro text-txtsecondary tabular-nums">{source.secs.toFixed(1)} s</span>
                {/if}
              {:else}
                <p class="text-xs text-txtsecondary leading-snug">
                  Pick a take with its <MoreVertical class="inline w-3 h-3 -mt-0.5" /> menu, or upload a clip.
                </p>
              {/if}
              <label class="btn btn--sm inline-flex items-center gap-1.5 self-start cursor-pointer {sourceBusy ? 'opacity-50 pointer-events-none' : ''}">
                <Upload class="w-3.5 h-3.5" />
                {sourceBusy ? "Reading…" : source ? "Replace" : "Upload a clip"}
                <input type="file" accept="audio/*,.wav,.mp3,.flac,.ogg" class="hidden" onchange={onSourceFile} />
              </label>
              {#if sourceError}<span class="text-error text-xs">{sourceError}</span>{/if}
            </div>
          {/if}

          {#if mode === "edit"}
            <div class="flex flex-col gap-1.5">
              <span class={labelCls}>Edit</span>
              <div class="seg w-full" role="group" aria-label="Edit type">
                <button class="flex-1" aria-pressed={editTask === "repaint"} onclick={() => (editTask = "repaint")} use:tip={"Regenerate a time window, keep the rest"}>Repaint</button>
                <button class="flex-1" aria-pressed={editTask === "complete"} onclick={() => (editTask = "complete")} use:tip={"Continue or finish the clip"}>Complete</button>
                <button class="flex-1" aria-pressed={editTask === "cover"} onclick={() => (editTask = "cover")} use:tip={"Restyle the whole clip, same length"}>Cover</button>
              </div>
            </div>

            {#if editTask === "repaint"}
              <div class="flex flex-col gap-1.5">
                <span class={labelCls}>Window</span>
                {#if source?.secs}
                  <div
                    bind:this={trackEl}
                    class="relative h-7 rounded bg-secondary cursor-crosshair touch-none"
                    onpointerdown={onTrackDown}
                    onpointermove={onTrackMove}
                    onpointerup={onTrackUp}
                    role="slider"
                    tabindex="0"
                    aria-label="Repaint window"
                    aria-valuemin={0}
                    aria-valuemax={Math.round(source.secs)}
                    aria-valuenow={Math.round(winStart)}
                    use:tip={"Drag across the clip to pick the part to repaint"}
                  >
                    <div
                      class="absolute inset-y-0 rounded-sm bg-primary/35 border-x-2 border-primary"
                      style="left: {(Math.min(winStart, source.secs) / source.secs) * 100}%; width: {(Math.max(0, Math.min(winEnd, source.secs) - winStart) / source.secs) * 100}%"
                    ></div>
                  </div>
                {/if}
                <div class="flex items-center gap-2">
                  <label class="flex-1 flex flex-col gap-0.5 text-micro text-txtsecondary">
                    Start (s)
                    <input type="number" min="0" step="0.1" bind:value={winStart} class={inputCls} />
                  </label>
                  <label class="flex-1 flex flex-col gap-0.5 text-micro text-txtsecondary">
                    End (s)
                    <input type="number" min="0" step="0.1" bind:value={winEnd} class={inputCls} />
                  </label>
                </div>
                {#if source?.secs}
                  <span class="text-micro text-txtsecondary tabular-nums">{fmtClock(winStart)} to {fmtClock(Math.min(winEnd, source.secs))}</span>
                {/if}
              </div>
              <div class="flex flex-col gap-1.5">
                <span class={labelCls}>Blend</span>
                <div class="seg w-full" role="group" aria-label="Repaint mode">
                  {#each ["conservative", "balanced", "aggressive"] as rm (rm)}
                    <button class="flex-1" aria-pressed={repaintMode === rm} onclick={() => (repaintMode = rm)}>{rm.slice(0, 1).toUpperCase() + rm.slice(1, 5)}</button>
                  {/each}
                </div>
                <label class="flex items-center gap-2 text-micro text-txtsecondary">
                  Strength
                  <input type="range" min="0" max="1" step="0.05" bind:value={repaintStrength} class="flex-1 accent-primary" />
                  <span class="font-mono tabular-nums w-8 text-right">{repaintStrength.toFixed(2)}</span>
                </label>
              </div>
            {:else if editTask === "cover"}
              <label class="flex items-center gap-2 text-micro text-txtsecondary" use:tip={"How much of the source's structure the cover keeps"}>
                Source strength
                <input type="range" min="0" max="1" step="0.05" bind:value={coverStrength} class="flex-1 accent-primary" />
                <span class="font-mono tabular-nums w-8 text-right">{coverStrength.toFixed(2)}</span>
              </label>
            {/if}
          {/if}

          {#if mode !== "stems"}
            <div class="flex flex-col gap-1.5">
              <div class="flex items-center gap-2">
                <span class={labelCls}>Lyrics</span>
                <span class="ml-auto text-micro text-txtsecondary">{isMiniMax ? "required" : isYue ? "empty = no lyrics, may still sing" : "empty = instrumental"}</span>
              </div>
              <textarea
                bind:value={$lyricsStore}
                rows="7"
                placeholder={"[verse]\nWe rise with the morning light\n\n[chorus]\n…"}
                class="{inputCls} resize-y font-mono leading-relaxed min-h-[6rem]"
              ></textarea>
            </div>

            <div class="flex items-end gap-2">
              {#if !isYue && (mode === "create" || editTask === "complete")}
                <label class="w-20 flex flex-col gap-0.5 text-micro text-txtsecondary">
                  Length (s)
                  <input type="number" min="1" step="1" bind:value={$durationStore} placeholder="auto" class={inputCls} />
                </label>
              {/if}
              <label class="flex-1 flex flex-col gap-0.5 text-micro text-txtsecondary">
                Seed
                <div class="flex items-center gap-1">
                  <input
                    type="text"
                    inputmode="numeric"
                    bind:value={seedText}
                    placeholder="random"
                    class="{inputCls} font-mono {seedInvalid ? '!border-error' : ''}"
                  />
                  <button
                    class="icon-btn shrink-0"
                    onclick={() => (seedText = seedLocked ? "" : String(Math.floor(Math.random() * 2 ** 31)))}
                    use:tip={seedLocked ? "Back to a random seed per take" : "Pin a seed"}
                    aria-label={seedLocked ? "Random seed" : "Pin a seed"}
                  >
                    {#if seedLocked}<Lock class="w-3.5 h-3.5" />{:else}<Dice5 class="w-3.5 h-3.5" />{/if}
                  </button>
                </div>
              </label>
            </div>

            <div class="flex flex-col gap-2">
              <button class="flex items-center gap-1.5 {labelCls} hover:text-txtmain self-start" onclick={() => (showAdvanced = !showAdvanced)}>
                <SlidersHorizontal class="w-3.5 h-3.5" /> Advanced
                <ChevronDown class="w-3.5 h-3.5 transition-transform {showAdvanced ? 'rotate-180' : ''}" />
              </button>
              {#if showAdvanced}
                <div class="grid grid-cols-2 gap-2">
                  <label class="flex flex-col gap-0.5 text-micro text-txtsecondary">
                    Steps
                    <input type="number" min="1" step="1" bind:value={$stepsStore} placeholder="model" class={inputCls} />
                  </label>
                  <label class="flex flex-col gap-0.5 text-micro text-txtsecondary">
                    Guidance
                    <input type="number" min="0" step="0.1" bind:value={$guidanceStore} placeholder="model" class={inputCls} />
                  </label>
                  <label class="flex flex-col gap-0.5 text-micro text-txtsecondary">
                    Language
                    <input type="text" bind:value={$languageStore} placeholder="en" class={inputCls} />
                  </label>
                  {#if isYue}
                    <label
                      class="flex flex-col gap-0.5 text-micro text-txtsecondary"
                      use:tip={"YuE2 can write a score (ABC) before the music: full plans the whole song, melody only the tune, off goes straight to audio"}
                    >
                      Planning
                      <Select bind:value={$cotStore} options={COT_OPTIONS} ariaLabel="Planning" />
                    </label>
                  {/if}
                  {#if isAce}
                    <label class="flex flex-col gap-0.5 text-micro text-txtsecondary">
                      BPM
                      <input type="number" min="30" max="300" step="1" bind:value={$bpmStore} placeholder="auto" class={inputCls} />
                    </label>
                    <label class="flex flex-col gap-0.5 text-micro text-txtsecondary">
                      Key
                      <input type="text" bind:value={$keyStore} placeholder="e.g. C major" class={inputCls} />
                    </label>
                    <label class="flex flex-col gap-0.5 text-micro text-txtsecondary">
                      Time signature
                      <input type="text" bind:value={$timeSigStore} placeholder="e.g. 4" class={inputCls} />
                    </label>
                    <label class="col-span-2 flex flex-col gap-0.5 text-micro text-txtsecondary">
                      Negative prompt
                      <input type="text" bind:value={$negativeStore} placeholder="what to steer away from" class={inputCls} />
                    </label>
                  {/if}
                </div>
                <span class="text-micro text-txtsecondary leading-snug">Blank fields use the model's own defaults.</span>
              {/if}
            </div>
          {:else}
            <p class="text-xs text-txtsecondary leading-snug">
              Splits the source into drums, bass, vocals and other. No prompt needed.
            </p>
          {/if}
        </div>
      </aside>

      <!-- RIGHT: takes grid over the composer. -->
      <div class="flex-1 min-w-0 flex flex-col min-h-0 px-6">
        <div class="flex items-center gap-2.5 h-10 shrink-0">
          <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">Takes</span>
          <span class="font-mono text-micro text-txtsecondary tabular-nums">{turns.length}</span>
          <div class="ml-auto flex items-center gap-2">
            <VolumeX class="w-3.5 h-3.5 text-txtsecondary shrink-0" />
            <input type="range" min="0" max="1" step="0.01" bind:value={$volumeStore} class="w-24 accent-primary" aria-label="Playback volume" />
            <Volume2 class="w-3.5 h-3.5 text-txtsecondary shrink-0" />
            <span class="font-mono text-micro text-txtsecondary tabular-nums w-9 text-right">{Math.round($volumeStore * 100)}%</span>
            <label class="shrink-0 flex items-center gap-1.5 pl-3 ml-1 border-l border-card-border-inner text-micro font-medium uppercase tracking-wide text-txtsecondary cursor-pointer" use:tip={"Auto-play new takes"}>
              <Toggle size="sm" bind:checked={$autoPlayStore} />
              Auto-play
            </label>
          </div>
        </div>

        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b" use:scrollFade>
          {#if turns.length === 0 && !isGenerating}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary text-center">
              <Music class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Describe a song below and press Enter. Each take lands here,<br />and any take can be repainted, covered or split into stems.</p>
            </div>
          {:else}
            <div class="grid grid-cols-1 xl:grid-cols-2 gap-2.5 content-start pb-4">
              {#each pagedTurns as item (item.ti)}
                {@const t = item.t}
                {@const ti = item.ti}
                {@const pending = !t.audio && !t.stems && !t.error && gen?.id === $activeMusicChatId && gen?.ti === ti}
                <div class="relative min-w-0 rounded-[0.625rem] border border-card-border bg-surface p-3 flex flex-col gap-2 {menuIdx === ti ? 'z-30' : ''}">
                  <p class="text-[0.8125rem] leading-snug text-txtmain whitespace-pre-wrap line-clamp-2 min-h-[2.25rem] pr-6">
                    {#if t.task === "separate"}
                      <span class="text-txtsecondary">Stems of</span> {t.sourceLabel || "a clip"}
                    {:else}
                      {t.prompt}
                    {/if}
                  </p>

                  {#if t.error}
                    <div class="text-error text-xs whitespace-pre-wrap break-words">{t.error}</div>
                  {:else if t.stems}
                    <div class="flex flex-col gap-1.5">
                      {#each t.stems as s (s.name)}
                        <div class="flex items-center gap-2 min-w-0">
                          <div class="flex-1 min-w-0">
                            <AudioPlayer src={s.audio} volume={$volumeStore} label={s.name} wide autoplay={autoPlayRef === s.audio} />
                          </div>
                          <button class="icon-btn shrink-0" onclick={() => download(s.audio, `${fileStem(t, ti)}-${s.name}.wav`)} use:tip={`Download ${s.name}`} aria-label={`Download ${s.name}`}>
                            <Download class="w-3.5 h-3.5" />
                          </button>
                        </div>
                      {/each}
                    </div>
                  {:else if t.audio}
                    <AudioPlayer src={t.audio} volume={$volumeStore} wide autoplay={autoPlayRef === t.audio} />
                  {:else if pending}
                    <div class="flex items-center justify-between gap-2 h-8">
                      <div class="flex items-center gap-2 text-txtsecondary">
                        <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                        <span class="reason-shimmer-white font-medium text-xs">{t.task === "separate" ? "Separating…" : t.task === "generate" ? "Composing…" : "Editing…"}</span>
                      </div>
                      <span class="font-mono text-micro text-txtsecondary tabular-nums">{fmtDur(elapsed)}</span>
                    </div>
                  {:else}
                    <div class="text-error text-xs">No audio returned.</div>
                  {/if}

                  <div class="flex items-center gap-1.5 min-w-0 flex-wrap">
                    <span class="shrink-0 rounded border border-card-border px-1.5 py-px font-mono text-micro text-txtsecondary">{TASK_LABEL[t.task]}</span>
                    {#if t.task === "repaint" && t.start !== undefined}
                      <span class="shrink-0 font-mono text-micro text-txtsecondary tabular-nums">{t.start}-{t.end === -1 ? "end" : t.end}s</span>
                    {/if}
                    {#if t.sourceLabel && t.task !== "separate" && t.task !== "generate"}
                      <span class="truncate max-w-[8rem] font-mono text-micro text-txtsecondary" use:tip={`Source: ${t.sourceLabel}`}>← {t.sourceLabel}</span>
                    {/if}
                    <span class="truncate min-w-0 max-w-[12rem] rounded border border-card-border px-1.5 py-px font-mono text-micro text-txtsecondary" use:tip={t.model}>{t.model}</span>
                    {#if t.seed !== undefined}
                      <button
                        class="shrink-0 rounded border border-card-border px-1.5 py-px font-mono text-micro text-txtsecondary hover:text-txtmain hover:border-primary/50"
                        onclick={() => (seedText = String(t.seed))}
                        use:tip={"Pin this seed for the next take"}
                      >
                        seed {t.seed}
                      </button>
                    {/if}
                    {#if t.secs}
                      <span class="shrink-0 font-mono text-micro text-txtsecondary tabular-nums" use:tip={"Generation time"}>{fmtDur(Math.round(t.secs))}</span>
                    {/if}
                    <div class="ml-auto flex items-center shrink-0">
                      {#if t.audio}
                        <button class="icon-btn" onclick={() => download(t.audio!, `${fileStem(t, ti)}.wav`)} use:tip={"Download"} aria-label="Download">
                          <Download class="w-3.5 h-3.5" />
                        </button>
                      {/if}
                    </div>
                  </div>

                  <div class="absolute top-2 right-2 z-20">
                    <button class="icon-btn !w-6 !h-6" onclick={() => (menuIdx = menuIdx === ti ? null : ti)} use:tip={"More"} aria-label="More">
                      <MoreVertical class="w-3.5 h-3.5" />
                    </button>
                    {#if menuIdx === ti}
                      <div class="absolute right-0 top-full mt-1 min-w-[10rem] flex flex-col rounded-lg border border-card-border bg-surface shadow-lg py-1 text-[0.8125rem]">
                        {#if t.audio && modelsFor.edit.length}
                          <button
                            class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-40"
                            onclick={() => { menuIdx = null; void useTakeAsSource(t.audio!, takeLabel(ti), "edit"); }}
                            disabled={sourceBusy}
                          >
                            <Wand2 class="w-3.5 h-3.5" /> Edit this take
                          </button>
                        {/if}
                        {#if t.audio && modelsFor.stems.length}
                          <button
                            class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-40"
                            onclick={() => { menuIdx = null; void useTakeAsSource(t.audio!, takeLabel(ti), "stems"); }}
                            disabled={sourceBusy}
                          >
                            <Layers class="w-3.5 h-3.5" /> Split into stems
                          </button>
                        {/if}
                        {#if t.task !== "separate"}
                          <button
                            class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-40"
                            onclick={() => { menuIdx = null; reuseSettings(t); }}
                            disabled={isGenerating}
                          >
                            <SlidersHorizontal class="w-3.5 h-3.5" /> Reuse settings
                          </button>
                        {/if}
                        <button
                          class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-40"
                          onclick={() => { menuIdx = null; void regenerate(ti); }}
                          disabled={isGenerating || (t.task !== "generate" && !t.source)}
                        >
                          <RefreshCw class="w-3.5 h-3.5" /> {t.task === "separate" ? "Run again" : "New take, new seed"}
                        </button>
                        <button
                          class="flex items-center gap-2 px-3 py-1.5 text-left text-error hover:bg-secondary disabled:opacity-40"
                          onclick={() => { menuIdx = null; deleteTurn(ti); }}
                          disabled={isGenerating}
                        >
                          <Trash2 class="w-3.5 h-3.5" /> Delete
                        </button>
                      </div>
                    {/if}
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>

        {#if pageCount > 1}
          <div class="shrink-0 flex items-center justify-center gap-3 pt-2">
            <button class="btn btn--sm" onclick={() => (page = Math.max(1, page - 1))} disabled={page <= 1}>Prev</button>
            <span class="font-mono text-micro text-txtsecondary tabular-nums">{page} / {pageCount}</span>
            <button class="btn btn--sm" onclick={() => (page = Math.min(pageCount, page + 1))} disabled={page >= pageCount}>Next</button>
          </div>
        {/if}

        <!-- Composer: the prompt (style for a song, instruction for an edit), then model + run. -->
        <div class="shrink-0 pt-2.5 pb-3">
          <div class="rounded-[1.125rem] border border-composer-border bg-surface hover:ring-1 hover:ring-composer-ring focus-within:border-primary transition-all">
            <textarea
              bind:this={promptEl}
              class="block w-full bg-transparent px-4 pt-3.5 pb-1.5 text-sm leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary pretty-scroll min-h-[3rem] max-h-[calc(40vh/var(--qm-scale))] disabled:opacity-50"
              rows="2"
              placeholder={mode === "stems"
                ? "Stems need no prompt: pick a source and press play"
                : mode === "edit"
                  ? editTask === "cover"
                    ? "Describe the cover, e.g. energetic pop with bright synths…"
                    : editTask === "complete"
                      ? "How should it continue, e.g. finish as a cinematic rock track…"
                      : "What goes in the window, e.g. a soaring guitar solo…"
                  : "Describe the song: genre, mood, instruments, vocals…"}
              disabled={isGenerating || mode === "stems"}
              bind:value={prompt}
              onkeydown={handleKeyDown}
            ></textarea>
            <div class="flex items-center gap-2.5 pl-3 pr-2 pb-2 pt-1 min-w-0">
              <div class="min-w-0">
                {#key mode}
                  <ModelSelector
                    bind:value={$modelStore}
                    placeholder="Select a model…"
                    disabled={isGenerating}
                    capabilities={[CAP[mode]]}
                    ghost
                    dropUp
                  />
                {/key}
              </div>
              {#if blockReason && !isGenerating}
                <span class="ml-auto truncate text-micro text-txtsecondary">{blockReason}</span>
              {:else}
                <span class="ml-auto"></span>
              {/if}
              {#if isGenerating}
                <button class="btn btn--icon !rounded-full w-8 h-8 shrink-0" onclick={cancelGeneration} use:tip={"Stop"} aria-label="Stop">
                  <Square class="w-3.5 h-3.5" fill="currentColor" />
                </button>
              {:else}
                <button
                  class="btn btn--primary btn--icon !rounded-full w-8 h-8 shrink-0"
                  onclick={send}
                  disabled={!canRun}
                  use:tip={mode === "stems" ? "Split" : mode === "edit" ? "Edit" : "Compose"}
                  aria-label={mode === "stems" ? "Split" : mode === "edit" ? "Edit" : "Compose"}
                >
                  <Play class="w-3.5 h-3.5" fill="currentColor" />
                </button>
              {/if}
            </div>
          </div>
          <div class="flex justify-center gap-4 mt-2 text-micro text-txtsecondary select-none">
            <span><kbd class="font-mono">Enter</kbd> run</span>
            <span><kbd class="font-mono">Shift+Enter</kbd> new line</span>
          </div>
        </div>
      </div>
    </div>
  {/if}
</div>
