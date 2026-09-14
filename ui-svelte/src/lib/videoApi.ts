import { inferenceHeaders } from "./inferenceAuth";
import type { SdApiLoraRef } from "./types";

// sd-server's NATIVE job API, which is what video generation runs on. Unlike
// /sdapi txt2img (one request, one picture, minutes of waiting) a video render
// is asynchronous by contract:
//
//   POST /sdcpp/v1/vid_gen   -> {"id":"job_…","status":"queued", …}   (milliseconds)
//   GET  /sdcpp/v1/jobs/{id} -> the same document, until status is terminal
//   POST /sdcpp/v1/jobs/{id}/cancel
//
// quartermaster proxies all three and keeps a server-side job→model registry,
// which is what makes the two id-only routes routable and what holds the model
// in VRAM for the life of the render (see internal/server/videojobs.go).
//
// THERE IS NO PROGRESS FIELD. The job document reports a status and nothing
// finer, so the UI's progress is an elapsed-time readout, not a percentage.
// Don't add a fake bar: a video render's per-step time varies by more than an
// order of magnitude between the sampler and the 3D VAE decode.

export type VideoJobStatus = "queued" | "generating" | "completed" | "failed" | "cancelled";

// The video result: ONE encoded clip, not a frame list. b64_json is the whole
// file, playable as `data:${mime_type};base64,${b64_json}`.
export interface VideoJobResult {
  b64_json?: string;
  mime_type?: string;
  output_format?: string;
  frame_count?: number;
  fps?: number;
}

export interface VideoJob {
  id: string;
  kind?: string;
  status: VideoJobStatus;
  created?: number;
  started?: number;
  queue_position?: number;
  poll_url?: string;
  result?: VideoJobResult;
  error?: { code?: string; message?: string };
}

// sample_params is sd.cpp's own nesting, not ours: the sampler knobs live one
// level down and the guidance scales one level below that. Sent verbatim.
export interface VideoGenRequest {
  model: string;
  prompt: string;
  negative_prompt?: string;
  width: number;
  height: number;
  video_frames: number;
  fps: number;
  seed?: number;
  output_format?: string;
  // Per-request LoRAs, the same {path, multiplier} shape /sdapi txt2img takes.
  // This is the ONLY way a turbo LoRA reaches a render: there is no launch-time
  // --lora flag, only --lora-model-dir, so which adapters apply is decided per
  // request and never at process start.
  lora?: SdApiLoraRef[];
  // First/last frame conditioning, as full data: URLs.
  //
  // Data URL rather than bare base64 because that is exactly what sd-server's
  // OWN web UI sends to this route: its bundled client builds the vid_gen body
  // with `init_image: e.init_image.dataUrl`. The /sdapi/* routes are A1111
  // compatible and take bare base64, which is why the Images tab strips the
  // prefix, but /v1/vid_gen is sd-server's native route and the shape its first
  // party client uses is the one shape guaranteed to be accepted.
  //
  // These are what make total video length independent of VRAM. A clip's cost is
  // fixed by its own size and frame count, so feeding the last frame of one
  // render back as the next render's init_image chains clips end to end at a
  // FLAT per-clip cost, however long the finished video gets. That is the only
  // way past the ceiling the length warnings describe.
  //
  // A text-to-video checkpoint ignores both fields rather than erroring, which
  // is why the UI offers them only for models that condition on them.
  init_image?: string;
  end_image?: string;
  sample_params?: {
    sample_steps?: number;
    sample_method?: string;
    scheduler?: string;
    guidance?: { txt_cfg?: number };
  };
}

export function isTerminalVideoStatus(s: VideoJobStatus | undefined): boolean {
  return s === "completed" || s === "failed" || s === "cancelled";
}

