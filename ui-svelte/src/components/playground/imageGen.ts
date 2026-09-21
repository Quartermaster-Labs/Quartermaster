// Pure launch-independent helpers for the playground Images tab, extracted from
// ImageInterface.svelte. Nothing here reads component state — the component owns
// the $state/$derived and calls into these.

// Aspect ratio × long-edge → concrete WxH. The short edge is rounded to a
// multiple of 64 (SD/VAE latent stride). One aspect + one size list beats a flat
// grid of every WxH combo.
export const ASPECTS = [
  { value: "1:1", label: "Square 1:1", w: 1, h: 1 },
  { value: "4:3", label: "Landscape 4:3", w: 4, h: 3 },
  { value: "3:2", label: "Landscape 3:2", w: 3, h: 2 },
  { value: "16:9", label: "Wide 16:9", w: 16, h: 9 },
  { value: "3:4", label: "Portrait 3:4", w: 3, h: 4 },
  { value: "2:3", label: "Portrait 2:3", w: 2, h: 3 },
  { value: "9:16", label: "Tall 9:16", w: 9, h: 16 },
];
export const SIZE_TIERS = [512, 768, 1024, 1280, 1536, 2048];

// Concrete [w,h] for an aspect id + long edge. Short edge snapped to /64.
export function aspectDims(aspectValue: string, longEdge: number): [number, number] {
  const a = ASPECTS.find((x) => x.value === aspectValue) ?? ASPECTS[0];
  if (a.w === a.h) return [longEdge, longEdge];
  const short = Math.max(64, Math.round((longEdge * Math.min(a.w, a.h)) / Math.max(a.w, a.h) / 64) * 64);
  return a.w > a.h ? [longEdge, short] : [short, longEdge];
}
export const SAMPLER_OPTIONS = ["", "euler_a", "euler", "heun", "dpm2", "dpmpp2s_a", "dpmpp2m", "dpmpp2mv2", "ipndm", "ipndm_v", "lcm", "ddim_trailing", "tcd"].map(
  (v) => ({ value: v, label: v || "Default sampler" })
);
// Full scheduler set supported by this sd-server build (leejet stable-diffusion.cpp,
// str_to_scheduler in examples/common/common.cpp). Sent verbatim as the sdapi
// "scheduler" field; sd.cpp matches these names exactly (no alias map, unlike
// samplers). "logit_normal" is omitted — absent from the pinned 2026-06-22 binary.
export const SCHEDULER_OPTIONS = [
  "", "discrete", "karras", "exponential", "ays", "gits", "sgm_uniform",
  "simple", "kl_optimal", "beta", "smoothstep", "bong_tangent", "lcm",
  "flux", "flux2", "ltx2",
].map((v) => ({ value: v, label: v || "Auto for model" }));

// Sensible per-model gen defaults, matched by id substring. Applied only when
// the user switches models (not on reload) so manual tweaks survive a refresh.
// ponytail: substring match, no backend "recommended params" field exists.
// size/negative optional: SDXL-anime models need 1024 (512 duplicates) + their
// booru quality-tag negative; distilled models leave both to the user's prefs.
export const SDXL_ANIME_NEG =
  "nsfw, lowres, (bad), text, error, fewer, extra, missing, worst quality, jpeg artifacts, low quality, watermark, unfinished, displeasing, oldest, early, chromatic aberration, signature, extra digits, artistic error, username, scan, [abstract]";
