import { inferenceHeaders } from "./inferenceAuth";

// Client for the music routes (internal/server/musicapi.go):
//   POST /v1/music/generations  text to music (ACE-Step, MiniMax Music 3)
//   POST /v1/music/edits        repaint / complete / cover (ACE-Step)
//   POST /v1/audio/separations  stems (HTDemucs)
//   POST /v1/music/transcriptions  recording to score (SheetSage2, ABC)
// All are synchronous: the reply is the finished result. Generation and
// edits answer raw WAV with the seed actually used in X-Seed; separation
// answers JSON with one base64 WAV per stem, transcription JSON {score, format}.
// POST /v1/tools/media/audio (internal/server/toolsapi.go) pulls the audio of a
// page through yt-dlp, for the References tab's link field.
//
// Source audio goes up as base64 JSON, not multipart. Both are accepted, but
// JSON is the shape the routes were verified against end to end.

export type MusicParams = {
  model: string;
  prompt: string;
  lyrics?: string;
  // Written score in the model's capabilities.music_score notation. Generation only.
  score?: string;
  duration?: number;
  seed?: number;
  steps?: number;
  guidance?: number;
  language?: string;
  options?: Record<string, string | number>;
};

export type EditTask = "repaint" | "complete" | "cover";

export type EditParams = MusicParams & {
  task: EditTask;
  audio: Blob;
  start?: number;
  end?: number;
  repaintMode?: string;
  repaintStrength?: number;
};

export type MusicResult = { blob: Blob; seed?: number };

function body(p: MusicParams): Record<string, unknown> {
  const b: Record<string, unknown> = { model: p.model, prompt: p.prompt };
  // Lyrics are sent even when empty: ACE-Step reads "" as instrumental, and
  // the server only checks them for MiniMax, which requires them.
  if (p.lyrics !== undefined) b.lyrics = p.lyrics;
  if (p.score) b.score = p.score;
  if (p.duration !== undefined) b.duration = p.duration;
  if (p.seed !== undefined) b.seed = p.seed;
  if (p.steps !== undefined) b.steps = p.steps;
  if (p.guidance !== undefined) b.guidance = p.guidance;
  if (p.language) b.language = p.language;
  if (p.options && Object.keys(p.options).length) b.options = p.options;
  return b;
}

// The server answers its own 400s as plain text, and passes audio.cpp's
// {"error":{"message"}} through. Either way the user wants the sentence.
export async function errorText(resp: Response): Promise<string> {
  const text = (await resp.text()).trim();
  try {
    const j = JSON.parse(text);
    const msg = j?.error?.message ?? j?.error ?? j?.message;
    if (typeof msg === "string" && msg) return msg;
  } catch {
    // not JSON
  }
  return text || `HTTP ${resp.status}`;
}

async function post(path: string, payload: unknown, signal?: AbortSignal): Promise<Response> {
  const resp = await fetch(path, {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(payload),
    signal,
  });
  if (!resp.ok) throw new Error(await errorText(resp));
  return resp;
}

async function audioResult(resp: Response): Promise<MusicResult> {
  const seed = Number(resp.headers.get("X-Seed"));
  return { blob: await resp.blob(), seed: Number.isFinite(seed) && resp.headers.has("X-Seed") ? seed : undefined };
}

export async function generateMusic(p: MusicParams, signal?: AbortSignal): Promise<MusicResult> {
  return audioResult(await post("/v1/music/generations", body(p), signal));
}

export async function editMusic(p: EditParams, signal?: AbortSignal): Promise<MusicResult> {
  const b = body(p);
  b.task = p.task;
  b.audio = await blobToBase64(p.audio);
  if (p.start !== undefined) b.start = p.start;
  if (p.end !== undefined) b.end = p.end;
  if (p.repaintMode) b.repaint_mode = p.repaintMode;
  if (p.repaintStrength !== undefined) b.repaint_strength = p.repaintStrength;
  return audioResult(await post("/v1/music/edits", b, signal));
}

export async function separateAudio(model: string, audio: Blob, signal?: AbortSignal): Promise<{ name: string; blob: Blob }[]> {
  const resp = await post("/v1/audio/separations", { model, audio: await blobToBase64(audio) }, signal);
  const j = (await resp.json()) as { stems?: { name: string; audio: string }[] };
  return (j.stems ?? []).map((s) => ({ name: s.name, blob: base64ToBlob(s.audio, "audio/wav") }));
}

export async function transcribeAudio(model: string, audio: Blob, signal?: AbortSignal): Promise<{ score: string; format: string }> {
  const resp = await post("/v1/music/transcriptions", { model, audio: await blobToBase64(audio) }, signal);
  const j = (await resp.json()) as { score?: string; format?: string };
  if (!j.score) throw new Error("The transcriber returned no score");
  return { score: j.score, format: j.format ?? "abc" };
}

export type LinkAudio = { blob: Blob; title: string; url: string; site: string; duration: number };

