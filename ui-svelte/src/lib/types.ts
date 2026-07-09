export type ConnectionState = "connected" | "connecting" | "disconnected";

export type ModelStatus = "ready" | "starting" | "stopping" | "stopped" | "shutdown" | "unknown";

export interface ModelCapabilities {
  vision?: boolean;
  audio_transcriptions?: boolean;
  audio_speech?: boolean;
  image_generation?: boolean;
  image_to_image?: boolean;
  function_calling?: boolean;
  reranker?: boolean;
  embeddings?: boolean;
}

export interface Model {
  id: string;
  state: ModelStatus;
  name: string;
  description: string;
  unlisted: boolean;
  peerID: string;
  capabilities?: ModelCapabilities;
  // Gguf path shared by a model's variants (ctx tiers, game, judge). Rows with
  // the same family are collapsed into one group. Empty => ungrouped.
  family?: string;
  // Swap group the model belongs to, and the listen addresses (ports) that
  // expose that group's catalog. The Models page sections by these.
  group?: string;
  listeners?: string[];
  // Actual argv the process spawned with, set only while running. Differs from
  // the config command after a live settings edit (new args apply on next load)
  // or a spawn-time offload rewrite — so the UI shows what's REALLY loaded.
  runningCmd?: string;
}

export interface TokenMetrics {
  cache_tokens: number;
  input_tokens: number;
  output_tokens: number;
  prompt_per_second: number;
  tokens_per_second: number;
  prompt_ms: number;
  time_to_first_ms: number;
}

export interface ActivityLogEntry {
  id: number;
  timestamp: string;
  model: string;
  req_path: string;
  resp_content_type: string;
  resp_status_code: number;
  tokens: TokenMetrics;
  duration_ms: number;
  has_capture: boolean;
  metadata?: Record<string, string>;
}

export interface ReqRespCapture {
  id: number;
  req_path: string;
  req_headers: Record<string, string>;
  req_body: string; // base64 encoded bytes
  resp_headers: Record<string, string>;
  resp_body: string; // base64 encoded bytes
}

export interface LogData {
  source: "upstream" | "proxy";
  data: string;
}

export interface InFlightStats {
  total: number;
}

export interface LiveTokens {
  model: string;
  output_tokens: number;
  elapsed_ms: number;
  // Measured time-to-first-token (ms); -1 until the first token lands.
  first_token_ms: number;
}

// BackendMetrics is one running llama-server's live state, scraped from its own
// /metrics + /props endpoints (server/backendmetrics.go). Keyed by model id.
export interface BackendMetrics {
  model: string;
  timestamp: string;
  ok: boolean;
  kv_cache_usage_ratio: number;
  kv_cache_tokens: number;
  requests_processing: number;
  requests_deferred: number;
  prompt_tokens_total: number;
  tokens_predicted_total: number;
  n_decode_total: number;
  prompt_seconds_total: number;
  predicted_seconds_total: number;
  n_ctx: number;
  total_slots: number;
  // Live per-request snapshot (from /slots + /metrics gauges): current prompt
  // size and rolling prompt/gen throughput, surfaced while still streaming.
  prompt_tokens: number;
  prompt_tokens_seconds: number;
  predicted_tokens_seconds: number;
}

export interface NetIOStat {
  name: string;
  bytes_recv: number;
  bytes_sent: number;
}

export interface SysStat {
  timestamp: string;
  cpu_util_per_core: number[];
  mem_total_mb: number;
  mem_used_mb: number;
  mem_free_mb: number;
  swap_total_mb: number;
  swap_used_mb: number;
  load_avg_1: number;
  load_avg_5: number;
  load_avg_15: number;
  net_io: NetIOStat[];
}

export interface GpuStat {
  timestamp: string;
  id: number;
  name: string;
  uuid: string;
  temp_c: number;
  vram_temp_c: number;
  gpu_util_pct: number;
  mem_util_pct: number;
  mem_used_mb: number;
  mem_total_mb: number;
  fan_speed_pct: number;
  power_draw_w: number;
}

export interface ForeignGpuProc {
  pid: number;
  name: string;
  mem_mb: number;
}

export interface PerformanceResponse {
  sys_stats: SysStat[];
  gpu_stats: GpuStat[];
  // Current-snapshot tally of GPU memory held by llama-server/sd-server
  // processes we did not spawn (a stray llama.cpp). Always present.
  foreign?: { mb: number; procs?: ForeignGpuProc[] };
  // Idle system-VRAM floor (MiB) sampled server-side (min used while no model
  // running). 0 = not observed yet. Preferred over the browser-only baseline.
  system_mb?: number;
}

export interface APIEventEnvelope {
  type: "modelStatus" | "logData" | "metrics" | "inflight" | "liveTokens" | "backendMetrics" | "perfsys" | "perfgpu";
  data: string;
}

export interface HistogramData {
  bins: number[];
  min: number;
  max: number;
  binSize: number;
  p99: number;
  p95: number;
  p50: number;
}

export interface VersionInfo {
  build_date: string;
  commit: string;
  version: string;
  // Present only on Windows release builds with update checking enabled.
  update_available?: boolean;
  latest_version?: string;
  release_url?: string;
}

// A managed API key. `models` empty => the key may reach every model (full
// access); otherwise it is scoped to that subset. `key` is the secret.
export interface ApiKey {
  name: string;
  key: string;
  models: string[];
  builtin?: boolean; // auto-managed Playground key; hidden from the key list
}

export type ScreenWidth = "xs" | "sm" | "md" | "lg" | "xl" | "2xl";

