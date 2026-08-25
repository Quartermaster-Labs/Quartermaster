// Pure helpers behind the Models table (components/ModelsTable.svelte): the
// grouping axes (family → row → quant → variant), plus search / filter / sort.
// Nothing here touches Svelte state, so it is unit-testable — see
// modelTable.test.ts.
import type { Model } from "./types";
import { prettifyModelName } from "./modelUtils";
import { QUANT_RE, QUANT_PREFIX_RE, CRUMB_RE, MIX_RE, CRUMB_PART_RE } from "./quant";

// runEnd returns the index one past the last part of the token starting at
// parts[i] — i + 1 for every token but a hand-mixed one, which spans the marker
// plus every fragment after it ("mix-q-k" out of "…-mix-q-k-mtp").
function runEnd(parts: string[], i: number): number {
  if (!MIX_RE.test(parts[i])) return i + 1;
  let j = i + 1;
  while (j < parts.length && (CRUMB_PART_RE.test(parts[j]) || QUANT_RE.test(parts[j]))) j++;
  return j;
}

// isMixStart: a mix marker counts as a quant only when a fragment follows it —
// "mix" alone is also an ordinary word in a model name, and cutting an id there
// would strand half the name.
function isMixStart(parts: string[], i: number): boolean {
  return MIX_RE.test(parts[i]) && runEnd(parts, i) > i + 1;
}

// quantIndex finds the FIRST quant-shaped part of an id. First, not last: what
// follows a quant is a build tag ("-MTP", "-preserved", "-MID-HIGH"), never a
// second weight type. Never index 0 — an id that IS a quant has no base left.
function quantIndex(parts: string[]): number {
  for (let i = 1; i < parts.length; i++) if (QUANT_RE.test(parts[i]) || isMixStart(parts, i)) return i;
  return -1;
}

// quantMergeKey is the weight type as the file was NAMED: the server's parse of
// the filename, else the id's own. It is what lets two clusters (the same
// download sitting in two folders) collapse into one pill, so it may only ever
// be a name both files actually agreed on - never a computed label, because two
// unrelated hand-built quants can both come out "Q3_K mix" and are not one
// download. Empty when the name says nothing, which keeps such files apart.
function quantMergeKey(m: Model): string {
  if (m.quant) return m.quant.toUpperCase();
  const parts = m.id.split("-");
  const i = quantIndex(parts);
  if (i < 0) return "";
  const start = i > 1 && QUANT_PREFIX_RE.test(parts[i - 1]) ? i - 1 : i;
  return parts.slice(start, runEnd(parts, i)).join("-").toUpperCase();
}

// quantOf is the same thing for DISPLAY, and falls back one step further: a file
// whose name names no weight type shows the tensor-derived truth ("IQ4_XS mix")
// rather than a blank pill. The name still wins where there is one - "UD-Q4_K_XL"
// is what was downloaded, even though its tensors are mostly Q5_K.
export function quantOf(m: Model): string {
  return quantMergeKey(m) || (m.quantLabel ?? "").toUpperCase();
}

// baseKey is an id with everything from the quant onwards cut off, so the same
// model at Q4_K_M and Q8_0 lands on ONE row. It cuts rather than removes the one
// part, because both variant suffixes ("…-Q4_K_M-32k") and build tags
// ("…-nvfp4-mtp-mid-high") trail the quant — splicing would leave every tier on
// a row of its own and strand quant crumbs in the display name.
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

// baseKey/familyOf have Go twins in internal/autogen/family.go (ModelBaseKey /
// FamilyKey), where they decide which models may share a draft gguf or an mmproj
// projector. Change the rules here and they must change there too, or a model
// groups one way in this table and inherits sidecars another way.
//
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

// One quant of one model, with its variants. key identifies the entry WITHIN its
// row and is what the table keys its pills on: `quant` cannot, because an id
// whose weight type the pattern does not recognise leaves it "" and a row may
// hold two such entries (a custom quant and its MTP rebuild).
export interface QuantEntry {
  key: string;
  quant: string; // "" when the id carries none
  base: Model;
  variants: VariantEntry[];
  live: boolean;
}

