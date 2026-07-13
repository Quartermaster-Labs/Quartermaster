<script lang="ts">
  import { get } from "svelte/store";
  import { models } from "../../stores/api";
  import { userPref } from "../../stores/prefs";
  import {
    speechSessions,
    activeSpeechChatId,
    generatingSpeechChatId,
    newSpeechChatId,
    deriveSpeechTitle,
    type SpeechSession,
    type Turn,
  } from "../../stores/speechHistory";
  import { generateSpeech } from "../../lib/speechApi";
  import { inferenceHeaders } from "../../lib/inferenceAuth";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import ModelSelector from "./ModelSelector.svelte";
  import { Volume2, VolumeX, Download, RefreshCw, Plus, Pencil, X, Save, Upload, Square, Check, MoreVertical, Trash2, Mic, ChevronLeft } from "lucide-svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import AudioPlayer from "./AudioPlayer.svelte";

  // Speech studio. Left column (60%): a voice panel that fills the height (voice
  // list + cloning) above a chat-composer-style text input. Right column (40%):
  // every generated clip as a waveform card, with an always-visible volume slider
  // pinned at the bottom. Threads still persist per user via speechSessions; this
  // tab renders one session's turns as a paginated clip library.

  const selectedModelStore = userPref<string>("playground-speech-model", "");
  const selectedVoiceStore = userPref<string>("playground-speech-voice", "");
  const autoPlayStore = userPref<boolean>("playground-speech-autoplay", false);
  const volumeStore = userPref<number>("playground-speech-volume", 1);

  // voice_design models have no speakers — instead the user writes/keeps their
  // own style-description presets (model-agnostic) and picks one like a voice.
  type VoicePreset = { name: string; instructions: string };
  const voicePresetsStore = userPref<VoicePreset[]>("playground-voice-presets", []);
  const selectedPresetStore = userPref<string>("playground-speech-preset", "");

  // Built-in starter presets so voice_design works out of the box. Read-only
  // (can't be deleted); user presets with the same name shadow them.
  const DEFAULT_PRESETS: VoicePreset[] = [
    { name: "Warm Narrator", instructions: "A warm, friendly middle-aged woman narrating an audiobook — calm and clear, at a relaxed, even pace." },
    { name: "Deep Announcer", instructions: "A deep, resonant male voice like a movie-trailer announcer — dramatic, confident, and slow." },
    { name: "Cheerful Assistant", instructions: "A bright, upbeat young voice — energetic and helpful, speaking quickly and clearly with a smile." },
    { name: "Calm Meditation", instructions: "A soft, soothing voice speaking slowly and gently, with long pauses, ideal for guided meditation." },
    { name: "Gruff Storyteller", instructions: "An older man with a slight gravelly rasp, telling a story by the fire — unhurried, warm, and expressive." },
  ];
  let allPresets = $derived([
    ...DEFAULT_PRESETS,
    ...$voicePresetsStore.filter((p) => !DEFAULT_PRESETS.some((d) => d.name === p.name)),
  ]);
  let isDefaultPreset = (name: string) => DEFAULT_PRESETS.some((d) => d.name === name);

  // "" = model's default speaker (tts-server picks it; never 400s on an unknown
  // name). Real speaker names come from refreshVoices() → GET /v1/audio/voices.
  const defaultVoices = [""];
  const CACHE_KEY = "playground-speech-voices-cache-v4"; // v4: base models keep "" + cloned voices; kind-aware

  let availableVoices = $state<string[]>(defaultVoices);
  let isLoadingVoices = $state(false);
  // voice_design models also expose no named speakers, but reject voice refs
  // ("voice references are only valid for base models"), so they must NOT show
  // cloning. Detect them by the talker-gguf suffix baked into the model id.
  let isVoiceDesign = $derived(/voice[\s_-]*design/i.test($selectedModelStore));
  // A base model has no named speakers → its "" default is valid and it accepts
  // voice clones. A custom_voice model has speakers and REQUIRES a named voice.
  let isBaseModel = $derived(availableVoices.includes("") && !isVoiceDesign);
  let activePreset = $derived(allPresets.find((p) => p.name === $selectedPresetStore) ?? null);

  // Resolve what to actually send + how to label the clip. voice_design sends no
  // speaker (rejected) — it sends the preset's style description as instructions.
  // A fallback turn lets regenerate reuse a clip's design if no preset is picked.
  function resolveGen(fallback?: Turn): { sendVoice: string; instructions: string; label: string } {
    if (isVoiceDesign) {
      return {
        sendVoice: "",
        instructions: activePreset?.instructions ?? fallback?.instructions ?? "",
        label: activePreset?.name ?? fallback?.voice ?? "Custom",
      };
    }
    const v = $selectedVoiceStore;
    return { sendVoice: v, instructions: "", label: v || "Default" };
  }

  // --- voice_design presets (create / delete) ------------------------------
  let showPreset = $state(false);
  let presetName = $state("");
  let presetInstr = $state("");
  function openPreset(preset?: VoicePreset) {
    presetName = preset?.name ?? "";
    presetInstr = preset?.instructions ?? "";
    showPreset = true;
  }
  function savePreset() {
    const n = presetName.trim();
    const d = presetInstr.trim();
    if (!n || !d) return;
    voicePresetsStore.update((list) => [...list.filter((p) => p.name !== n), { name: n, instructions: d }]);
    selectedPresetStore.set(n);
    showPreset = false;
  }
  function deletePreset(name: string) {
    voicePresetsStore.update((list) => list.filter((p) => p.name !== name));
    if (get(selectedPresetStore) === name) selectedPresetStore.set("");
  }

  function getVoicesCache(): Record<string, string[]> {
    if (typeof window === "undefined") return {};
    try {
      const saved = localStorage.getItem(CACHE_KEY);
      return saved ? JSON.parse(saved) : {};
    } catch {
      return {};
    }
  }
  function saveVoicesCache(cache: Record<string, string[]>) {
    if (typeof window === "undefined") return;
    try {
      localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
    } catch (e) {
      console.error("Error saving voices cache", e);
    }
  }

  // Apply a voice list and keep the selection valid — a custom_voice model has
  // named speakers and REQUIRES one (no "Default"), so an out-of-list selection
  // (e.g. "" carried over from a base model) is snapped to the first speaker.
  function applyVoices(voices: string[]) {
    availableVoices = voices;
    if (!voices.includes(get(selectedVoiceStore))) selectedVoiceStore.set(voices[0] ?? "");
  }

  // Restore cached voices for the selected model, else auto-fetch its real list.
  let lastVoiceModel: string | null = null;
  $effect(() => {
    const model = $selectedModelStore;
    if (model === lastVoiceModel) return;
    lastVoiceModel = model;
    // Cache seeds the UI instantly. Only hit the server when the model is
    // ALREADY loaded — GET /v1/audio/voices proxies to tts-server and would
    // otherwise force a model load just from opening the tab. A manual refresh
    // or the first generation (which loads the model anyway) fetches fresh.
    const cache = getVoicesCache();
    applyVoices(model && cache[model] ? cache[model] : defaultVoices);
    if (model && get(models).some((m) => m.id === model && m.state === "ready")) refreshVoices();
  });

  async function refreshVoices() {
    const model = $selectedModelStore;
    if (!model || isLoadingVoices) return;
    isLoadingVoices = true;
    try {
      const response = await fetch(`/v1/audio/voices?model=${encodeURIComponent(model)}`, {
        headers: inferenceHeaders(),
      });
      let voices = defaultVoices;
      if (response.ok) {
        const data = await response.json();
        const got: unknown[] = Array.isArray(data) ? data : data.voices || [];
        // tts-server returns {voices:[{name,kind}]} (kind = "speaker" for built-in,
        // "registered" for a clone); also tolerate bare strings.
        const norm = got
          .map((v) => (typeof v === "string" ? { name: v, kind: "speaker" } : (v as { name?: string; kind?: string })))
          .filter((v): v is { name: string; kind?: string } => !!v?.name);
        const speakers = norm.filter((v) => v.kind !== "registered").map((v) => v.name);
        const registered = norm.filter((v) => v.kind === "registered").map((v) => v.name);
        // custom_voice model (named speakers) requires a named voice → no "".
        // base model (no speakers) → "" default plus any cloned voices.
        voices = speakers.length ? [...speakers, ...registered] : ["", ...registered];
      }
      const cache = getVoicesCache();
      cache[model] = voices;
      saveVoicesCache(cache);
      applyVoices(voices);
    } catch {
      applyVoices(defaultVoices);
    } finally {
      isLoadingVoices = false;
    }
  }

  // Delete a registered clone: DELETE /v1/audio/voices/{name}?model= (rewritten
  // to tts-server's /v1/voices/{name}). Only base-model clones are deletable.
  async function deleteVoice(name: string) {
    const model = $selectedModelStore;
    if (!model || !name || !confirm(`Delete voice "${name}"?`)) return;
    try {
      const resp = await fetch(`/v1/audio/voices/${encodeURIComponent(name)}?model=${encodeURIComponent(model)}`, {
        method: "DELETE",
        headers: inferenceHeaders(),
      });
      if (!resp.ok) throw new Error((await resp.text()) || `HTTP ${resp.status}`);
      if (get(selectedVoiceStore) === name) selectedVoiceStore.set("");
      await refreshVoices();
    } catch (e) {
      console.error("delete voice failed", e);
    }
  }

  // --- voice cloning (base models) -----------------------------------------
  // POST /v1/audio/voices {model,name,wav_b64,ref_text?} → tts-server registers
  // a cloned voice (base64 WAV, ref_text enables ICL clone mode). Path is
  // rewritten to /v1/voices by the reverse proxy; auth via inferenceHeaders().
  // Multi-step clone modal: step "choose" collects the name + method, then either
  // "live" (read a passage) or "clip" (upload a file).
  let showClone = $state(false);
  let cloneStep = $state<"choose" | "live" | "clip">("choose");
  let newVoiceName = $state("");
  let newVoiceRefText = $state("");
  let newVoiceFile = $state<File | null>(null);
  let creatingVoice = $state(false);
  let createVoiceError = $state("");

  // Live-record clone reads a fixed passage aloud, so the recording ships with a
  // known ref_text (ICL cloning wants matching transcript).
  const RECORD_SCRIPT =
    "The north wind and the sun were disputing which was the stronger, when a traveler came along wrapped in a warm cloak. They agreed that the one who first made the traveler take his cloak off should be considered stronger than the other.";
  let recording = $state(false);
  let recordedBlob = $state<Blob | null>(null);
  let recordedUrl = $state("");
  let recError = $state("");
  let recorder: MediaRecorder | null = null;
  let recChunks: Blob[] = [];

  function onVoiceFile(e: Event) {
    newVoiceFile = (e.target as HTMLInputElement).files?.[0] ?? null;
  }

  function blobToBase64(blob: Blob): Promise<string> {
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => {
        const s = r.result as string; // data:...;base64,XXXX
        resolve(s.slice(s.indexOf(",") + 1));
      };
      r.onerror = () => reject(r.error);
      r.readAsDataURL(blob);
    });
  }

  // MediaRecorder yields webm/opus, but tts-server wants WAV. Decode → mono →
  // 16-bit PCM WAV. ponytail: mono 16-bit is enough for a voice ref; bump to
  // stereo/float only if cloning quality demands it.
  function encodeWav(audio: AudioBuffer): Blob {
    const ch = audio.numberOfChannels;
    const len = audio.length;
    const mono = new Float32Array(len);
    for (let c = 0; c < ch; c++) {
      const cd = audio.getChannelData(c);
      for (let i = 0; i < len; i++) mono[i] += cd[i] / ch;
    }
    const rate = audio.sampleRate;
    const view = new DataView(new ArrayBuffer(44 + len * 2));
    const w = (o: number, s: string) => {
      for (let i = 0; i < s.length; i++) view.setUint8(o + i, s.charCodeAt(i));
    };
    w(0, "RIFF");
    view.setUint32(4, 36 + len * 2, true);
    w(8, "WAVE");
    w(12, "fmt ");
    view.setUint32(16, 16, true);
    view.setUint16(20, 1, true); // PCM
    view.setUint16(22, 1, true); // mono
    view.setUint32(24, rate, true);
    view.setUint32(28, rate * 2, true); // byte rate
    view.setUint16(32, 2, true); // block align
    view.setUint16(34, 16, true); // bits
    w(36, "data");
    view.setUint32(40, len * 2, true);
    let off = 44;
    for (let i = 0; i < len; i++) {
      const s = Math.max(-1, Math.min(1, mono[i]));
      view.setInt16(off, s < 0 ? s * 0x8000 : s * 0x7fff, true);
      off += 2;
    }
    return new Blob([view], { type: "audio/wav" });
  }

  async function startRecording() {
    recError = "";
    recordedBlob = null;
    if (recordedUrl) URL.revokeObjectURL(recordedUrl);
    recordedUrl = "";
    try {
      if (!navigator.mediaDevices?.getUserMedia) {
        recError =
          "Microphone needs a secure context. Open this page via http://localhost or serve it over HTTPS.";
        return;
      }
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      recChunks = [];
      recorder = new MediaRecorder(stream);
      recorder.ondataavailable = (e) => {
        if (e.data.size) recChunks.push(e.data);
      };
      recorder.onstop = async () => {
        stream.getTracks().forEach((t) => t.stop());
        try {
          const buf = await new Blob(recChunks).arrayBuffer();
          const AC = window.AudioContext || (window as any).webkitAudioContext;
          const ctx = new AC();
          const audio = await ctx.decodeAudioData(buf);
          ctx.close();
          recordedBlob = encodeWav(audio);
          recordedUrl = URL.createObjectURL(recordedBlob);
        } catch {
          recError = "Could not process the recording.";
        }
      };
      recorder.start();
      recording = true;
    } catch {
      recError = "Microphone access was denied.";
    }
  }
  function stopRecording() {
    recorder?.stop();
    recording = false;
  }

  function openClone() {
    if (recording) stopRecording();
    if (recordedUrl) URL.revokeObjectURL(recordedUrl);
    recordedUrl = "";
    recordedBlob = null;
    recError = "";
    createVoiceError = "";
    newVoiceName = "";
    newVoiceRefText = "";
    newVoiceFile = null;
    cloneStep = "choose";
    showClone = true;
  }
  function closeClone() {
    if (recording) stopRecording();
    if (recordedUrl) URL.revokeObjectURL(recordedUrl);
    recordedUrl = "";
    recordedBlob = null;
    recError = "";
    showClone = false;
  }

  // Shared submit for both clone paths: POST base64 WAV + optional ref_text.
  async function submitClone(source: Blob, refText: string): Promise<boolean> {
    const model = $selectedModelStore;
    const name = newVoiceName.trim();
    if (!model || !name || !source || creatingVoice) return false;
    creatingVoice = true;
    createVoiceError = "";
    try {
      const wav_b64 = await blobToBase64(source);
      const body: Record<string, unknown> = { model, name, wav_b64 };
      if (refText.trim()) body.ref_text = refText.trim();
      const resp = await fetch("/v1/audio/voices", {
        method: "POST",
        headers: { ...inferenceHeaders(), "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!resp.ok) throw new Error((await resp.text()) || `HTTP ${resp.status}`);
      await refreshVoices();
      selectedVoiceStore.set(name);
      return true;
    } catch (e) {
      createVoiceError = e instanceof Error ? e.message : "Voice creation failed";
      return false;
    } finally {
      creatingVoice = false;
    }
  }

  // tts-server only decodes PCM16 WAV, so an uploaded mp3/aac/float-wav must be
  // transcoded first — same decode→encodeWav path as the mic recording.
  async function fileToWav(file: Blob): Promise<Blob> {
    const buf = await file.arrayBuffer();
    const AC = window.AudioContext || (window as any).webkitAudioContext;
    const ctx = new AC();
    const audio = await ctx.decodeAudioData(buf);
    ctx.close();
    return encodeWav(audio);
  }

  async function createVoice() {
    if (!newVoiceFile) return;
    let wav: Blob;
    try {
      wav = await fileToWav(newVoiceFile);
    } catch {
      createVoiceError = "Audio format unrecognized — use a WAV or MP3 clip.";
      return;
    }
    const ok = await submitClone(wav, newVoiceRefText);
    if (ok) closeClone();
  }
  async function finalizeRecording() {
    if (!recordedBlob) return;
    const ok = await submitClone(recordedBlob, RECORD_SCRIPT);
    if (ok) closeClone();
  }

  // Ensure a valid active thread exists (first run / persisted id gone). Mirrors
  // the image tab's initImageChats.
  function initSpeechChats() {
    const sessions = get(speechSessions);
    let id = get(activeSpeechChatId);
    if (!sessions.some((s) => s.id === id)) {
      const recent = sessions.reduce<SpeechSession | null>(
        (best, s) => (!best || s.updatedAt > best.updatedAt ? s : best),
        null,
      );
      id = recent ? recent.id : "";
      if (!id) {
        const s: SpeechSession = { id: newSpeechChatId(), title: "New speech", turns: [], updatedAt: Date.now() };
        speechSessions.set([s]);
        id = s.id;
      }
      activeSpeechChatId.set(id);
    }
  }
  initSpeechChats();

  let activeSession = $derived($speechSessions.find((s) => s.id === $activeSpeechChatId));
  let turns = $derived(activeSession?.turns ?? []);

  // Clip library: newest first, paginated. Each entry keeps its original turn
  // index (ti) so audioEls[ti] auto-play and edit/regenerate stay correct.
  const PAGE_SIZE = 8;
  let page = $state(1);
  let ordered = $derived(turns.map((t, ti) => ({ t, ti })).reverse());
  let pageCount = $derived(Math.max(1, Math.ceil(ordered.length / PAGE_SIZE)));
  let pagedTurns = $derived(ordered.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE));
  // Jump to the newest page on a new clip / thread switch; clamp if it shrinks.
  $effect(() => {
    void turns.length;
    void $activeSpeechChatId;
    page = 1;
  });
  $effect(() => {
    if (page > pageCount) page = pageCount;
  });

  // Session the generation loop is writing to (null = idle). Mirrored to the store
  // so the rail can flag the generating row. One thread generates at a time.
  let genId = $state<string | null>(null);
  let isGenerating = $derived(genId !== null);
  $effect(() => {
    generatingSpeechChatId.set(genId);
  });
  $effect(() => {
    playgroundStores.speechGenerating.set(isGenerating);
  });

  // --- store helpers: turns live in speechSessions, keyed by session id ---
  function sessionById(id: string): SpeechSession | undefined {
    return get(speechSessions).find((s) => s.id === id);
  }
  function patchSession(id: string, fields: Partial<SpeechSession>, bump = false) {
    speechSessions.update((ss) => {
      const i = ss.findIndex((s) => s.id === id);
      if (i === -1) return ss;
      const copy = [...ss];
      copy[i] = { ...copy[i], ...fields, ...(bump ? { updatedAt: Date.now() } : {}) };
      return copy;
    });
  }
  function appendTurn(id: string, turn: Turn) {
    const s = sessionById(id);
    if (!s) return;
    const nextTurns = [...s.turns, turn];
    const title = s.titled ? s.title : deriveSpeechTitle(nextTurns);
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
    const title = s.titled ? s.title : deriveSpeechTitle(next);
    patchSession(id, { turns: next, title }, bump);
  }

  let prompt = $state("");
  let promptEl = $state<HTMLTextAreaElement | null>(null);
  // Auto-grow the composer to fit its text, up to 50% of the viewport (matches
  // the chat composer's JS-driven grow). Runs on type and on clear-after-send.
  $effect(() => {
    void prompt;
    const el = promptEl;
    if (!el) return;
    el.style.height = "auto";
    if (el.scrollHeight > 0) el.style.height = Math.min(el.scrollHeight, Math.floor(window.innerHeight * 0.5)) + "px";
  });
  let abortController = $state<AbortController | null>(null);
  let elapsed = $state(0);
  let editingIdx = $state<number | null>(null);
  let editText = $state("");
  let menuIdx = $state<number | null>(null); // open three-dot menu (turn index)
  // AudioPlayer component instances (expose play() for auto-play on completion).
  let audioEls: (AudioPlayer | null)[] = $state([]);

  let hasModels = $derived($models.some((m) => !m.unlisted));

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

  function fmtDur(s: number): string {
    if (s < 60) return `${s}s`;
    return `${Math.floor(s / 60)}m ${s % 60}s`;
  }

  // wav blobs are small — persist them as data URLs so a clip survives a reload
  // and the server round-trip (an object URL would die on refresh).
  function blobToDataUrl(blob: Blob): Promise<string> {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result as string);
      reader.onerror = () => reject(reader.error);
      reader.readAsDataURL(blob);
    });
  }

  // Run a turn already appended at index ti of session `id` and fold its result /
  // error back into the store. Writes by session id, so the reply lands even if the
  // user switched threads mid-generation. genId gates one turn at a time.
  async function runTurn(id: string, ti: number, text: string, voice: string, instructions: string, onAbort: () => void) {
    genId = id;
    abortController = new AbortController();
    try {
      const blob = await generateSpeech($selectedModelStore, text, voice, abortController.signal, instructions);
      const audio = await blobToDataUrl(blob);
      updateTurn(id, ti, { audio, secs: elapsed });
      if (get(autoPlayStore) && id === get(activeSpeechChatId)) {
        queueMicrotask(() => audioEls[ti]?.play());
      }
    } catch (err) {
      if (err instanceof Error && err.name === "AbortError") {
        const s = sessionById(id);
        if (s) patchSession(id, { turns: s.turns.filter((_, i) => i !== ti) });
        onAbort();
      } else {
        updateTurn(id, ti, { error: err instanceof Error ? err.message : "An error occurred" });
      }
    } finally {
      genId = null;
      abortController = null;
    }
  }

  async function send() {
    const text = prompt.trim();
    if (!$selectedModelStore || isGenerating || !text) return;
    if (isVoiceDesign && !activePreset) return; // need a design preset selected
    const id = $activeSpeechChatId;
    if (!sessionById(id)) return;
    const { sendVoice, instructions, label } = resolveGen();
    prompt = "";
    const ti = sessionById(id)!.turns.length;
    appendTurn(id, { text, voice: label, instructions, audio: undefined });
    await runTurn(id, ti, text, sendVoice, instructions, () => {
      prompt = text;
    });
  }

  // Re-run a turn with its same text, dropping everything after it (mirrors the
  // image tab's regenerate) — uses the current voice selection.
  async function regenerate(idx: number) {
    if (isGenerating || !$selectedModelStore) return;
    const id = $activeSpeechChatId;
    const s = sessionById(id);
    const t = s?.turns[idx];
    if (!s || !t) return;
    const { sendVoice, instructions, label } = resolveGen(t);
    setTurns(id, [...s.turns.slice(0, idx), { text: t.text, voice: label, instructions, audio: undefined }], true);
    await runTurn(id, idx, t.text, sendVoice, instructions, () => {});
  }

  function startEdit(idx: number) {
    if (isGenerating) return;
    editingIdx = idx;
    editText = turns[idx].text;
  }
  function cancelEdit() {
    editingIdx = null;
    editText = "";
  }
  async function saveEdit() {
    const idx = editingIdx;
    if (idx === null) return;
    const text = editText.trim();
    editingIdx = null;
    editText = "";
    if (isGenerating || !$selectedModelStore || !text) return;
    const id = $activeSpeechChatId;
    const s = sessionById(id);
    if (!s) return;
    const { sendVoice, instructions, label } = resolveGen(s.turns[idx]);
    setTurns(id, [...s.turns.slice(0, idx), { text, voice: label, instructions, audio: undefined }], true);
    await runTurn(id, idx, text, sendVoice, instructions, () => {});
  }
  function editKeyDown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void saveEdit();
    } else if (event.key === "Escape") {
      cancelEdit();
    }
  }

  function cancelGeneration() {
    abortController?.abort();
  }

  // Delete a generated clip from the active thread. Blocked while generating so
  // the in-flight turn index stays stable.
  function deleteTurn(idx: number) {
    if (isGenerating) return;
    const id = $activeSpeechChatId;
    const s = sessionById(id);
    if (!s) return;
    setTurns(id, s.turns.filter((_, i) => i !== idx), true);
  }

  function downloadAudio(t: Turn) {
    if (!t.audio) return;
    const a = document.createElement("a");
    a.href = t.audio;
    a.download = `${t.voice || "speech"}-${Date.now()}.wav`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
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
      <Volume2 class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No models configured. Add models to your configuration to generate speech.</p>
    </div>
  {:else}
    <div class="flex-1 flex flex-col md:flex-row gap-4 min-h-0 w-full py-4">
      <!-- LEFT (60%): voice panel fills the height, composer pinned below -------- -->
      <div class="w-full md:w-3/5 shrink-0 flex flex-col gap-3 min-h-0">
        <!-- Voice panel — borderless table. voice_design lists user presets; every
             other model type lists the server's voices. -->
        <div class="flex-1 min-h-0 flex flex-col gap-2 px-1">
          <div class="flex items-center justify-between shrink-0">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">{isVoiceDesign ? "Voice preset" : "Voice"}</span>
            {#if !isVoiceDesign}
              <button
                class="p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
                onclick={refreshVoices}
                disabled={isLoadingVoices || !$selectedModelStore}
                title="Load the voices this model actually offers"
              >
                <RefreshCw class="w-3.5 h-3.5 {isLoadingVoices ? 'animate-spin' : ''}" />
              </button>
            {/if}
          </div>

          {#if isVoiceDesign}
            <!-- Design presets: pick one like a voice; each is a saved style desc. -->
            <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll flex flex-col">
              {#each allPresets as p (p.name)}
                <div
                  class="group flex items-center gap-2 pr-1 rounded-md {$selectedPresetStore === p.name ? 'bg-[#141414] text-white' : 'text-txtmain hover:bg-secondary'}"
                >
                  <button class="flex-1 min-w-0 flex flex-col items-start px-3 py-1.5 text-left" onclick={() => selectedPresetStore.set(p.name)}>
                    <span class="truncate w-full text-[0.8125rem] font-medium">{p.name}</span>
                    <span class="truncate w-full text-[0.6875rem] {$selectedPresetStore === p.name ? 'text-white/60' : 'text-txtsecondary'}">{p.instructions}</span>
                  </button>
                  {#if $selectedPresetStore === p.name}<Check class="w-4 h-4 shrink-0" />{/if}
                  <button
                    class="shrink-0 p-1 rounded opacity-0 group-hover:opacity-100 {$selectedPresetStore === p.name ? 'text-white/70 hover:text-white' : 'text-txtsecondary hover:text-primary'}"
                    onclick={() => openPreset(p)}
                    title="Edit preset"
                  >
                    <Pencil class="w-3.5 h-3.5" />
                  </button>
                  {#if !isDefaultPreset(p.name)}
                    <button
                      class="shrink-0 p-1 rounded opacity-0 group-hover:opacity-100 {$selectedPresetStore === p.name ? 'text-white/70 hover:text-white' : 'text-txtsecondary hover:text-red-500'}"
                      onclick={() => deletePreset(p.name)}
                      title="Delete preset"
                    >
                      <Trash2 class="w-3.5 h-3.5" />
                    </button>
                  {/if}
                </div>
              {/each}
            </div>
            <button class="shrink-0 inline-flex items-center gap-1.5 text-[0.8125rem] text-primary hover:underline" onclick={() => openPreset()}>
              <Plus class="w-4 h-4" /> Design a voice
            </button>
          {:else}
            <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll flex flex-col">
              {#each availableVoices as v (v)}
                <div
                  class="group flex items-center gap-2 pr-1 rounded-md {$selectedVoiceStore === v ? 'bg-[#141414] text-white' : 'text-txtmain hover:bg-secondary'}"
                >
                  <button
                    class="flex-1 min-w-0 flex items-center justify-between gap-2 px-3 py-1.5 text-[0.8125rem] text-left"
                    onclick={() => selectedVoiceStore.set(v)}
                  >
                    <span class="truncate">{v || "Default"}</span>
                  </button>
                  {#if isBaseModel && v}
                    <button
                      class="shrink-0 p-1 rounded opacity-0 group-hover:opacity-100 {$selectedVoiceStore === v ? 'text-white/70 hover:text-white' : 'text-txtsecondary hover:text-red-500'}"
                      onclick={() => deleteVoice(v)}
                      title="Delete voice"
                    >
                      <Trash2 class="w-3.5 h-3.5" />
                    </button>
                  {/if}
                </div>
              {/each}
            </div>

            <!-- Voice cloning (base models). tts-server accepts a clone on any model,
                 but only base models need it — custom_voice ships its own speakers. -->
            {#if isBaseModel}
              <button
                class="shrink-0 inline-flex items-center gap-1.5 text-[0.8125rem] text-primary hover:underline"
                onclick={openClone}
              >
                <Plus class="w-4 h-4" /> Clone a voice
              </button>
            {/if}
          {/if}
        </div>

        <!-- Text input — chat composer chrome: model picker centered. Enter sends. -->
        <div class="composer-shell shrink-0">
          <textarea
            bind:this={promptEl}
            class="composer-textarea pretty-scroll min-h-[3.5rem] max-h-[50vh]"
            rows="2"
            placeholder={turns.length ? "Add another line to speak…" : "Enter text to convert to speech…"}
            disabled={isGenerating}
            bind:value={prompt}
            onkeydown={handleKeyDown}
          ></textarea>

          <div class="flex items-center justify-between gap-2">
            <div class="flex-1 min-w-0 flex justify-center">
              <ModelSelector
                bind:value={$selectedModelStore}
                placeholder="Select a speech model…"
                disabled={isGenerating}
                category="tts"
                ghost
                dropUp
              />
            </div>

            {#if isGenerating}
              <button class="composer-icon-btn shrink-0" onclick={cancelGeneration} title="Stop">
                <Square class="w-[1.125rem] h-[1.125rem]" fill="currentColor" />
              </button>
            {/if}
          </div>
        </div>
      </div>

      <!-- RIGHT (40%): clip library + always-visible volume ---------------------- -->
      <div class="flex-1 min-w-0 flex flex-col min-h-0">
        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b" use:scrollFade>
          {#if turns.length === 0 && !isGenerating}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
              <Volume2 class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Generated clips appear here. Enter text on the left to start.</p>
            </div>
          {:else}
            <div class="flex flex-col gap-3 pb-6 px-1">
              {#each pagedTurns as item (item.ti)}
                {@const t = item.t}
                {@const ti = item.ti}
                <div class="relative rounded-xl border border-card-border bg-surface p-2 flex flex-col gap-1.5 {menuIdx === ti ? 'z-30' : ''}">
                  {#if editingIdx === ti}
                    <!-- Edit mode: textarea replaces the card body. -->
                    <div class="flex flex-col gap-2">
                      <textarea
                        class="w-full px-2.5 py-1.5 rounded-lg border border-card-border bg-surface text-[0.8125rem] resize-none focus:outline-none focus:ring-2 focus:ring-primary/40"
                        rows="2"
                        bind:value={editText}
                        onkeydown={editKeyDown}
                      ></textarea>
                      <div class="flex justify-end gap-1.5">
                        <button class="p-1.5 rounded hover:bg-secondary text-txtsecondary" onclick={cancelEdit} title="Cancel"><X class="w-4 h-4" /></button>
                        <button class="p-1.5 rounded hover:bg-secondary text-txtsecondary" onclick={saveEdit} title="Save & regenerate"><Save class="w-4 h-4" /></button>
                      </div>
                    </div>
                  {:else}
                    <!-- Three-dot menu pinned to the card's top-right corner. -->
                    <div class="absolute top-1.5 right-1.5 z-20">
                      <button
                        class="p-1 rounded hover:bg-secondary text-txtsecondary disabled:opacity-40"
                        onclick={() => (menuIdx = menuIdx === ti ? null : ti)}
                        title="More"
                      >
                        <MoreVertical class="w-4 h-4" />
                      </button>
                      {#if menuIdx === ti}
                        <div class="absolute right-0 top-full mt-1 min-w-[8rem] flex flex-col rounded-lg border border-card-border bg-surface shadow-lg py-1 text-[0.8125rem]">
                          <button
                            class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-40"
                            onclick={() => { menuIdx = null; startEdit(ti); }}
                            disabled={isGenerating}
                          >
                            <Pencil class="w-3.5 h-3.5" /> Edit
                          </button>
                          <button
                            class="flex items-center gap-2 px-3 py-1.5 text-left text-txtmain hover:bg-secondary disabled:opacity-40"
                            onclick={() => { menuIdx = null; regenerate(ti); }}
                            disabled={isGenerating}
                          >
                            <RefreshCw class="w-3.5 h-3.5" /> Regenerate
                          </button>
                          <button
                            class="flex items-center gap-2 px-3 py-1.5 text-left text-red-500 hover:bg-secondary disabled:opacity-40"
                            onclick={() => { menuIdx = null; deleteTurn(ti); }}
                            disabled={isGenerating}
                          >
                            <Trash2 class="w-3.5 h-3.5" /> Delete
                          </button>
                        </div>
                      {/if}
                    </div>

                    <!-- Transcript (2 lines); pr-6 clears the corner menu. -->
                    <p class="font-serif text-xs leading-snug tracking-tight text-txtmain/90 whitespace-pre-wrap line-clamp-2 pr-6">{t.text}</p>

                    {#if t.error}
                      <div class="text-red-500 text-[0.8125rem]">{t.error}</div>
                    {:else if t.audio}
                      <div class="flex items-center gap-1.5">
                        <div class="flex-1 min-w-0">
                          <AudioPlayer src={t.audio} volume={$volumeStore} label={t.voice || "Default"} bind:this={audioEls[ti]} />
                        </div>
                        <!-- Download, bottom-right. -->
                        <button
                          class="shrink-0 self-end p-1 rounded hover:bg-secondary text-txtsecondary"
                          onclick={() => downloadAudio(t)}
                          title="Download"
                        >
                          <Download class="w-4 h-4" />
                        </button>
                      </div>
                    {:else if genId !== $activeSpeechChatId || ti !== turns.length - 1}
                      <div class="text-red-500 text-[0.8125rem]">No audio returned.</div>
                    {:else}
                      <!-- In-flight: spinner + label + elapsed. -->
                      <div class="flex items-center justify-between gap-2">
                        <div class="flex items-center gap-2 text-txtsecondary">
                          <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                          <span class="reason-shimmer-white font-medium text-[0.8125rem]">Generating speech…</span>
                        </div>
                        <span class="text-[0.6875rem] text-txtsecondary tabular-nums">{fmtDur(elapsed)}</span>
                      </div>
                    {/if}
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>

        {#if pageCount > 1}
          <div class="shrink-0 flex items-center justify-center gap-3 pt-3 text-[0.8125rem]">
            <button
              class="px-2.5 py-1 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
              onclick={() => (page = Math.max(1, page - 1))}
              disabled={page <= 1}
            >
              Prev
            </button>
            <span class="text-txtsecondary tabular-nums">{page} / {pageCount}</span>
            <button
              class="px-2.5 py-1 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
              onclick={() => (page = Math.min(pageCount, page + 1))}
              disabled={page >= pageCount}
            >
              Next
            </button>
          </div>
        {/if}

        <!-- Playback volume + auto-play — always visible, pinned at the bottom. -->
        <div class="shrink-0 flex items-center gap-2 pt-3 mt-2 border-t border-card-border">
          <VolumeX class="w-4 h-4 text-txtsecondary shrink-0" />
          <input type="range" min="0" max="1" step="0.01" bind:value={$volumeStore} class="flex-1 accent-primary" />
          <Volume2 class="w-4 h-4 text-txtsecondary shrink-0" />
          <span class="text-[0.6875rem] text-txtsecondary tabular-nums w-9 text-right">{Math.round($volumeStore * 100)}%</span>
          <label class="shrink-0 flex items-center gap-1.5 pl-2 ml-1 border-l border-card-border text-[0.6875rem] uppercase tracking-wide text-txtsecondary cursor-pointer" title="Auto-play new clips">
            <input type="checkbox" class="accent-primary w-3.5 h-3.5" bind:checked={$autoPlayStore} />
            Auto-play
          </label>
        </div>
      </div>
    </div>

    <!-- Multi-step clone modal: choose name+method → live | clip. -->
    {#if showClone}
      <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
        <div class="w-full max-w-lg flex flex-col gap-4 rounded-2xl border border-card-border bg-surface p-5 shadow-xl">
          <div class="flex items-center gap-2">
            {#if cloneStep !== "choose"}
              <button class="p-1 -ml-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary" onclick={() => (cloneStep = "choose")} title="Back">
                <ChevronLeft class="w-4 h-4" />
              </button>
            {/if}
            <h3 class="flex-1 text-sm font-medium text-txtmain">
              {cloneStep === "choose" ? "Clone a voice" : cloneStep === "live" ? "Record live" : "Clone from clip"}
            </h3>
            <button class="p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary" onclick={closeClone} title="Close">
              <X class="w-4 h-4" />
            </button>
          </div>

          {#if cloneStep === "choose"}
            <!-- Step 1: name + method. -->
            <input
              class="w-full px-3 py-2 rounded-md border border-card-border bg-surface text-[0.8125rem] focus:outline-none focus:ring-2 focus:ring-primary/40"
              placeholder="Voice name"
              bind:value={newVoiceName}
            />
            <div class="flex items-stretch gap-3">
              <button
                class="flex-1 flex flex-col items-center gap-2 px-3 py-4 rounded-lg border border-card-border text-txtmain hover:bg-secondary disabled:opacity-40 transition-colors"
                onclick={() => (cloneStep = "live")}
                disabled={!newVoiceName.trim()}
              >
                <Mic class="w-6 h-6" />
                <span class="text-[0.8125rem] font-medium">Clone live</span>
                <span class="text-[0.6875rem] text-txtsecondary text-center">Read a short passage aloud</span>
              </button>
              <div class="w-px bg-card-border"></div>
              <button
                class="flex-1 flex flex-col items-center gap-2 px-3 py-4 rounded-lg border border-card-border text-txtmain hover:bg-secondary disabled:opacity-40 transition-colors"
                onclick={() => (cloneStep = "clip")}
                disabled={!newVoiceName.trim()}
              >
                <Upload class="w-6 h-6" />
                <span class="text-[0.8125rem] font-medium">Clone from clip</span>
                <span class="text-[0.6875rem] text-txtsecondary text-center">Upload a WAV or MP3 sample</span>
              </button>
            </div>
          {:else if cloneStep === "live"}
            <!-- Step 2a: read the passage, record, finalize. -->
            <p class="text-xs text-txtsecondary">Read the passage aloud, then stop and finalize.</p>
            <div class="rounded-lg border border-card-border bg-black/5 p-3 text-[0.8125rem] leading-relaxed font-serif text-txtmain max-h-40 overflow-y-auto pretty-scroll">
              {RECORD_SCRIPT}
            </div>
            {#if recError}<span class="text-red-500 text-xs">{recError}</span>{/if}
            {#if createVoiceError}<span class="text-red-500 text-xs">{createVoiceError}</span>{/if}
            <div class="flex items-center gap-3">
              {#if recording}
                <button
                  class="shrink-0 inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-red-600 text-white text-[0.8125rem] font-medium hover:bg-red-700 transition-colors"
                  onclick={stopRecording}
                >
                  <Square class="w-4 h-4" fill="currentColor" /> Stop
                  <span class="w-2 h-2 rounded-full bg-white animate-pulse"></span>
                </button>
              {:else}
                <button
                  class="shrink-0 inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-[#141414] text-white text-[0.8125rem] font-medium hover:opacity-90 transition-opacity"
                  onclick={startRecording}
                >
                  <Mic class="w-4 h-4" /> {recordedBlob ? "Re-record" : "Record"}
                </button>
              {/if}
              {#if recordedUrl}
                <div class="flex-1 min-w-0"><AudioPlayer src={recordedUrl} volume={$volumeStore} /></div>
              {/if}
            </div>
            <div class="flex justify-end gap-2">
              <button class="px-2.5 py-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary text-[0.8125rem] transition-colors" onclick={closeClone}>Cancel</button>
              <button
                class="px-2.5 py-1 rounded-md bg-primary text-btn-primary-text text-[0.8125rem] font-medium hover:bg-primary-hover disabled:opacity-40 transition-colors"
                onclick={finalizeRecording}
                disabled={creatingVoice || recording || !recordedBlob}
              >
                {creatingVoice ? "Cloning…" : "Finalize"}
              </button>
            </div>
          {:else}
            <!-- Step 2b: upload a clip. -->
            <label class="flex items-center gap-2 px-3 py-2 rounded-md border border-card-border bg-surface text-[0.8125rem] text-txtsecondary hover:bg-secondary cursor-pointer truncate">
              <Upload class="w-4 h-4 shrink-0" />
              <span class="truncate">{newVoiceFile ? newVoiceFile.name : "Choose a WAV or MP3 sample…"}</span>
              <input type="file" accept="audio/wav,audio/mpeg,audio/mp3,.wav,.mp3" class="hidden" onchange={onVoiceFile} />
            </label>
            <textarea
              class="w-full px-3 py-2 rounded-md border border-card-border bg-surface text-[0.8125rem] resize-none focus:outline-none focus:ring-2 focus:ring-primary/40"
              rows="2"
              placeholder="Reference transcript (optional — improves cloning)"
              bind:value={newVoiceRefText}
            ></textarea>
            {#if createVoiceError}<span class="text-red-500 text-xs">{createVoiceError}</span>{/if}
            <div class="flex justify-end gap-2">
              <button class="px-2.5 py-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary text-[0.8125rem] transition-colors" onclick={closeClone}>Cancel</button>
              <button
                class="px-2.5 py-1 rounded-md bg-primary text-btn-primary-text text-[0.8125rem] font-medium hover:bg-primary-hover disabled:opacity-40 transition-colors"
                onclick={createVoice}
                disabled={creatingVoice || !newVoiceFile}
              >
                {creatingVoice ? "Cloning…" : "Clone voice"}
              </button>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Design-a-voice preset modal (voice_design models). -->
    {#if showPreset}
      <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
        <div class="w-full max-w-lg flex flex-col gap-4 rounded-2xl border border-card-border bg-surface p-5 shadow-xl">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-medium text-txtmain">Design a voice</h3>
            <button class="p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary" onclick={() => (showPreset = false)} title="Close">
              <X class="w-4 h-4" />
            </button>
          </div>
          <input
            class="w-full px-3 py-2 rounded-md border border-card-border bg-surface text-[0.8125rem] focus:outline-none focus:ring-2 focus:ring-primary/40"
            placeholder="Preset name"
            bind:value={presetName}
          />
          <textarea
            class="w-full px-3 py-2 rounded-md border border-card-border bg-surface text-[0.8125rem] resize-none focus:outline-none focus:ring-2 focus:ring-primary/40"
            rows="4"
            placeholder="Describe the voice — e.g. “a calm elderly man with a slight rasp, speaking slowly and warmly”"
            bind:value={presetInstr}
          ></textarea>
          <div class="flex justify-end gap-2">
            <button class="px-2.5 py-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary text-[0.8125rem] transition-colors" onclick={() => (showPreset = false)}>Cancel</button>
            <button
              class="px-2.5 py-1 rounded-md bg-primary text-btn-primary-text text-[0.8125rem] font-medium hover:bg-primary-hover disabled:opacity-40 transition-colors"
              onclick={savePreset}
              disabled={!presetName.trim() || !presetInstr.trim()}
            >
              Save preset
            </button>
          </div>
        </div>
      </div>
    {/if}
  {/if}
</div>
