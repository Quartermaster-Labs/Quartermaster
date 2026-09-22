// Pure launch-independent helpers for the playground Video tab, the sibling of
// imageGen.ts. Same shape, different numbers: a video model's usable canvas is
// roughly a quarter of an image model's (the sampler holds one latent PER FRAME)
// and it has two knobs no image has, frame count and fps.

import { ASPECTS, aspectDims, nearestAspect, SAMPLER_OPTIONS, SCHEDULER_OPTIONS, fmtDur } from "./imageGen";
export { ASPECTS, aspectDims, nearestAspect, SAMPLER_OPTIONS, SCHEDULER_OPTIONS, fmtDur };

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

// LTX-2.x is the third grid and the one exception to "rounds up": its VAE
// compresses 8 frames into one latent frame, so the count is 8k+1 and sd.cpp
// floors anything off-grid rather than raising it. 153 is a HARD ceiling, not a
// taste call: the checkpoint's positional_embedding_max_pos caps the temporal
// axis at 20 latent frames, and asking for more indexes past the table.
export const LTX_FRAME_OPTIONS = Array.from({ length: 19 }, (_, i) => 9 + i * 8);

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

/**
 * Latent tokens the sampler holds for one clip at this size and length.
 *
 * The divisors are the model's actual compression, and LTX's are not the others'.
 * Wan/H3 compress 8x spatially then patch-embed 2x2 on top (hence /16) and 4x
 * along time; LTX's VAE goes 32x spatially and 8x temporally and then patchifies
 * 1x1, so nothing halves again. That is a 4x difference per pixel and a 2x
 * difference per frame, which is why LTX can be offered a 1280-wide default where
 * H3 is capped at 640: charging it the /16 rate would paint every usable setting
 * orange.
 */
export function videoTokens(width: number, height: number, frames: number, id = ""): number {
  if (isLtx(id)) {
    return Math.ceil(width / 32) * Math.ceil(height / 32) * Math.ceil(frames / 8);
  }
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
export function vramWarning(width: number, height: number, frames: number, totalVramGB: number, id = ""): string {
  const t = videoTokens(width, height, frames, id);
  const budget = videoTokenBudget(totalVramGB);
  if (t <= budget) return "";
  return (
    `Probably will not fit. ${width}x${height} at ${frames} frames is ${t.toLocaleString()} latent tokens, ` +
    `about ${(t / budget).toFixed(1)}x what ${Math.round(totalVramGB)}GB is estimated to hold ` +
    `(~${budget.toLocaleString()}). Still selectable: this is an estimate that ignores VAE tiling and ` +
    `backend offload. If the render fails, cut length or resolution.`
  );
}

/**
 * Whether this model conditions on a first (and optionally last) frame.
 *
 * Matched on the checkpoint name because there is no capability bit for it: the
 * distinction lives in the WEIGHTS, not in any metadata the gguf scan reads. The
 * families that carry it spell it in the filename, which is why the local
 * checkpoint is minimax_h3_FL2VA (first-last frame to video + audio) and Wan
 * ships separate i2v/flf2v variants beside its t2v ones.
 *
 * Getting this wrong is cheap in one direction and not the other: a false
 * negative hides a working control, while a false positive offers a picker whose
 * images the backend silently drops, so the pattern stays narrow and explicit.
 */
export function supportsFrameRefs(id: string): boolean {
  // LTX is the one family that does not spell it in the filename: frame
  // conditioning is in every LTX-2.x checkpoint rather than in a separate i2v
  // variant, so the family name IS the capability here.
  return /fl2v|flf2v|i2v|ltx/.test(id.toLowerCase());
}

/** Highest frame count the family's grid is offered up to. */
export function maxFramesFor(id: string): number {
  if (isLtx(id)) return 153;
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

/** True when the model id names an LTX-2.x, the family on the 8k+1 grid. */
export function isLtx(id: string): boolean {
  return id.toLowerCase().includes("ltx");
}

export function frameOptionsFor(id: string): number[] {
  if (isLtx(id)) return LTX_FRAME_OPTIONS;
  return isH3(id) ? H3_FRAME_OPTIONS : FRAME_OPTIONS;
}

/** Snap a frame count onto the model family's grid, the way the backend will. */
export function snapFrames(n: number, id = ""): number {
  const clamped = Math.max(5, Math.min(maxFramesFor(id), Math.round(n)));
  // LTX floors where the others align up, so snapping the same direction the
  // backend does means rounding DOWN here: a value snapped up would be silently
  // shortened again on arrival and the label would name a clip length the file
  // never had.
  if (isLtx(id)) return Math.max(9, Math.floor((clamped - 1) / 8) * 8 + 1);
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
  // LTX-2.x. Both rows share the framing (1280x704 is well inside budget at
  // LTX's compression) and differ only in the schedule: the distilled
  // checkpoint is genuinely 8-step and guidance-free, the dev one wants ~20
  // euler steps at cfg 3.0. They are TENSOR-IDENTICAL, so the name is the only
  // thing that can tell them apart - hence the explicit branch in
  // videoDefaultsFor rather than a substring row, which cannot express
  // "ltx AND distilled".
  { match: "ltx", steps: 20, cfg: 3.0, sampler: "euler", scheduler: "discrete", size: "1280x704", frames: 121, fps: 24, maxDim: 1280 },
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

/** LTX's distilled schedule, the twin of autogen's isDistilledName. */
const LTX_DISTILLED = { steps: 8, cfg: 1.0 };

export function videoDefaultsFor(id: string) {
  const l = id.toLowerCase();
  const d = VIDEO_DEFAULTS.find((x) => l.includes(x.match));
  if (d && isLtx(l) && /distill|turbo/.test(l)) return { ...d, ...LTX_DISTILLED };
  return d;
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
