import { derived, writable } from "svelte/store";
import { vramTotals, foreignVram, systemVram, guardForeignVram } from "./perf";
import { models, estimatePlan, type PlanEstimate } from "./api";
import type { ModelCapabilities } from "../lib/types";

// VRAM split: "system" (OS + other apps + game) vs the loaded llama-server.
// Where per-process attribution exists (nvidia-smi, or the Windows PDH counter on
// AMD/Intel) the server measures the split directly and the gauge just uses it -
// see systemFloorMb. Where it does not, we fall back to sampling an idle
// baseline: whenever no model is loaded, the live used VRAM IS the system floor,
// and once a model loads model usage ≈ live used − baseline. That fallback goes
// stale the moment another app claims VRAM while a model is resident, which is
// why it is the fallback and not the primary. Before the first idle sample the
// baseline is unknown, so everything is attributed to system (safe under-report).
//
// Whenever we hold a load-plan estimate for EVERY loaded model we further break
// their slice into model weights / KV cache / runtime overhead (the estimate is
// the only source we have for the component split — the driver only reports a
// single total). With two models loaded this used to give up and paint one flat
// "Model(s)" block, which is exactly when the split is most useful.
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

// estimateSegments splits a load-plan estimate into its VRAM components using the
// SAME labels + colors as the live status-rail breakdown, so the config-editor
// preview and the rail read as one consistent widget. estVramGB folds the KV,
// checkpoint, draft, compute-buffer, vision-projector and headroom reserves in;
// subtract each so "Weights" is the pure model-file share on GPU (why the total
// exceeds the raw gguf size). When KV lives in RAM it (and its checkpoints) cost
// no VRAM.
export function estimateSegments(est: PlanEstimate, kvInRam = false): VramSegment[] {
  const kvMb = Math.max(0, (kvInRam ? 0 : est.kvReserveGB) * 1024);
  const ckptMb = Math.max(0, (kvInRam ? 0 : est.checkpointGB ?? 0) * 1024);
  // Draft/MTP weights are folded into estVramGB (always in VRAM), not into KV/ckpt.
  const draftMb = Math.max(0, (est.draftGB ?? 0) * 1024);
  const computeMb = Math.max(0, (est.computeBufGB ?? 0) * 1024);
  const mmprojMb = Math.max(0, (est.mmprojGB ?? 0) * 1024);
  const overheadMb = Math.max(0, (est.overheadGB ?? 0) * 1024);
  const weightsMb = Math.max(0, est.estVramGB * 1024 - kvMb - ckptMb - draftMb - computeMb - mmprojMb - overheadMb);
  const segs: VramSegment[] = [];
  if (weightsMb > 0)
    segs.push({ label: "Weights", mb: weightsMb, class: "bg-primary", detail: "model file weights on GPU" });
  if (mmprojMb > 0)
    segs.push({ label: "Vision projector", mb: mmprojMb, class: "bg-primary/60", detail: "mmproj weights + CLIP compute reserve" });
  if (draftMb > 0)
    segs.push({ label: "Draft", mb: draftMb, class: "bg-primary/40", detail: "speculative draft / MTP model on GPU" });
  if (computeMb > 0)
    segs.push({ label: "Compute buffer", mb: computeMb, class: "bg-success", detail: "logits + activations" + (est.computeBufGB > 0 ? " (+CUDA context if NVIDIA)" : "") });
  if (kvMb > 0)
    segs.push({ label: "KV cache", mb: kvMb, class: "bg-warning", detail: `attention cache (ctx ${est.ctx})` });
  if (ckptMb > 0)
    segs.push({ label: "Checkpoints", mb: ckptMb, class: "bg-error", detail: "context-checkpoint KV snapshots" });
  if (overheadMb > 0)
    segs.push({ label: "Headroom", mb: overheadMb, class: "bg-txtsecondary/30", detail: "reserved safety headroom (vramOverheadGB)" });
  return segs;
}

// Plan estimates for the loaded models, keyed by model id. Refreshed as models
// come and go: an id already fetched (or in flight) is never re-fetched, and an
// id that leaves the ready set is dropped, so a swap costs exactly one request
// for the model that actually arrived.
const activeEstimates = writable<Record<string, PlanEstimate>>({});
let estCache: Record<string, PlanEstimate> = {};
// In-flight ids double as the "do we still want this?" flag: an id deleted here
// by an unload makes the late-arriving response drop on the floor instead of
// resurrecting a model that is gone.
const estInflight = new Set<string>();

