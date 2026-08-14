// Pure helpers behind the Models table (components/ModelsTable.svelte): the
// grouping axes (family → row → quant → variant), plus search / filter / sort.
// Nothing here touches Svelte state, so it is unit-testable — see
// modelTable.test.ts.
import type { Model } from "./types";
import { prettifyModelName } from "./modelUtils";

// Quant tokens as they appear in a gguf-derived model id. Mirrors the server's
// quantFromPath (internal/server/modelmeta.go) so a model whose payload predates
// the field still groups correctly.
const QUANT_RE = /^(?:I?Q\d+(?:_[A-Za-z0-9]+)*|BF16|FP16|F16|F32|FP8|MXFP4)$/i;

// Recipe markers that belong to the quant rather than the model name: unsloth
// writes "UD-Q4_K_XL" (dynamic), mradermacher "i1-Q4_K_M" (imatrix).
const QUANT_PREFIX_RE = /^(?:UD|i1)$/i;

// quantIndex finds the FIRST quant-shaped part of an id. First, not last: what
// follows a quant is a build tag ("-MTP", "-preserved"), and autogen appends the
// quant a second time when the filename didn't end in it, so the last match is
// often the duplicate. Never index 0 — an id that IS a quant has no base left.
function quantIndex(parts: string[]): number {
  for (let i = 1; i < parts.length; i++) if (QUANT_RE.test(parts[i])) return i;
  return -1;
}

// quantOf returns the model's weight type: the server-parsed field when present,
// else derived from the id, with a UD/i1 prefix folded in.
export function quantOf(m: Model): string {
  if (m.quant) return m.quant.toUpperCase();
  const parts = m.id.split("-");
  const i = quantIndex(parts);
  if (i < 0) return "";
  const withPrefix = i > 1 && QUANT_PREFIX_RE.test(parts[i - 1]) ? `${parts[i - 1]}-${parts[i]}` : parts[i];
  return withPrefix.toUpperCase();
}

// baseKey is an id with everything from the quant onwards cut off, so the same
// model at Q4_K_M and Q8_0 lands on ONE row. It cuts rather than removes the one
// part, because both variant suffixes ("…-Q4_K_M-32k") and build tags
// ("…-q4_k_m-mtp-q4_k_m") trail the quant — splicing would leave every tier on a
// row of its own and strand quant crumbs in the display name.
export function baseKey(id: string): string {
  const parts = id.split("-");
  while (parts.length > 1 && /^gguf$/i.test(parts[parts.length - 1])) parts.pop();
  let i = quantIndex(parts);
  if (i > 1 && QUANT_PREFIX_RE.test(parts[i - 1])) i--;
  if (i > 0) parts.length = i;
  return parts.join("-") || id;
}

// A parameter count as publishers write it: 27b, 4b, 0.6b, 350m, gemma's e2b.
const SIZE_RE = /^[a-z]?\d+(?:\.\d+)?[bm]$/i;
// A MoE active-parameter tail: the "a3b" of "qwen3.6-35b-a3b".
const MOE_RE = /^a\d+(?:\.\d+)?b$/i;

// familyOf is the finetune detector: it reduces a base key to <model><size>, so
// "thinkingcap-qwen3.6-27b" and "qwen3.6-27b-uncensored-heretic-v2" both resolve
// to "qwen3.6-27b" and cluster under one heading. Deliberately keyed on the
// parameter count — every finetune keeps it, and it is the one token a tuner
// never rewrites. Anything with no size token is its own family.
export function familyOf(key: string): string {
  const parts = key.split("-");
  const i = parts.findIndex((p) => SIZE_RE.test(p) && !MOE_RE.test(p));
  if (i < 1) return key;
  // "gemma-4-12b": the bare version number belongs to the name.
  const start = /^\d+(?:\.\d+)?$/.test(parts[i - 1]) && i > 1 ? i - 2 : i - 1;
  const end = i + 1 < parts.length && MOE_RE.test(parts[i + 1]) ? i + 2 : i + 1;
  return parts.slice(start, end).join("-");
}

// One variant of one quant: a sibling model id that extends the quant's base id
// (ctx tiers, "-vision", named variants). label is the suffix shown on the pill.
export interface VariantEntry {
  model: Model;
  label: string;
}

// One quant of one model, with its variants.
export interface QuantEntry {
  quant: string; // "" when the id carries none
  base: Model;
  variants: VariantEntry[];
  live: boolean;
}

