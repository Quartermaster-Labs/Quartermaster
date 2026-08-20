import type { SpeechGenerationRequest } from "./types";
import { inferenceHeaders } from "./inferenceAuth";
import { safeVoice } from "./voices";

// tts-server (both engines) generates on ONE worker thread — concurrent POSTs
// just queue up inside it. Overlapping them buys nothing and does harm: TTS.cpp
// keys its completed-task map by `rand()` and never erases an entry after it is
// read, so the more requests a session makes, the likelier a new task collides
// with a finished one and the client is handed the WRONG task's result (stale
// audio, or a 500 "Model returned an empty response." when the stale entry is a
// /v1/audio/voices task). Serialising every caller through one chain keeps the
// map small and the collision odds low. See STALE_RESPONSE below.
let ttsChain: Promise<unknown> = Promise.resolve();
function serialized<T>(fn: () => Promise<T>): Promise<T> {
  const run = ttsChain.then(fn, fn);
  // The chain must not break on a failed/aborted request.
  ttsChain = run.then(
    () => undefined,
    () => undefined,
  );
  return run;
}

// The upstream symptom of the id collision described above. It is transient by
// nature — a retry draws a fresh id — so one retry turns a hard failure into a
// hiccup. Fix it properly by patching tts.cpp's simple_server_task/`get()`.
const STALE_RESPONSE = "Model returned an empty response";

export function generateSpeech(
  model: string,
  input: string,
  voice: string,
  signal?: AbortSignal,
  instructions?: string
): Promise<Blob> {
  return serialized(async () => {
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
    try {
      return await speechRequest(model, input, voice, signal, instructions);
    } catch (e) {
      if (signal?.aborted || !(e as Error)?.message?.includes(STALE_RESPONSE)) throw e;
      return await speechRequest(model, input, voice, signal, instructions);
    }
  });
}

async function speechRequest(
  model: string,
  input: string,
  voice: string,
  signal?: AbortSignal,
  instructions?: string
): Promise<Blob> {
  const request: SpeechGenerationRequest = {
    model,
    input,
    // One choke point for every caller (Speech tab, read-aloud, voice preview):
    // the voice pref is shared across models, and a name one engine knows can
    // ABORT the other's server process. See safeVoice.
    voice: safeVoice(model, voice),
    response_format: "wav",
    // voice_design models design a voice from this style description; the
    // tts-server maps OpenAI `instructions` → the ABI `instruct` field.
    ...(instructions ? { instructions } : {}),
  };

  const response = await fetch("/v1/audio/speech", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(request),
    signal,
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`Speech API error: ${response.status} - ${errorText}`);
  }

  return response.blob();
}

// --- streamed read-aloud ----------------------------------------------------
// A TTS request only returns once the WHOLE clip is synthesised, so speaking a
// long reply in one shot means waiting for the entire answer before the first
// word is audible (linear in characters — a minute of audio costs a minute-ish
// of Kokoro CPU). Instead we cut the text at sentence boundaries and pipeline:
// synthesise chunk N+1 while chunk N plays, so the wait is only the first chunk.

// First chunk is deliberately tiny (one or two sentences) to get sound out fast;
// later chunks are bigger so per-request overhead and prosody breaks stay rare.
// Chunk 1 sits in between: with a single request in flight ahead of playback, a
// tiny chunk 0 followed straight by a full-size one has to synthesise ~3x its own
// playtime before the audio runs dry, which stutters on a slow CPU. Ramping keeps
// each chunk's synthesis roughly within the previous chunk's playback window.
//
// The ceiling is NOT a latency choice: Kokoro's context is 512 tokens and TTS.cpp
// decides whether a prompt fits by comparing the PHONEMIZED string's byte length
// against it — IPA symbols are 2-3 UTF-8 bytes each, so ~250 characters of English
// can already look "too long" and get routed into its internal chunker, which
// reads chunks.back() off an empty vector and dies with "vector too long". Staying
// under that keeps every request on the single-shot path.
const FIRST_CHUNK = 140;
const RAMP_CHUNK = 190;
const NEXT_CHUNK = 240;

