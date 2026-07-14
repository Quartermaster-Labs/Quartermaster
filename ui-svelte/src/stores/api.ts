import { writable } from "svelte/store";
import { persistentStore } from "./persistent";
import type {
  Model,
  ActivityLogEntry,
  VersionInfo,
  LogData,
  APIEventEnvelope,
  ReqRespCapture,
  InFlightStats,
  LiveTokens,
  BackendMetrics,
  PerformanceResponse,
  ApiKey,
} from "../lib/types";
import { connectionState } from "./theme";

const LOG_LENGTH_LIMIT = 1024 * 100; /* 100KB of log data */

// Stores
export const models = writable<Model[]>([]);
// Last modelStatus snapshot, for detecting load-start edges (clear stale stats).
let prevModelStatus: Model[] = [];
export const proxyLogs = writable<string>("");
export const upstreamLogs = writable<string>("");
export const metrics = writable<ActivityLogEntry[]>([]);
export const inFlightRequests = writable<number>(0);
// Live generation progress for the in-flight streaming request (null when idle).
export const liveTokens = writable<LiveTokens | null>(null);
// Per-running-backend live state scraped from llama-server /metrics + /props,
// keyed by model id (KV-cache fill, slot/queue saturation, throughput totals).
export const backendMetrics = writable<Record<string, BackendMetrics>>({});
export const versionInfo = writable<VersionInfo>({
  build_date: "unknown",
  commit: "unknown",
  version: "unknown",
});

let apiEventSource: EventSource | null = null;

function appendLog(newData: string, store: typeof proxyLogs | typeof upstreamLogs): void {
  store.update((prev) => {
    const updatedLog = prev + newData;
    return updatedLog.length > LOG_LENGTH_LIMIT ? updatedLog.slice(-LOG_LENGTH_LIMIT) : updatedLog;
  });
}

export function enableAPIEvents(enabled: boolean): void {
  if (!enabled) {
    apiEventSource?.close();
    apiEventSource = null;
    metrics.set([]);
    inFlightRequests.set(0);
    liveTokens.set(null);
    backendMetrics.set({});
    return;
  }

  let retryCount = 0;
  const initialDelay = 1000; // 1 second

  const connect = () => {
    apiEventSource?.close();
    apiEventSource = new EventSource("/api/events");

    connectionState.set("connecting");

    apiEventSource.onopen = () => {
      // Clear everything on connect to keep things in sync
      proxyLogs.set("");
      upstreamLogs.set("");
      metrics.set([]);
      inFlightRequests.set(0);
      liveTokens.set(null);
      backendMetrics.set({});
      models.set([]);
      prevModelStatus = [];
      retryCount = 0;
      connectionState.set("connected");
    };

    apiEventSource.onmessage = (e: MessageEvent) => {
      try {
        const message = JSON.parse(e.data) as APIEventEnvelope;
        switch (message.type) {
          case "modelStatus": {
            const newModels = JSON.parse(message.data) as Model[];
            // Sort models by name and id
            newModels.sort((a, b) => {
              return (a.name + a.id).localeCompare(b.name + b.id, undefined, { numeric: true });
            });
            // Clear stale inference readout when a model just started loading:
            // any id newly entering "starting" means a (re)load is underway, so
            // the previous model's live token/KV stats no longer apply.
            const prevStarting = new Set(
              prevModelStatus.filter((m) => m.state === "starting").map((m) => m.id),
            );
            const nowStarting = newModels.filter(
              (m) => m.state === "starting" && !prevStarting.has(m.id),
            );
            if (nowStarting.length > 0) {
              liveTokens.set(null);
              backendMetrics.set({});
              // Tally EVERY real load edge (button, playground, or API-triggered
              // swap) so the quick-load ranking reflects actual usage, not only
              // dashboard-button loads.
              for (const m of nowStarting) recordLoad(m.id);
            }
            prevModelStatus = newModels;
            models.set(newModels);
            break;
          }

          case "logData": {
            const logData = JSON.parse(message.data) as LogData;
            switch (logData.source) {
              case "proxy":
                appendLog(logData.data, proxyLogs);
                break;
              case "upstream":
                appendLog(logData.data, upstreamLogs);
                break;
            }
            break;
          }

          case "metrics": {
            const newMetrics = JSON.parse(message.data) as ActivityLogEntry[];
            metrics.update((prevMetrics) => [...newMetrics, ...prevMetrics]);
            break;
          }
          case "inflight": {
            const stats = JSON.parse(message.data) as InFlightStats;
            const total = stats.total ?? 0;
            inFlightRequests.set(total);
            // No requests in flight => clear any stale live-token readout.
            if (total <= 0) liveTokens.set(null);
            break;
          }
          case "liveTokens": {
            liveTokens.set(JSON.parse(message.data) as LiveTokens);
            break;
          }
          case "backendMetrics": {
            const snapshot = JSON.parse(message.data) as BackendMetrics[];
            backendMetrics.set(Object.fromEntries(snapshot.map((m) => [m.model, m])));
            break;
          }
        }
      } catch (err) {
        console.error(e.data, err);
      }
    };

    apiEventSource.onerror = () => {
      apiEventSource?.close();
      retryCount++;
      const delay = Math.min(initialDelay * Math.pow(2, retryCount - 1), 5000);
      connectionState.set("disconnected");
      setTimeout(connect, delay);
    };
  };

  connect();
}