// One table row = one model, whatever quants exist for it.
export interface ModelRow {
  key: string;
  label: string;
  family: string;
  quants: QuantEntry[];
  live: boolean;
  unlisted: boolean;
}

export function isLive(m: Model): boolean {
  return m.state === "ready" || m.state === "starting" || m.state === "stopping";
}

// suffix of a variant id relative to its base ("qwen3-32k" => "32k").
function suffix(id: string, base: string): string {
  const s = id.startsWith(base) ? id.slice(base.length).replace(/^[-_:.@]/, "") : id;
  return s || id;
}

// buildQuant collapses the models of ONE quant into base + variants. The base is
// the shortest id (variants are always the base plus a suffix).
function buildQuant(quant: string, members: Model[]): QuantEntry {
  const sorted = [...members].sort((a, b) => a.id.length - b.id.length || a.id.localeCompare(b.id));
  const base = sorted[0];
  const variants = sorted
    .slice(1)
    .map((m) => ({ model: m, label: suffix(m.id, base.id) }))
    .sort((a, b) => a.label.localeCompare(b.label));
  return { quant, base, variants, live: sorted.some(isLive) };
}

// buildRows groups a flat model list by model, then by quant. Quants are ordered
// largest file first (highest quality reads as the headline) with unknown-size
// entries last.
export function buildRows(models: Model[]): ModelRow[] {
  const byBase = new Map<string, Map<string, Model[]>>();
  for (const m of models) {
    const q = quantOf(m);
    const key = baseKey(m.id);
    const quants = byBase.get(key) ?? new Map<string, Model[]>();
    quants.set(q, [...(quants.get(q) ?? []), m]);
    byBase.set(key, quants);
  }

  const rows: ModelRow[] = [];
  for (const [key, quants] of byBase) {
    const entries = [...quants.entries()]
      .map(([q, members]) => buildQuant(q, members))
      .sort((a, b) => (b.base.sizeGB ?? 0) - (a.base.sizeGB ?? 0) || a.quant.localeCompare(b.quant));
    rows.push({
      key,
      label: key,
      family: familyOf(key),
      quants: entries,
      live: entries.some((e) => e.live),
      unlisted: entries.every((e) => e.base.unlisted),
    });
  }
  return rows;
}

// pickQuant chooses which quant a row shows by default: whatever is loaded,
// else the first (largest) one.
export function pickQuant(row: ModelRow): QuantEntry {
  return row.quants.find((q) => q.live) ?? row.quants[0];
}

export type StateFilter = "all" | "loaded" | "idle";

export interface FilterOpts {
  search: string;
  state: StateFilter;
  showUnlisted: boolean;
}

// matches is the search predicate: id, display name and quant of ANY member,
// plus the family — so "qwen3.6-27b" finds every finetune of it.
export function matches(row: ModelRow, needle: string): boolean {
  if (!needle) return true;
  const q = needle.toLowerCase();
  if (row.label.toLowerCase().includes(q) || row.family.toLowerCase().includes(q)) return true;
  for (const qe of row.quants) {
    if (qe.quant.toLowerCase().includes(q)) return true;
    for (const m of [qe.base, ...qe.variants.map((v) => v.model)]) {
      if (m.id.toLowerCase().includes(q)) return true;
      if ((m.name ?? "").toLowerCase().includes(q)) return true;
    }
  }
  return false;
}

export function filterRows(rows: ModelRow[], opts: FilterOpts): ModelRow[] {
  return rows.filter((r) => {
    if (!opts.showUnlisted && r.unlisted) return false;
    if (opts.state === "loaded" && !r.live) return false;
    if (opts.state === "idle" && r.live) return false;
    return matches(r, opts.search);
  });
}

// "none" is a real state, not a placeholder: the header cycles asc → desc → none
// so the operator can get back to the catalog's own order.
export type SortKey = "none" | "name" | "quant" | "size" | "vram" | "ram";
export type SortDir = "asc" | "desc";

// nextSort is the header-click cycle. A different column always starts over at
// ascending.
export function nextSort(cur: SortKey, dir: SortDir, clicked: SortKey): { key: SortKey; dir: SortDir } {
  if (cur !== clicked) return { key: clicked, dir: "asc" };
  if (dir === "asc") return { key: clicked, dir: "desc" };
  return { key: "none", dir: "asc" };
}

