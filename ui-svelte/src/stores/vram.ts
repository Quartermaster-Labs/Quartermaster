import { derived, writable } from "svelte/store";
import { latestGpu } from "./perf";
import { models, estimatePlan, type PlanEstimate } from "./api";

// VRAM split: "system" (OS + other apps + game) vs the loaded llama-server. We
// can't query per-process VRAM portably, so we sample an idle baseline: whenever
// no model is loaded, the live used VRAM IS the system floor. Once a model loads,
// model usage ≈ live used − baseline. Before the first idle sample the baseline
// is unknown, so everything is attributed to system (safe under-report).
//
// When a single model is loaded we further break its slice into model weights /
// KV cache / CUDA-runtime overhead using the load-plan estimate (the only source
// we have for the component split — the driver only reports a single total).
let baselineMb: number | null = null;

export interface VramSegment {
  label: string;
  mb: number;
  /** Tailwind bg-* class for the bar segment. */
  class: string;
  /** Hover detail: what occupies this slice. */
  detail: string;
}

export interface VramBreakdown {
  usedMb: number;
  totalMb: number;
  segments: VramSegment[];
}

// estimateSegments splits a load-plan estimate into the canonical VRAM
// components (weights / KV / checkpoints) using the SAME labels + colors as the
// live status-rail breakdown, so the config-editor preview and the rail read as
// one consistent widget. estVramGB folds the checkpoint reserve into overhead,
// so subtract both KV and checkpoints to leave the true weights share. When KV
// lives in RAM it (and its checkpoints) cost no VRAM.
export function estimateSegments(est: PlanEstimate, kvInRam = false): VramSegment[] {
  const kvMb = Math.max(0, (kvInRam ? 0 : est.kvReserveGB) * 1024);
  const ckptMb = Math.max(0, (kvInRam ? 0 : est.checkpointGB ?? 0) * 1024);
  const weightsMb = Math.max(0, est.estVramGB * 1024 - kvMb - ckptMb);
  const segs: VramSegment[] = [];
  if (weightsMb > 0)
    segs.push({ label: "Weights", mb: weightsMb, class: "bg-primary", detail: "model weights + compute on GPU" });
  if (kvMb > 0)
    segs.push({ label: "KV cache", mb: kvMb, class: "bg-warning", detail: `attention cache (ctx ${est.ctx})` });
  if (ckptMb > 0)
    segs.push({ label: "Checkpoints", mb: ckptMb, class: "bg-error", detail: "context-checkpoint KV snapshots" });
  return segs;
}

// Plan estimate for the currently loaded model, refreshed when the active model
// changes. Drives the weights/KV/overhead component split.
const activeEstimate = writable<{ id: string; est: PlanEstimate } | null>(null);
let estFetchId: string | null = null;

models.subscribe(($models) => {
  const ready = $models.filter((m) => m.state === "ready");
  if (ready.length === 1) {
    const id = ready[0].id;
    if (estFetchId !== id) {
      estFetchId = id;
      estimatePlan(id, { actual: true })
        .then((est) => activeEstimate.set({ id, est }))
        .catch(() => activeEstimate.set(null));
    }
  } else {
    estFetchId = null;
    activeEstimate.set(null);
  }
});

