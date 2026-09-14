import type { Model } from "./types";

export interface GroupedModels {
  local: Model[];
  localMatching: Model[];
  peersByProvider: Record<string, Model[]>;
}

// prettifyModelName turns a raw model id ("gemma-3-12b-it") into a display label
// ("Gemma 3 12b It"): dashes/underscores become spaces, each word capitalized.
// Cosmetic only — callers keep model.id for load/unload/links.
export function prettifyModelName(s: string): string {
  return s
    .split(/[-_]/)
    .map((w) => (w ? w[0].toUpperCase() + w.slice(1) : w))
    .join(" ");
}

export type ModelCategory = "llm" | "image" | "video" | "3d" | "segment" | "tts" | "transcribe" | "embed";

// Sub-menu order under the Models tab. LLM is the catch-all default.
export const MODEL_CATEGORIES: { id: ModelCategory; label: string }[] = [
  { id: "llm", label: "LLM" },
  { id: "image", label: "Image" },
  { id: "video", label: "Video" },
  { id: "3d", label: "3D" },
  { id: "segment", label: "Segment" },
  { id: "tts", label: "TTS" },
  { id: "transcribe", label: "Transcribe" },
  { id: "embed", label: "Embed" },
];

// Bucket a model by its declared capabilities. Anything that isn't clearly an
// image/audio/embedding model falls through to "llm". Segmentation is checked
// BEFORE image because SAM also declares in:image/out:image (image_to_image),
// and 3D before image for the same reason: a TRELLIS.2 model takes an image and
// is not a diffusion one.
export function modelCategory(m: Model): ModelCategory {
  const c = m.capabilities;
  if (c?.segmentation) return "segment";
  if (c?.image_to_3d) return "3d";
  // Before image: a video model is an sd-server diffusion model like any other
  // and may well advertise image capabilities alongside, but it answers on the
  // job API, so bucketing it as "image" would put it in the wrong tab.
  if (c?.video_generation) return "video";
  if (c?.image_generation || c?.image_to_image) return "image";
  if (c?.audio_speech) return "tts";
  if (c?.audio_transcriptions) return "transcribe";
  if (c?.embeddings) return "embed";
  return "llm";
}

// How much of the GPU a loaded model is holding, for ranking several live models
// against each other. The sizer's prediction comes first because that is what is
// actually resident; the file size is the fallback for rows it never estimated
// (exec-per-request backends, peers). Both are optional, so `||` rather than
// `??`: a 0 here means "unknown", not "a zero-byte model".
export function modelWeightGB(m: Model): number {
  return m.estVramGB || m.sizeGB || 0;
}

// The one to name when the rail has room for a single model. Ties fall back to
// the id so the pick is stable frame to frame rather than depending on the order
// the server happened to list them in.
export function largestModel(ms: Model[]): Model | undefined {
  return ms.slice().sort((a, b) => modelWeightGB(b) - modelWeightGB(a) || a.id.localeCompare(b.id))[0];
}

export function matchesCapabilities(model: Model, required: string[], matchAny = false): boolean {
  if (!required.length) return true;
  if (!model.capabilities) return false;
  const caps = model.capabilities as Record<string, boolean>;
  if (matchAny) {
    return required.some((cap) => caps[cap] === true);
  }
  return required.every((cap) => caps[cap] === true);
}

export function groupModels(models: Model[], capabilities?: string[], matchAny = false): GroupedModels {
  const available = models.filter((m) => !m.unlisted);
  const local = available.filter((m) => !m.peerID);
  const peerModels = available.filter((m) => m.peerID);

  let localMatching: Model[] = [];
  let localRest: Model[] = [];

  if (capabilities && capabilities.length > 0) {
    for (const model of local) {
      if (matchesCapabilities(model, capabilities, matchAny)) {
        localMatching.push(model);
      } else {
        localRest.push(model);
      }
    }
  } else {
    localRest = local;
  }

  const peersByProvider = peerModels.reduce(
    (acc, model) => {
      const peerId = model.peerID || "unknown";
      if (!acc[peerId]) acc[peerId] = [];
      acc[peerId].push(model);
      return acc;
    },
    {} as Record<string, Model[]>
  );

  return { local: localRest, localMatching, peersByProvider };
}

// Which playground tab a model opens in, and what the row's button says. Both
// are keyed off modelCategory so the precedence rules live in ONE place: a video
// model also advertises image_generation, and a TRELLIS.2 one advertises
// image_to_image, so testing raw capability flags here (as the panels used to)
// labelled them "Generate"/"Chat" for the wrong tab. null = no interactive UI:
// embedders/rerankers are API-only, SAM segmenters are driven from the Images
// playground's select tool, and 3D has no playground surface yet.
const PLAYGROUND_TABS: Partial<Record<ModelCategory, { tab: string; label: string }>> = {
  llm: { tab: "chat", label: "Chat" },
  image: { tab: "images", label: "Generate" },
  video: { tab: "video", label: "Generate" },
  "3d": { tab: "3d", label: "Generate" },
  tts: { tab: "speech", label: "Speak" },
  transcribe: { tab: "audio", label: "Transcribe" },
};

export function playgroundTarget(m: Model): { tab: string; label: string } | null {
  if (m.capabilities?.reranker) return null;
  return PLAYGROUND_TABS[modelCategory(m)] ?? null;
}
