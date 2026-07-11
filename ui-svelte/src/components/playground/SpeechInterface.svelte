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
  import { Volume2, VolumeX, Download, RefreshCw, Plus, Pencil, X, Save, Upload, Square, Check } from "lucide-svelte";
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

  // "" = model's default speaker (tts-server picks it; never 400s on an unknown
  // name). Real speaker names come from refreshVoices() → GET /v1/audio/voices.
  const defaultVoices = [""];
  const CACHE_KEY = "playground-speech-voices-cache-v4"; // v4: base models keep "" + cloned voices; kind-aware

  let availableVoices = $state<string[]>(defaultVoices);
  let isLoadingVoices = $state(false);
  // A base model has no named speakers → its "" default is valid and it accepts
  // voice clones. A custom_voice model has speakers and REQUIRES a named voice.
  let isBaseModel = $derived(availableVoices.includes(""));

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
    const cache = getVoicesCache();
    if (model && cache[model]) applyVoices(cache[model]);
    else {
      applyVoices(defaultVoices);
      if (model) refreshVoices();
    }
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

  // --- voice cloning (base models) -----------------------------------------
  // POST /v1/audio/voices {model,name,wav_b64,ref_text?} → tts-server registers
  // a cloned voice (base64 WAV, ref_text enables ICL clone mode). Path is
  // rewritten to /v1/voices by the reverse proxy; auth via inferenceHeaders().
  let showCreateVoice = $state(false);
  let newVoiceName = $state("");
  let newVoiceRefText = $state("");
  let newVoiceFile = $state<File | null>(null);
  let creatingVoice = $state(false);
  let createVoiceError = $state("");

  function onVoiceFile(e: Event) {
    newVoiceFile = (e.target as HTMLInputElement).files?.[0] ?? null;
  }

  function fileToBase64(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => {
        const s = r.result as string; // data:...;base64,XXXX
        resolve(s.slice(s.indexOf(",") + 1));
      };
      r.onerror = () => reject(r.error);
      r.readAsDataURL(file);
    });
  }

  async function createVoice() {
    const model = $selectedModelStore;
    const name = newVoiceName.trim();
    if (!model || !name || !newVoiceFile || creatingVoice) return;
    creatingVoice = true;
    createVoiceError = "";
    try {
      const wav_b64 = await fileToBase64(newVoiceFile);
      const body: Record<string, unknown> = { model, name, wav_b64 };
      const rt = newVoiceRefText.trim();
      if (rt) body.ref_text = rt;
      const resp = await fetch("/v1/audio/voices", {
        method: "POST",
        headers: { ...inferenceHeaders(), "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!resp.ok) throw new Error((await resp.text()) || `HTTP ${resp.status}`);
      await refreshVoices();
      selectedVoiceStore.set(name);
      showCreateVoice = false;
      newVoiceName = "";
      newVoiceRefText = "";
      newVoiceFile = null;
    } catch (e) {
      createVoiceError = e instanceof Error ? e.message : "Voice creation failed";
    } finally {
      creatingVoice = false;
    }
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
  let abortController = $state<AbortController | null>(null);
  let elapsed = $state(0);
  let editingIdx = $state<number | null>(null);
  let editText = $state("");
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
  async function runTurn(id: string, ti: number, text: string, voice: string, onAbort: () => void) {
    genId = id;
    abortController = new AbortController();
    try {
      const blob = await generateSpeech($selectedModelStore, text, voice, abortController.signal);
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
    const id = $activeSpeechChatId;
    if (!sessionById(id)) return;
    const voice = $selectedVoiceStore;
    prompt = "";
    const ti = sessionById(id)!.turns.length;
    appendTurn(id, { text, voice, audio: undefined });
    await runTurn(id, ti, text, voice, () => {
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
    const voice = $selectedVoiceStore;
    setTurns(id, [...s.turns.slice(0, idx), { text: t.text, voice, audio: undefined }], true);
    await runTurn(id, idx, t.text, voice, () => {});
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
    const voice = $selectedVoiceStore;
    setTurns(id, [...s.turns.slice(0, idx), { text, voice, audio: undefined }], true);
    await runTurn(id, idx, text, voice, () => {});
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
        <!-- Voice panel — rounded like the composer, takes the free space. -->
        <div class="flex-1 min-h-0 flex flex-col gap-3 rounded-3xl border border-card-border bg-surface p-4">
          <div class="flex items-center justify-between shrink-0">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Voice</span>
            <button
              class="p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
              onclick={refreshVoices}
              disabled={isLoadingVoices || !$selectedModelStore}
              title="Load the voices this model actually offers"
            >
              <RefreshCw class="w-3.5 h-3.5 {isLoadingVoices ? 'animate-spin' : ''}" />
            </button>
          </div>

          <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll flex flex-col gap-0.5 -mx-1 px-1">
            {#each availableVoices as v (v)}
              <button
                class="flex items-center justify-between gap-2 px-3 py-2 rounded-lg text-[0.8125rem] text-left transition-colors {$selectedVoiceStore === v ? 'bg-primary text-btn-primary-text' : 'text-txtmain hover:bg-secondary'}"
                onclick={() => selectedVoiceStore.set(v)}
              >
                <span class="truncate">{v || "Default"}</span>
                {#if $selectedVoiceStore === v}<Check class="w-4 h-4 shrink-0" />{/if}
              </button>
            {/each}
          </div>

          <!-- Voice cloning (base models). tts-server accepts a clone on any model,
               but only base models need it — custom_voice ships its own speakers. -->
          {#if isBaseModel}
            <div class="shrink-0">
              {#if showCreateVoice}
                <div class="flex flex-col gap-2 rounded-lg border border-card-border p-2.5">
                  <input
                    class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface text-[0.8125rem] focus:outline-none focus:ring-2 focus:ring-primary/40"
                    placeholder="Voice name"
                    bind:value={newVoiceName}
                  />
                  <label class="flex items-center gap-2 px-2.5 py-1.5 rounded-md border border-card-border bg-surface text-[0.8125rem] text-txtsecondary hover:bg-secondary cursor-pointer truncate">
                    <Upload class="w-4 h-4 shrink-0" />
                    <span class="truncate">{newVoiceFile ? newVoiceFile.name : "Choose reference WAV…"}</span>
                    <input type="file" accept="audio/wav,.wav" class="hidden" onchange={onVoiceFile} />
                  </label>
                  <textarea
                    class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface text-[0.8125rem] resize-none focus:outline-none focus:ring-2 focus:ring-primary/40"
                    rows="2"
                    placeholder="Reference transcript (optional — improves cloning)"
                    bind:value={newVoiceRefText}
                  ></textarea>
                  {#if createVoiceError}
                    <span class="text-red-500 text-xs">{createVoiceError}</span>
                  {/if}
                  <div class="flex justify-end gap-2">
                    <button
                      class="px-2.5 py-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary text-[0.8125rem] transition-colors"
                      onclick={() => (showCreateVoice = false)}
                    >
                      Cancel
                    </button>
                    <button
                      class="px-2.5 py-1 rounded-md bg-primary text-btn-primary-text text-[0.8125rem] font-medium hover:bg-primary-hover disabled:opacity-40 transition-colors"
                      onclick={createVoice}
                      disabled={creatingVoice || !newVoiceName.trim() || !newVoiceFile}
                    >
                      {creatingVoice ? "Creating…" : "Create"}
                    </button>
                  </div>
                </div>
              {:else}
                <button
                  class="inline-flex items-center gap-1.5 text-[0.8125rem] text-primary hover:underline"
                  onclick={() => (showCreateVoice = true)}
                >
                  <Plus class="w-4 h-4" /> Clone a voice
                </button>
              {/if}
            </div>
          {/if}

          <!-- Auto-play -->
          <label class="shrink-0 flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary pt-1 border-t border-card-border">
            <span>Auto-play</span>
            <input type="checkbox" class="accent-primary w-4 h-4" bind:checked={$autoPlayStore} />
          </label>
        </div>

        <!-- Text input — chat composer chrome: model picker centered, Generate right. -->
        <div class="composer-shell shrink-0">
          <textarea
            class="composer-textarea pretty-scroll min-h-[3.5rem] max-h-[16rem]"
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
            {:else}
              <button
                class="shrink-0 inline-flex items-center gap-1 px-2.5 py-1 rounded-full bg-primary text-btn-primary-text text-xs font-medium hover:bg-primary-hover active:bg-primary-active disabled:opacity-40 transition-colors"
                onclick={send}
                disabled={!prompt.trim() || !$selectedModelStore}
                title="Generate speech"
              >
                <Volume2 class="w-3.5 h-3.5" /> Generate
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
                <div class="rounded-xl border border-card-border bg-surface p-3 flex flex-col gap-2.5">
                  <!-- Spoken text (editable) -->
                  {#if editingIdx === ti}
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
                    <div class="flex items-start justify-between gap-2">
                      <span class="text-[0.8125rem] leading-relaxed whitespace-pre-wrap min-w-0">{t.text}</span>
                      <button
                        class="shrink-0 p-1 rounded text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
                        onclick={() => startEdit(ti)}
                        disabled={isGenerating}
                        title="Edit text"
                      >
                        <Pencil class="w-3.5 h-3.5" />
                      </button>
                    </div>
                  {/if}

                  <!-- Audio / state -->
                  {#if t.error}
                    <div class="text-red-500 text-[0.8125rem]">{t.error}</div>
                  {:else if t.audio}
                    <AudioPlayer src={t.audio} volume={$volumeStore} bind:this={audioEls[ti]} />
                    <div class="flex flex-wrap items-center gap-1 pt-1 border-t border-card-border">
                      <button
                        class="p-1 rounded hover:bg-secondary text-txtsecondary disabled:opacity-40"
                        onclick={() => regenerate(ti)}
                        disabled={isGenerating}
                        title="Regenerate"
                      >
                        <RefreshCw class="w-4 h-4" />
                      </button>
                      <button
                        class="p-1 rounded hover:bg-secondary text-txtsecondary"
                        onclick={() => downloadAudio(t)}
                        title="Download"
                      >
                        <Download class="w-4 h-4" />
                      </button>
                      <span class="ml-2 flex items-center gap-1 text-[0.6875rem] text-txtsecondary">
                        <Volume2 class="w-3 h-3" />{t.voice || "Default"}
                      </span>
                      {#if t.secs != null}
                        <span class="ml-auto flex items-center self-center text-[0.6875rem] text-txtsecondary tabular-nums">{fmtDur(t.secs)}</span>
                      {/if}
                    </div>
                  {:else if genId !== $activeSpeechChatId || ti !== turns.length - 1}
                    <div class="text-red-500 text-[0.8125rem]">No audio returned.</div>
                  {:else}
                    <!-- In-flight: spinner + label + elapsed. -->
                    <div class="flex items-center justify-between gap-2 pt-1 border-t border-card-border">
                      <div class="flex items-center gap-2 text-txtsecondary">
                        <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                        <span class="reason-shimmer-white font-medium text-[0.8125rem]">Generating speech…</span>
                      </div>
                      <span class="text-[0.6875rem] text-txtsecondary tabular-nums">{fmtDur(elapsed)}</span>
                    </div>
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

        <!-- Playback volume — always visible, pinned at the bottom. -->
        <div class="shrink-0 flex items-center gap-2 pt-3 mt-2 border-t border-card-border">
          <VolumeX class="w-4 h-4 text-txtsecondary shrink-0" />
          <input type="range" min="0" max="1" step="0.01" bind:value={$volumeStore} class="flex-1 accent-primary" />
          <Volume2 class="w-4 h-4 text-txtsecondary shrink-0" />
          <span class="text-[0.6875rem] text-txtsecondary tabular-nums w-9 text-right">{Math.round($volumeStore * 100)}%</span>
        </div>
      </div>
    </div>
  {/if}
</div>