// Fetch version info when connected
connectionState.subscribe(async (status) => {
  if (status === "connected") {
    try {
      const response = await fetch("/api/version");
      if (!response.ok) {
        throw new Error(`HTTP error! status: ${response.status}`);
      }
      const data: VersionInfo = await response.json();
      versionInfo.set(data);
    } catch (error) {
      console.error(error);
    }
  }
});

export async function listModels(): Promise<Model[]> {
  try {
    const response = await fetch("/api/models/");
    if (!response.ok) {
      throw new Error(`HTTP error! status: ${response.status}`);
    }
    const data = await response.json();
    return data || [];
  } catch (error) {
    console.error("Failed to fetch models:", error);
    return [];
  }
}

export async function unloadAllModels(): Promise<void> {
  try {
    const response = await fetch(`/api/models/unload`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to unload models: ${response.status}`);
    }
  } catch (error) {
    console.error("Failed to unload models:", error);
    throw error;
  }
}

export async function unloadSingleModel(model: string): Promise<void> {
  try {
    const response = await fetch(`/api/models/unload/${model}`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to unload model: ${response.status}`);
    }
  } catch (error) {
    console.error("Failed to unload model", model, error);
    throw error;
  }
}

// Per-model load tally, persisted. Powers "most-loaded first" ordering in the
// dashboard quick-load picker.
export const loadCounts = persistentStore<Record<string, number>>("loadCounts", {});
function recordLoad(model: string): void {
  loadCounts.update((c) => ({ ...c, [model]: (c[model] ?? 0) + 1 }));
}

export async function loadModel(model: string, signal?: AbortSignal): Promise<void> {
  try {
    const response = await fetch(`/upstream/${model}/?_=${Date.now()}`, {
      method: "GET",
      signal,
    });
    if (!response.ok) {
      throw new Error(`Failed to load model: ${response.status}`);
    }
    // Load tallying happens on the modelStatus "starting" edge (captures
    // playground + API-swap loads too), so no manual recordLoad here.
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      return;
    }
    console.error("Failed to load model:", error);
    throw error;
  }
}

// ---- Per-model config editor (cogwheel) ----

export interface ModelVariant {
  name: string;
  ctx?: number;
  vramTargetGB?: number;
  kvK?: string;
  kvV?: string;
  spec?: string;
  reasoningFmt?: string;
  ub?: number;
  dry?: boolean | null;
  ctxCheckpoints?: number | null; // null/undefined => generator default
  unlisted?: boolean;
  preserveThinking?: boolean | null; // null/undefined => on (Qwen3.6 default)
  slotCache?: boolean | null; // null/undefined => inherit model-wide; true/false explicit
  // Engine knobs (variant carries the full launch shape; zero/empty => generator default).
  kvInRam?: boolean;
  cpuOffload?: number;
  flashAttn?: string; // "" (inherit/on) | "on" | "off"
  mmap?: string; // "" (inherit) | "on" | "off"
  mlock?: boolean;
  threads?: number;
  parallel?: number;
  extraArgs?: string;
  // Sampler / speculative sub-knobs (0/empty => inherit model-wide).
  dryMultiplier?: number;
  dryBase?: number;
  dryAllowedLength?: number;
  specDraftNMax?: number;
  specDefault?: boolean;
  specNgramSizeN?: number;
  specNgramSizeM?: number;
  specNgramMinHits?: number;
  // Image (sd-server) knobs; empty/0 => inherit the model-wide override.
  vaePath?: string;
  clipLPath?: string;
  clipGPath?: string;
  t5Path?: string;
  textEncoderPath?: string;
  offloadToCpu?: string;
  teOnCpu?: string;
  vaeTiling?: string;
  diffusionFa?: string;
  defaultSteps?: number;
  defaultCfg?: number;
  defaultSampler?: string;
  defaultWidth?: number;
  defaultHeight?: number;
}

