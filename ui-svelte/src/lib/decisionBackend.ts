import { askConfirm } from "./confirm";
import type { Model } from "./types";

// A decision model needs a llama.cpp new enough to serve /v1/systemone. The
// server checks the build the model launches with (decision_backend_outdated,
// internal/server/decisionsupport.go); an older llama-server either refuses the
// gguf or loads it as a plain LLM and 404s every question, so the user is told
// before the load rather than left with a model that silently does nothing.
//
// Asked once per model per page session: "Load anyway" is a deliberate choice
// (a custom build the scan cannot read, say), and repeating the dialog on
// every run of the Decision tab would only train people to click through it.
const acknowledged = new Set<string>();

export async function confirmDecisionBackend(m: Model | undefined): Promise<boolean> {
  if (!m?.capabilities?.decision_backend_outdated || acknowledged.has(m.id)) return true;
  const ok = await askConfirm({
    title: "llama.cpp needs an update",
    body:
      `${m.name || m.id} is a decision model, and the llama.cpp build it launches with ` +
      "predates decision-model support (/v1/systemone), so it cannot answer questions. " +
      "Update llama.cpp in Settings > Backends, or pick a newer build for this model in its settings.",
    confirmLabel: "Load anyway",
    danger: true,
  });
  if (ok) acknowledged.add(m.id);
  return ok;
}