export const vramBreakdown = derived(
  [latestGpu, models, activeEstimate],
  ([$gpu, $models, $est]): VramBreakdown | null => {
    if (!$gpu) return null;

    const live = $models.filter(
      (m) => m.state === "ready" || m.state === "starting" || m.state === "stopping",
    );
    const used = $gpu.mem_used_mb;
    // Capture the idle floor, keeping the MINIMUM of idle readings rather than
    // the latest. Side effect in a derived is fine here — it only records the
    // floor; the emitted value is pure of it. Taking the min rejects transient
    // pollution that would otherwise be misread as system usage: unload lag (the
    // driver hasn't freed the model's VRAM yet) and the load race (llama-server
    // allocates VRAM before its state flips to "starting") both produce briefly
    // high "idle" samples. A stale-high baseline would make modelMb collapse to
    // ~0, dumping the whole model slice into "System".
    if (live.length === 0) {
      baselineMb = baselineMb === null ? used : Math.min(baselineMb, used);
    }

    // System floor. Prefer the measured idle baseline. If we never caught an
    // idle sample (e.g. a model was already resident at page load) but we have a
    // load-plan estimate for the single loaded model, fall back to
    // used − estimated-model-VRAM so the model slice still shows instead of
    // being attributed entirely to "System".
    let sysFloor: number;
    let measured = true;
    if (baselineMb !== null) {
      sysFloor = Math.min(baselineMb, used);
    } else if ($est && live.length === 1 && live[0].id === $est.id) {
      sysFloor = Math.max(0, used - $est.est.estVramGB * 1024);
      measured = false;
    } else {
      sysFloor = used;
      measured = false;
    }
    const modelMb = Math.max(0, used - sysFloor);

    const systemSeg: VramSegment = {
      label: "System",
      mb: sysFloor,
      class: "bg-info",
      detail: "OS, other apps" + (measured ? "" : " (estimated — no idle baseline yet)"),
    };

    // Component split when we have a fresh estimate for the single loaded model.
    if (modelMb > 0 && $est && live.length === 1 && live[0].id === $est.id) {
      const estTotalMb = $est.est.estVramGB * 1024;
      const kvEstMb = Math.max(0, $est.est.kvReserveGB * 1024);
      const ckptEstMb = Math.max(0, ($est.est.checkpointGB ?? 0) * 1024);
      // estVramGB folds the checkpoint reserve into overhead, so subtract both
      // KV and checkpoints to leave the true weights share.
      const weightsEstMb = Math.max(0, estTotalMb - kvEstMb - ckptEstMb);

      // Fit the estimated components inside the measured model slice. If the
      // measurement exceeds the estimate, the surplus is CUDA context + compute
      // buffers. If it's under, scale the components down proportionally.
      let weightsMb: number;
      let kvMb: number;
      let ckptMb: number;
      let overheadMb: number;
      if (estTotalMb <= modelMb) {
        weightsMb = weightsEstMb;
        kvMb = kvEstMb;
        ckptMb = ckptEstMb;
        overheadMb = modelMb - estTotalMb;
      } else {
        const scale = estTotalMb > 0 ? modelMb / estTotalMb : 0;
        weightsMb = weightsEstMb * scale;
        kvMb = kvEstMb * scale;
        ckptMb = ckptEstMb * scale;
        overheadMb = 0;
      }

      const name = live[0].name || live[0].id;
      const segments: VramSegment[] = [systemSeg];
      if (weightsMb > 0)
        segments.push({ label: "Weights", mb: weightsMb, class: "bg-primary", detail: `${name} model weights on GPU` });
      if (kvMb > 0)
        segments.push({ label: "KV cache", mb: kvMb, class: "bg-warning", detail: `${name} attention cache (ctx ${$est.est.ctx})` });
      if (ckptMb > 0)
        segments.push({ label: "Checkpoints", mb: ckptMb, class: "bg-error", detail: `${name} context-checkpoint KV snapshots` });
      if (overheadMb > 0)
        segments.push({ label: "CUDA", mb: overheadMb, class: "bg-success", detail: "CUDA context + compute buffers" });
      return { usedMb: used, totalMb: $gpu.mem_total_mb, segments };
    }

    // Fallback: undifferentiated model slice (no estimate, or >1 model).
    const modelNames = live.map((m) => m.name || m.id);
    return {
      usedMb: used,
      totalMb: $gpu.mem_total_mb,
      segments: [
        systemSeg,
        {
          label: "Model(s)",
          mb: modelMb,
          class: "bg-primary",
          detail: modelNames.length ? modelNames.join(", ") : "none loaded",
        },
      ],
    };
  },
);