// cfg here = sdapi cfg_scale (true CFG / txt_cfg); for flux-dev models keep it 1.0
// and the DISTILLED guidance is baked server-side via autogen --guidance (the
// /sdapi route has no per-request key for it). denoise = img2img strength.
// maxDim = largest long edge this model handles; bigger tiers are greyed out in
// the Size picker. Absent → DEFAULT_MAX_DIM (1536). 2048 is opt-in per model.
export const DEFAULT_MAX_DIM = 1536;
// Batch ceiling for one prompt (sdapi `batch_size`). sd.cpp renders the images
// sequentially, so N images cost N× the time with no way to stop mid-run except
// unloading the model — a low cap keeps a fat-fingered value from parking the
// GPU for an hour.
export const MAX_BATCH = 8;
// annotEdit = the model targets a local edit SEMANTICALLY, by reading a region
// marked on the image itself, rather than by latent masking. Unlocks the brush's
// Annotate mode, which sends the tinted overlay as a reference instead of a mask.
// alphaPrompt = this model can render a real alpha channel, and the way to ask
// for it is WORDING, not a flag: the phrasing wraps the user's subject. Verbatim
// from the model card, because the trigger is a learned caption pattern and a
// paraphrase is not guaranteed to hit it. Presence of the field is what puts the
// transparency button in the composer, so a second such model is a data edit.
export const IMAGE_DEFAULTS: { match: string; steps: number; cfg: number; sampler: string; scheduler: string; size?: string; negative?: string; denoise?: number; maxDim?: number; annotEdit?: boolean; alphaPrompt?: { prefix: string; suffix: string } }[] = [
  { match: "z-image", steps: 10, cfg: 1.0, sampler: "euler", scheduler: "discrete" },
  // Kontext: surgical edit — low denoise so it doesn't redraw the whole scene.
  { match: "kontext", steps: 24, cfg: 1.0, sampler: "euler", scheduler: "discrete", denoise: 0.55 },
  // Qwen-Image-Edit Rapid (Phr00t AIO, Lightning 2511 8-step distill): cfg MUST
  // be 1.0 (cfg>1 burns to a solid yellow frame). Repo recipe is euler_ancestral
  // + beta @ 4-8 steps — this sd.cpp build DOES support the beta schedule, so use
  // it (was standing in with discrete when beta wasn't reachable). The ANCESTRAL
  // sampler is what the few-step distill needs (plain euler undercooks at 8 →
  // needed 20 to compensate). Ref-image edit (extra_images), so denoise unused.
  { match: "qwen-rapid", steps: 8, cfg: 1.0, sampler: "euler_a", scheduler: "beta" },
  // Qwen-Image 2.1 (the 7B single-stream base, NOT a distill): trained WITHOUT
  // guidance, so cfg must be 1.0. Verified off the pipeline rather than inferred
  // from the family: diffusers' QwenImage21Pipeline defaults true_cfg_scale to
  // 1.0 ("meant to be sampled without guidance") and carries no guidance_scale
  // parameter at all, and the model card passes neither, only steps=40. That
  // INVERTS the 20B line (true_cfg_scale 4.0, 50 steps, guidance_scale present)
  // which reports the same qwen_image arch tag, which is exactly why this needs
  // its own row instead of a shared qwen one. At cfg 1.0 the negative prompt is
  // ignored server-side, so leaving it empty is honest. Do NOT raise this after
  // reading leejet's docs/qwen_image_2.1.md, whose example passes --cfg-scale
  // 6.0: 1.0 is confirmed correct in practice on this model. Native canvas is 2048
  // (the card's 1:1); 1536 would silently cap the model's own resolution.
  // annotEdit: the card promises local edits "via circles, painted annotations,
  // or separate masks", i.e. the region is communicated through the reference
  // path and read by the VLM, not by masking the latent.
  { match: "qwen-image-2.1", steps: 40, cfg: 1.0, sampler: "euler", scheduler: "discrete", maxDim: 2048, annotEdit: true,
    alphaPrompt: { prefix: "This is an RGBA image with transparency.", suffix: "The image has alpha channel and the background is transparent." } },
  // Fill: inpaint — always fully regenerates the masked area (denoise 1.0).
  // Guidance-distilled but NOT step-distilled (BFL reference is 50): 20 leaves
  // soft seams at mask edges on large fills, 25 is the practical knee.
  { match: "fill", steps: 25, cfg: 1.0, sampler: "euler", scheduler: "discrete", denoise: 1.0 },
  // AnimagineXL 3.1 / Illustrious SDXL-anime: Euler a, <30 steps, cfg 5-7, 1024.
  { match: "animagine", steps: 28, cfg: 7, sampler: "euler_a", scheduler: "discrete", size: "1024x1024", negative: SDXL_ANIME_NEG },
  { match: "illustrious", steps: 28, cfg: 7, sampler: "euler_a", scheduler: "discrete", size: "1024x1024", negative: SDXL_ANIME_NEG },
];
// Wrap a subject in the model's transparency phrasing. Split prefix/suffix
// rather than a single prepended sentence because the card's example brackets
// the subject on BOTH sides ("This is an RGBA image with transparency. <subject>
// The image has alpha channel and the background is transparent."), and the
// closing half is the one that names the alpha channel. Returns the text
// unchanged when the model has no such mode, so callers need no branch.
export function withAlphaPrompt(id: string, text: string): string {
  const a = defaultsFor(id)?.alphaPrompt;
  if (!a) return text;
  return `${a.prefix} ${text.trim()} ${a.suffix}`.replace(/\s+/g, " ").trim();
}

export function defaultsFor(id: string) {
  // Ids are derived from filenames, so one model reaches us spelled either way
  // (qwen_image_2.1-q8_0 from the repo's own naming, qwen-image-2.1-Q8 from a
  // rename). Normalise the separator on both sides rather than carrying a row
  // per spelling, which would fail silently: a missed match is not an error,
  // it is the generic cfg 7 quietly burning a guidance-free model.
  const norm = (s: string) => s.toLowerCase().replace(/_/g, "-");
  const l = norm(id);
  return IMAGE_DEFAULTS.find((d) => l.includes(norm(d.match)));
}

