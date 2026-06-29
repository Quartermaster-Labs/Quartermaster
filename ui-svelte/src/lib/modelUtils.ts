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

export type ModelCategory = "llm" | "image" | "tts" | "transcribe" | "embed";

// Sub-menu order under the Models tab. LLM is the catch-all default.
export const MODEL_CATEGORIES: { id: ModelCategory; label: string }[] = [
  { id: "llm", label: "LLM" },
  { id: "image", label: "Image" },
  { id: "tts", label: "TTS" },
  { id: "transcribe", label: "Transcribe" },
  { id: "embed", label: "Embed" },
];

// Bucket a model by its declared capabilities. Anything that isn't clearly an
// image/audio/embedding model falls through to "llm".
export function modelCategory(m: Model): ModelCategory {
  const c = m.capabilities;
  if (c?.image_generation || c?.image_to_image) return "image";
  if (c?.audio_speech) return "tts";
  if (c?.audio_transcriptions) return "transcribe";
  if (c?.embeddings) return "embed";
  return "llm";
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
