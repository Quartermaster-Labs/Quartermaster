// Landing-page copy and data. Split from build.mjs so editing a headline or
// adding a feature card never means reading render code.
//
// The docs half of the site is NOT here: those pages come from
// internal/server/wiki_articles.json, the same corpus the app's Help modal and
// the `wiki_search` tool read. Anything a user might also need while the app is
// open belongs in an article, not on this page.

export const REPO = "https://github.com/Quartermaster-Labs/quartermaster";
export const UPSTREAM = "https://github.com/mostlygeek/llama-swap";

export const HERO = {
  eyebrow: "Local inference, all in one binary",
  // `accent` is rendered as gradient text inside the <h1>.
  title: ["Run", "any model", "on your own machine."],
  lede:
    "quartermaster is a local inference engine for text, image and audio models — point it at " +
    "your models folder and it works out a near-optimal setup per model, then hot-swaps between " +
    "them on demand behind one OpenAI- and Anthropic-compatible API.",
};

// Lucide (ISC) path data, inlined so the page makes no third-party request.
// Same icon set the app itself uses, so a feature card and its screen match.
export const ICONS = {
  wand: '<path d="m15 4 1 2 2 1-2 1-1 2-1-2-2-1 2-1z"/><path d="M9 11 3 17l4 4 6-6"/><path d="m14 7 3 3"/><path d="M5 6h.01M19 13h.01"/>',
  gauge: '<path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>',
  refresh: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>',
  save: '<path d="M15.2 3a2 2 0 0 1 1.4.6l3.8 3.8a2 2 0 0 1 .6 1.4V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"/><path d="M17 21v-7a1 1 0 0 0-1-1H8a1 1 0 0 0-1 1v7"/><path d="M7 3v4a1 1 0 0 0 1 1h7"/>',
  network: '<rect x="9" y="2" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="16" y="16" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3"/><path d="M12 12V8"/>',
  layers: '<path d="m12.8 2.5 8.7 4.4a1 1 0 0 1 0 1.8l-8.7 4.3a2 2 0 0 1-1.6 0L2.5 8.7a1 1 0 0 1 0-1.8l8.7-4.4a2 2 0 0 1 1.6 0Z"/><path d="m2.5 12.6 8.7 4.4a2 2 0 0 0 1.6 0l8.7-4.4"/><path d="m2.5 17.1 8.7 4.4a2 2 0 0 0 1.6 0l8.7-4.4"/>',
  play: '<circle cx="12" cy="12" r="10"/><path d="m10 8 6 4-6 4z"/>',
  shield: '<path d="M20 13c0 5-3.5 7.5-7.7 9a1 1 0 0 1-.6 0C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.2-2.7a1 1 0 0 1 1.5 0C14.5 3.8 17 5 19 5a1 1 0 0 1 1 1z"/>',
  windows: '<path d="M3 5.5 10 4.5v7H3z"/><path d="M11.5 4.3 21 3v8.5h-9.5z"/><path d="M3 12.5h7v7L3 18.5z"/><path d="M11.5 12.5H21V21l-9.5-1.3z"/>',
  container: '<path d="M22 7.7c0-.6-.4-1.2-.8-1.5l-6.3-3.9a1.7 1.7 0 0 0-1.7 0l-6.3 3.9C6.4 6.5 6 7.1 6 7.7v8.6c0 .6.4 1.2.8 1.5l6.3 3.9a1.7 1.7 0 0 0 1.7 0l6.3-3.9c.4-.3.9-.9.9-1.5z"/><path d="M10 21.9V14L2.2 9.5"/><path d="m10 14 11.7-6.8"/>',
  terminal: '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>',
  book: '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/>',
  box: '<path d="M21 8a2 2 0 0 0-1-1.7l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.7l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
};

