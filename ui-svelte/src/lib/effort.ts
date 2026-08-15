// Reasoning-effort selection for the chat composer.
//
// Effort is a chat-TEMPLATE feature, not a sampler knob: the template validates
// the value against its own list and raise_exception's on anything else (which
// surfaces as a 500), so the options here are whatever the server advertised as
// `capabilities.reasoning_effort` — never a ladder hardcoded on this side. A
// model that advertises nothing gets a plain on/off, which is what this control
// was before it grew levels.

export const EFFORT_OFF = "none";
export const EFFORT_ON = "on";

// Display order only. Unknown levels all rank the same, so a template with a
// vocabulary we don't recognise keeps the order it advertised (Array.sort is
// stable).
const RANK: Record<string, number> = { minimal: 1, low: 2, medium: 3, high: 4, xhigh: 5, max: 6 };

const LABELS: Record<string, string> = {
  minimal: "Minimal",
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
};

export interface EffortOption {
  value: string;
  label: string;
}

function label(level: string): string {
  return LABELS[level.toLowerCase()] ?? level.charAt(0).toUpperCase() + level.slice(1);
}

function rank(level: string): number {
  return RANK[level.toLowerCase()] ?? 99;
}

// effortOptions builds the dropdown for one model. "None" is always first —
// turning thinking off entirely is not a level on any template's ladder, it is
// the separate enable_thinking switch.
export function effortOptions(levels: string[] | undefined): EffortOption[] {
  const off: EffortOption = { value: EFFORT_OFF, label: "None" };
  if (!levels || levels.length === 0) return [off, { value: EFFORT_ON, label: "On" }];
  const sorted = [...levels].sort((a, b) => rank(a) - rank(b));
  return [off, ...sorted.map((v) => ({ value: v, label: label(v) }))];
}

// resolveEffort maps the persisted pick onto something THIS model accepts, so
// one setting can follow the user across models with different ladders (and
// across models with none at all).
//
// Medium when the pick isn't on the ladder: the template's own default is the
// top of it (xhigh on Qwen 3.8), which spends minutes on turns that don't want
// it. A ladder with no "medium" falls back to its middle rung.
export function resolveEffort(pick: string, levels: string[] | undefined): string {
  if (pick === EFFORT_OFF) return EFFORT_OFF;
  const ls = levels ?? [];
  if (ls.length === 0) return EFFORT_ON;
  const exact = ls.find((l) => l.toLowerCase() === pick.toLowerCase());
  if (exact) return exact;
  const mid = ls.find((l) => l.toLowerCase() === "medium");
  if (mid) return mid;
  const sorted = [...ls].sort((a, b) => rank(a) - rank(b));
  return sorted[Math.floor((sorted.length - 1) / 2)];
}

// requestEffort is what goes on the wire as the OpenAI `reasoning_effort` field.
// Empty for both off (enable_thinking carries that) and for a model with no
// ladder (there is nothing the template would accept).
export function requestEffort(resolved: string): string {
  return resolved === EFFORT_OFF || resolved === EFFORT_ON ? "" : resolved;
}