// friendlyVideoError mirrors sdApi's: a 502/503/504 means the backend is gone
// rather than that the request was wrong, and the gateway's own text is noise.
async function friendlyVideoError(response: Response, what: string): Promise<string> {
  if (response.status === 502 || response.status === 503 || response.status === 504) {
    return "Video model unavailable - it crashed, was evicted, or is still loading. Try again in a moment.";
  }
  const text = await response.text().catch(() => "");
  let detail = text;
  try {
    const parsed = JSON.parse(text);
    if (typeof parsed?.error === "string") detail = parsed.error;
    else if (typeof parsed?.error?.message === "string") detail = parsed.error.message;
  } catch {
    // not JSON - keep the raw text
  }
  return `${what} failed (${response.status})${detail ? `: ${detail}` : ""}`;
}

export async function startVideoJob(request: VideoGenRequest, signal?: AbortSignal): Promise<VideoJob> {
  const response = await fetch("/sdcpp/v1/vid_gen", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(request),
    signal,
  });
  if (!response.ok) throw new Error(await friendlyVideoError(response, "Video generation"));
  const job: VideoJob = await response.json();
  if (!job?.id) throw new Error("Video generation failed: the backend returned no job id");
  return job;
}

export async function fetchVideoJob(id: string, signal?: AbortSignal): Promise<VideoJob> {
  const response = await fetch(`/sdcpp/v1/jobs/${encodeURIComponent(id)}`, {
    headers: inferenceHeaders(),
    signal,
  });
  if (!response.ok) throw new Error(await friendlyVideoError(response, "Job poll"));
  return response.json();
}

// Cancel is best-effort by design: sd.cpp refuses to interrupt a job that has
// already entered the sampler ("job is currently generating and cannot be
// interrupted yet"), so the caller must treat a rejection as information, not
// as a failure to report loudly.
export async function cancelVideoJob(id: string): Promise<boolean> {
  try {
    const response = await fetch(`/sdcpp/v1/jobs/${encodeURIComponent(id)}/cancel`, {
      method: "POST",
      headers: inferenceHeaders(),
    });
    return response.ok;
  } catch {
    return false;
  }
}

/** Playable src for a finished job, or "" when it carries nothing playable. */
export function videoSrc(result: VideoJobResult | undefined): string {
  if (!result?.b64_json) return "";
  // output_format "avi" (and anything with a non-video mime) is a real
  // possibility from sd.cpp and no browser plays it inline, so say so upstream
  // rather than mounting a <video> that silently shows nothing.
  const mime = result.mime_type || "video/mp4";
  return `data:${mime};base64,${result.b64_json}`;
}

/** True when the finished clip is something a <video> element can actually play. */
export function isPlayable(result: VideoJobResult | undefined): boolean {
  const mime = result?.mime_type ?? "";
  return !!result?.b64_json && mime.startsWith("video/");
}

// The poll interval the UI uses. Matched to the server's own watcher: polling
// faster only adds requests, since the cached document it serves from is
// refreshed on that cadence anyway.
export const VIDEO_POLL_MS = 2000;

/**
 * Poll a job until it reaches a terminal state. onUpdate fires on every
 * document, so the caller can show queue position and status transitions.
 * Aborting the signal STOPS POLLING ONLY: it does not stop the render, which is
 * what cancelVideoJob is for.
 */
export async function awaitVideoJob(
  id: string,
  onUpdate: (job: VideoJob) => void,
  signal?: AbortSignal,
): Promise<VideoJob> {
  for (;;) {
    if (signal?.aborted) throw new DOMException("aborted", "AbortError");
    const job = await fetchVideoJob(id, signal);
    onUpdate(job);
    if (isTerminalVideoStatus(job.status)) return job;
    await new Promise<void>((resolve, reject) => {
      const t = setTimeout(resolve, VIDEO_POLL_MS);
      signal?.addEventListener(
        "abort",
        () => {
          clearTimeout(t);
          reject(new DOMException("aborted", "AbortError"));
        },
        { once: true },
      );
    });
  }
}