// One table row = one model, whatever quants exist for it.
export interface ModelRow {
  // key groups the row (server-derived, see Model.modelKey); label names it, and
  // is always id-derived - a header key is built to compare, not to read, and
  // "Qwen2.5 Vl Instruct 7b Instruct" is not what the row should say.
  key: string;
  label: string;
  // family is the finetune cluster's key; familyLabel is the id-derived name to
  // show for it, for the same reason label exists.
  family: string;
  familyLabel: string;
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
function buildQuant(key: string, members: Model[]): QuantEntry {
  const sorted = [...members].sort((a, b) => a.id.length - b.id.length || a.id.localeCompare(b.id));
  const base = sorted[0];
  const quant = quantOf(base);
  const variants = sorted
    .slice(1)
    .map((m) => ({ model: m, label: suffix(m.id, base.id) }))
    .sort((a, b) => a.label.localeCompare(b.label));
  return { key, quant, base, variants, live: sorted.some(isLive) };
}

// A cluster is the set of served ids that are ONE gguf: the base plus every
// variant autogen emits from it (ctx tiers, "-game", the "-vision" twin). The
// server hands us that identity for free as `family`, the -m path — and unlike
// the id it survives a weight type the quant pattern has never heard of, which
// is exactly when the id-derived grouping collapses: baseKey finds no token to
// cut at, returns the whole id, and every tier of ONE model becomes a row of its
// own (qwen3.8-27b-mix-q-k did this: 5 rows for 1 model).
//
// So the gguf, not the id, decides which models share a quant entry, and the
// entry's base id then decides the row. Models with no family (peers, upstreams
// quartermaster does not launch) keep the id-only path.
//
// This axis has no Go twin: internal/autogen/family.go groups FILES on disk,
// where the path is the identity already and the question does not arise.
function clusterModels(models: Model[]): Model[][] {
  const byGguf = new Map<string, Model[]>();
  const loose: Model[][] = [];
  for (const m of models) {
    if (!m.family) {
      loose.push([m]);
      continue;
    }
    const c = byGguf.get(m.family);
    if (c) c.push(m);
    else byGguf.set(m.family, [m]);
  }
  return [...byGguf.values(), ...loose];
}

// buildRows groups a flat model list by model, then by quant. Quants are ordered
// largest file first (highest quality reads as the headline) with unknown-size
// entries last.
export function buildRows(models: Model[]): ModelRow[] {
  const byBase = new Map<string, Map<string, Model[]>>();
  for (const cluster of clusterModels(models)) {
    // The shortest id in a cluster is its base — every variant is that id plus a
    // suffix — so it, not an arbitrary member, names the row and the quant.
    const base = cluster.reduce((a, b) => (b.id.length < a.id.length || (b.id.length === a.id.length && b.id < a.id) ? b : a));
    const q = quantMergeKey(base);
    const key = base.modelKey || baseKey(base.id);
    // Two clusters merge into one quant entry only when they agree on a REAL
    // quant token — two folders holding the same Q8_0 download. With no token to
    // agree on, the gguf keeps them apart rather than fusing a custom quant with
    // an unrelated one that also failed to parse.
    const qKey = q || base.family || base.id;
    const quants = byBase.get(key) ?? new Map<string, Model[]>();
    quants.set(qKey, [...(quants.get(qKey) ?? []), ...cluster]);
    byBase.set(key, quants);
  }

  const rows: ModelRow[] = [];
  for (const [key, quants] of byBase) {
    const entries = [...quants.entries()]
      .map(([qKey, members]) => buildQuant(qKey, members))
      .sort((a, b) => (b.base.sizeGB ?? 0) - (a.base.sizeGB ?? 0) || a.quant.localeCompare(b.quant) || a.key.localeCompare(b.key));
    // Name the row after its shortest base id, not after whichever quant sorted
    // first: the label must not change when a bigger quant is downloaded.
    const head = entries.reduce((a, b) => (b.base.id.length < a.base.id.length || (b.base.id.length === a.base.id.length && b.base.id < a.base.id) ? b : a)).base;
    const label = baseKey(head.id);
    rows.push({
      key,
      label,
      family: head.familyKey || familyOf(label),
      familyLabel: familyOf(label),
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
    const fresh = { key: r.family, label: prettifyModelName(r.familyLabel || r.family), rows: [r] };
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
