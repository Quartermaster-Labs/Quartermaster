// Pure helpers for the playground 3D tab, the sibling of imageGen.ts and
// videoGen.ts. Much smaller than either, for a reason worth stating: TRELLIS.2
// has no size, no sampler, no scheduler and no CFG. It takes an image and five
// numbers, and the defaults are the backend's own CLI defaults rather than
// anything tuned per checkpoint, because there is exactly one checkpoint family
// and its defaults are documented (docs/three-d-generation.md).

export { fmtDur } from "./imageGen";

/** The backend's own defaults, restated so the UI can show them and reset to them. */
export const THREED_DEFAULTS = {
  steps: 12,
  textureSize: 1024,
  pipeline: 512,
  shapeOnly: false,
  seed: -1,
};

// Atlas resolution. 2048 is offered but is not free: the texture bake is the
// Vulkan stage, and the atlas is what makes a default mesh a 40 MB file.
export const TEXTURE_SIZE_OPTIONS = [
  { value: "512", label: "512" },
  { value: "1024", label: "1024" },
  { value: "2048", label: "2048" },
];

// The coordinate resolution the model works at. Only two profiles exist.
// 1024 is flagged rather than hidden: it is the higher-quality profile and the
// docs call it unsafe on some GPUs, which is a warning, not a block.
export const PIPELINE_OPTIONS = [
  { value: "512", label: "512 · standard" },
  { value: "1024", label: "1024 · high", warn: true, title: "The higher-quality profile. Not safe on every GPU: it can exhaust VRAM or fail the texture bake. See Known issues." },
];

/**
 * Rough wall-clock for one mesh, in seconds.
 *
 * Anchored on the one figure on record: about 90 seconds for a 512-profile
 * image at the default 12 steps on a discrete GPU (docs/three-d-generation.md).
 * Everything else scales off that, and it is deliberately coarse: it exists so
 * the composer can say "about a minute and a half" before a user commits to a
 * generation that holds the whole backend, not to be accurate to the second.
 *
 * Shape-only skips the texture bake entirely, which the docs call "much
 * faster"; the 1024 pipeline is four times the coordinate volume but nothing
 * like four times the time, since the bake and the postprocess do not scale
 * with it.
 */
export function estimateSeconds(steps: number, pipeline: number, shapeOnly: boolean): number {
  const base = 90 * (steps / THREED_DEFAULTS.steps);
  const profile = pipeline >= 1024 ? 2.2 : 1;
  const texture = shapeOnly ? 0.55 : 1;
  return Math.round(base * profile * texture);
}

/** "about 2m" / "about 90s", for the composer hint. Deliberately vague. */
export function estimateLabel(steps: number, pipeline: number, shapeOnly: boolean): string {
  const s = estimateSeconds(steps, pipeline, shapeOnly);
  if (s < 90) return `about ${Math.round(s / 10) * 10}s`;
  return `about ${Math.round(s / 30) / 2}m`;
}
