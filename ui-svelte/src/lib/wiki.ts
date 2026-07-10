import type { ToolDef } from "./types";

// The quartermaster help wiki. ONE source of truth for both the in-app Help
// button (WikiModal renders these articles) and the `wiki_search` tool the
// playground models call to answer "how do I…" questions about the platform.
// Keep bodies short and factual — they're fed verbatim into a model's context.
export interface WikiArticle {
  id: string;
  title: string;
  // Extra search terms beyond the title/body words (synonyms, symptoms).
  keywords: string[];
  // Markdown. Rendered in the Help modal; sent as plain text to the model.
  body: string;
}

export const WIKI_ARTICLES: WikiArticle[] = [
  {
    id: "overview",
    title: "What is Quartermaster",
    keywords: ["about", "intro", "engine", "what", "swap", "local"],
    body: `Quartermaster is an all-in-one **local inference engine** — a front-end over llama.cpp (text/vision models) and stable-diffusion.cpp (images) that runs entirely on your own machine. Nothing is sent to a cloud service; weights, prompts, and conversations stay local.

It discovers the model files you have on disk, works out sensible runtime settings for each one automatically (context length, GPU offload, KV cache), and **hot-swaps** models in and out of VRAM on demand — a request for a model that isn't loaded triggers a load (evicting others if needed).

Two web UIs: the **operator dashboard** (main port — catalog, loading, config, metrics) and the **playground** (its own port — chat, images, speech, transcription, rerank).`,
  },
  {
    id: "loading-models",
    title: "Loading and swapping models",
    keywords: [
      "load",
      "unload",
      "swap",
      "start",
      "stop",
      "evict",
      "vram",
      "group",
    ],
    body: `You don't manually start models most of the time — sending a request to a model (from the playground or an API client) **loads it on demand**. If another model occupies the VRAM it needs, Quartermaster stops that one first (eviction).

- **Models page** (dashboard): browse the catalog, click a model to load/unload it explicitly.
- **Idle unload**: a model with an unload timeout stops itself after being idle, freeing VRAM.
- **Swap groups**: models in the same group can't be resident at once; loading one evicts its group-mates. Models in different groups (and enough VRAM) can coexist.
- First load of a large model is slow (reading weights from disk); subsequent swaps are faster while the file is warm in OS cache.`,
  },
  {
    id: "model-config",
    title: "Per-model configuration (the cogwheel editor)",
    keywords: [
      "config",
      "settings",
      "cogwheel",
      "context",
      "ctx",
      "offload",
      "variant",
      "tune",
      "params",
      "gpu layers",
    ],
    body: `On the Models page, the cogwheel opens the per-model config editor. It shows the exact launch command and lets you tune:

- **Context size (ctx)**: how many tokens the model can hold. Bigger ctx uses more KV-cache VRAM.
- **Target VRAM**: a budget; Quartermaster computes GPU offload (\`-ngl\` / \`--n-cpu-moe\`) to fit. Lower target = more layers on CPU = slower but fits smaller cards.
- **Variants**: alternate profiles of the same model (e.g. different ctx tiers) selectable per request.

**Save & reload** applies changes live — a running model keeps serving under its old settings and the new ones take effect on its next load. The staging card shows what a running model is *actually* loaded with, which can differ from the pending config until it reloads.`,
  },
  {
    id: "config-variants",
    title: "Config variants (normal vs fleet-wide)",
    keywords: [
      "variant",
      "variants",
      "profile",
      "tier",
      "ctx tier",
      "high context",
      "low context",
      "fleet",
      "default",
      "alternate",
      "preset",
    ],
    body: `A **variant** is a named alternate profile of a model — most often a **high- or low-context** version. Each variant surfaces as its own selectable entry (the variant name becomes an id suffix, e.g. \`mymodel-32k\` or \`mymodel-128k\`) and launches with its own settings — context size, VRAM target, KV type, speculative decoding, sampler, and the rest. Pick a variant in the playground's model selector; each loads and swaps independently, like any other model.

Why bother: a bigger context window holds more conversation/documents but costs more VRAM, so a low-context variant fits on a smaller card or leaves room for other models, while a high-context variant is there when you need the extra room.

**Inheritance**: a variant only overrides the fields you set on it; anything left blank inherits the model-wide config. So a high-context variant can set just \`ctx\` and inherit everything else.

Two kinds, both edited in the cogwheel config editor:

- **Normal (per-model) variants** — belong to one model and appear only on it. Use them for that model's context tiers (e.g. 32k / 64k / 128k) or a specially tuned profile of that one model.
- **Fleet-wide (default) variants** — shared by **every** model, saved globally (not on any single model). Use them for a profile you want available everywhere, like a standard low- and high-context pair. They're edited in the same editor but saved separately, so a change applies across the whole fleet at once.

Autogen can also emit **context-tier variants automatically** from a model's ctx tiers, so you often get 32k/64k/128k options without defining them by hand.`,
  },
  {
    id: "autogen",
    title: "Automatic config generation",
    keywords: [
      "autogen",
      "generate",
      "auto",
      "defaults",
      "discover",
      "gguf",
      "estimate",
    ],
    body: `Rather than hand-writing a config per model, Quartermaster **auto-generates** one at startup: it scans your model folders for GGUF/checkpoint files, estimates each model's VRAM footprint (weights + KV + compute buffers), and derives runtime flags (context, GPU offload, KV type) that fit your hardware.

This means adding a model is usually just dropping the file in the watched folder — no manual YAML. You can still override any of it per-model in the cogwheel editor, and those overrides survive regeneration.`,
  },
  {
    id: "playground-chat",
    title: "Chat playground",
    keywords: [
      "chat",
      "playground",
      "conversation",
      "vision",
      "image attach",
      "reasoning",
      "thinking",
      "rewrite",
      "history",
      "compaction",
    ],
    body: `The Chat tab is a full chat client for your local models:

- **Vision**: attach an image (paperclip) to a vision-capable model.
- **Reasoning**: thinking models stream their reasoning into a collapsible box. A **Thinking Budget** (Settings) caps reasoning tokens so a model can't overthink forever.
- **Web search**: optional — the model searches the web via SearXNG (see the web-search article).
- **Wiki**: models can look up this help wiki to answer questions about Quartermaster.
- **Rewrite mode**: ask the model to rewrite selected text and see a side-by-side diff.
- **History**: chats are saved server-side per user; long chats auto-compact (older turns summarized) to stay within context.`,
  },
  {
    id: "web-search",
    title: "Web search (SearXNG)",
    keywords: [
      "search",
      "web",
      "searxng",
      "internet",
      "tool",
      "cors",
      "current",
      "news",
    ],
    body: `Chat can let the model search the web for fresh or niche facts. It goes through a **SearXNG** instance you run.

Setup (playground **Settings → Web Search**):
1. Toggle Web Search on.
2. Enter your SearXNG URL (e.g. \`http://localhost:8888\`) and hit **Test**.
3. SearXNG must have **JSON format enabled** (\`formats: [html, json]\`).

**Requirements** : the model must support **tool calling**.
Knobs:
- **Max/Turn** caps searches per message;
- **Throttle ms** spaces requests so SearXNG's rate limiter doesn't trip;
- **Dedupe** reuses the result for a repeated query.

A bare "Failed to fetch" on Test is almost always CORS or a wrong host/port.`,
  },
  {
    id: "images",
    title: "Image generation",
    keywords: [
      "image",
      "images",
      "sd",
      "stable diffusion",
      "txt2img",
      "img2img",
      "inpaint",
      "hires",
      "kontext",
      "flux",
      "cfg",
      "blurry",
    ],
    body: `The Images tab drives stable-diffusion.cpp models:

- **txt2img** and **img2img** (needs a source image + a denoise strength).
- **Hires fix**: a second upscaled pass for sharper detail (txt2img only).
- **Reference images** (Kontext-class models): condition on a subject/style image while the prompt drives the edit.
- **Inpainting**: paint a mask; white = regenerate, black = keep.
- **Style presets** keep a batch visually consistent.

Tips: **turbo/lightning** checkpoints usually need **CFG = 1.0** — a higher CFG makes them blurry. SDXL/SD models need a **full checkpoint** (they can't be split-loaded from separate UNet + text-encoder files).`,
  },
  {
    id: "speech-audio",
    title: "Speech and transcription",
    keywords: [
      "speech",
      "tts",
      "voice",
      "audio",
      "transcribe",
      "whisper",
      "stt",
      "text to speech",
    ],
    body: `Two playground tabs cover audio:

- **Speech (TTS)**: type text, pick a voice, generate spoken audio from a text-to-speech model.
- **Transcription (STT)**: upload an audio file and get the transcript from a speech-recognition model.

Both use the same on-demand loading as every other model — the first request loads the model.`,
  },
  {
    id: "rerank-embed",
    title: "Rerank and embeddings",
    keywords: [
      "rerank",
      "reranker",
      "embedding",
      "embed",
      "search",
      "relevance",
    ],
    body: `**Rerank** (playground tab): give a query and a list of documents; a reranker model scores each document's relevance so you can order results — useful for RAG pipelines.

**Embeddings**: embedding models are served on the OpenAI-compatible \`/v1/embeddings\` endpoint for use by external apps (vector search, semantic similarity).`,
  },
  {
    id: "api-keys",
    title: "API keys and access",
    keywords: [
      "api",
      "key",
      "auth",
      "token",
      "openai",
      "access",
      "scope",
      "endpoint",
    ],
    body: `Quartermaster serves an **OpenAI-compatible API** (\`/v1/chat/completions\`, \`/v1/embeddings\`, image and audio routes), so existing OpenAI clients work by pointing at your server's URL.

The **API Keys** page (dashboard) creates keys and optionally **scopes** each key to a subset of models. Keys gate inference and model discovery; the dashboard itself stays open. Point any OpenAI SDK at the server base URL and use your key as the API key.`,
  },
  {
    id: "observe",
    title: "Observe: activity, logs, performance, KV cache",
    keywords: [
      "observe",
      "logs",
      "activity",
      "metrics",
      "performance",
      "kv cache",
      "monitor",
      "debug",
      "tokens per second",
    ],
    body: `The **Observe** page has tabbed monitoring:

- **Activity**: per-request history — path, status, token counts, throughput, duration; drill into a captured request/response.
- **Logs**: live proxy + upstream (llama-server) logs.
- **Performance**: CPU, system RAM, and GPU charts over time.
- **Context → KV Cache**: how much of each model's context window is filled, plus prompt-canonicalization stats.

Live metrics stream over SSE, so the page updates in real time while a model generates.`,
  },
  {
    id: "gpu-memory",
    title: "GPU memory / VRAM",
    keywords: [
      "vram",
      "gpu",
      "memory",
      "oom",
      "out of memory",
      "offload",
      "budget",
      "foreign",
    ],
    body: `Quartermaster tracks GPU memory live and breaks it down (System / Weights / Draft / KV / Checkpoints / CUDA / Foreign). The VRAM budget already accounts for memory the system and other apps hold, so its estimates target *free* VRAM.

- **Foreign**: VRAM held by llama-server/sd-server processes Quartermaster didn't spawn (a stray llama.cpp) — it counts these so it won't overcommit.
- If a model won't load or crashes on load, the usual cause is **not enough free VRAM**. Lower that model's **Target VRAM** (more layers to CPU) or its **context size** in the cogwheel editor, or unload other models.`,
  },
  {
    id: "settings",
    title: "Playground settings (tokens, thinking)",
    keywords: [
      "settings",
      "max tokens",
      "thinking budget",
      "reasoning",
      "temperature",
      "length",
    ],
    body: `Playground **Settings** (gear in the side rail):

- **Max Tokens**: cap on a single response's length.
- **Thinking Budget**: max reasoning tokens before a thinking model is forced to answer — stops it overthinking. 0 = unlimited. It's enforced as a clean server-side stop so the model's KV cache stays warm.
- **Web Search**: see the web-search article.
- **Temperature** (per-chat): higher = more varied/creative, lower = more focused.`,
  },
  {
    id: "troubleshooting",
    title: "Troubleshooting common issues",
    keywords: [
      "problem",
      "error",
      "help",
      "not working",
      "slow",
      "crash",
      "wont load",
      "fails",
      "broken",
      "fix",
    ],
    body: `Common issues and fixes:

- **Model won't load / crashes on load** → not enough free VRAM. Lower its Target VRAM or context in the cogwheel editor, or unload other models.
- **Generation is slow** → too many layers on CPU (low VRAM target), or context is very large. Raise Target VRAM if you have headroom.
- **Web search fails ("Failed to fetch")** → SearXNG unreachable, CORS-blocked, or JSON format not enabled. Re-check the URL with Test.
- **Blurry images** → turbo/lightning checkpoints need CFG = 1.0. SDXL needs a full checkpoint file.
- **Model swaps unexpectedly** → another request pulled in a group-mate and evicted yours; give heavy models their own swap group or enough VRAM to coexist.
- **Config change didn't take effect** → running models keep old settings until they reload; the staging card shows what's actually loaded.`,
  },
];