models.subscribe(($models) => {
  // Only the llama-server backends have a plan to estimate: the sizer reads a
  // gguf and models llama's KV/offload. A diffusion/TTS/transcription/
  // segmentation/3D backend has no llama plan at all (the TRELLIS.2 package is a
  // DIRECTORY, not a gguf), so asking is a guaranteed error for every model that
  // goes ready.
  const ready = $models.filter((m) => m.state === "ready" && llamaSized(m)).map((m) => m.id);
  const readySet = new Set(ready);
  let dropped = false;
  for (const id of Object.keys(estCache)) {
    if (!readySet.has(id)) {
      delete estCache[id];
      dropped = true;
    }
  }
  for (const id of [...estInflight]) if (!readySet.has(id)) estInflight.delete(id);
  if (dropped) {
    estCache = { ...estCache };
    activeEstimates.set(estCache);
  }
  for (const id of ready) {
    if (estCache[id] || estInflight.has(id)) continue;
    estInflight.add(id);
    estimatePlan(id, { actual: true })
      .then((est) => {
        if (!estInflight.delete(id)) return; // unloaded while we were fetching
        estCache = { ...estCache, [id]: est };
        activeEstimates.set(estCache);
      })
      .catch(() => {
        estInflight.delete(id);
      });
  }
});

/** The bits of a Model this store needs, so the split can be unit-tested. */
export interface VramModelRef {
  id: string;
  name?: string;
}

// llamaSized reports whether a model's load plan is a llama one. The capability
// flags are how the server marks a non-llama backend (image_generation for
// sd-server, audio_speech for tts-server, image_to_3d for trellis2-server, ...);
// a llama model carries none of them, and often no capabilities block at all.
// vLLM is the exception the flags cannot see -- the server answers its estimate
// request with a 400 instead, which the catch below drops.
export function llamaSized(m: { capabilities?: ModelCapabilities }): boolean {
  const c = m.capabilities;
  if (!c) return true;
  return !(
    c.image_generation ||
    c.image_to_image ||
    c.image_to_3d ||
    c.segmentation ||
    c.audio_speech ||
    c.audio_transcriptions
  );
}

