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

// Frame counts are NOT free-form, and an off-grid number is not rejected: it is
// silently changed. sd.cpp ALIGNS --video-frames UP to the family's grid, and
// there are two grids.
//
//   - MiniMax-H3: 17k+5, minimum 5, i.e. 5, 22, 39, 56, 73, 90, 107.
//   - every other family: the largest 4n+1.
//
// So a user who asks H3 for 25 gets 39, a third longer than the clip they sized
// their prompt for. Only exact values are offered. sd.cpp: align_video_frames,
// gated on SDVersion.
export const FRAME_OPTIONS = [13, 17, 25, 33, 49, 65, 81];
export const H3_FRAME_OPTIONS = [5, 22, 39, 56, 73, 90, 107];

/** True when the model id names a MiniMax-H3, the one family on the 17k+5 grid. */
export function isH3(id: string): boolean {
  const l = id.toLowerCase();
  return l.includes("minimax") || l.includes("h3");
}

export function frameOptionsFor(id: string): number[] {
  return isH3(id) ? H3_FRAME_OPTIONS : FRAME_OPTIONS;
}

/** Snap a frame count onto the model family's grid, the way the backend will. */
export function snapFrames(n: number, id = ""): number {
  const clamped = Math.max(5, Math.min(241, Math.round(n)));
  if (isH3(id)) return Math.max(5, Math.ceil((clamped - 5) / 17) * 17 + 5);
  return Math.round((clamped - 1) / 4) * 4 + 1;
}

export const FPS_OPTIONS = [8, 12, 16, 24, 30];

// H3 is FIXED at 24 fps: sd.cpp's request handler overrides whatever was asked
// for and logs a warning, so offering the other rows would be a lie.
export function fpsOptionsFor(id: string): number[] {
  return isH3(id) ? [24] : FPS_OPTIONS;
}

// Per-model defaults matched by id substring, same mechanism as IMAGE_DEFAULTS.
// The H3 row is not taste: the model conditions at cfg 1.0 (the generic 5 gives
// mush), 640x384 is its training resolution, 56 is on its frame grid, and 24 fps
// is the only rate it will run at. steps 20 is sd-server's own default and the
// right number for the BASE model: the 4-step figure belongs to the turbo LoRAs,
// which arrive per request via <lora:name:1.0> in the prompt, not at launch.
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
  { match: "minimax", steps: 20, cfg: 1.0, sampler: "euler", scheduler: "discrete", size: "640x384", frames: 56, fps: 24 },
  { match: "h3", steps: 20, cfg: 1.0, sampler: "euler", scheduler: "discrete", size: "640x384", frames: 56, fps: 24 },
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
