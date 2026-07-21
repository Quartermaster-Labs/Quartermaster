// Backend registry taxonomy — the single UI-side source of truth for what a
// backend *kind* is and which model class it serves. Mirrors autogen's
// `kindClass` (internal/autogen/vllm.go): a class is the model family
// ("llm"/"image"/…), a kind is the concrete engine that serves it
// ("llama"/"vllm"/…). Several engines can share one class — llama.cpp and vLLM
// are both LLM backends, so the ★ default is one-per-class, not one-per-engine.

export interface BackendEngine {
  kind: string; // stored value (autogen kind)
  label: string; // human name
  hint?: string;
}

export interface BackendClassDef {
  id: string; // autogen class id
  label: string;
  blurb: string;
  engines: BackendEngine[];
}

export const BACKEND_CLASSES: BackendClassDef[] = [
  {
    id: "llm",
    label: "Language models",
    blurb: "Text / vision / embedding GGUFs. One backend serves them all.",
    engines: [
      { kind: "llama", label: "llama.cpp", hint: "llama-server — the default engine; all sizing knobs apply." },
      { kind: "vllm", label: "vLLM", hint: "vllm serve — its own arg set; llama.cpp KV/offload knobs are ignored." },
    ],
  },
  {
    id: "image",
    label: "Image generation",
    blurb: "Diffusion models (SD / SDXL / Flux / Qwen-Image).",
    engines: [{ kind: "sd", label: "sd-server", hint: "stable-diffusion.cpp server." }],
  },
  {
    id: "tts",
    label: "Speech",
    blurb: "Text-to-speech models.",
    engines: [{ kind: "tts", label: "tts-server", hint: "qwentts.cpp / tts-server." }],
  },
  {
    id: "asr",
    label: "Transcription",
    blurb: "Speech-to-text models (Parakeet / FastConformer).",
    engines: [{ kind: "asr", label: "parakeet-server", hint: "parakeet.cpp — runs faster than realtime on CPU alone." }],
  },
  {
    id: "segment",
    label: "Segmentation",
    blurb: "Mask / segment-anything models.",
    engines: [{ kind: "sam", label: "sam3-server", hint: "SAM3 wrapper server." }],
  },
  {
    id: "upscale",
    label: "Upscaling",
    blurb: "Run per request, never loaded into a swap group.",
    engines: [{ kind: "upscale", label: "realesrgan-ncnn", hint: "realesrgan-ncnn-vulkan, exec-per-request." }],
  },
  {
    id: "custom",
    label: "Other",
    blurb: "Registered but not wired to a model class.",
    engines: [{ kind: "custom", label: "custom" }],
  },
];

// kind -> class. Accepts autogen's aliases so a hand-edited sidecar row still
// lands in the right group. Unknown kinds fall back to "custom" so nothing is
// hidden from the UI (autogen returns "" for those — it just won't auto-pick).
export function backendClass(kind: string): string {
  switch ((kind ?? "").trim().toLowerCase()) {
    case "llama":
    case "llama.cpp":
    case "server":
    case "vllm":
      return "llm";
    case "sd":
    case "sd-server":
    case "image":
      return "image";
    case "tts":
    case "tts-server":
    case "speech":
      return "tts";
    case "asr":
    case "parakeet":
    case "parakeet-server":
    case "transcribe":
      return "asr";
    case "sam":
    case "sam3":
    case "segment":
      return "segment";
    case "upscale":
    case "realesrgan":
    case "esrgan":
      return "upscale";
  }
  return "custom";
}

export function backendClassDef(kind: string): BackendClassDef | undefined {
  const cls = backendClass(kind);
  return BACKEND_CLASSES.find((c) => c.id === cls);
}

// Display name for a kind, e.g. "llama" => "llama.cpp".
export function engineLabel(kind: string): string {
  const def = backendClassDef(kind);
  return def?.engines.find((e) => e.kind === kind)?.label ?? kind;
}
