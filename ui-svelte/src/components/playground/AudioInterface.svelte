<script lang="ts">
  import { get } from "svelte/store";
  import { models } from "../../stores/api";
  import { modelCategory } from "../../lib/modelUtils";
  import { userPref } from "../../stores/prefs";
  import { transcribeAudio } from "../../lib/audioApi";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import ModelSelector from "./ModelSelector.svelte";
  import { scrollFade } from "../../lib/scrollFade";
  import { ASR_SAMPLE_RATE, concat, encodeWav, resample, rms } from "../../lib/wav";
  import { Mic, Square, Upload, FileAudio, Copy, Check, Trash2, Download, X, Radio } from "lucide-svelte";

  // Transcription studio, same shape as the speech tab: left column (60%) is the
  // source panel — live mic or a dropped file — with the model picker in a
  // composer-style shell pinned below; right column (40%) is the transcript
  // library, newest first. Transcripts are session-local (no server history).

  const selectedModelStore = userPref<string>("playground-audio-model", "");
  const modeStore = userPref<"live" | "file">("playground-audio-mode", "live");

  type Entry = { id: number; label: string; text: string; source: "live" | "file"; error?: string };
  let entries = $state<Entry[]>([]);
  let nextId = 1;

  let error = $state<string | null>(null);
  let copiedId = $state<number | null>(null);

  // The picker hard-filters to transcribe-capable models and renders nothing when
  // none exist — so gate on that list here too, otherwise the tab shows an empty
  // composer and a dead (silently disabled) record button.
  let asrModels = $derived($models.filter((m) => !m.unlisted && modelCategory(m) === "transcribe"));
  let hasModels = $derived(asrModels.length > 0);
  let hasModel = $derived($selectedModelStore !== "");

  // No stored pick (or one that no longer exists) → take the first ASR model.
  $effect(() => {
    const list = asrModels;
    if (!list.length) return;
    if (!list.some((m) => m.id === get(selectedModelStore))) selectedModelStore.set(list[0].id);
  });

  // --- file transcription ---------------------------------------------------
  const ACCEPTED_FORMATS = [".mp3", ".wav", ".ogg", ".flac", ".m4a"];
  const MAX_FILE_SIZE = 25 * 1024 * 1024;

  let selectedFile = $state<File | null>(null);
  let isTranscribing = $state(false);
  let abortController: AbortController | null = null;
  let isDragging = $state(false);
  let fileInput = $state<HTMLInputElement | null>(null);

  function validateFile(file: File): string | null {
    const ext = "." + file.name.split(".").pop()?.toLowerCase();
    if (!ACCEPTED_FORMATS.includes(ext)) return `Invalid file type. Accepted: ${ACCEPTED_FORMATS.join(", ")}`;
    if (file.size > MAX_FILE_SIZE) return "File too large. Maximum: 25MB";
    return null;
  }

  function takeFile(file: File | undefined | null) {
    if (!file) return;
    const bad = validateFile(file);
    if (bad) {
      error = bad;
      selectedFile = null;
      return;
    }
    error = null;
    selectedFile = file;
  }

  function handleFileSelect(event: Event) {
    takeFile((event.target as HTMLInputElement).files?.[0]);
  }

  function handleDrop(event: DragEvent) {
    event.preventDefault();
    isDragging = false;
    modeStore.set("file");
    takeFile(event.dataTransfer?.files[0]);
  }

  async function transcribeFile() {
    const file = selectedFile;
    if (!file || !hasModel || isTranscribing) return;
    isTranscribing = true;
    error = null;
    abortController = new AbortController();
    try {
      const res = await transcribeAudio($selectedModelStore, file, abortController.signal);
      const text = (res.text ?? "").trim();
      entries = [{ id: nextId++, label: file.name, text, source: "file" }, ...entries];
      selectedFile = null;
      if (fileInput) fileInput.value = "";
    } catch (err) {
      if (!(err instanceof Error && err.name === "AbortError")) {
        error = err instanceof Error ? err.message : "An error occurred";
      }
    } finally {
      isTranscribing = false;
      abortController = null;
    }
  }

  function cancelFile() {
    abortController?.abort();
  }

  // --- live transcription ---------------------------------------------------
  // There is no streaming ASR endpoint, so the stream is segmented client-side:
  // raw PCM is captured, cut at a silence gap (or a hard max length), downsampled
  // to 16 kHz mono WAV, and POSTed. Segments are sent one at a time so a live
  // session can't stack N concurrent loads on the same model.
  const SILENCE_RMS = 0.012; // below this a frame counts as silence
  const SILENCE_HOLD = 0.6; // seconds of trailing silence that ends a segment
  const MIN_SEG = 1.2; // don't ship anything shorter than this
  const MAX_SEG = 12; // hard cut so a monologue still streams in

  let listening = $state(false);
  let level = $state(0);
  let liveText = $state("");
  let pending = $state(0); // segments queued or in flight
  let micError = $state("");

  let audioCtx: AudioContext | null = null;
  let micStream: MediaStream | null = null;
  let processor: ScriptProcessorNode | null = null;
  let srcNode: MediaStreamAudioSourceNode | null = null;

  let frames: Float32Array[] = [];
  let frameSamples = 0;
  let speechSamples = 0;
  let silenceSamples = 0;
  let captureRate = 48000;

  let queue: Float32Array[] = [];
  let draining = false;

  async function startListening() {
    if (listening || !hasModel) return;
    micError = "";
    liveText = "";
    if (!navigator.mediaDevices?.getUserMedia) {
      micError = "Microphone needs a secure context. Open this page via http://localhost or serve it over HTTPS.";
      return;
    }
    try {
      micStream = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      });
    } catch {
      micError = "Microphone access was denied.";
      return;
    }
    const AC = window.AudioContext || (window as any).webkitAudioContext;
    audioCtx = new AC();
    captureRate = audioCtx.sampleRate;
    srcNode = audioCtx.createMediaStreamSource(micStream);
    // ScriptProcessorNode is deprecated but universally available; an AudioWorklet
    // would need a separately-bundled module for one RMS + copy loop.
    processor = audioCtx.createScriptProcessor(4096, 1, 1);
    processor.onaudioprocess = (e) => onFrame(e.inputBuffer.getChannelData(0));
    srcNode.connect(processor);
    // Sink at zero gain: ScriptProcessor only fires while connected to the graph,
    // but routing the mic to the speakers would feed back.
    const mute = audioCtx.createGain();
    mute.gain.value = 0;
    processor.connect(mute);
    mute.connect(audioCtx.destination);

    frames = [];
    frameSamples = speechSamples = silenceSamples = 0;
    listening = true;
  }

  function onFrame(input: Float32Array) {
    const frame = new Float32Array(input); // the backing buffer is reused
    const loud = rms(frame);
    level = Math.min(1, loud * 12);
    frames.push(frame);
    frameSamples += frame.length;
    if (loud >= SILENCE_RMS) {
      speechSamples += frame.length;
      silenceSamples = 0;
    } else {
      silenceSamples += frame.length;
    }

    const secs = frameSamples / captureRate;
    const gapEnded = speechSamples > 0 && silenceSamples / captureRate >= SILENCE_HOLD && secs >= MIN_SEG;
    if (gapEnded || secs >= MAX_SEG) flushSegment();
  }

  function flushSegment() {
    const secs = frameSamples / captureRate;
    const voiced = speechSamples / captureRate;
    const buf = concat(frames);
    frames = [];
    frameSamples = speechSamples = silenceSamples = 0;
    // Skip near-silent segments outright — sending them just makes the model
    // hallucinate filler ("Thank you.", subtitle credits, …).
    if (secs < 0.4 || voiced < 0.3) return;
    queue.push(buf);
    pending = queue.length + (draining ? 1 : 0);
    void drain();
  }

  async function drain() {
    if (draining) return;
    draining = true;
    while (queue.length) {
      const buf = queue.shift()!;
      pending = queue.length + 1;
      try {
        const wav = encodeWav(resample(buf, captureRate, ASR_SAMPLE_RATE), ASR_SAMPLE_RATE);
        const res = await transcribeAudio($selectedModelStore, wav, undefined, "segment.wav");
        const text = (res.text ?? "").trim();
        if (text) liveText = liveText ? `${liveText} ${text}` : text;
      } catch (err) {
        micError = err instanceof Error ? err.message : "Transcription failed";
      }
    }
    draining = false;
    pending = 0;
  }

  async function stopListening() {
    if (!listening) return;
    listening = false;
    if (processor) processor.onaudioprocess = null;
    processor?.disconnect();
    srcNode?.disconnect();
    micStream?.getTracks().forEach((t) => t.stop());
    await audioCtx?.close().catch(() => {});
    processor = srcNode = null;
    micStream = null;
    audioCtx = null;
    level = 0;

    flushSegment(); // ship whatever is still buffered
    await drain();
    const text = liveText.trim();
    if (text) {
      entries = [{ id: nextId++, label: "Live recording", text, source: "live" }, ...entries];
    }
    liveText = "";
  }

  $effect(() => {
    playgroundStores.audioTranscribing.set(isTranscribing || listening || pending > 0);
  });

  // Releasing the mic on unmount — a stray ScriptProcessor keeps the tab's
  // recording indicator lit forever otherwise.
  $effect(() => () => {
    if (listening) void stopListening();
  });

  // --- transcript actions ---------------------------------------------------
  function copyEntry(e: Entry) {
    navigator.clipboard.writeText(e.text).then(() => {
      copiedId = e.id;
      setTimeout(() => (copiedId = null), 1500);
    });
  }

  function downloadEntry(e: Entry) {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([e.text], { type: "text/plain" }));
    a.download = `${e.label.replace(/\.[^.]+$/, "") || "transcript"}.txt`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(a.href);
  }

  function deleteEntry(id: number) {
    entries = entries.filter((e) => e.id !== id);
  }

  function formatFileSize(bytes: number): string {
    if (bytes < 1024) return bytes + " B";
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
    return (bytes / (1024 * 1024)).toFixed(1) + " MB";
  }
