// Model browser client — the `/api/hub/*` surface (internal/server/hubapi.go).
//
// Every call is proxied by quartermaster rather than hitting the hub from the
// browser: no CORS, and a Hugging Face token stays server-side.

export interface HubModel {
  id: string; // "owner/name"
  source: string;
  author: string;
  name: string;
  downloads: number;
  likes: number;
  updated?: string;
  // When the repo was first published — what "Trendy" judges by. `updated`
  // moves for a README fix, so it cannot answer "is this a new release".
  created?: string;
  pipeline?: string;
  tags?: string[];
  gated: boolean;
  private: boolean;
  // Size in billions of parameters, read server-side out of the repo NAME
  // (`hub.ParamsB`); absent when the name states none. The same number the
  // "Under 120B" filter judges by, so the badge can never disagree with it.
  paramsB?: number;
}

export interface HubFile {
  path: string;
  sizeBytes: number;
  shard?: number;
  shards?: number;
  group: string;
  // Vision/audio mmproj file, flagged server-side (`hub.classify`) — a companion
  // to a model's weights, not a model. Drives the badge, the sort order and the
  // "companion" fit column in the picker.
  projector?: boolean;
  // Already in the models folder at the size the hub reports, filled in
  // server-side (`Manager.LocalFiles`). A `.part` does not count — half a file
  // is not a model, and that row stays a download.
  local?: boolean;
}

export interface HubDetail extends HubModel {
  readme?: string;
  files: HubFile[];
}

export interface HubJobFile {
  path: string;
  size: number;
  done: number;
  skipped?: boolean;
}

export interface HubJob {
  id: string;
  source: string;
  repo: string;
  label?: string;
  dir: string;
  files: HubJobFile[];
  // "paused" is the one phase that is neither running nor terminal: stopped on
  // purpose, bytes kept, resumable — including after a restart.
  phase: "queued" | "checking" | "downloading" | "registering" | "paused" | "done" | "error" | "canceled";
  downloaded: number;
  total: number;
  error?: string;
  gated?: boolean;
  started: string;
  finished?: string;
}

export interface HubSources {
  sources: { id: string; name: string }[];
  modelsRoot: string;
  hasToken: boolean;
}

export class HubApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function hubFetch<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    // The server sends a plain-text message for every hub failure, and it is
    // written to be read (accept the license, not enough disk, …) — so surface
    // it verbatim rather than a status code.
    throw new HubApiError(res.status, (await res.text()) || res.statusText);
  }
  return (await res.json()) as T;
}

export function getHubSources(): Promise<HubSources> {
  return hubFetch<HubSources>("/api/hub/sources");
}

/**
 * MAX_PARAMS_B is the default size cap, in billions of parameters.
 *
 * A hub's top-by-downloads page is dominated by frontier-size repos that no
 * single-GPU box can run, which buries everything usable. The cap is applied on
 * the repo NAME server-side (`hub.ParamsB`), so a repo that doesn't state its
 * size is kept rather than hidden — hence the visible toggle rather than a
 * silent filter.
 */
export const MAX_PARAMS_B = 120;

/** TRENDY_DAYS is the window the "Trendy" filter calls a new release. */
export const TRENDY_DAYS = 14;

export interface HubSearchOpts {
  q: string;
  sort: string;
  maxParamsB: number;
  kind: string;
  source: string;
  limit: number;
  /** 0 = any age; otherwise keep repos created within N days. */
  maxAgeDays: number;
  /** Offset into the HUB's own result list — see HubPage.nextSkip. */
  skip: number;
}

export interface HubPage {
  models: HubModel[];
  // Where the next page starts. It counts the hub's rows, not the ones that
  // survived the server-side size/age filters, so a caller must page by this
  // number rather than by models.length or it will re-request or skip rows.
  nextSkip: number;
  hasMore: boolean;
}