export interface ModelOverride {
  // Backend registry entry id this model launches with ("" => auto-pick the class
  // default). Its kind decides which knobs below apply (llama vs vllm).
  backend?: string;
  vllmGpuUtil?: number; // --gpu-memory-utilization (0/undefined => 0.90)
  vllmTensorParallel?: number; // --tensor-parallel-size (>1 emits the flag)
  ctx?: number;
  kvK?: string;
  kvV?: string;
  kvInRam?: boolean;
  vramTargetGB?: number;
  cpuOffload?: number;
  spec?: string;
  reasoningFmt?: string;
  reasoningBudget?: number; // --reasoning-budget token cap; 0/undefined => no cap
  preserveThinking?: boolean; // keep prior-turn <think> in history (Qwen3.6+); needs reasoning on
  flashAttn?: string; // "" (on) | "on" | "off" | "auto"
  mmap?: string; // "" (auto) | "on" | "off"
  mlock?: boolean;
  threads?: number; // 0 => global default
  parallel?: number; // 0 => 1
  ub?: number; // 0 => auto (physical batch -ub/-b)
  extraArgs?: string; // extra llama-server flags appended verbatim (passthrough)
  unlisted?: boolean;
  skip?: boolean;
  slotCache?: boolean; // opt this model into on-disk slot KV persistence (needs the global toggle on)
  ctxVariants?: number[]; // per-model ctx tiers (e.g. 32768, 65536)
  ctxCheckpoints?: number | null; // model-wide --ctx-checkpoints; null/undefined => auto, 0 disables
  variants?: ModelVariant[];
  // Dry sampler: null/undefined => fleet default (off), true => on, false => off.
  dry?: boolean | null;
  dryMultiplier?: number; // 0/undefined => 0.8
  dryBase?: number; // 0/undefined => 1.75
  dryAllowedLength?: number; // 0/undefined => 3
  // Speculative-decode sub-knobs (emitted per spec backend; 0/false => omit).
  specDraftNMax?: number; // draft-mtp; 0 => 2
  specDefault?: boolean;
  specNgramSizeN?: number;
  specNgramSizeM?: number;
  specNgramMinHits?: number;
  // Image (sd-server) knobs; ignored for llama models. Component paths empty => omit.
  vaePath?: string;
  clipLPath?: string;
  clipGPath?: string;
  t5Path?: string;
  textEncoderPath?: string;
  // Placement tri-states: "" => generator default, "on"/"off" pin it.
  offloadToCpu?: string;
  teOnCpu?: string;
  vaeTiling?: string;
  diffusionFa?: string;
  // Generation defaults (0/empty => sd-server default).
  defaultSteps?: number;
  defaultCfg?: number;
  defaultSampler?: string;
  defaultWidth?: number;
  defaultHeight?: number;
}

export interface ModelConfig {
  id: string;
  gguf: string;
  cmd: string;
  maxCtx: number;
  blockCount: number;
  isMTP: boolean;
  isDflash: boolean; // paired *-dflash-*.gguf sidecar => draft-dflash usable
  isImage: boolean; // diffusion model => image config form
  isAudio: boolean; // Qwen3-TTS talker (tts-server) => audio config form
  hasOverride: boolean;
  override: ModelOverride | null;
  /** Fleet-wide variants (e.g. game), shared by every model; saved globally. */
  defaultVariants?: ModelVariant[];
  /** Backend registry, so the editor can offer a per-model backend picker. */
  backends?: BackendEntry[];
}