</script>

<div class="flex flex-col h-full">
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <Mic class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No transcription model configured. Add a speech-to-text model (e.g. Whisper) to transcribe audio.</p>
    </div>
  {:else}
    <div class="flex-1 flex flex-col md:flex-row gap-4 min-h-0 w-full py-4">
      <!-- LEFT (60%): source panel fills the height, composer pinned below -------- -->
      <div class="w-full md:w-3/5 shrink-0 flex flex-col gap-3 min-h-0">
        <div class="flex-1 min-h-0 flex flex-col gap-2 px-1">
          <div class="flex items-center justify-between shrink-0">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Source</span>
            <div class="seg">
              <button aria-pressed={$modeStore === "live"} onclick={() => modeStore.set("live")} disabled={isTranscribing}>Live mic</button>
              <button aria-pressed={$modeStore === "file"} onclick={() => modeStore.set("file")} disabled={listening}>File</button>
            </div>
          </div>

          {#if $modeStore === "live"}
            <!-- Live capture: big record control, level meter, running transcript. -->
            <div class="flex-1 min-h-0 flex flex-col gap-3">
              <div class="shrink-0 flex items-center gap-3">
                {#if listening}
                  <button
                    class="shrink-0 inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-red-600 text-white text-[0.8125rem] font-medium hover:bg-red-700 transition-colors"
                    onclick={stopListening}
                  >
                    <Square class="w-4 h-4" fill="currentColor" /> Stop
                    <span class="w-2 h-2 rounded-full bg-white animate-pulse"></span>
                  </button>
                {:else}
                  <button
                    class="shrink-0 inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-[#141414] text-white text-[0.8125rem] font-medium hover:opacity-90 disabled:opacity-40 transition-opacity"
                    onclick={startListening}
                    disabled={!hasModel}
                    title={hasModel ? "Start listening" : "Select a model first"}
                  >
                    <Mic class="w-4 h-4" /> Start listening
                  </button>
                  {#if !hasModel}
                    <span class="shrink-0 text-[0.6875rem] text-txtsecondary">Pick a transcription model below first.</span>
                  {/if}
                {/if}

                <!-- Input level; also the "the mic is actually live" tell. -->
                <div class="flex-1 h-1.5 rounded-full bg-secondary overflow-hidden">
                  <div class="h-full bg-primary transition-[width] duration-75" style="width: {Math.round(level * 100)}%"></div>
                </div>

                {#if pending > 0}
                  <span class="shrink-0 inline-flex items-center gap-1.5 text-[0.6875rem] text-txtsecondary tabular-nums">
                    <span class="inline-block w-3 h-3 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
                    {pending}
                  </span>
                {/if}
              </div>

              {#if micError}<span class="shrink-0 text-red-500 text-xs">{micError}</span>{/if}

              <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll rounded-xl border border-card-border bg-surface p-3">
                {#if liveText}
                  <p class="font-serif text-sm leading-relaxed text-txtmain whitespace-pre-wrap">{liveText}</p>
                {:else}
                  <div class="h-full flex flex-col items-center justify-center gap-2 text-txtsecondary text-center">
                    <Radio class="w-8 h-8 opacity-40" strokeWidth={1.5} />
                    <p class="text-[0.8125rem]">
                      {listening ? "Listening — speak, then pause; text appears each time you stop for a moment." : "Speak into the mic and the text shows up here live."}
                    </p>
                  </div>
                {/if}
              </div>
              <p class="shrink-0 text-[0.6875rem] text-txtsecondary px-1">
                Audio is cut at natural pauses (or every {MAX_SEG}s) and transcribed segment by segment. Stopping saves the whole run to the library.
              </p>
            </div>
          {:else}
            <!-- File capture: drop zone / selected file. -->
            <div
              role="region"
              aria-label="Audio file drop zone"
              class="flex-1 min-h-0 flex items-center justify-center rounded-xl border border-dashed transition-colors {isDragging
                ? 'border-primary bg-primary/10'
                : 'border-card-border bg-surface'}"
              ondragover={(e) => { e.preventDefault(); isDragging = true; }}
              ondragleave={() => (isDragging = false)}
              ondrop={handleDrop}
            >
              {#if isTranscribing}
                <div class="text-center text-txtsecondary">
                  <div class="inline-block w-8 h-8 border-4 border-primary border-t-transparent rounded-full animate-spin mb-2"></div>
                  <p class="reason-shimmer-white text-[0.8125rem]">Transcribing audio…</p>
                </div>
              {:else if selectedFile}
                <div class="flex flex-col items-center gap-2 text-txtsecondary p-4">
                  <FileAudio class="w-8 h-8 opacity-60" strokeWidth={1.5} />
                  <p class="text-[0.8125rem] font-medium text-txtmain">{selectedFile.name}</p>
                  <p class="text-[0.6875rem]">{formatFileSize(selectedFile.size)}</p>
                  <div class="flex items-center gap-2 mt-1">
                    <button
                      class="px-2.5 py-1 rounded-md bg-primary text-btn-primary-text text-[0.8125rem] font-medium hover:bg-primary-hover disabled:opacity-40 transition-colors"
                      onclick={transcribeFile}
                      disabled={!hasModel}
                    >
                      Transcribe
                    </button>
                    <button
                      class="px-2.5 py-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary text-[0.8125rem] transition-colors"
                      onclick={() => { selectedFile = null; if (fileInput) fileInput.value = ""; }}
                    >
                      Clear
                    </button>
                  </div>
                </div>
              {:else}
                <div class="flex flex-col items-center gap-2 text-txtsecondary p-8 text-center">
                  <Upload class="w-8 h-8 opacity-40" strokeWidth={1.5} />
                  <p class="text-[0.8125rem]">Drop an audio file here</p>
                  <button class="text-[0.8125rem] text-primary hover:underline" onclick={() => fileInput?.click()}>or browse files</button>
                  <p class="text-[0.6875rem] mt-2">{ACCEPTED_FORMATS.join(", ")} · max 25MB</p>
                </div>
              {/if}
            </div>
            {#if isTranscribing}
              <button class="shrink-0 self-end text-[0.8125rem] text-red-500 hover:underline" onclick={cancelFile}>Cancel</button>
            {/if}
            <input type="file" accept={ACCEPTED_FORMATS.join(",")} class="hidden" onchange={handleFileSelect} bind:this={fileInput} />
          {/if}

          {#if error}<span class="shrink-0 text-red-500 text-xs px-1">{error}</span>{/if}
        </div>

        <!-- Model picker in the composer shell, matching the speech tab. -->
        <div class="composer-shell shrink-0">
          <div class="flex items-center justify-center gap-2">
            <ModelSelector
              bind:value={$selectedModelStore}
              placeholder="Select a transcription model…"
              disabled={isTranscribing || listening}
              category="transcribe"
              ghost
              dropUp
            />
          </div>
        </div>
      </div>

      <!-- RIGHT (40%): transcript library ---------------------------------------- -->
      <div class="flex-1 min-w-0 flex flex-col min-h-0">
        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b" use:scrollFade>
          {#if entries.length === 0}
            <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary text-center px-4">
              <FileAudio class="w-10 h-10 opacity-40" strokeWidth={1.5} />
              <p>Transcripts appear here. Record or drop a file on the left to start.</p>
            </div>
          {:else}
            <div class="flex flex-col gap-3 pb-6 px-1">
              {#each entries as e (e.id)}
                <div class="rounded-xl border border-card-border bg-surface p-2 flex flex-col gap-1.5">
                  <div class="flex items-center gap-1.5">
                    {#if e.source === "live"}
                      <Mic class="w-3.5 h-3.5 shrink-0 text-txtsecondary" />
                    {:else}
                      <FileAudio class="w-3.5 h-3.5 shrink-0 text-txtsecondary" />
                    {/if}
                    <span class="flex-1 min-w-0 truncate text-[0.6875rem] uppercase tracking-wide text-txtsecondary">{e.label}</span>
                    <button class="shrink-0 p-1 rounded hover:bg-secondary text-txtsecondary" onclick={() => copyEntry(e)} title="Copy">
                      {#if copiedId === e.id}<Check class="w-4 h-4 text-green-500" />{:else}<Copy class="w-4 h-4" />{/if}
                    </button>
                    <button class="shrink-0 p-1 rounded hover:bg-secondary text-txtsecondary" onclick={() => downloadEntry(e)} title="Download .txt">
                      <Download class="w-4 h-4" />
                    </button>
                    <button class="shrink-0 p-1 rounded hover:bg-secondary text-txtsecondary hover:text-red-500" onclick={() => deleteEntry(e.id)} title="Delete">
                      <Trash2 class="w-4 h-4" />
                    </button>
                  </div>
                  <p class="font-serif text-[0.8125rem] leading-relaxed text-txtmain whitespace-pre-wrap">{e.text || "(no speech detected)"}</p>
                </div>
              {/each}
            </div>
          {/if}
        </div>

        {#if entries.length > 0}
          <div class="shrink-0 flex items-center gap-2 pt-3 mt-2 border-t border-card-border text-[0.6875rem] uppercase tracking-wide text-txtsecondary">
            <span class="tabular-nums">{entries.length} transcript{entries.length === 1 ? "" : "s"}</span>
            <div class="flex-1"></div>
            <button class="inline-flex items-center gap-1 hover:text-txtmain transition-colors" onclick={() => (entries = [])}>
              <X class="w-3.5 h-3.5" /> Clear all
            </button>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>