// `neu` marks the things this fork added on top of llama-swap. The badge is the
// honest version of "features" on a fork's landing page: it says what you get
// here that you would not get upstream, without pretending the rest is ours.
export const FEATURES = [
  {
    icon: "wand",
    title: "Config that writes itself",
    neu: true,
    body:
      "Point it at a folder. Every GGUF is identified from its own header, and context length, " +
      "GPU offload, CPU-MoE split and KV-cache sizing are computed per model and per architecture " +
      "— no hand-baked config variant per quant.",
  },
  {
    icon: "gauge",
    title: "VRAM-aware load planning",
    neu: true,
    body:
      "Samples free VRAM at startup and sizes each model to actually fit, including the compute " +
      "buffer large-vocab models spill on. Live per-model VRAM and RAM gauges while it runs.",
  },
  {
    icon: "refresh",
    title: "On-demand model swapping",
    body:
      "One endpoint, every model. A request naming a model that isn't loaded swaps it in, evicting " +
      "whatever no longer fits — inherited from llama-swap and kept at the core.",
  },
  {
    icon: "save",
    title: "KV-cache that survives eviction",
    neu: true,
    body:
      "Snapshots a slot's KV-cache to disk before the model is evicted and restores it when the " +
      "conversation comes back, so a long chat isn't re-prefilled because a throwaway request " +
      "borrowed the GPU.",
  },
  {
    icon: "network",
    title: "Multi-port catalogs",
    neu: true,
    body:
      "Bind several listeners on one shared scheduler, each with its own /v1/models view. Loading " +
      "on one port can evict on another — one process, one GPU accounting.",
  },
  {
    icon: "layers",
    title: "Text, image, audio, and more",
    body:
      "Orchestrates llama-server, stable-diffusion.cpp, TTS and transcription servers, rerank and " +
      "embedding models, upscaling and segmentation — behind one OpenAI-compatible surface.",
  },
  {
    icon: "play",
    title: "A playground, not just a proxy",
    neu: true,
    body:
      "Chat with tool calling and web search, generate and edit images, speak and transcribe — on " +
      "its own port with per-user login and server-side history.",
  },
  {
    icon: "shield",
    title: "Safe to put on your LAN",
    neu: true,
    body:
      "API keys can be scoped to individual models. Bind the API to your tailnet and the dashboard " +
      "and config endpoints answer to localhost only unless you widen them yourself.",
  },
];

// Screenshots shown in the tabbed gallery. `file` is a name under docs/assets/;
// an entry whose file is missing is skipped with a warning at build time rather
// than shipping a broken <img>, so the gallery grows as shots are captured.
// Capture with `npm run shots -- --demo`, adopt with `npm run site -- --adopt`.
export const GALLERY = [
  { file: "dashboard.png", label: "Dashboard", caption: "Loaded models, live VRAM and throughput at a glance." },
  { file: "models.png", label: "Models", caption: "Every discovered model, grouped by GGUF with its variants." },
  { file: "model-config.png", label: "Per-model config", caption: "Edit context, KV and speculative decoding — or reset to the computed default." },
  { file: "observe.png", label: "Observe", caption: "Activity, logs and performance on one page." },
  { file: "browse.png", label: "Model hub", caption: "Search Hugging Face, pick a quant, download straight into your models folder." },
  { file: "images.png", label: "Image models", caption: "Diffusion backends sit in the same catalog as the text ones." },
];

export const INSTALL = [
  {
    icon: "windows",
    title: "Windows installer",
    body:
      "A per-user install — no admin rights. The first-run wizard fetches the inference backends, " +
      "asks for your models folder, and generates a config before the window opens.",
    // Filled in by build.mjs from the latest GitHub release.
    download: true,
  },
  {
    icon: "container",
    title: "Docker",
    body:
      "The unified image bundles the backends. Tags are published per compute backend — swap " +
      "unified-cuda for unified-vulkan on AMD or Intel.",
    code:
      "docker run --gpus all -p 1250:1250 -v ./models:/models \\\n" +
      "  ghcr.io/quartermaster-labs/quartermaster:unified-cuda",
  },
  {
    icon: "terminal",
    title: "From source",
    body:
      "Go 1.24+ and Node for the UI. Builds a single binary into build/; bring your own backend " +
      "binaries and let it generate a config on first run.",
    code:
      "git clone https://github.com/Quartermaster-Labs/quartermaster\n" +
      "cd quartermaster\n" +
      "make windows        # or linux / mac",
  },
];
