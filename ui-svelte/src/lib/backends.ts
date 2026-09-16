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
      { kind: "llama", label: "llama.cpp", hint: "llama-server - the default engine; all sizing knobs apply." },
      { kind: "vllm", label: "vLLM", hint: "vllm serve - its own arg set; llama.cpp KV/offload knobs are ignored." },
    ],
  },
  {
    id: "image",
    label: "Image generation",
    blurb: "Diffusion models (SD / SDXL / Flux / Qwen-Image).",
    engines: [{ kind: "sd", label: "sd-server", hint: "stable-diffusion.cpp server." }],
  },
  {
    id: "3d",
    label: "3D generation",
    blurb: "Image-to-3D mesh models (TRELLIS.2).",
    engines: [
      { kind: "trellis2", label: "trellis2-server", hint: "TRELLIS.2 - one image in, a textured GLB out." },
    ],
  },
  {
    id: "tts",
    label: "Speech",
    blurb: "Text-to-speech models.",
    engines: [
      { kind: "tts", label: "tts-server", hint: "qwentts.cpp - Qwen3-TTS talker + paired codec gguf." },
      { kind: "ttscpp", label: "TTS.cpp", hint: "mmwillet/TTS.cpp - Kokoro / Parler / Orpheus ggufs, self-contained, CPU only." },
    ],
  },
  {
    id: "asr",
    label: "Transcription",
    blurb: "Speech-to-text models (Parakeet / FastConformer).",
    engines: [{ kind: "asr", label: "parakeet-server", hint: "parakeet.cpp - runs faster than realtime on CPU alone." }],
  },
  {
    id: "audio",
    label: "Audio",
    blurb: "One engine across speech, transcription (and music later).",
    engines: [
      {
        kind: "audiocpp",
        label: "audio.cpp",
        hint: "audiocpp_server - 72 model families behind one binary; serves TTS and ASR from the same install.",
      },
    ],
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

// kind -> the MODEL classes it can serve. Mirrors autogen's `kindClasses`
// (internal/autogen/vllm.go) and accepts the same aliases, so a hand-edited
// sidecar row still lands in the right group. Nearly every engine serves one
// class; audio.cpp serves two from one install, which is why this returns a
// list. Unknown kinds return nothing (autogen returns no class for those — they
// just never auto-pick).
export function backendClasses(kind: string): string[] {
  switch ((kind ?? "").trim().toLowerCase()) {
    case "llama":
    case "llama.cpp":
    case "server":
    case "vllm":
      return ["llm"];
    case "sd":
    case "sd-server":
    case "image":
      return ["image"];
    case "tts":
    case "tts-server":
    case "speech":
    case "ttscpp":
    case "tts.cpp":
    case "kokoro":
      return ["tts"];
    case "asr":
    case "parakeet":
    case "parakeet-server":
    case "transcribe":
      return ["asr"];
    case "audiocpp":
    case "audio.cpp":
    case "audiocpp-server":
      return ["tts", "asr"];
    case "sam":
    case "sam3":
    case "segment":
      return ["segment"];
    case "trellis2":
    case "trellis":
    case "3d":
      return ["3d"];
    case "upscale":
    case "realesrgan":
    case "esrgan":
      return ["upscale"];
  }
  return [];
}

// Can a backend of this kind run a model of that class? The test to use
// anywhere a model is matched against the registry — an equality check against
// backendClass() silently hides a multi-class engine from every class but the
// one it is grouped under, which is how audio.cpp went missing from the TTS and
// ASR backend pickers.
export function backendServesClass(kind: string, cls: string): boolean {
  return backendClasses(kind).includes(cls);
}

// kind -> the group it is FILED under in the UI: a single bucket per row, since
// a row can only be drawn once. That is the served class for a single-class
// engine, and a group of its own for a multi-class one — audio.cpp belongs in
// neither Speech nor Transcription because it is both, and filing it under
// either would hide it from anyone looking for the other. Unknown kinds fall
// back to "custom" so nothing vanishes from the UI.
/** True for the audio.cpp engine under any of its kind spellings. Mirrors Go's
 *  isAudioCppKind; used to split a class into the engines that can actually read
 *  a given model's weights. */
export function isAudioCppBackend(kind: string): boolean {
  switch ((kind ?? "").trim().toLowerCase()) {
    case "audiocpp":
    case "audio.cpp":
    case "audiocpp-server":
      return true;
  }
  return false;
}

export function backendClass(kind: string): string {
  switch ((kind ?? "").trim().toLowerCase()) {
    case "audiocpp":
    case "audio.cpp":
    case "audiocpp-server":
      return "audio";
  }
  return backendClasses(kind)[0] ?? "custom";
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

// --- Picker labels ---------------------------------------------------------
// A registry row carries more than one backend of the same engine: the
// manager keeps a derived row per installed build (vulkan / rocm / cuda, one per
// version), and the model editor has to tell them apart. BackendEntry
// (stores/api) is structurally assignable to this, so these stay testable
// without touching the store.
export interface BackendPickEntry {
  id: string;
  kind: string;
  name: string;
  path: string;
  default?: boolean;
  managed?: boolean;
  build?: boolean;
  component?: string;
  version?: string;
  variant?: string;
}

// The closed trigger renders the label alone, so the half that decides whether a
// build works at all (the variant) belongs in it. The version goes on the detail
// line, where two builds of one variant stay distinguishable.
export function backendOptionLabel(e: BackendPickEntry): string {
  const name = (e.name ?? "").trim() || engineLabel(e.kind);
  const variant = (e.variant ?? "").trim();
  return `${name}${variant ? ` (${variant})` : ""}${e.default ? " ★" : ""}`;
}

// Second line of an option: the version, because two builds of one variant are
// exactly the case the picker exists for.
export function backendOptionDetail(e: BackendPickEntry): string {
  const version = (e.version ?? "").trim();
  const engine = engineLabel(e.kind);
  return version ? `${version} · ${engine}` : engine;
}

export interface BackendPickOption {
  value: string;
  label: string;
  detail: string;
}

// Pickable options for one class's rows. Labels have to stay unique on their
// own, because the closed trigger shows the label and nothing else: with two
// versions of one variant installed, both would read identically there. Only the
// colliding rows grow a version suffix (usually none do).
export function backendPickOptions(rows: BackendPickEntry[]): BackendPickOption[] {
  const labels = rows.map(backendOptionLabel);
  const seen = new Map<string, number>();
  for (const l of labels) seen.set(l, (seen.get(l) ?? 0) + 1);
  return rows.map((r, i) => {
    const version = (r.version ?? "").trim();
    const label = seen.get(labels[i])! > 1 && version ? `${labels[i]} · ${version}` : labels[i];
    return { value: r.id, label, detail: backendOptionDetail(r) };
  });
}

// Drop the derived row of a build some other row already names: the manager's
// own row while that build is the active one, or a hand-entered row pointing at
// the same exe. Without this, activating a build makes it appear twice with the
// same label, once starred.
//
// keepId is the row the current model pins. It is never dropped, so a pin that
// later becomes a duplicate still shows its own value instead of rendering an
// empty select.
export function hideDuplicateBuildRows<T extends BackendPickEntry>(rows: T[], keepId = ""): T[] {
  const taken = new Set(
    rows
      .filter((r) => !r.build)
      .map((r) => normExePath(r.path))
      .filter(Boolean),
  );
  return rows.filter((r) => !r.build || r.id === keepId || !taken.has(normExePath(r.path)));
}

// Same file, same row, whichever slash the writer used (paths travel through
// YAML written on Windows and read on Linux).
function normExePath(path: string): string {
  return (path ?? "").trim().replace(/\\/g, "/").toLowerCase();
}
