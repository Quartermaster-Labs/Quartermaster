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
  PerformanceResponse,
} from "../lib/types";
import { connectionState } from "./theme";

const LOG_LENGTH_LIMIT = 1024 * 100; /* 100KB of log data */

// Stores
export const models = writable<Model[]>([]);
export const proxyLogs = writable<string>("");
export const upstreamLogs = writable<string>("");
export const metrics = writable<ActivityLogEntry[]>([]);
export const inFlightRequests = writable<number>(0);
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
      models.set([]);
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
            inFlightRequests.set(stats.total ?? 0);
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
    recordLoad(model);
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
  unlisted?: boolean;
  aliases?: string[];
}

export interface ModelOverride {
  ctx?: number;
  kvK?: string;
  kvV?: string;
  kvInRam?: boolean;
  spec?: string;
  reasoningFmt?: string;
  aliases?: string[];
  unlisted?: boolean;
  skip?: boolean;
  variants?: ModelVariant[];
}

export interface ModelConfig {
  id: string;
  gguf: string;
  cmd: string;
  maxCtx: number;
  isMTP: boolean;
  hasOverride: boolean;
  override: ModelOverride | null;
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
}

export async function estimatePlan(model: string, p: EstimateParams): Promise<PlanEstimate> {
  const q = new URLSearchParams();
  if (p.ctx) q.set("ctx", String(p.ctx));
  if (p.kvK) q.set("kvK", p.kvK);
  if (p.kvV) q.set("kvV", p.kvV);
  if (p.kvInRam) q.set("kvInRam", "true");
  if (p.spec) q.set("spec", p.spec);
  if (p.vram) q.set("vram", String(p.vram));
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
  autoVram: boolean;
  overridden: boolean;
  defaults: { targetVramGB: number; vramOverheadGB: number; maxRamGB: number };
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
