// Pure launch-independent helpers for the playground Video tab, the sibling of
// imageGen.ts. Same shape, different numbers: a video model's usable canvas is
// roughly a quarter of an image model's (the sampler holds one latent PER FRAME)
// and it has two knobs no image has, frame count and fps.

import { ASPECTS, aspectDims, SAMPLER_OPTIONS, SCHEDULER_OPTIONS, fmtDur } from "./imageGen";
export { ASPECTS, aspectDims, SAMPLER_OPTIONS, SCHEDULER_OPTIONS, fmtDur };

// Long-edge tiers. 1360 is the top rung because 16:9 with a 768 short side is
// MiniMax-H3's native output size (its model card: "the shorter side is set to
// 768 pixels by default"), and 1365.33 has to round to a multiple of 16.
//
// Offered is not the same as reachable: VIDEO_DEFAULT_MAX_DIM stays at 960,
// because above that a clip does not fit a 24GB card once the 3D VAE decode
// peaks. The rungs above the cap unlock only for a model that LAUNCHES at a
// larger size, which is a per-install decision (see modelMax in
// VideoInterface.svelte).
export const VIDEO_SIZE_TIERS = [
  384, 448, 512, 576, 640, 704, 768, 832, 896, 960, 1024, 1152, 1280, 1360,
];
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
//
// Both ladders run to the MODEL's ceiling, not the card's. H3 is documented for
// clips up to 15 seconds, which at its fixed 24 fps is 345 frames, and the whole
// 17k+5 grid up to there is offered. Whether a given rung FITS is a different
// question that depends on resolution, and nothing here can answer it: a long
// clip at a large canvas will run the backend out of VRAM. The picker's job is
// to never offer a number the backend would silently change underneath you.
export const FRAME_OPTIONS = [
  13, 17, 21, 25, 29, 33, 41, 49, 57, 65, 73, 81, 97, 113, 129, 161, 193, 241,
];
export const H3_FRAME_OPTIONS = Array.from({ length: 21 }, (_, i) => 5 + i * 17);

// ---------------------------------------------------------------------------
// Rough VRAM feasibility.
//
// Peak VRAM for a video render is dominated by the sampler, which holds the
// latents for the WHOLE clip at once rather than a frame at a time. So the
// honest proxy is the token count: the 3D VAE compresses 8x spatially and 4x
// temporally, then a 2x2 patch embed halves each spatial axis again.
//
// The anchor is the one explicit claim on record: the tier ladder stopped at
// 1280 because "above that a 25-frame clip does not fit a 24GB card". 1280x720
// at 25 frames is 80 * 45 * 7 = 25200 tokens, so that is the last size believed
// to fit and the budget is set there.
//
// Deliberately NOT anchored on 13440, which is where VIDEO_DEFAULT_MAX_DIM (960
// long edge at 25f) and the H3 family default (640x384 at 56f) both land. Those
// two agreeing is a nice check that the token proxy tracks something real, but
// they are the CONSERVATIVE default, not the ceiling. Calibrating on them would
// paint settings orange that render fine, and a warning nobody believes is worse
// than no warning.
//
// Both figures are inherited judgements, not measurements. The model's WEIGHTS
// sit outside this budget and are roughly fixed (~11GB for a Q4 video DiT), so
// what scales with a bigger card is the leftover, not the total. Everything
// downstream treats the result as a WARNING and never as a block: the estimate
// ignores --vae-tiling, backend offload and latent quantisation, so a wrong
// "unavailable" would hide a setting the card could actually manage.
export const VIDEO_TOKEN_BUDGET_24GB = 25200;
const VIDEO_WEIGHTS_GB = 11;

/** Latent tokens the sampler holds for one clip at this size and length. */
export function videoTokens(width: number, height: number, frames: number): number {
  return Math.ceil(width / 16) * Math.ceil(height / 16) * Math.ceil(frames / 4);
}

/** Tokens a card of this size is estimated to hold, once weights are paid for. */
export function videoTokenBudget(totalVramGB: number): number {
  const head = Math.max(1, totalVramGB - VIDEO_WEIGHTS_GB);
  return Math.round((VIDEO_TOKEN_BUDGET_24GB * head) / (24 - VIDEO_WEIGHTS_GB));
}

/**
 * Warning text for a setting that is valid but probably will not fit, or "" when
 * it is within budget. Returned as prose because it is shown as a tooltip on an
 * orange row: the row stays selectable, it just says what it is likely to cost.
 */
export function vramWarning(width: number, height: number, frames: number, totalVramGB: number): string {
  const t = videoTokens(width, height, frames);
  const budget = videoTokenBudget(totalVramGB);
  if (t <= budget) return "";
  return (
    `Probably will not fit. ${width}x${height} at ${frames} frames is ${t.toLocaleString()} latent tokens, ` +
    `about ${(t / budget).toFixed(1)}x what ${Math.round(totalVramGB)}GB is estimated to hold ` +
    `(~${budget.toLocaleString()}). Still selectable: this is an estimate that ignores VAE tiling and ` +
    `backend offload. If the render fails, cut length or resolution.`
  );
}

/** Highest frame count the family's grid is offered up to. */
export function maxFramesFor(id: string): number {
  return isH3(id) ? 345 : 241;
}

/**
 * Clip length as a label. Seconds lead because that is the number a person is
 * actually choosing; the frame count trails because it is what the backend
 * takes and what the grid constrains. The two cannot be collapsed into one: the
 * grid is defined in frames, so the seconds are whatever those frames divide
 * into and are rarely round (H3's rungs land on 2.3s, 3.0s, 3.8s...).
 */
export function clipLabel(frames: number, fps: number): string {
  const secs = frames / Math.max(1, fps);
  return `${secs.toFixed(1)}s \u00b7 ${frames}f`;
}

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
  const clamped = Math.max(5, Math.min(maxFramesFor(id), Math.round(n)));
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
// mush), 56 is on its frame grid, and 24 fps is the only rate it will run at.
//
// 640x384 is NOT the model's native size. H3 outputs at a 768 short side, so
// this row is a deliberate downscale that fits a mid-range card, and it is the
// LOWEST-precedence source of a size: a box that can afford the real thing sets
// defaultWidth/defaultHeight in the generate file, and those launch flags win
// here (see videoSettingsFor) and also raise the Size picker's ceiling.
//
// steps 20 is sd-server's own default and the right number for the BASE model:
// the 4-step figure belongs to the turbo LoRAs, which arrive per request via the
// LoRA picker or <lora:name:1.0> in the prompt, not at launch.
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
