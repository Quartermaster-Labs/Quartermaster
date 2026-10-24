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
    type ScoreOrigin,
    type Turn,
  } from "../../stores/musicHistory";
  import { generateMusic, editMusic, separateAudio, transcribeAudio, fetchLinkAudio, uploadMedia, fetchBlob, toWav, wavSeconds, type EditTask } from "../../lib/musicApi";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { matchesCapabilities } from "../../lib/modelUtils";
  import ModelSelector from "./ModelSelector.svelte";
  import {
    Music,
    Download,
    RefreshCw,
    Upload,
    Square,
    MoreVertical,
    Trash2,
    Play,
    Dice5,
    Volume2,
    VolumeX,
    Lock,
    ChevronDown,
    Wand2,
    Layers,
    SlidersHorizontal,
    X,
    Link as LinkIcon,
    FileText,
    Copy,
    ScrollText,
    Plus,
    Info,
    Library,
  } from "lucide-svelte";
  import ScoreList from "./ScoreList.svelte";
  import ScoreViewer from "./ScoreViewer.svelte";
  import { scoreLibrary, addScore, abcTitle, type LibraryScore } from "../../stores/scoreLibrary";
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
  //   stems   a clip, uploaded or pulled from a link (yt-dlp), split into
  //           four stems (HTDemucs)
  //   scores  the score library (stores/scoreLibrary.ts): every score read
  //           from a song, plus uploaded .abc files. Needs no model, so it is
  //           always offered; the composer's + can pick from it too.
  // Each mode has its own model pick, filtered by capability, so switching modes
  // never leaves a model selected that the route would refuse.
  //
  // A reference song is not a mode. While the picked generator takes a score
  // (YuE2), the composer's + accepts a recording, file or link, and SheetSage2
  // writes its melody down in the background; the score rides above the
  // composer as a chip until it is dismissed.
  //
  // Every audio field a take holds is a /api/media ref (see stores/musicHistory.ts):
  // results are uploaded once as they land, and the thread only carries refs.

  type Mode = "create" | "edit" | "stems" | "scores";
  type ModelMode = Exclude<Mode, "scores">;
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
  const scoreStore = userPref<string>("playground-music-score", "");
  // Where the score in the box was extracted from. Kept with a copy of the
  // score it describes: once the box is edited the two differ and the origin no
  // longer applies, so a hand-written score is never credited to a recording.
  const scoreOriginStore = userPref<(ScoreOrigin & { score: string }) | null>("playground-music-score-origin", null);

  const COT_OPTIONS = [
    { value: "", label: "model (full)" },
    { value: "full", label: "full" },
    { value: "melody", label: "melody" },
    { value: "off", label: "off" },
  ];

  const CAP: Record<ModelMode, string[]> = {
    create: ["music_generation"],
    edit: ["music_edit"],
    stems: ["audio_separation"],
  };
  const STORE: Record<ModelMode, typeof genModelStore> = { create: genModelStore, edit: editModelStore, stems: stemsModelStore };

  let listed = $derived($models.filter((m) => !m.unlisted));
  let modelsFor = $derived({
    create: listed.filter((m) => matchesCapabilities(m, CAP.create, true)),
    edit: listed.filter((m) => matchesCapabilities(m, CAP.edit, true)),
    stems: listed.filter((m) => matchesCapabilities(m, CAP.stems, true)),
  });
  let hasAny = $derived(modelsFor.create.length + modelsFor.edit.length + modelsFor.stems.length > 0);

  // A launch link that named a separator (or an editor) lands in the generation
  // pick; move it to the mode that can run it instead of leaving a model there
  // the generation route would refuse.
  $effect(() => {
    const id = $genModelStore;
    if (!id || !listed.length) return;
    const m = listed.find((x) => x.id === id);
    if (!m || matchesCapabilities(m, CAP.create, true)) return;
    if (matchesCapabilities(m, CAP.stems, true)) {
      stemsModelStore.set(id);
      modeStore.set("stems");
    }
    genModelStore.set("");
  });

  // Keep each mode's pick valid: an empty or vanished id falls back to the
  // first model that can serve the mode.
  $effect(() => {
    for (const mode of ["create", "edit", "stems"] as ModelMode[]) {
      const store = STORE[mode];
      const cur = get(store);
      const pool = modelsFor[mode];
      if (pool.length && !pool.some((m) => m.id === cur)) store.set(pool[0].id);
    }
  });

  // A mode with no model is hidden; fall back to one that has. The library
  // needs none.
  let modes = $derived<Mode[]>([...(["create", "edit", "stems"] as ModelMode[]).filter((m) => modelsFor[m].length > 0), "scores"]);
  $effect(() => {
    if (modes.length && !modes.includes($modeStore)) modeStore.set(modes[0]);
  });
  let mode = $derived($modeStore);
  // The composer's mode: Scores has no composer, so it keeps Create's pick.
  let runMode = $derived<ModelMode>(mode === "scores" ? "create" : mode);
  let modelStore = $derived(STORE[runMode]);
  let model = $derived(mode === "edit" ? $editModelStore : mode === "stems" ? $stemsModelStore : $genModelStore);

  // MiniMax Music 3 requires lyrics and takes none of ACE-Step's metadata
  // options. YuE2 takes none of them either, and has no length: the song is as
  // long as its lyrics, so a duration is a 400. The server keys this off the
  // generated family, which the catalog does not ship; the id is the next best
  // thing, and the server's 400 is the backstop if a rename ever fools it.
  let isMiniMax = $derived(/minimax/i.test(model));
  let isYue = $derived(/yue/i.test(model));
  // The score notation the picked model advertises (capabilities.music_score),
  // or "" when it takes none. Shown for any model that says so, not by name.
  let scoreFormat = $derived(listed.find((m) => m.id === model)?.capabilities?.music_score ?? "");
  // Mirrors the server's cap (maxMusicScoreBytes).
  const MAX_SCORE_BYTES = 64 << 10;
  let scoreError = $state("");
  let scoreBlocked = $derived(!!scoreFormat && isYue && $cotStore === "off" && !!$scoreStore.trim());

  async function onScoreFile(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    scoreError = "";
    if (file.size > MAX_SCORE_BYTES) {
      scoreError = `${file.name} is larger than ${MAX_SCORE_BYTES >> 10} KiB`;
      return;
    }
    try {
      const text = await file.text();
      scoreStore.set(text);
      addScore(text, "uploaded", abcTitle(text, file.name.replace(/\.[^.]+$/, "")));
    } catch (err) {
      scoreError = err instanceof Error ? err.message : String(err);
    }
  }
  let isAce = $derived(mode !== "stems" && !isMiniMax && !isYue);
  // Transcribers have no mode of their own: they only ever feed the score.
  let transcribers = $derived(listed.filter((m) => !!m.capabilities?.music_transcription));
  let scoreModels = $derived(modelsFor.create.filter((m) => !!m.capabilities?.music_score));
  // The origin applies only while the box still holds the score it came with.
  let scoreOrigin = $derived<ScoreOrigin | null>(
    $scoreOriginStore && $scoreOriginStore.score === $scoreStore ? { label: $scoreOriginStore.label, url: $scoreOriginStore.url } : null,
  );

  const DISCLAIMER =
    "For personal, non-commercial fun. A score extracted from a recording is that song's melody, so anything generated from it is a derivative of someone else's work. SheetSage2 and YuE2 are released for non-commercial use only.";

  // --- reference song -> score -------------------------------------------------
  // The composer's + takes a recording and transcribes it with no Run press.
  // Offered only while the picked generator takes a score: for any other model
  // the transcription would swap SheetSage into VRAM, and the generator back
  // out, for a result nothing reads. Not a take: the score lands in the form,
  // credited to its recording, and is copied into the song that uses it.
  type ScoreJob = { label: string; stage: string; error?: string };
  let scoreJob = $state<ScoreJob | null>(null);
  let scoreAbort: AbortController | null = null;
  let scoreBusy = $derived(!!scoreJob && !scoreJob.error);
  let attachOpen = $state(false);
  let attachEl = $state<HTMLElement | undefined>();
  let attachLink = $state("");
  let scoreOpen = $state(false);

  type Loaded = { wav: Blob; label: string; link?: string };
  async function extractScore(label: string, stage: string, load: (signal: AbortSignal) => Promise<Loaded>) {
    const tm = transcribers[0];
    if (!tm) return;
    scoreAbort?.abort();
    const ac = new AbortController();
    scoreAbort = ac;
    attachOpen = false;
    scoreOpen = false;
    scoreJob = { label, stage };
    try {
      const got = await load(ac.signal);
      if (scoreAbort !== ac) return;
      scoreJob = { label: got.label, stage: "Reading the melody…" };
      const res = await transcribeAudio(tm.id, got.wav, ac.signal);
      if (scoreAbort !== ac) return;
      scoreStore.set(res.score);
      scoreOriginStore.set({ label: got.label, url: got.link, score: res.score });
      addScore(res.score, "extracted", abcTitle(res.score, got.label), { label: got.label, url: got.link });
      // A reference was attached to be used: with planning off YuE2 would 400 it.
      if (get(cotStore) === "off") cotStore.set("melody");
      scoreJob = null;
    } catch (e) {
      if (scoreAbort !== ac) return;
      scoreJob = e instanceof Error && e.name === "AbortError" ? null : { label, stage: "", error: e instanceof Error ? e.message : String(e) };
    } finally {
      if (scoreAbort === ac) scoreAbort = null;
    }
  }

  function onReferenceFile(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    void extractScore(file.name, "Decoding…", async () => {
      try {
        return { wav: await toWav(file), label: file.name };
      } catch {
        throw new Error("The browser could not decode that file. Use a WAV, MP3, FLAC or OGG clip.");
      }
    });
  }

  function onReferenceLink() {
    const url = attachLink.trim();
    if (!url) return;
    attachLink = "";
    void extractScore(url, "Downloading…", async (signal) => {
      const a = await fetchLinkAudio(url, signal);
      try {
        return { wav: await toWav(a.blob), label: a.title, link: a.url };
      } catch {
        throw new Error("The browser could not decode the audio that link served.");
      }
    });
  }

  function dismissScore() {
    scoreAbort?.abort();
    scoreAbort = null;
    scoreJob = null;
    scoreOpen = false;
    scoreError = "";
    scoreStore.set("");
    scoreOriginStore.set(null);
  }

  // --- source clip (edit + stems) -------------------------------------------
  // `url` is what the player plays: a /api/media ref for a take, an object URL
  // for a file the user picked. `ref` is set once the clip is on the server.
  // `link` is the page a fetched clip came from.
  type Source = { url: string; ref?: string; label: string; blob: Blob; secs: number | null; link?: string };
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

  // Pull a page's audio through the server's yt-dlp (POST /v1/tools/media/audio)
  // and decode it here, exactly like an uploaded file.
  let linkText = $state("");
  let linkBusy = $state(false);
  let linkAbort: AbortController | null = null;
  async function fetchLink() {
    const url = linkText.trim();
    if (!url || linkBusy) return;
    sourceError = "";
    linkBusy = true;
    sourceBusy = true;
    linkAbort = new AbortController();
    try {
      const a = await fetchLinkAudio(url, linkAbort.signal);
      let blob: Blob;
      try {
        blob = await toWav(a.blob);
      } catch {
        throw new Error("The browser could not decode the audio that link served.");
      }
      if (source && !source.ref) URL.revokeObjectURL(source.url);
      source = { url: URL.createObjectURL(blob), label: a.title, blob, secs: await wavSeconds(blob), link: a.url };
      linkText = "";
      resetWindow();
    } catch (e) {
      if (!(e instanceof Error && e.name === "AbortError")) sourceError = e instanceof Error ? e.message : "Could not fetch that link";
    } finally {
      linkBusy = false;
      sourceBusy = false;
      linkAbort = null;
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
  let rootEl = $state<HTMLElement | null>(null);

  // A new take means the user is done listening: stop whatever this tab is
  // playing (a take, a stem, the source clip) rather than talk over the run.
  function stopPlayback() {
    rootEl?.querySelectorAll("audio").forEach((a) => a.pause());
  }
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
    stopPlayback();
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
  function formTurn(task: MusicTask, text: string, src?: string, srcLabel?: string, srcUrl?: string): Turn {
    const t: Turn = { task, model, prompt: text };
    if (task === "separate" || task === "transcribe") return { ...t, source: src, sourceLabel: srcLabel, sourceUrl: srcUrl };
    const lyrics = get(lyricsStore);
    if (lyrics.trim()) t.lyrics = lyrics;
    const score = get(scoreStore);
    if (scoreFormat && task === "generate" && score.trim()) {
      t.score = score;
      if (scoreOrigin) t.scoreOrigin = scoreOrigin;
    }
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
      if (srcUrl) t.sourceUrl = srcUrl;
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
    if (t.task === "transcribe") {
      const res = await transcribeAudio(t.model, await fetchBlob(t.source!), signal);
      const label = t.sourceLabel || "a recording";
      addScore(res.score, "extracted", abcTitle(res.score, label), { label, url: t.sourceUrl });
      return { score: res.score };
    }
    const base = {
      model: t.model,
      prompt: t.prompt,
      lyrics: t.lyrics ?? "",
      score: t.score,
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
    if (mode === "create" && scoreBlocked) return false;
    if (mode === "create" && scoreBusy) return false;
    if (mode === "create") return !!prompt.trim();
    if (!source || !prompt.trim()) return false;
    return editTask !== "repaint" || windowValid;
  });

  let blockReason = $derived.by(() => {
    if (!model) return "Pick a model";
    if (mode === "stems") return source ? "" : "Pick a clip or paste a link on the left";
    if (mode === "edit" && !source) return "Pick a source clip on the left";
    if (isMiniMax && !$lyricsStore.trim()) return "MiniMax Music 3 needs lyrics";
    if (mode === "create" && scoreBlocked) return "A score needs planning on (melody or full)";
    if (mode === "create" && scoreBusy) return "Reading the reference song…";
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
      await runTake(formTurn("separate", "", ref, src.label, src.link), (sig) => execute(sessionLast(), sig));
      return;
    }
    prompt = "";
    await runTake(formTurn(editTask, text, ref, src.label, src.link), (sig) => execute(sessionLast(), sig), () => (prompt = text));
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
    if (t.task === "transcribe") again.score = undefined;
    await runTake(again, (sig) => execute(sessionLast(), sig));
  }

  // Load a take's settings back into the form, seed included, so it can be
  // varied from there.
  function reuseSettings(t: Turn) {
    if (t.task === "separate" || t.task === "transcribe") return;
    prompt = t.prompt;
    lyricsStore.set(t.lyrics ?? "");
    scoreStore.set(t.score ?? "");
    scoreOriginStore.set(t.score && t.scoreOrigin ? { ...t.scoreOrigin, score: t.score } : null);
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
    const m: ModelMode = t.task === "generate" ? "create" : "edit";
    if (modelsFor[m].some((x) => x.id === t.model)) STORE[m].set(t.model);
    modeStore.set(m);
    if (t.source && t.task !== "generate") void useTakeAsSource(t.source, t.sourceLabel || "source", m);
  }

  // Into Create with a generator that takes a score, if the current one does not.
  function toScoreModel() {
    if (scoreModels.length && !scoreModels.some((m) => m.id === get(genModelStore))) genModelStore.set(scoreModels[0].id);
    modeStore.set("create");
  }

  // A transcription take from before the composer did this itself.
  function useScore(t: Turn) {
    if (!t.score) return;
    scoreStore.set(t.score);
    scoreOriginStore.set({ label: t.sourceLabel || "a recording", url: t.sourceUrl, score: t.score });
    toScoreModel();
  }

  // --- score library ------------------------------------------------------------
  const pickedScoreStore = userPref<string>("playground-music-library-pick", "");
  let pickedScore = $derived($scoreLibrary.find((s) => s.id === $pickedScoreStore) ?? null);
  // The picked score vanished (deleted here or in another tab): show the newest.
  $effect(() => {
    if (!pickedScore && $scoreLibrary.length) pickedScoreStore.set([...$scoreLibrary].sort((a, b) => b.updatedAt - a.updatedAt)[0].id);
  });
  let libraryOpen = $state(false);

  // A library score into the composer, credited to its recording if it has one.
  function useLibraryScore(s: LibraryScore) {
    libraryOpen = false;
    scoreAbort?.abort();
    scoreJob = null;
    scoreError = "";
    scoreStore.set(s.abc);
    scoreOriginStore.set(s.origin ? { ...s.origin, score: s.abc } : null);
    if (get(cotStore) === "off") cotStore.set("melody");
    toScoreModel();
  }

  // A take as the reference for the next song.
  function extractScoreFrom(ref: string, label: string) {
    toScoreModel();
    void extractScore(label, "Loading…", async () => ({ wav: await fetchBlob(ref), label }));
  }

  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // clipboard denied: the text is selectable in the card
    }
  }
  function downloadText(text: string, name: string) {
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    download(url, name);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  // The take whose References popup is open.
  let refsOf = $state<{ t: Turn; ti: number } | null>(null);
  function settingsRows(t: Turn): [string, string][] {
    const rows: [string, string][] = [["Model", t.model]];
    if (t.seed !== undefined) rows.push(["Seed", String(t.seed)]);
    if (t.duration !== undefined) rows.push(["Length", `${t.duration} s`]);
    if (t.steps !== undefined) rows.push(["Steps", String(t.steps)]);
    if (t.guidance !== undefined) rows.push(["Guidance", String(t.guidance)]);
    if (t.task === "repaint" && t.start !== undefined) rows.push(["Window", `${t.start} to ${t.end === -1 ? "end" : t.end} s`]);
    if (t.repaintMode && t.task === "repaint") rows.push(["Blend", t.repaintMode]);
    if (t.strength !== undefined && (t.task === "repaint" || t.task === "cover")) rows.push(["Strength", String(t.strength)]);
    for (const [k, v] of Object.entries(t.options ?? {})) rows.push([k, String(v)]);
    return rows;
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
    transcribe: "Score",
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
  {#if !hasAny && !$scoreLibrary.length}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <Music class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No music models. Download ACE-Step, MiniMax Music 3, YuE2, HTDemucs or SheetSage2 from the model browser to use this tab.</p>
    </div>
  {:else}
    <div bind:this={rootEl} class="flex-1 min-h-0 flex">
      <!-- LEFT: mode + settings. -->
      <aside class="w-[19.375rem] shrink-0 flex flex-col min-h-0 bg-rail border-r border-card-border-inner">
        <div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner shrink-0">
          <div class="seg w-full" role="group" aria-label="Mode">
            {#each modes as m (m)}
              <button class="flex-1" aria-pressed={mode === m} onclick={() => modeStore.set(m)}>
                {m === "create" ? "Create" : m === "edit" ? "Edit" : m === "stems" ? "Stems" : "Scores"}
              </button>
            {/each}
          </div>
        </div>

        {#if mode === "scores"}
          <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll p-3">
            <ScoreList selected={$pickedScoreStore} onselect={(s) => pickedScoreStore.set(s.id)} />
          </div>
        {:else}
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
              <form class="flex items-center gap-1.5" onsubmit={(e) => { e.preventDefault(); void fetchLink(); }}>
                <div class="relative flex-1 min-w-0">
                  <LinkIcon class="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-txtsecondary pointer-events-none" />
                  <input
                    type="url"
                    bind:value={linkText}
                    placeholder="or paste a link (YouTube, SoundCloud…)"
                    class="{inputCls} !pl-7"
                    disabled={linkBusy}
                  />
                </div>
                {#if linkBusy}
                  <button type="button" class="btn btn--sm shrink-0" onclick={() => linkAbort?.abort()}>Cancel</button>
                {:else}
                  <button type="submit" class="btn btn--sm shrink-0" disabled={!linkText.trim() || sourceBusy}>Fetch</button>
                {/if}
              </form>
              {#if linkBusy}<span class="reason-shimmer-white text-micro">Downloading the audio…</span>{/if}
              {#if source?.link}
                <a href={source.link} target="_blank" rel="noopener noreferrer" class="truncate text-micro text-txtsecondary hover:text-txtmain underline">{source.link}</a>
              {/if}
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
            <p class="text-xs text-txtsecondary leading-snug">Splits the source into drums, bass, vocals and other. No prompt needed.</p>
          {/if}
        </div>
        {/if}
        <!-- One volume for every player on the page, the source clip included. It
             lives here, apart from the takes, so it reads as the page's volume. -->
        <div class="shrink-0 flex items-center gap-2 px-3 h-10 border-t border-card-border-inner" use:tip={"Playback volume for every player: the source clip and all takes"}>
          <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary shrink-0">Volume</span>
          <VolumeX class="w-3.5 h-3.5 text-txtsecondary shrink-0" />
          <input type="range" min="0" max="1" step="0.01" bind:value={$volumeStore} class="flex-1 min-w-0 accent-primary" aria-label="Playback volume, all players" />
          <Volume2 class="w-3.5 h-3.5 text-txtsecondary shrink-0" />
          <span class="font-mono text-micro text-txtsecondary tabular-nums w-9 text-right shrink-0">{Math.round($volumeStore * 100)}%</span>
        </div>
      </aside>

      {#if mode === "scores"}
        <!-- RIGHT: the picked library score. -->
        <div class="flex-1 min-w-0 flex flex-col min-h-0 px-6 pb-3">
          {#if pickedScore}
            <ScoreViewer
              score={pickedScore}
              volume={$volumeStore}
              useLabel={scoreModels.length ? "Use in Create" : ""}
              onuse={useLibraryScore}
              disclaimer={DISCLAIMER}
            />
          {:else}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary text-center">
              <Library class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Scores read from songs land here.<br />Upload .abc files on the left to add your own.</p>
            </div>
          {/if}
        </div>
      {:else}
      <!-- RIGHT: takes grid over the composer. -->
      <div class="flex-1 min-w-0 flex flex-col min-h-0 px-6">
        <div class="flex items-center gap-2.5 h-10 shrink-0">
          <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary">Takes</span>
          <span class="font-mono text-micro text-txtsecondary tabular-nums">{turns.length}</span>
          <div class="ml-auto flex items-center gap-2">
            <label class="shrink-0 flex items-center gap-1.5 text-micro font-medium uppercase tracking-wide text-txtsecondary cursor-pointer" use:tip={"Auto-play new takes"}>
              <Toggle size="sm" bind:checked={$autoPlayStore} />
              Auto-play
            </label>
          </div>
        </div>

        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b" use:scrollFade>
          {#if turns.length === 0 && !isGenerating}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary text-center">
              <Music class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Describe a song below and press Enter. Each take lands here,<br />and any take can be repainted, covered, split into stems or turned into a score.</p>
            </div>
          {:else}
            <div class="grid grid-cols-1 xl:grid-cols-2 gap-2.5 content-start pb-4">
              {#each pagedTurns as item (item.ti)}
                {@const t = item.t}
                {@const ti = item.ti}
                {@const done = !!(t.audio || t.stems || (t.task === "transcribe" && t.score))}
                {@const pending = !done && !t.error && gen?.id === $activeMusicChatId && gen?.ti === ti}
                <div class="relative min-w-0 rounded-[0.625rem] border border-card-border bg-surface p-3 flex flex-col gap-2 {menuIdx === ti ? 'z-30' : ''}">
                  <p class="text-[0.8125rem] leading-snug text-txtmain whitespace-pre-wrap line-clamp-2 min-h-[2.25rem] pr-6">
                    {#if t.task === "separate"}
                      <span class="text-txtsecondary">Stems of</span> {t.sourceLabel || "a clip"}
                    {:else if t.task === "transcribe"}
                      <span class="text-txtsecondary">Score of</span> {t.sourceLabel || "a clip"}
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
                  {:else if t.task === "transcribe" && t.score}
                    <pre class="max-h-40 overflow-auto pretty-scroll rounded bg-background border border-card-border-inner p-2 font-mono text-micro leading-relaxed text-txtmain whitespace-pre">{t.score}</pre>
                    <div class="flex items-center gap-1.5 flex-wrap">
                      <button class="btn btn--sm btn--primary inline-flex items-center gap-1.5" onclick={() => useScore(t)} disabled={isGenerating || !modelsFor.create.some((m) => !!m.capabilities?.music_score)} use:tip={"Put this score in the Create form"}>
                        <FileText class="w-3.5 h-3.5" /> Use this score
                      </button>
                      <button class="icon-btn" onclick={() => copyText(t.score!)} use:tip={"Copy"} aria-label="Copy score">
                        <Copy class="w-3.5 h-3.5" />
                      </button>
                      <button class="icon-btn" onclick={() => downloadText(t.score!, `${fileStem(t, ti)}.abc`)} use:tip={"Download .abc"} aria-label="Download score">
                        <Download class="w-3.5 h-3.5" />
                      </button>
                    </div>
                    <p class="text-micro text-txtsecondary leading-snug opacity-80">{DISCLAIMER}</p>
                  {:else if t.audio}
                    <AudioPlayer src={t.audio} volume={$volumeStore} wide autoplay={autoPlayRef === t.audio} />
                  {:else if pending}
                    <div class="flex items-center justify-between gap-2 h-8">
                      <div class="flex items-center gap-2 text-txtsecondary">
                        <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                        <span class="reason-shimmer-white font-medium text-xs">{t.task === "separate" ? "Separating…" : t.task === "transcribe" ? "Transcribing…" : t.task === "generate" ? "Composing…" : "Editing…"}</span>
                      </div>
                      <span class="font-mono text-micro text-txtsecondary tabular-nums">{fmtDur(elapsed)}</span>
                    </div>
                  {:else}
                    <div class="text-error text-xs">{t.task === "transcribe" ? "No score returned." : "No audio returned."}</div>
                  {/if}

                  <div class="flex items-center gap-1.5 min-w-0 flex-wrap">
                    <span class="shrink-0 rounded border border-card-border px-1.5 py-px font-mono text-micro text-txtsecondary">{TASK_LABEL[t.task]}</span>
                    {#if t.task === "repaint" && t.start !== undefined}
                      <span class="shrink-0 font-mono text-micro text-txtsecondary tabular-nums">{t.start}-{t.end === -1 ? "end" : t.end}s</span>
                    {/if}
                    {#if t.sourceLabel && t.task !== "separate" && t.task !== "transcribe" && t.task !== "generate"}
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
                      {#if t.task !== "separate" && t.task !== "transcribe"}
                        <button class="icon-btn" onclick={() => (refsOf = { t, ti })} use:tip={"References: what went into this take"} aria-label="References">
                          <ScrollText class="w-3.5 h-3.5" />
                        </button>
                      {/if}
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
                        {#if t.audio && transcribers.length && scoreModels.length}
                          <button
                            class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-40"
                            onclick={() => { menuIdx = null; void extractScoreFrom(t.audio!, takeLabel(ti)); }}
                            disabled={sourceBusy}
                          >
                            <FileText class="w-3.5 h-3.5" /> Use as reference
                          </button>
                        {/if}
                        {#if t.task !== "separate" && t.task !== "transcribe"}
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
                          <RefreshCw class="w-3.5 h-3.5" /> {t.task === "separate" || t.task === "transcribe" ? "Run again" : "New take, new seed"}
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
          <!-- The score the next song is sung over, floating on the composer so it
               is plain that it applies; X drops it (and stops a running read). -->
          {#if mode === "create" && scoreFormat && (scoreJob || $scoreStore.trim() || scoreError)}
            <div class="mb-2 flex flex-col gap-1.5">
              {#if scoreOpen && !scoreJob}
                <textarea
                  bind:value={$scoreStore}
                  rows="6"
                  aria-label="Score"
                  class="{inputCls} resize-y font-mono leading-relaxed min-h-[4rem] max-h-[calc(30vh/var(--qm-scale))]"
                ></textarea>
              {/if}
              <div
                class="self-start max-w-full inline-flex items-center gap-1.5 rounded-xl border bg-surface pl-2.5 pr-1 py-1 text-xs shadow-sm {scoreJob?.error || scoreError
                  ? 'border-error'
                  : 'border-card-border'}"
              >
                <FileText class="w-3.5 h-3.5 shrink-0 text-txtsecondary" />
                {#if scoreJob}
                  <span class="truncate min-w-0">{scoreJob.label}</span>
                  {#if scoreJob.error}
                    <span class="truncate min-w-0 text-error" use:tip={scoreJob.error}>{scoreJob.error}</span>
                  {:else}
                    <span class="reason-shimmer-white text-micro shrink-0">{scoreJob.stage}</span>
                  {/if}
                {:else if scoreError}
                  <span class="truncate min-w-0 text-error">{scoreError}</span>
                {:else}
                  <button
                    class="inline-flex items-center gap-1 min-w-0 text-txtmain hover:text-primary"
                    onclick={() => (scoreOpen = !scoreOpen)}
                    use:tip={scoreOpen ? "Hide the score" : "Show and edit the score"}
                  >
                    <span class="truncate">Score{scoreOrigin ? ` from ${scoreOrigin.label}` : ` (${scoreFormat.toUpperCase()})`}</span>
                    <ChevronDown class="w-3.5 h-3.5 shrink-0 transition-transform {scoreOpen ? 'rotate-180' : ''}" />
                  </button>
                  {#if scoreOrigin?.url}
                    <a href={scoreOrigin.url} target="_blank" rel="noopener noreferrer" class="shrink-0 text-txtsecondary hover:text-txtmain" use:tip={"Open the original"} aria-label="Open the original">
                      <LinkIcon class="w-3.5 h-3.5" />
                    </a>
                  {/if}
                  {#if scoreOrigin}
                    <span class="shrink-0 text-txtsecondary" use:tip={DISCLAIMER}><Info class="w-3.5 h-3.5" /></span>
                  {/if}
                {/if}
                <button class="icon-btn !w-6 !h-6 shrink-0" onclick={dismissScore} use:tip={scoreBusy ? "Stop and drop the reference" : "Drop the score"} aria-label="Drop the score">
                  <X class="w-3.5 h-3.5" />
                </button>
              </div>
            </div>
          {/if}
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
            <div class="flex items-center gap-2.5 pl-2 pr-2 pb-2 pt-1 min-w-0">
              {#if mode === "create" && scoreFormat}
                <div bind:this={attachEl} class="relative shrink-0">
                  <button
                    class="icon-btn !rounded-full w-8 h-8"
                    onclick={() => (attachOpen = !attachOpen)}
                    disabled={isGenerating}
                    aria-haspopup="menu"
                    aria-expanded={attachOpen}
                    aria-label="Add a reference song"
                    use:tip={"Add a reference song: its melody becomes the score"}
                  >
                    <Plus class="w-4 h-4" />
                  </button>
                  {#if attachOpen}
                    <div class="absolute left-0 bottom-full mb-2 z-30 w-72 flex flex-col rounded-lg border border-card-border bg-surface shadow-lg py-1 text-[0.8125rem]" role="menu">
                      {#if transcribers.length}
                        <label class="flex items-center gap-2 px-3 py-1.5 text-txtmain hover:bg-secondary cursor-pointer">
                          <Upload class="w-3.5 h-3.5" /> Upload a song
                          <input type="file" accept="audio/*,.wav,.mp3,.flac,.ogg" class="hidden" onchange={onReferenceFile} />
                        </label>
                        <form class="flex items-center gap-1.5 px-3 py-1.5" onsubmit={(e) => { e.preventDefault(); onReferenceLink(); }}>
                          <div class="relative flex-1 min-w-0">
                            <LinkIcon class="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-txtsecondary pointer-events-none" />
                            <input type="url" bind:value={attachLink} placeholder="YouTube or other link" class="{inputCls} !pl-7" />
                          </div>
                          <button type="submit" class="btn btn--sm shrink-0" disabled={!attachLink.trim()}>Add</button>
                        </form>
                        <span class="px-3 pb-1 text-micro text-txtsecondary leading-snug">SheetSage2 reads its melody into a score. No Run needed.</span>
                      {:else}
                        <span class="px-3 py-1.5 text-xs text-txtsecondary leading-snug">Download SheetSage2 to use a song as a reference.</span>
                      {/if}
                      <div class="my-1 border-t border-card-border-inner"></div>
                      {#if scoreFormat === "abc"}
                        <button class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-50" role="menuitem" disabled={!$scoreLibrary.length} onclick={() => { attachOpen = false; libraryOpen = true; }}>
                          <Library class="w-3.5 h-3.5" /> From the library…
                          <span class="ml-auto font-mono text-micro text-txtsecondary tabular-nums">{$scoreLibrary.length}</span>
                        </button>
                      {/if}
                      <label class="flex items-center gap-2 px-3 py-1.5 text-txtmain hover:bg-secondary cursor-pointer">
                        <FileText class="w-3.5 h-3.5" /> Load a .{scoreFormat} score
                        <input type="file" accept=".{scoreFormat},text/plain" class="hidden" onchange={(e) => { attachOpen = false; scoreAbort?.abort(); scoreJob = null; void onScoreFile(e); }} />
                      </label>
                    </div>
                  {/if}
                </div>
              {/if}
              <div class="min-w-0">
                {#key mode}
                  <ModelSelector
                    bind:value={$modelStore}
                    placeholder="Select a model…"
                    disabled={isGenerating}
                    capabilities={CAP[runMode]}
                    matchAny
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
      {/if}
    </div>
  {/if}
</div>

<svelte:window
  onkeydown={(e) => {
    if (e.key !== "Escape") return;
    if (refsOf) refsOf = null;
    libraryOpen = false;
    attachOpen = false;
  }}
  onpointerdown={(e) => { if (attachOpen && attachEl && !attachEl.contains(e.target as Node)) attachOpen = false; }}
/>

{#if libraryOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="presentation" onclick={(e) => { if (e.target === e.currentTarget) libraryOpen = false; }}>
    <div class="w-full max-w-md rounded-2xl border border-card-border bg-surface p-5 shadow-xl max-h-[calc(80vh/var(--qm-scale))] flex flex-col gap-3" role="dialog" aria-modal="true" aria-label="Score library">
      <div class="flex items-center gap-2">
        <Library class="w-4 h-4 text-txtsecondary" />
        <span class="font-medium">Sing over a library score</span>
        <button class="icon-btn ml-auto" onclick={() => (libraryOpen = false)} aria-label="Close"><X class="w-4 h-4" /></button>
      </div>
      <div class="min-h-0 overflow-y-auto pretty-scroll -mx-1 px-1">
        <ScoreList dense onselect={useLibraryScore} />
      </div>
    </div>
  </div>
{/if}

{#if refsOf}
  {@const rt = refsOf.t}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="presentation" onclick={(e) => { if (e.target === e.currentTarget) refsOf = null; }}>
    <div class="w-full max-w-lg rounded-2xl border border-card-border bg-surface p-5 shadow-xl max-h-[calc(80vh/var(--qm-scale))] overflow-y-auto pretty-scroll flex flex-col gap-3.5" role="dialog" aria-modal="true" aria-label="References">
      <div class="flex items-center gap-2">
        <ScrollText class="w-4 h-4 text-txtsecondary" />
        <span class="font-medium">References</span>
        <span class="font-mono text-micro text-txtsecondary">{TASK_LABEL[rt.task]} · {takeLabel(refsOf.ti)}</span>
        <button class="icon-btn ml-auto" onclick={() => (refsOf = null)} aria-label="Close"><X class="w-4 h-4" /></button>
      </div>

      {#if rt.prompt}
        <div class="flex flex-col gap-1">
          <span class={labelCls}>{rt.task === "generate" ? "Style" : "Instruction"}</span>
          <p class="text-xs text-txtmain whitespace-pre-wrap">{rt.prompt}</p>
        </div>
      {/if}

      {#if rt.source}
        <div class="flex flex-col gap-1">
          <span class={labelCls}>Source clip</span>
          <AudioPlayer src={rt.source} volume={$volumeStore} label={rt.sourceLabel || "source"} wide />
          {#if rt.sourceUrl}
            <a href={rt.sourceUrl} target="_blank" rel="noopener noreferrer" class="truncate text-micro text-txtsecondary hover:text-txtmain underline">{rt.sourceUrl}</a>
          {/if}
        </div>
      {/if}

      {#if rt.lyrics !== undefined && rt.task !== "separate" && rt.task !== "transcribe"}
        <div class="flex flex-col gap-1">
          <span class={labelCls}>Lyrics</span>
          {#if rt.lyrics.trim()}
            <pre class="max-h-48 overflow-auto pretty-scroll rounded bg-background border border-card-border-inner p-2 font-mono text-micro leading-relaxed whitespace-pre-wrap">{rt.lyrics}</pre>
          {:else}
            <span class="text-xs text-txtsecondary">None</span>
          {/if}
        </div>
      {/if}

      {#if rt.score && rt.task !== "transcribe"}
        <div class="flex flex-col gap-1">
          <div class="flex items-center gap-2">
            <span class={labelCls}>Score</span>
            <button class="icon-btn ml-auto !w-6 !h-6" onclick={() => copyText(rt.score!)} use:tip={"Copy"} aria-label="Copy score"><Copy class="w-3.5 h-3.5" /></button>
          </div>
          <pre class="max-h-48 overflow-auto pretty-scroll rounded bg-background border border-card-border-inner p-2 font-mono text-micro leading-relaxed whitespace-pre">{rt.score}</pre>
          {#if rt.scoreOrigin}
            <span class="text-micro text-txtsecondary">
              Extracted from
              {#if rt.scoreOrigin.url}
                <a href={rt.scoreOrigin.url} target="_blank" rel="noopener noreferrer" class="underline hover:text-txtmain">{rt.scoreOrigin.label}</a>
              {:else}
                {rt.scoreOrigin.label}
              {/if}
            </span>
          {/if}
        </div>
      {/if}

      {#if rt.stems?.length}
        <div class="flex flex-col gap-1">
          <span class={labelCls}>Stems</span>
          {#each rt.stems as st (st.name)}
            <AudioPlayer src={st.audio} volume={$volumeStore} label={st.name} wide />
          {/each}
        </div>
      {/if}

      <div class="flex flex-col gap-1">
        <span class={labelCls}>Settings</span>
        <div class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
          {#each settingsRows(rt) as [k, v] (k)}
            <span class="text-txtsecondary">{k}</span>
            <span class="font-mono break-all">{v}</span>
          {/each}
        </div>
      </div>

      {#if rt.scoreOrigin || rt.sourceUrl}
        <p class="text-micro text-txtsecondary leading-snug opacity-80">{DISCLAIMER}</p>
      {/if}
    </div>
  </div>
{/if}