export async function getModelConfig(model: string): Promise<ModelConfig> {
  const response = await fetch(`/api/models/${encodeURIComponent(model)}/config`);
  if (!response.ok) {
    throw new Error(`Failed to load model config: ${response.status} ${await response.text()}`);
  }
  return await response.json();
}

export async function putModelOverride(model: string, override: ModelOverride): Promise<void> {
  const response = await fetch(`/api/models/${encodeURIComponent(model)}/override`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(override),
  });
  if (!response.ok) {
    throw new Error(`Failed to save override: ${response.status} ${await response.text()}`);
  }
}

export async function resetModelOverride(model: string): Promise<void> {
  const response = await fetch(`/api/models/${encodeURIComponent(model)}/override`, {
    method: "DELETE",
  });
  if (!response.ok) {
    throw new Error(`Failed to reset override: ${response.status} ${await response.text()}`);
  }
}

export async function putModelVariant(model: string, variant: ModelVariant): Promise<void> {
  const response = await fetch(`/api/models/${encodeURIComponent(model)}/variant`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(variant),
  });
  if (!response.ok) {
    throw new Error(`Failed to save variant: ${response.status} ${await response.text()}`);
  }
}

// Replace the fleet-wide default variants (e.g. game). Shared by every model, so
// this is a global save distinct from a per-model override.
export async function putDefaultVariants(variants: ModelVariant[]): Promise<void> {
  const response = await fetch(`/api/default-variants`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(variants),
  });
  if (!response.ok) {
    throw new Error(`Failed to save default variants: ${response.status} ${await response.text()}`);
  }
}

// --- API key manager (admin-only, local) ---

// List the managed API keys. Secrets are included; the UI hides them behind a
// per-row visibility toggle.
export async function listApiKeys(): Promise<ApiKey[]> {
  const response = await fetch("/api/apikeys");
  if (!response.ok) {
    throw new Error(`Failed to load API keys: ${response.status} ${await response.text()}`);
  }
  return (await response.json()) || [];
}

// Create (new name) or update (existing name keeps its secret) an API key.
// `models` empty => full access. Returns the resulting key incl. its secret.
export async function upsertApiKey(name: string, models: string[]): Promise<ApiKey> {
  const response = await fetch("/api/apikeys", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, models }),
  });
  if (!response.ok) {
    throw new Error(`Failed to save API key: ${response.status} ${await response.text()}`);
  }
  return await response.json();
}

export async function deleteApiKey(name: string): Promise<void> {
  const response = await fetch(`/api/apikeys/${encodeURIComponent(name)}`, { method: "DELETE" });
  if (!response.ok) {
    throw new Error(`Failed to delete API key: ${response.status} ${await response.text()}`);
  }
}

// Live load-plan preview for a candidate tuning (no persistence). Powers the
// editor's VRAM/RAM estimate.
export interface PlanEstimate {
  ctx: number;
  ngl: number;
  nCpuMoe: number;
  estVramGB: number;
  estRamGB: number;
  targetVramGB: number;
  maxRamGB: number;
  kvReserveGB: number;
  checkpointGB: number;
  draftGB: number;
  computeBufGB: number;
  mmprojGB: number;
  overheadGB: number;
  ramExceeded: boolean;
  isMoE: boolean;
}

export interface EstimateParams {
  ctx?: number;
  kvK?: string;
  kvV?: string;
  kvInRam?: boolean;
  spec?: string;
  vram?: number;
  cpuOffload?: number;
  /** null/undefined => llama default (32); 0 disables. */
  ctxCheckpoints?: number | null;
  /** Seed the estimate from the model's loaded command (the running variant)
   * instead of re-sizing the solo profile with defaults. */
  actual?: boolean;
}

// Render the full launch command for a candidate override (no persistence).
// Powers the editor's two-way launch-parameters box: form edits call this to
// refresh the command text (computed -ngl/-c/--n-cpu-moe included).
export async function previewCmd(model: string, override: ModelOverride): Promise<string> {
  const response = await fetch(`/api/models/${encodeURIComponent(model)}/preview`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(override),
  });
  if (!response.ok) {
    throw new Error(`Failed to preview command: ${response.status} ${await response.text()}`);
  }
  return (await response.json()).cmd as string;
}