// loadedSegments splits the measured model slice (modelMb) into per-component
// segments, summing the same component across ALL loaded models: two models put
// their weights in one "Weights" segment, their caches in one "KV cache", and so
// on. Aggregating by component rather than by model keeps the legend the same
// seven colors no matter how many models are resident, and keeps the bar
// readable when a third one loads.
//
// Returns null when any loaded model has no estimate yet (mid-load, or the
// estimate request failed), which is the caller's cue to fall back to the flat
// undifferentiated slice.
export function loadedSegments(
  live: VramModelRef[],
  estimates: Record<string, PlanEstimate>,
  modelMb: number,
): VramSegment[] | null {
  if (live.length === 0) return null;
  const ests = live.map((m) => estimates[m.id]);
  if (ests.some((e) => !e)) return null;

  const parts = ests.map((e) => {
    const kv = Math.max(0, e.kvReserveGB * 1024);
    const ckpt = Math.max(0, (e.checkpointGB ?? 0) * 1024);
    // Draft/MTP is charged in VRAM whatever the KV placement, so it is its own
    // slice rather than part of KV.
    const draft = Math.max(0, (e.draftGB ?? 0) * 1024);
    const compute = Math.max(0, (e.computeBufGB ?? 0) * 1024);
    const mmproj = Math.max(0, (e.mmprojGB ?? 0) * 1024);
    const headroom = Math.max(0, (e.overheadGB ?? 0) * 1024);
    const total = Math.max(0, e.estVramGB * 1024);
    // estVramGB folds every reserve in; subtract each to leave the pure
    // model-file weights share (why the total exceeds the raw gguf size).
    const weights = Math.max(0, total - kv - ckpt - draft - compute - mmproj - headroom);
    return { kv, ckpt, draft, compute, mmproj, headroom, total, weights, ctx: e.ctx };
  });
  const sum = (pick: (p: (typeof parts)[number]) => number) =>
    parts.reduce((a, p) => a + pick(p), 0);
  const estTotalMb = sum((p) => p.total);

  // Fit the estimated components inside the measured model slice. If the
  // measurement exceeds the estimate, the surplus is unaccounted runtime
  // overhead. If it's under, scale the components down proportionally.
  let scale = 1;
  let surplusMb = 0;
  if (estTotalMb <= modelMb) {
    surplusMb = modelMb - estTotalMb;
  } else {
    scale = estTotalMb > 0 ? modelMb / estTotalMb : 0;
  }

  const names = live.map((m) => m.name || m.id);
  // One model reads as "<name> ...", several as "<a> + <b> ...", so a hover on
  // the shared segment still says whose VRAM is in it.
  const who = names.join(" + ");
  const ctxDetail =
    parts.length === 1
      ? `ctx ${parts[0].ctx}`
      : names.map((n, i) => `${n} ctx ${parts[i].ctx}`).join(", ");

  const segments: VramSegment[] = [];
  const push = (label: string, mb: number, cls: string, detail: string) => {
    if (mb > 0) segments.push({ label, mb, class: cls, detail });
  };
  push("Weights", sum((p) => p.weights) * scale, "bg-primary", `${who} model file weights on GPU`);
  push("Vision projector", sum((p) => p.mmproj) * scale, "bg-primary/60", `${who} mmproj weights + CLIP compute reserve`);
  push("Draft", sum((p) => p.draft) * scale, "bg-primary/40", `${who} speculative draft / MTP model on GPU`);
  push("Compute buffer", sum((p) => p.compute) * scale, "bg-success", `${who} logits + activations`);
  push("KV cache", sum((p) => p.kv) * scale, "bg-warning", `${who} attention cache (${ctxDetail})`);
  push("Checkpoints", sum((p) => p.ckpt) * scale, "bg-error", `${who} context-checkpoint KV snapshots`);
  push("Headroom", sum((p) => p.headroom) * scale, "bg-txtsecondary/30", "reserved safety headroom (vramOverheadGB)");
  push("Overhead", surplusMb, "bg-success/60", "measured runtime overhead beyond the estimate");
  return segments;
}

/** Where the "System" figure came from, which decides how the hover explains it. */
export type SystemFloorSource = "live" | "idle" | "baseline" | "estimate" | "unknown";

export interface SystemFloorInput {
  /** Used VRAM with the stray-inference slice already carved out. */
  usedMb: number;
  /** The stray llama-server/sd-server slice, which gets its own segment. */
  strayForeignMb: number;
  /** Live per-process "not ours" measurement from the OOM guard; null if untrusted. */
  guardForeignMb: number | null;
  /** Server-sampled idle floor; 0 = never observed a long enough idle stretch. */
  serverIdleMb: number;
  /** This tab's own idle baseline; null before it ever saw an idle sample. */
  browserBaselineMb: number | null;
  /** Summed load-plan estimate for the loaded models; null if any is missing. */
  estTotalMb: number | null;
}

// systemFloorMb decides how much of the card belongs to the OS and other apps.
//
// The preference order matters, and the live reading has to come first. The idle
// floors below it are sampled ONLY while no model is loaded, so they freeze for
// as long as one stays resident: an app that claims VRAM afterwards (a game,
// Blender, Unity) never moves them, and every MiB it takes is misread as model
// usage - which then lands in the "Overhead" residual, since that segment is just
// measured-minus-estimated. The guard's figure is a per-process attribution
// recomputed on every sample, so it tracks those apps as they come and go.
//
// The guard counts stray llama-servers as foreign too, but the gauge already
// draws those as their own red segment, so they are subtracted out rather than
// counted twice.
export function systemFloorMb(i: SystemFloorInput): { mb: number; source: SystemFloorSource } {
  const clamp = (mb: number) => Math.min(Math.max(0, mb), Math.max(0, i.usedMb));
  if (i.guardForeignMb !== null) {
    return { mb: clamp(i.guardForeignMb - i.strayForeignMb), source: "live" };
  }
  if (i.serverIdleMb > 0) return { mb: clamp(i.serverIdleMb), source: "idle" };
  if (i.browserBaselineMb !== null) return { mb: clamp(i.browserBaselineMb), source: "baseline" };
  // No idle sample anywhere (a model was already resident at page load). Back
  // out the model slice from its estimate so it still shows, instead of painting
  // the whole card as System.
  if (i.estTotalMb !== null) return { mb: clamp(i.usedMb - i.estTotalMb), source: "estimate" };
  return { mb: clamp(i.usedMb), source: "unknown" };
}