export type TextContentPart = {
  type: "text";
  text: string;
};

export type ImageContentPart = {
  type: "image_url";
  image_url: { url: string };
};

export type ContentPart = TextContentPart | ImageContentPart;

export interface ToolCall {
  id: string;
  type: "function";
  function: { name: string; arguments: string };
}

export interface ToolDef {
  type: "function";
  function: { name: string; description: string; parameters: object };
}

export interface ChatMessage {
  role: "user" | "assistant" | "system" | "tool";
  content: string | ContentPart[];
  reasoning_content?: string;
  reasoningTimeMs?: number;
  // Total wall time of the assistant turn (ms), shown in the message footer.
  genTimeMs?: number;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  // Web searches run during this assistant turn, folded into the one bubble and
  // shown as collapsible sections (like reasoning). Display-only; the raw tool
  // plumbing sent to the model is reconstructed separately and not stored here.
  // `at` is the assistant content offset where this search ran, so the UI can
  // render the block inline between the surrounding text (legacy items lack it
  // and fall back to the top of the bubble). `duringReasoning` marks a search the
  // model ran mid-think (field-based reasoning_content); `reasoningAt` is its
  // offset into reasoning_content, so the UI nests it inside the reasoning box
  // instead of dropping it below.
  searches?: { query: string; results: string; at?: number; reasoningAt?: number; duringReasoning?: boolean; sources?: { title: string; url: string }[] }[];
  // Rewrite mode. On a user message: the "how to help" instruction (content is the
  // prose to rewrite). On the assistant reply: the original text it was asked to
  // rewrite, so the bubble can render a side-by-side diff against its output.
  rewriteInstruction?: string;
  rewriteOriginal?: string;
}

export function getTextContent(content: string | ContentPart[]): string {
  if (typeof content === "string") {
    return content;
  }
  const textParts = content.filter((part): part is TextContentPart => part.type === "text");
  return textParts.map((part) => part.text).join("\n");
}

export function getImageUrls(content: string | ContentPart[]): string[] {
  if (typeof content === "string") {
    return [];
  }
  return content
    .filter((part): part is ImageContentPart => part.type === "image_url")
    .map((part) => part.image_url.url);
}

export interface ChatCompletionRequest {
  model: string;
  messages: ChatMessage[];
  stream: boolean;
  temperature?: number;
  max_tokens?: number;
}

export interface ImageGenerationRequest {
  model: string;
  prompt: string;
  n?: number;
  size?: string;
}

export interface ImageGenerationResponse {
  created: number;
  data: Array<{
    url?: string;
    b64_json?: string;
  }>;
}

// SDAPI types (stable-diffusion.cpp)
export type ImageApiMode = "openai" | "sdapi";
// Generation mode in the Images playground. img2img needs a source image.
export type ImageGenMode = "txt2img" | "img2img";

export interface SdApiLora {
  name: string;
  path: string;
}

export interface SdApiLoraRef {
  path: string;
  multiplier: number;
}

export interface SdApiTxt2ImgRequest {
  model?: string;
  prompt: string;
  negative_prompt?: string;
  width?: number;
  height?: number;
  steps?: number;
  cfg_scale?: number;
  seed?: number;
  batch_size?: number;
  sampler_name?: string;
  scheduler?: string;
  lora?: SdApiLoraRef[];
  // Hires fix: generate, then run a second upscaled diffusion pass for sharper
  // detail at higher res. Only txt2img honors these — img2img ignores enable_hr
  // (verified against sd-server f440ad9). Upscalers: Latent, Lanczos, Nearest.
  // denoising_strength here is the second-pass strength.
  enable_hr?: boolean;
  hr_scale?: number;
  hr_upscaler?: string;
  hr_steps?: number;
  denoising_strength?: number;
  // Reference images (base64, no data-URI prefix) for Kontext-class models: the
  // model conditions on them while the prompt drives the edit — subject + style
  // refs for reference-driven / style-transfer generation. The /sdapi route reads
  // this array (→ gen_params.ref_images) on BOTH txt2img and img2img, verified
  // against stable-diffusion.cpp routes_sdapi.cpp. (The singular `ref_image` the
  // webui uses is ignored here — it's an array field named extra_images.)
  extra_images?: string[];
}

// img2img reuses every txt2img field plus a source image (base64, no data-URI
// prefix) and a denoise strength (0 = keep source, 1 = ignore it).
export interface SdApiImg2ImgRequest extends SdApiTxt2ImgRequest {
  init_images: string[];
  denoising_strength?: number;
  // Inpaint mask (base64, no data-URI prefix): white = regenerate, black = keep.
  // inpainting_mask_invert (0/1) flips that polarity. Both on the sdapi img2img
  // route (verified against sd-server f440ad9). Only sent when a mask is painted.
  mask?: string;
  inpainting_mask_invert?: number;
}

export interface SdApiResponse {
  images: string[];
  parameters: Record<string, unknown>;
  info: string;
}

// A saved style: the params that keep a batch of placeholders visually
// consistent. Stored in localStorage (no backend). size is "WxH".
export interface ImageStylePreset {
  name: string;
  suffix: string;
  negativePrompt: string;
  steps: number;
  cfgScale: number;
  sampler: string;
  scheduler: string;
  size: string;
  loras: SdApiLoraRef[];
}

export interface AudioTranscriptionRequest {
  file: File;
  model: string;
}

export interface AudioTranscriptionResponse {
  text: string;
}

export interface SpeechGenerationRequest {
  model: string;
  input: string;
  voice: string;
}