export async function searchHub(opts: Partial<HubSearchOpts> = {}): Promise<HubPage> {
  const {
    q = "",
    sort = "downloads",
    maxParamsB = MAX_PARAMS_B,
    kind = "llm",
    source = "hf",
    limit = 30,
    maxAgeDays = 0,
    skip = 0,
  } = opts;
  const v = new URLSearchParams({ q, sort, source, limit: String(limit) });
  // Category tab. The hub ANDs its own filter tags, so this narrows server-side
  // — a 30-row page filtered here would mostly render an empty tab.
  if (kind) v.set("kind", kind);
  if (maxParamsB > 0) v.set("maxParams", String(maxParamsB));
  if (maxAgeDays > 0) v.set("maxAgeDays", String(maxAgeDays));
  if (skip > 0) v.set("skip", String(skip));
  const r = await hubFetch<HubPage>(`/api/hub/search?${v}`);
  return { models: r.models ?? [], nextSkip: r.nextSkip ?? skip + limit, hasMore: !!r.hasMore };
}

/**
 * revealFolder opens a downloaded model's folder in the OS file manager.
 *
 * The server does the opening (the browser cannot), and only for paths inside
 * the models root. No argument means the models root itself.
 */
export function revealFolder(path = ""): Promise<{ opened: string }> {
  return hubFetch<{ opened: string }>("/api/hub/reveal", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path }),
  });
}

/**
 * HubEstimate is the REAL pre-download sizing, from the candidate's GGUF header
 * (Range-fetched server-side) run through the same planner the config editor
 * uses — not the size-only guess `verdictFor` makes. `err` set means the header
 * could not be read or parsed; the caller falls back to `verdictFor`.
 */
export interface HubEstimate {
  repo: string;
  path: string;
  fits: boolean;
  ctx: number; // window the planner picked for the configured VRAM target
  maxCtx: number; // the model's own trained ceiling
  atMax: boolean; // ctx reached maxCtx, i.e. "max context"
  offload: boolean; // part of the model lands on the CPU
  estVramGB: number;
  targetVramGB: number;
  err?: string;
}

export async function estimateHubFile(repo: string, path: string, source = "hf"): Promise<HubEstimate> {
  const v = new URLSearchParams({ repo, path, source });
  return hubFetch<HubEstimate>(`/api/hub/estimate?${v}`);
}

/** 131072 → "128k". Context windows are quoted in k everywhere else in this UI. */
export function humanCtx(n: number): string {
  if (!n) return "";
  if (n >= 1_000_000) return `${(n / 1_048_576).toFixed(1)}M`;
  if (n >= 1024) return `${Math.round(n / 1024)}k`;
  return String(n);
}

export async function getHubModel(id: string, source = "hf"): Promise<HubDetail> {
  const d = await hubFetch<HubDetail>(`/api/hub/model/${id}?source=${encodeURIComponent(source)}`);
  d.files ??= [];
  return d;
}

// Author avatars are looked up one at a time and reused everywhere. The cache
// holds the PROMISE, not the result, so thirty rows by the same publisher
// rendering in one frame make one request rather than thirty. A failed lookup
// resolves to "" and stays cached — the row draws a monogram, and an author who
// has no picture must not be re-asked on every keystroke.
const avatarCache = new Map<string, Promise<string>>();

export function getAuthorAvatar(author: string, source = "hf"): Promise<string> {
  const key = `${source}/${author}`;
  let p = avatarCache.get(key);
  if (!p) {
    p = hubFetch<{ url: string }>(`/api/hub/avatar?source=${encodeURIComponent(source)}&author=${encodeURIComponent(author)}`)
      .then((r) => r.url || "")
      .catch(() => "");
    avatarCache.set(key, p);
  }
  return p;
}

export async function getHubJobs(): Promise<HubJob[]> {
  const jobs = await hubFetch<HubJob[]>("/api/hub/jobs");
  return jobs ?? [];
}

export function startHubDownload(repo: string, files: string[], label = "", source = "hf"): Promise<{ jobId: string }> {
  return hubFetch<{ jobId: string }>("/api/hub/download", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source, repo, files, label }),
  });
}

function jobAction(path: string, jobId: string): Promise<unknown> {
  return hubFetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ jobId }),
  });
}