// systemDetail is the hover text for the System segment, naming how the figure
// was arrived at so a surprising number is debuggable from the UI alone.
export function systemDetail(source: SystemFloorSource): string {
  switch (source) {
    case "live":
      return "OS, other apps (live per-process measurement)";
    case "idle":
      return "OS, other apps (idle floor - apps started since a model loaded may show under Overhead)";
    case "baseline":
      return "OS, other apps (this tab's idle baseline)";
    case "estimate":
      return "OS, other apps (estimated - no idle baseline yet)";
    default:
      return "OS, other apps";
  }
}

// foreignSeg builds the red "Foreign" segment for VRAM held by a llama-server
// we didn't spawn. Returns [] when none detected.
function foreignSeg(mb: number, procs?: { name: string }[]): VramSegment[] {
  if (mb <= 0) return [];
  const names = (procs ?? []).map((p) => p.name).join(", ");
  return [
    {
      label: "Foreign",
      mb,
      class: "bg-error",
      detail: "llama-server not owned by us" + (names ? ` - ${names}` : ""),
    },
  ];
}

export const vramBreakdown = derived(
  [vramTotals, models, activeEstimates, foreignVram, systemVram, guardForeignVram],
  ([$gpu, $models, $ests, $foreign, $sysVram, $guardForeign]): VramBreakdown | null => {
    if (!$gpu) return null;

    const live = $models.filter(
      (m) => m.state === "ready" || m.state === "starting" || m.state === "stopping",
    );
    const rawUsed = $gpu.usedMb;
    // Carve foreign VRAM out of the total before splitting the rest into
    // system / model components; it gets its own red segment.
    const foreignMb = Math.min(Math.max(0, $foreign?.mb ?? 0), rawUsed);
    const foreign = foreignSeg(foreignMb, $foreign?.procs);
    const used = rawUsed - foreignMb;
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
      // No model loaded: ALL live VRAM is system by definition. Don't subtract
      // the (possibly stale-low) baseline floor — VRAM that grew since the
      // baseline was sampled (a game/app opening) would otherwise leak into a
      // "Model(s)" slice even though nothing is loaded.
      return {
        usedMb: rawUsed,
        totalMb: $gpu.totalMb,
        segments: [{ label: "System", mb: used, class: "bg-info", detail: "OS, other apps" }, ...foreign],
      };
    }

    // Total estimated VRAM across the loaded models, or null while any of them
    // is still missing an estimate (a model mid-load has none yet).
    const estTotalMb = live.every((m) => $ests[m.id])
      ? live.reduce((a, m) => a + $ests[m.id].estVramGB * 1024, 0)
      : null;

    const { mb: sysFloor, source } = systemFloorMb({
      usedMb: used,
      strayForeignMb: foreignMb,
      guardForeignMb: $guardForeign,
      serverIdleMb: $sysVram,
      browserBaselineMb: baselineMb,
      estTotalMb,
    });
    const modelMb = Math.max(0, used - sysFloor);

    const systemSeg: VramSegment = {
      label: "System",
      mb: sysFloor,
      class: "bg-info",
      detail: systemDetail(source),
    };

    // Component split whenever every loaded model has a fresh estimate, however
    // many that is.
    const split = modelMb > 0 ? loadedSegments(live, $ests, modelMb) : null;
    if (split) {
      return { usedMb: rawUsed, totalMb: $gpu.totalMb, segments: [systemSeg, ...split, ...foreign] };
    }

    // Fallback: undifferentiated model slice (a model still loading, or an
    // estimate request that failed).
    const modelNames = live.map((m) => m.name || m.id);
    return {
      usedMb: rawUsed,
      totalMb: $gpu.totalMb,
      segments: [
        systemSeg,
        {
          label: "Model(s)",
          mb: modelMb,
          class: "bg-primary",
          detail: modelNames.length ? modelNames.join(", ") : "none loaded",
        },
        ...foreign,
      ],
    };
  },
);