// Sidebar grouping for the Help modal — one place, keyed by article id, so the
// articles themselves stay a flat list (search/tool don't care about groups).
// Order here is the display order; any id not listed falls under "More".
export const WIKI_CATEGORIES: { title: string; ids: string[] }[] = [
  { title: "Getting started", ids: ["overview"] },
  { title: "Models & config", ids: ["loading-models", "model-config", "config-variants", "autogen"] },
  { title: "Playground", ids: ["playground-chat", "web-search", "images", "speech-audio", "rerank-embed", "settings"] },
  { title: "Monitoring & VRAM", ids: ["observe", "gpu-memory"] },
  { title: "API & access", ids: ["api-keys"] },
  { title: "Troubleshooting", ids: ["troubleshooting"] },
];

// Group articles into their display categories, keeping only groups with a
// member in `list` (so it works for both the full list and search results).
export function groupWikiArticles(list: WikiArticle[]): { title: string; items: WikiArticle[] }[] {
  const known = new Set(WIKI_CATEGORIES.flatMap((c) => c.ids));
  const groups = WIKI_CATEGORIES.map((c) => ({
    title: c.title,
    items: c.ids.map((id) => list.find((a) => a.id === id)).filter((a): a is WikiArticle => !!a),
  }));
  const orphans = list.filter((a) => !known.has(a.id));
  if (orphans.length) groups.push({ title: "More", items: orphans });
  return groups.filter((g) => g.items.length > 0);
}