// The audio of a page (YouTube or any site yt-dlp handles), as the site serves
// it (m4a/webm): the server has no ffmpeg, the browser decodes both. Callers
// run it through toWav like an uploaded file.
export async function fetchLinkAudio(url: string, signal?: AbortSignal): Promise<LinkAudio> {
  const resp = await post("/v1/tools/media/audio", { url }, signal);
  const j = (await resp.json()) as { audio?: string; mime?: string; title?: string; url?: string; site?: string; duration?: number };
  if (!j.audio) throw new Error("No audio came back for that link");
  return {
    blob: base64ToBlob(j.audio, j.mime || "application/octet-stream"),
    title: j.title || j.url || url,
    url: j.url || url,
    site: j.site || "",
    duration: j.duration ?? 0,
  };
}

// Store one clip in the user's media dir and get back its /api/media ref
// (POST /api/media). Threads hold refs only; see stores/musicHistory.ts.
export async function uploadMedia(blob: Blob, type = "audio/wav"): Promise<string> {
  const resp = await fetch("/api/media", { method: "POST", headers: { "Content-Type": blob.type || type }, body: blob });
  if (!resp.ok) throw new Error(`Could not save the result: ${await errorText(resp)}`);
  const j = (await resp.json()) as { url?: string };
  if (!j.url) throw new Error("Could not save the result: no media ref returned");
  return j.url;
}

export async function fetchBlob(url: string): Promise<Blob> {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`Could not read ${url}: HTTP ${resp.status}`);
  return resp.blob();
}

// The routes read WAV only. A WAV passes through byte for byte (re-encoding it
// would make an edit differ from the CLI fed the same file); anything else the
// browser can decode is re-encoded to PCM16 at its own rate, channels kept.
export async function toWav(file: Blob): Promise<Blob> {
  const head = new Uint8Array(await file.slice(0, 12).arrayBuffer());
  const tag = (o: number) => String.fromCharCode(...head.slice(o, o + 4));
  if (head.length === 12 && tag(0) === "RIFF" && tag(8) === "WAVE") return file.type ? file : new Blob([file], { type: "audio/wav" });
  const AC = window.AudioContext || (window as any).webkitAudioContext;
  const ctx = new AC();
  try {
    return encodeWavPcm16(await ctx.decodeAudioData(await file.arrayBuffer()));
  } finally {
    ctx.close();
  }
}

// Interleaved PCM16, up to two channels. lib/wav.ts encodes mono, which is
// right for speech and throws away the stereo image of a song.
export function encodeWavPcm16(audio: AudioBuffer): Blob {
  const ch = Math.min(2, audio.numberOfChannels);
  const len = audio.length;
  const rate = audio.sampleRate;
  const data = len * ch * 2;
  const view = new DataView(new ArrayBuffer(44 + data));
  const w = (o: number, s: string) => {
    for (let i = 0; i < s.length; i++) view.setUint8(o + i, s.charCodeAt(i));
  };
  w(0, "RIFF");
  view.setUint32(4, 36 + data, true);
  w(8, "WAVE");
  w(12, "fmt ");
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, ch, true);
  view.setUint32(24, rate, true);
  view.setUint32(28, rate * ch * 2, true);
  view.setUint16(32, ch * 2, true);
  view.setUint16(34, 16, true);
  w(36, "data");
  view.setUint32(40, data, true);
  const chans = Array.from({ length: ch }, (_, c) => audio.getChannelData(c));
  let off = 44;
  for (let i = 0; i < len; i++) {
    for (let c = 0; c < ch; c++) {
      const s = Math.max(-1, Math.min(1, chans[c][i]));
      view.setInt16(off, s < 0 ? s * 0x8000 : s * 0x7fff, true);
      off += 2;
    }
  }
  return new Blob([view], { type: "audio/wav" });
}

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const s = r.result as string;
      resolve(s.slice(s.indexOf(",") + 1));
    };
    r.onerror = () => reject(r.error);
    r.readAsDataURL(blob);
  });
}

function base64ToBlob(b64: string, type: string): Blob {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new Blob([bytes], { type });
}

// Seconds of audio in a PCM WAV, from its header, without decoding it. Used to
// warn before an edit the server will refuse (the ~24 s VAE encoder cap on
// Vulkan); null when the header is not one this reads.
export async function wavSeconds(blob: Blob): Promise<number | null> {
  const buf = new DataView(await blob.slice(0, 4096).arrayBuffer());
  if (buf.byteLength < 12) return null;
  let off = 12;
  let byteRate = 0;
  while (off + 8 <= buf.byteLength) {
    const id = String.fromCharCode(buf.getUint8(off), buf.getUint8(off + 1), buf.getUint8(off + 2), buf.getUint8(off + 3));
    const size = buf.getUint32(off + 4, true);
    if (id === "fmt " && off + 20 <= buf.byteLength) byteRate = buf.getUint32(off + 16, true);
    if (id === "data") return byteRate > 0 ? Math.min(size, blob.size - off - 8) / byteRate : null;
    off += 8 + size + (size & 1);
  }
  return null;
}