// Fallback settings for a model with no entry above. Switching models must reset
// the panel either way: leaving the previous model's preset in place (e.g. the
// anime booru negative, or cfg 1.0 from a distilled model) silently mis-renders
// the new one. Mirrors the userPref initial values.
export const GENERIC_DEFAULTS = {
  match: "",
  steps: 20,
  cfg: 7,
  sampler: "",
  scheduler: "",
  negative: "",
  denoise: 0.6,
} satisfies (typeof IMAGE_DEFAULTS)[number];

// What the model itself says it wants, read off its launch line by the server
// (apiModel.genDefaults). Absent fields = the flag was not emitted, i.e. no
// opinion, so the preset/generic value stands.
export interface ModelGenDefaults {
  steps?: number;
  cfg?: number;
  sampler?: string;
  width?: number;
  height?: number;
}

// The settings a model should load with, in precedence order:
//   1. what the model is actually LAUNCHED with (`gen`, from its own argv)
//   2. its entry in IMAGE_DEFAULTS
//   3. GENERIC_DEFAULTS
// The model's own configuration has to win over the table below it, otherwise
// setting defaultCfg on a model does nothing visible: the playground puts an
// explicit cfg_scale on every /sdapi request, so the launch flag it overrides is
// the very thing the user just configured. The table stays as the source for
// what the command line cannot express (scheduler, negative prompt, denoise) and
// for models that declare nothing.
// `negative`/`denoise` are always present here so a switch clears a stale preset
// value instead of inheriting it.
export function settingsFor(id: string, gen?: ModelGenDefaults) {
  const d = defaultsFor(id) ?? GENERIC_DEFAULTS;
  const presetSize = "size" in d ? d.size : undefined;
  return {
    steps: gen?.steps || d.steps,
    cfg: gen?.cfg || d.cfg,
    sampler: gen?.sampler || d.sampler,
    scheduler: d.scheduler,
    negative: d.negative ?? GENERIC_DEFAULTS.negative,
    denoise: d.denoise ?? GENERIC_DEFAULTS.denoise,
    // Only a launch line naming BOTH edges states a size; one alone is a
    // half-specified canvas the aspect picker cannot honour.
    size: gen?.width && gen?.height ? `${gen.width}x${gen.height}` : presetSize,
  };
}

export type SdStagePhase = "encode" | "cond" | "sample" | "decode" | null;

export interface SdProgress {
  label: string;
  phase: SdStagePhase;
  step: number;
  totalSteps: number;
  secPerIt: number;
}

// Progress from sd-server's stdout (mirrored into upstreamLogs). A whole gen runs
// several phases, each printing its own "N/M - Xs/it" bar: encode_first_stage
// (VAE-encode the ref image), text-encode + weight streaming, the sampler, then
// decode_first_stage (VAE-decode output). Only the sampler's total equals the
// requested steps; the VAE bars have their own small tile counts. Naively taking
// the newest N/M made the encode's "4/4" look like finished steps — so instead we
// pick the live PHASE from the latest log marker and label the bar accordingly,
// giving feedback during the slow encode/stream/decode stages too.
export function parseSdProgress(tail: string, expected: number): SdProgress {
  // Live phase = the last marker present in the tail (they print in this order).
  const marks: [number, string, Exclude<SdStagePhase, null>][] = [
    [tail.lastIndexOf("EDIT mode"), "Encoding image…", "encode"],
    [tail.lastIndexOf("encode_first_stage completed"), "Encoding prompt…", "cond"],
    [
      Math.max(tail.lastIndexOf("get_learned_condition completed"), tail.lastIndexOf("generating image:")),
      "Sampling",
      "sample",
    ],
    [tail.lastIndexOf("decoding"), "Decoding…", "decode"],
  ];
  let cur: (typeof marks)[number] | null = null;
  for (const m of marks) if (m[0] >= 0 && (!cur || m[0] > cur[0])) cur = m;
  const phase: SdStagePhase = cur ? cur[2] : null;
  const label = cur ? cur[1] : "Preparing…";

  // Newest "N/M - Xs/it" bar. For sampling accept only total===steps (skip a stale
  // encode bar before the first sampler tick); for encode/decode take the VAE bar
  // (total!==steps); condition/prepare have no step bar → indeterminate.
  let picked: RegExpExecArray | null = null;
  if (phase === "sample" || phase === "encode" || phase === "decode") {
    const re = /(\d+)\/(\d+)\s*-\s*([\d.]+)s\/it/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(tail)) !== null) {
      const isSampler = +m[2] === expected;
      if (phase === "sample" ? isSampler : !isSampler) picked = m;
    }
  }
  if (!picked) return { label, phase, step: 0, totalSteps: 0, secPerIt: 0 };
  return { label, phase, step: +picked[1], totalSteps: +picked[2], secPerIt: +picked[3] };
}

// "90s" → "1m 30s"; sub-minute stays "45s". Image gens run minutes on an 8GB card.
export function fmtDur(s: number): string {
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}