// Advertised to playground models so they can answer "how does X work" about
// quartermaster instead of guessing. Local + free — no network, no rate limit.
export const WIKI_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "wiki_search",
    description:
      "Search the quartermaster help wiki for how the platform works — loading/swapping models, per-model config, the playground (chat/images/speech), web search setup, API keys, GPU/VRAM, and troubleshooting. Use this whenever the user asks how to do something in quartermaster or hits a problem with it, so your answer matches the actual app.",
    parameters: {
      type: "object",
      properties: {
        query: {
          type: "string",
          description:
            "What to look up, e.g. 'why won't my model load' or 'set up web search'",
        },
      },
      required: ["query"],
    },
  },
};

const WIKI_MAX_RESULTS = 3;

// Score articles by term overlap (title > keywords > body) and return the best
// few in full. ponytail: naive substring scan — the wiki is ~15 short articles,
// swap for a real index only if it grows into the hundreds.
export function searchWiki(query: string): WikiArticle[] {
  const terms = query.toLowerCase().match(/[a-z0-9]+/g) ?? [];
  if (terms.length === 0) return [];
  const scored = WIKI_ARTICLES.map((a) => {
    const title = a.title.toLowerCase();
    const keys = a.keywords.join(" ").toLowerCase();
    const body = a.body.toLowerCase();
    let score = 0;
    for (const t of terms) {
      if (title.includes(t)) score += 3;
      if (keys.includes(t)) score += 2;
      if (body.includes(t)) score += 1;
    }
    return { a, score };
  }).filter((s) => s.score > 0);
  scored.sort((x, y) => y.score - x.score);
  return scored.slice(0, WIKI_MAX_RESULTS).map((s) => s.a);
}

// Plain-text tool message fed back to the model. On a miss, list the topics so
// the model can steer the user or retry with a better query. `numbers[i]` is
// the citation number for `results[i]` — resolved by the caller so an article
// repeated across searches in the same turn reuses its earlier number instead
// of minting a duplicate (a wiki "[n]" opens the Help modal to that article).
export function formatWikiResults(
  query: string,
  results: WikiArticle[],
  numbers: number[],
): string {
  if (results.length === 0) {
    const topics = WIKI_ARTICLES.map((a) => `- ${a.title}`).join("\n");
    return `No wiki article matched "${query}". Available topics:\n${topics}`;
  }
  return results
    .map((a, i) => `## [${numbers[i]}] ${a.title}\n${a.body}`)
    .join("\n\n---\n\n");
}