export interface SortOpts {
  favorites?: Set<string>;
  selected?: (row: ModelRow) => QuantEntry;
}

// sortRows orders by the requested column, floating favorites to the very top
// and loaded models just under them: both are what the operator came to the page
// for, and burying either under an alphabetical sort is the thing the card grid
// got right. "none" keeps the catalog's own order under the same two pins.
export function sortRows(rows: ModelRow[], key: SortKey, dir: SortDir, opts: SortOpts = {}): ModelRow[] {
  const pick = opts.selected ?? pickQuant;
  const fav = (r: ModelRow): boolean => opts.favorites?.has(r.key) ?? false;
  const num = (r: ModelRow, f: (m: Model) => number | undefined): number => f(pick(r).base) ?? -1;
  const cmp = (a: ModelRow, b: ModelRow): number => {
    switch (key) {
      case "quant":
        return pick(a).quant.localeCompare(pick(b).quant);
      case "size":
        return num(a, (m) => m.sizeGB) - num(b, (m) => m.sizeGB);
      case "vram":
        return num(a, (m) => m.estVramGB) - num(b, (m) => m.estVramGB);
      case "ram":
        return num(a, (m) => m.estRamGB) - num(b, (m) => m.estRamGB);
      case "name":
        return a.label.localeCompare(b.label);
      default:
        return 0;
    }
  };
  const sign = dir === "desc" ? -1 : 1;
  return [...rows].sort((a, b) => {
    if (fav(a) !== fav(b)) return fav(a) ? -1 : 1;
    if (a.live !== b.live) return a.live ? -1 : 1;
    if (key === "none") return 0; // stable: the catalog's own order
    const c = cmp(a, b);
    return c !== 0 ? sign * c : a.label.localeCompare(b.label);
  });
}

// One rendered block: a finetune family and its rows, or a lone model.
export interface FamilyGroup {
  key: string;
  label: string;
  rows: ModelRow[];
}

// groupFamilies clusters already-sorted rows by family WITHOUT reordering them:
// a family takes the position of its best-ranked member, so a loaded or favorite
// finetune still pulls its relatives to the top and the sort stays legible.
export function groupFamilies(rows: ModelRow[]): FamilyGroup[] {
  const out: FamilyGroup[] = [];
  const byKey = new Map<string, FamilyGroup>();
  for (const r of rows) {
    const g = byKey.get(r.family);
    if (g) {
      g.rows.push(r);
      continue;
    }
    const fresh = { key: r.family, label: prettifyModelName(r.family), rows: [r] };
    byKey.set(r.family, fresh);
    out.push(fresh);
  }
  return out;
}

// fmtGB renders a size column: "18.6" with one decimal, an em dash for absent
// (0 means "the sizer doesn't model this class", not "free").
export function fmtGB(v: number | undefined): string {
  if (!v || v <= 0) return "-";
  return v >= 100 ? v.toFixed(0) : v.toFixed(1);
}

// Crumbs a quant leaves in a DISPLAY name. autogen strips the quant from the id
// only when it trails the filename, and prettifying splits what's left on "_":
// "ThinkingCap-Qwen3.6-27B-Q4_K_M-MTP" arrives named "Thinkingcap Qwen3.6 27b K
// M". Trailing-only and word-shaped, so "…V2 Native Mtp Preserved" is untouched.
const CRUMB_RE = /^(?:UD|I1|K|M|S|L|X{1,2}[SLM]|I?Q\d+(?:_[A-Za-z0-9]+)*|BF16|FP16|F16|F32|FP8|MXFP4)$/i;

// stripQuantCrumbs drops those trailing fragments. It never returns empty: a
// name that is ALL crumbs is the model's real name (a "Q8" nickname), not debris.
export function stripQuantCrumbs(name: string): string {
  const parts = name.split(" ");
  while (parts.length > 1 && CRUMB_RE.test(parts[parts.length - 1])) parts.pop();
  return parts.join(" ");
}

// rowLabel is the display name for a row: prettified base id, or the model's own
// name when the payload carries one and the caller asked for names.
export function rowLabel(row: ModelRow, mode: "id" | "name"): string {
  if (mode === "id") return row.label;
  const n = pickQuant(row).base.name;
  return stripQuantCrumbs(prettifyModelName(n || row.label));
}