export async function estimatePlan(model: string, p: EstimateParams): Promise<PlanEstimate> {
  const q = new URLSearchParams();
  if (p.ctx) q.set("ctx", String(p.ctx));
  if (p.kvK) q.set("kvK", p.kvK);
  if (p.kvV) q.set("kvV", p.kvV);
  if (p.kvInRam) q.set("kvInRam", "true");
  if (p.spec) q.set("spec", p.spec);
  if (p.vram) q.set("vram", String(p.vram));
  if (p.cpuOffload) q.set("cpuOffload", String(p.cpuOffload));
  if (p.ctxCheckpoints != null) q.set("ctxCheckpoints", String(p.ctxCheckpoints));
  if (p.actual) q.set("actual", "true");
  const response = await fetch(`/api/models/${encodeURIComponent(model)}/estimate?${q.toString()}`);
  if (!response.ok) {
    throw new Error(`Failed to estimate plan: ${response.status} ${await response.text()}`);
  }
  return await response.json();
}

// ---- Global settings (dashboard GPU-memory card) ----

export interface AppSettings {
  targetVramGB: number;
  vramOverheadGB: number;
  maxRamGB: number;
  ttlSec: number; // idle-eviction timeout baked into every model's ttl (0 = never)
  autoVram: boolean;
  overridden: boolean;
  defaults: { targetVramGB: number; vramOverheadGB: number; maxRamGB: number; ttlSec: number };
  modelsRoot: string;
  categoryRoots: Record<string, string> | null;
  slotCache: SlotCacheSettings;
  backends: BackendExes;
  backendList: BackendEntry[];
}

// Backend executable paths (llama-server / sd-server / tts-server). Blank => the
// generate-file value / sibling default. Set a Vulkan/ROCm build here on AMD/Intel.
export interface BackendExes {
  serverExe: string;
  sdServerExe: string;
  ttsServerExe: string;
}

// One row of the backend registry. kind ∈ llama | sd | tts | vllm | custom.
// Only llama/sd/tts currently feed model loading; extras persist for later wiring.
export interface BackendEntry {
  id: string;
  kind: string;
  name: string;
  path: string;
  default: boolean; // the auto-pick for this backend's model class
}

// On-disk slot KV persistence knobs (dashboard slot-KV section). Zero values
// fall back to the server defaults (30k tokens / 10 GB / 20 sessions).
export interface SlotCacheSettings {
  enable: boolean;
  path: string;
  minSaveTokens: number;
  maxDiskGB: number;
  maxSessions: number;
}

export async function getSettings(): Promise<AppSettings> {
  const response = await fetch("/api/settings");
  if (!response.ok) {
    throw new Error(`Failed to load settings: ${response.status} ${await response.text()}`);
  }
  return await response.json();
}

export async function putSettings(p: {
  targetVramGB: number;
  vramOverheadGB: number;
  maxRamGB: number;
  ttlSec: number;
}): Promise<void> {
  const response = await fetch("/api/settings", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(p),
  });
  if (!response.ok) {
    throw new Error(`Failed to save settings: ${response.status} ${await response.text()}`);
  }
}

export async function putBackends(list: BackendEntry[]): Promise<void> {
  const response = await fetch("/api/settings/backends", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(list),
  });
  if (!response.ok) {
    throw new Error(`Failed to save backends: ${response.status} ${await response.text()}`);
  }
}

export async function putSlotCache(p: SlotCacheSettings): Promise<void> {
  const response = await fetch("/api/settings/slotcache", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(p),
  });
  if (!response.ok) {
    throw new Error(`Failed to save slot-cache settings: ${response.status} ${await response.text()}`);
  }
}

// Opens the host's native folder dialog and sets the scan folder for one
// category. Returns the chosen path, or null when the user cancelled (204).
export async function pickModelsFolder(category: string): Promise<string | null> {
  const response = await fetch("/api/settings/root/pick", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ category }),
  });
  if (response.status === 204) return null;
  if (!response.ok) {
    throw new Error(`Failed to set models folder: ${response.status} ${await response.text()}`);
  }
  const body = (await response.json()) as { path: string };
  return body.path;
}