// Cancel stops the job and DISCARDS its bytes — partials plus anything this job
// finished, since half a sharded GGUF is not a model. That is the whole
// difference from pause, and why the UI asks before calling it.
export function cancelHubDownload(jobId: string): Promise<unknown> {
  return jobAction("/api/hub/cancel", jobId);
}

export function pauseHubDownload(jobId: string): Promise<unknown> {
  return jobAction("/api/hub/pause", jobId);
}

export function resumeHubDownload(jobId: string): Promise<unknown> {
  return jobAction("/api/hub/resume", jobId);
}

// --- shaping the file list for the picker ---

export interface FileOption {
  group: string; // logical download key
  label: string; // the file's own name — see groupFiles
  files: HubFile[]; // every shard of this file — one logical download
  sizeBytes: number;
  projector: boolean; // an mmproj companion, not a model on its own
  // Every shard is already on disk. Partly-downloaded sets are NOT local: one
  // shard of three is not a model, so the row keeps its download button (which
  // skips the shards already there).
  local: boolean;
}

/**
 * groupFiles turns a repo's flat file list into the rows the picker offers.
 * Multi-part GGUFs collapse onto one row: a lone shard is not a model, so
 * offering shard 2 of 3 as its own download would only produce a broken folder.
 * A sharded set is labelled by its group key, i.e. the shared name with the
 * `-00001-of-00003` part removed, so the row names the set rather than one part.
 *
 * The label is the file's WHOLE NAME. It used to be the quant tag picked out of
 * that name by a regex, which is shorter but wrong twice over: a miss is silent
 * (an unrecognised recipe marker or suffix mislabelled the row instead of
 * failing), and two different files can reduce to the same tag — `mmproj-F16`
 * rendered as a bare "F16", indistinguishable from the model's own F16 weights.
 * Names are longer, and they are what the publisher actually wrote.
 */
export function groupFiles(files: HubFile[]): FileOption[] {
  const by = new Map<string, FileOption>();
  for (const f of files) {
    let opt = by.get(f.group);
    if (!opt) {
      opt = { group: f.group, label: baseName(f.group), files: [], sizeBytes: 0, projector: !!f.projector, local: true };
      by.set(f.group, opt);
    }
    opt.files.push(f);
    opt.sizeBytes += f.sizeBytes;
    opt.local = opt.local && !!f.local;
  }
  const out = [...by.values()];
  for (const o of out) o.files.sort((a, b) => (a.shard ?? 0) - (b.shard ?? 0));
  out.sort((a, b) => Number(a.projector) - Number(b.projector) || a.sizeBytes - b.sizeBytes);
  return out;
}

function baseName(p: string): string {
  const i = p.lastIndexOf("/");
  return i >= 0 ? p.slice(i + 1) : p;
}

export type FitVerdict = "fits" | "spills" | "toobig" | "unknown";

/**
 * verdictFor is a COARSE fits-on-GPU call from file size alone.
 *
 * It is deliberately not an estimate: the real sizer needs the GGUF header
 * (layer count, KV geometry), which we do not have before downloading. The
 * allowance below stands in for context + compute buffers so a quant that
 * exactly equals the VRAM target does not read as "fits". A proper
 * Range-read-the-header estimate is the next step; until then this is a hint,
 * and the model's own config page is the authority once it is on disk.
 */
export function verdictFor(sizeBytes: number, targetVramGB: number): FitVerdict {
  if (!targetVramGB || !sizeBytes) return "unknown";
  const gb = sizeBytes / 1024 ** 3;
  const withOverhead = gb * 1.15 + 0.6;
  if (withOverhead <= targetVramGB) return "fits";
  if (gb <= targetVramGB * 2) return "spills";
  return "toobig";
}

export function humanBytes(n: number): string {
  if (!n) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  // Two decimals from KiB up: quant sizes differ by tenths of a GiB, and "4 GiB"
  // vs "4 GiB" for two files that are 300 MiB apart is the number being useless.
  // Raw bytes stay whole — "512.00 B" is noise.
  return `${v.toFixed(i === 0 ? 0 : 2)} ${units[i]}`;
}

export function humanCount(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n ?? 0);
}
