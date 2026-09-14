// Pure launch-independent helpers for the playground Video tab, the sibling of
// imageGen.ts. Same shape, different numbers: a video model's usable canvas is
// roughly a quarter of an image model's (the sampler holds one latent PER FRAME)
// and it has two knobs no image has, frame count and fps.

import { ASPECTS, aspectDims, SAMPLER_OPTIONS, SCHEDULER_OPTIONS, fmtDur } from "./imageGen";
export { ASPECTS, aspectDims, SAMPLER_OPTIONS, SCHEDULER_OPTIONS, fmtDur };

// Long-edge tiers. Stops at 1280: above that a 25-frame clip does not fit a 24GB
// card even before the 3D VAE decode, which is the real peak.
export const VIDEO_SIZE_TIERS = [384, 512, 640, 720, 832, 960, 1280];
export const VIDEO_DEFAULT_MAX_DIM = 960;

// sd.cpp normalizes --video-frames DOWN to the largest 4n+1 it can sample (34
// becomes 33), so offering anything else just silently changes the user's
// number. These are exact.
export const FRAME_OPTIONS = [13, 17, 25, 33, 49, 65, 81];

/** Nearest valid 4n+1 frame count, clamped to a sane range. */
export function snapFrames(n: number): number {
  const clamped = Math.max(5, Math.min(241, Math.round(n)));
  return Math.round((clamped - 1) / 4) * 4 + 1;
}

export const FPS_OPTIONS = [8, 12, 16, 24, 30];

// Per-model defaults matched by id substring, same mechanism as IMAGE_DEFAULTS.
// The H3 row is not taste: it is a 4-step distill that ABORTS when asked for
// guidance above 1.0, and 640x384x25 is what the model card trains at.
export const VIDEO_DEFAULTS: {
  match: string;
  steps: number;
  cfg: number;
  sampler: string;
  scheduler: string;
  size?: string;
  frames?: number;
  fps?: number;
  maxDim?: number;
}[] = [
  { match: "minimax", steps: 4, cfg: 1.0, sampler: "euler", scheduler: "discrete", size: "640x384", frames: 25, fps: 24 },
  { match: "h3", steps: 4, cfg: 1.0, sampler: "euler", scheduler: "discrete", size: "640x384", frames: 25, fps: 24 },
  { match: "wan", steps: 20, cfg: 5, sampler: "euler", scheduler: "discrete", size: "832x480", frames: 81, fps: 16 },
];

export const VIDEO_GENERIC_DEFAULTS = {
  match: "",
  steps: 20,
  cfg: 5,
  sampler: "",
  scheduler: "",
  negative: "",
  frames: 25,
  fps: 16,
  size: "640x384",
};

export function videoDefaultsFor(id: string) {
  const l = id.toLowerCase();
  return VIDEO_DEFAULTS.find((d) => l.includes(d.match));
}

// What the model is actually launched with, read off its argv by the server
// (apiModel.genDefaults). Video adds frames/fps to the image fields.
export interface VideoModelGenDefaults {
  steps?: number;
  cfg?: number;
  sampler?: string;
  width?: number;
  height?: number;
  frames?: number;
  fps?: number;
}

// Precedence, highest first: the model's own launch flags, then the preset
// table, then the generic fallback. Identical reasoning to settingsFor in
// imageGen.ts: the playground puts an explicit value on every request, so a
// launch flag that did not win here would be invisible.
export function videoSettingsFor(id: string, gen?: VideoModelGenDefaults) {
  const d = videoDefaultsFor(id) ?? VIDEO_GENERIC_DEFAULTS;
  const presetSize = "size" in d ? d.size : undefined;
  return {
    steps: gen?.steps || d.steps,
    cfg: gen?.cfg || d.cfg,
    sampler: gen?.sampler || d.sampler,
    scheduler: d.scheduler,
    negative: VIDEO_GENERIC_DEFAULTS.negative,
    frames: gen?.frames || d.frames || VIDEO_GENERIC_DEFAULTS.frames,
    fps: gen?.fps || d.fps || VIDEO_GENERIC_DEFAULTS.fps,
    size: gen?.width && gen?.height ? `${gen.width}x${gen.height}` : presetSize,
  };
}

/**
 * Human label for a job status. The job document is the ONLY progress signal the
 * API gives (there is no percentage field), so the queue position is worth
 * showing: a job sitting at "queued" behind another one looks identical to a
 * stalled render otherwise.
 */
export function videoStatusLabel(status: string | undefined, queuePosition?: number): string {
  switch (status) {
    case "queued":
      return queuePosition && queuePosition > 0 ? `Queued (#${queuePosition})` : "Queued…";
    case "generating":
      return "Rendering…";
    case "completed":
      return "Done";
    case "failed":
      return "Failed";
    case "cancelled":
      return "Cancelled";
  }
  return "Starting…";
}