// Opens the host's native folder dialog and returns the chosen path (or null
// when cancelled). Unlike pickModelsFolder it does not persist — the caller
// binds the path into a form field.
export async function pickFolder(): Promise<string | null> {
  const response = await fetch("/api/pick-folder", { method: "POST" });
  if (response.status === 204) return null;
  if (!response.ok) {
    throw new Error(`Folder picker failed: ${response.status} ${await response.text()}`);
  }
  const body = (await response.json()) as { path: string };
  return body.path;
}

// Opens the host's native open-file dialog to pick a backend executable.
// Returns the path, or null when cancelled (204) or unsupported (501) — the
// caller then leaves the field as-is for manual typing.
export async function pickBackend(): Promise<string | null> {
  const response = await fetch("/api/settings/backend/pick", { method: "POST" });
  if (response.status === 204 || response.status === 501) return null;
  if (!response.ok) {
    throw new Error(`File picker failed: ${response.status} ${await response.text()}`);
  }
  const body = (await response.json()) as { path: string };
  return body.path;
}

export async function resetSettings(): Promise<void> {
  const response = await fetch("/api/settings", { method: "DELETE" });
  if (!response.ok) {
    throw new Error(`Failed to reset settings: ${response.status} ${await response.text()}`);
  }
}

export async function getCapture(id: number): Promise<ReqRespCapture | null> {
  try {
    const response = await fetch(`/api/captures/${id}`);
    if (response.status === 404) {
      return null;
    }
    if (!response.ok) {
      throw new Error(`Failed to fetch capture: ${response.status}`);
    }
    return await response.json();
  } catch (error) {
    console.error("Failed to fetch capture:", error);
    return null;
  }
}

// --- Slot KV-cache monitoring (Observe → KV Cache tab) ---

export interface KvCacheCounters {
  saves: number;
  restoreHits: number;
  restoreSeeds: number;
  misses: number;
  errors: number;
  confirmedReuses: number;
  confirmedMisses: number;
  cachedTokensSeen: number;
  preambleMints: number;
  preambleHits: number;
}

export interface KvCacheEvent {
  time: string;
  model: string;
  op: string; // save | restore-hit | restore-seed | seed-pending | miss | error
  key: string;
  detail?: string;
  bytes?: number;
  tokens?: number;
}

export interface KvCacheFile {
  model: string;
  key: string;
  bytes: number;
  modAt: string;
  preamble?: string;
}

export interface KvCacheStats {
  enabled: boolean;
  dir?: string;
  maxBytes?: number;
  maxFiles?: number;
  diskBytes?: number;
  counters?: KvCacheCounters;
  files?: KvCacheFile[];
  preambleFiles?: KvCacheFile[];
  events?: KvCacheEvent[];
}

export async function fetchKvCache(): Promise<KvCacheStats | null> {
  try {
    const response = await fetch("/api/kvcache");
    if (!response.ok) throw new Error(`HTTP error! status: ${response.status}`);
    return await response.json();
  } catch (error) {
    console.error("Failed to fetch KV cache stats:", error);
    return null;
  }
}

// --- Prompt canonicalization monitoring (Context Management → Canonicalization) ---

export interface CanonCounters {
  seen: number;
  rewritten: number;
  bytesRemoved: number;
}

export interface CanonEvent {
  time: string;
  model: string;
  rule: string;
  bytes: number;
}

export interface CanonStats {
  counters?: CanonCounters;
  events?: CanonEvent[];
}

export async function fetchCanon(): Promise<CanonStats | null> {
  try {
    const response = await fetch("/api/canon");
    if (!response.ok) throw new Error(`HTTP error! status: ${response.status}`);
    return await response.json();
  } catch (error) {
    console.error("Failed to fetch canonicalization stats:", error);
    return null;
  }
}

export async function fetchPerformance(after?: string): Promise<PerformanceResponse | null> {
  try {
    const url = after ? `/api/performance?after=${encodeURIComponent(after)}` : "/api/performance";
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`HTTP error! status: ${response.status}`);
    }
    return await response.json();
  } catch (error) {
    console.error("Failed to fetch performance data:", error);
    return null;
  }
}