/** Split prose into sentence-aligned chunks, small first then larger. */
export function splitForSpeech(
  text: string,
  first = FIRST_CHUNK,
  rest = NEXT_CHUNK,
  ramp = Math.min(RAMP_CHUNK, rest),
): string[] {
  // Sentence-ish pieces: end punctuation followed by whitespace, or a hard break.
  const pieces = text
    .split(/(?<=[.!?…:;])\s+|\n+/)
    .map((s) => s.trim())
    .filter(Boolean);
  const out: string[] = [];
  let buf = "";
  const cap = () => (out.length === 0 ? first : out.length === 1 ? ramp : rest);
  for (const p of pieces) {
    // A single piece longer than the cap (unpunctuated wall of text) is cut on
    // whitespace so one runaway sentence can't stall the whole playback.
    if (!buf && p.length > cap()) {
      let s = p;
      while (s.length > cap()) {
        const limit = cap();
        const brk = s.lastIndexOf(" ", limit);
        const at = brk > limit / 2 ? brk : limit;
        out.push(s.slice(0, at).trim());
        s = s.slice(at).trim();
      }
      buf = s;
      continue;
    }
    if (buf && buf.length + 1 + p.length > cap()) {
      out.push(buf);
      buf = p;
    } else {
      buf = buf ? `${buf} ${p}` : p;
    }
  }
  if (buf) out.push(buf);
  return out;
}

export interface SpeakOptions {
  signal: AbortSignal;
  instructions?: string;
  /**
   * Already-synthesised chunks, indexed as splitForSpeech() produced them. A
   * hit is replayed instead of regenerated, so speaking the same text in the
   * same voice a second time costs nothing. Sparse and safe to grow: a caller
   * that stops halfway keeps what it got.
   */
  cached?: Blob[];
  /** Hands back each freshly synthesised chunk so the caller can cache it. */
  onChunk?: (index: number, blob: Blob) => void;
  /** The element currently playing (null when nothing is), so callers can stop it. */
  onAudio?: (el: HTMLAudioElement | null) => void;
  /** Fired once, before the first request, with the chunks the text was cut
   * into — how many `onChunk` calls a full run will make, and the exact prose
   * each one speaks (which is what locates it in the rendered reply). */
  onChunks?: (chunks: string[]) => void;
  /** Fired once, when the first chunk actually starts playing. */
  onPlaying?: () => void;
  /** Fired as each chunk starts playing, so callers can follow the reader. */
  onChunkPlaying?: (index: number) => void;
  /**
   * Volume (0-1) and playback rate, read fresh for EVERY chunk rather than
   * captured once: a run is many <audio> elements over minutes, and a slider
   * moved mid-clip has to survive the handover to the next one. Live changes
   * inside a chunk are the caller's job (it holds the element via onAudio).
   */
  settings?: () => { volume: number; rate: number };
}

function playBlob(blob: Blob, opts: SpeakOptions): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const url = URL.createObjectURL(blob);
    const el = new Audio(url);
    const st = opts.settings?.();
    if (st) {
      el.volume = Math.min(1, Math.max(0, st.volume));
      // Resample instead of transposing, so a faster read still sounds human.
      el.preservesPitch = true;
      el.playbackRate = st.rate;
    }
    const done = () => {
      opts.signal.removeEventListener("abort", onAbort);
      opts.onAudio?.(null);
      URL.revokeObjectURL(url);
    };
    const onAbort = () => {
      el.pause();
      done();
      resolve();
    };
    if (opts.signal.aborted) {
      URL.revokeObjectURL(url);
      resolve();
      return;
    }
    opts.signal.addEventListener("abort", onAbort);
    el.onended = () => {
      done();
      resolve();
    };
    el.onerror = () => {
      done();
      reject(new Error("Playback failed"));
    };
    opts.onAudio?.(el);
    el.play().then(() => opts.onPlaying?.(), (e) => {
      done();
      reject(e);
    });
  });
}

/**
 * Speak `text` chunk by chunk, keeping one synthesis request in flight ahead of
 * playback. Resolves when the last chunk finishes (or the signal aborts).
 */
export async function speakStreamed(
  model: string,
  text: string,
  voice: string,
  opts: SpeakOptions,
): Promise<void> {
  const chunks = splitForSpeech(text);
  if (!chunks.length) return;
  opts.onChunks?.(chunks);
  const fetchAt = (i: number) => {
    const hit = opts.cached?.[i];
    if (hit) return Promise.resolve(hit);
    const p = generateSpeech(model, chunks[i], voice, opts.signal, opts.instructions).then((b) => {
      opts.onChunk?.(i, b);
      return b;
    });
    // If playback stops early we never await this one; keep it from surfacing as
    // an unhandled rejection (attaching a handler here doesn't swallow the await).
    p.catch(() => {});
    return p;
  };
  let pending = fetchAt(0);
  let first = true;
  for (let i = 0; i < chunks.length; i++) {
    const blob = await pending;
    if (opts.signal.aborted) return;
    pending = i + 1 < chunks.length ? fetchAt(i + 1) : Promise.resolve(new Blob());
    const isFirst = first;
    first = false;
    await playBlob(blob, {
      ...opts,
      onPlaying: () => {
        opts.onChunkPlaying?.(i);
        if (isFirst) opts.onPlaying?.();
      },
    });
    if (opts.signal.aborted) return;
  }
}
