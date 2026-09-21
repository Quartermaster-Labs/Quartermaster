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
  // A file this project cannot load on its own: a README, a tokenizer, a
  // config, the .safetensors original a quant was made from. Listed so a repo
  // can always be assembled by hand, but kept behind the picker's "all files"
  // toggle and never sized.
  aux?: boolean;
  // Local, but the repo has since replaced this file under the same name: what
  // is on disk was fetched at a content id the hub no longer serves. Set only
  // when both ids are known, so it never fires on a hand-copied file we have
  // no record of. Downloading it overwrites the old copy.
  stale?: boolean;
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
  /** Whether opening the models folder in the OS file manager can work for THIS
   *  browser. False when quartermaster runs headless (a container has no
   *  xdg-open) or when the dashboard is open from another machine, where the
   *  server's file manager would appear on a screen nobody is watching. The UI
   *  browses the tree in-app instead. */
  canReveal?: boolean;
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

// --- the audio.cpp catalog ---

/** One downloadable set of repo files: a family at a precision. The files are
 *  ONE download, not alternatives — several packages ship a sidecar (a vocab.txt)
 *  the gguf is unusable without. */
export interface AudioCppPackage {
  id: string;
  displayName: string;
  description?: string;
  precision?: string;
  default?: boolean;
  repo: string;
  revision?: string;
  gated?: boolean;
  files: string[];
  /** Every file already on disk. A partly-present set is NOT local. */
  local: boolean;
  /** Total download for the whole set, 0 when the hub could not be asked and
   *  nothing is on disk to measure. */
  sizeBytes?: number;
}

export interface AudioCppFamily {
  family: string;
  displayName: string;
  description?: string;
  category?: string;
  status?: string;
  tasks?: string[];
  languages?: string[];
  packages: AudioCppPackage[];
  /** What a model of this family would be emitted as: "tts" | "asr" | "". */
  task?: string;
  /** False for a family audio.cpp can run and quartermaster cannot serve yet
   *  (music, separation, codecs) or one newer than our family table; `reason`
   *  says which, because they are different fixes. */
  supported: boolean;
  reason?: string;
}

export interface AudioCppCatalog {
  installed: boolean;
  backendId?: string;
  backendName?: string;
  specsDir?: string;
  modelsRoot?: string;
  families: AudioCppFamily[];
  error?: string;
}

/**
 * The audio.cpp catalog, read from the model_specs/ the INSTALLED backend ships.
 *
 * audio.cpp publishes its weights as a few shared GGUF repos rather than one
 * repo per model, so a hub search for them answers with hundreds of loose file
 * names. This is the same downloader (a package's files go to startHubDownload
 * unchanged); what the catalog adds is which files belong together.
 */
export async function getAudioCppCatalog(): Promise<AudioCppCatalog> {
  const c = await hubFetch<AudioCppCatalog>("/api/hub/audiocpp");
  return { ...c, families: c?.families ?? [] };
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

/** One entry in a models-folder listing. `size` is 0 for directories: a folder's
 *  real size means walking it, which is not worth a stat storm on a models tree. */
export interface HubFileEntry {
  name: string;
  path: string;
  dir: boolean;
  size: number;
  modified?: string;
}

/** One directory under the models root. `parent` is empty AT the root, which is
 *  how the UI knows not to offer a step up the server would refuse. */
export interface HubFolder {
  root: string;
  path: string;
  rel: string;
  parent: string;
  entries: HubFileEntry[];
  truncated?: boolean;
}

/**
 * listHubFolder reads one directory under the models root.
 *
 * The in-app answer to revealFolder: a headless server has no file manager to
 * open, so the browser renders the listing itself. Read-only, and the server
 * refuses any path outside the models root. No argument means the root.
 */
export function listHubFolder(path = ""): Promise<HubFolder> {
  const v = path ? `?path=${encodeURIComponent(path)}` : "";
  return hubFetch<HubFolder>(`/api/hub/files${v}`);
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

/**
 * estimateHubFiles sizes a whole repo over ONE connection, calling back per row
 * as the server resolves it.
 *
 * Deliberately not `Promise.all` over estimateHubFile: a browser allows six
 * connections per origin and the /api/events stream holds one for the life of
 * the page, so a pool of five per-row requests left the page with no socket at
 * all — pressing Download did nothing until a header fetch completed. The
 * server answers a repeated `path=` with NDJSON, one object per line, so this
 * costs one socket and still paints rows as they land instead of all at the end.
 *
 * Rows arrive in COMPLETION order, which is why the caller matches on
 * `e.path` rather than on the order it asked in.
 */
export async function estimateHubFiles(
  repo: string,
  paths: string[],
  source: string,
  onRow: (e: HubEstimate) => void,
  signal?: AbortSignal
): Promise<void> {
  if (paths.length === 0) return;
  if (paths.length === 1) {
    // One row is still a plain JSON answer, and asking for it that way keeps the
    // common single-file case off the streaming path entirely.
    onRow(await estimateHubFile(repo, paths[0], source));
    return;
  }
  const v = new URLSearchParams({ repo, source });
  for (const p of paths) v.append("path", p);
  const res = await fetch(`/api/hub/estimate?${v}`, { signal });
  if (!res.ok) throw new HubApiError(res.status, (await res.text()) || res.statusText);
  if (!res.body) return;

  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  const drain = (last: boolean): void => {
    // A chunk boundary can fall mid-line, so the tail is held back until the
    // newline that ends it arrives — except on the final flush.
    const lines = buf.split("\n");
    buf = last ? "" : (lines.pop() ?? "");
    for (const line of lines) {
      if (!line.trim()) continue;
      try {
        onRow(JSON.parse(line) as HubEstimate);
      } catch {
        // A truncated or malformed line costs that row its number, nothing more.
      }
    }
  };
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    drain(false);
  }
  buf += dec.decode();
  drain(true);
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

// force refetches files already on disk. The server detects an upstream
// replacement on its own (see hub.haveCurrent); this is the override for what
// it cannot see — a file swapped behind its back, or one downloaded before it
// started recording what it fetched.
export function startHubDownload(
  repo: string,
  files: string[],
  label = "",
  source = "hf",
  force = false
): Promise<{ jobId: string }> {
  return hubFetch<{ jobId: string }>("/api/hub/download", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source, repo, files, label, force }),
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

/**
 * clearFinishedDownloads dismisses the finished rows — done, errored and
 * canceled — from the downloads panel, and answers how many went.
 *
 * History only: nothing running or paused is touched and NO bytes are deleted,
 * which is the whole difference from cancel. An errored job keeps its partial
 * and its journal record, so it returns as a resumable paused row on the next
 * start rather than being silently destroyed by a tidy-up button.
 */
export async function clearFinishedDownloads(): Promise<number> {
  const r = await hubFetch<{ cleared: number }>("/api/hub/clear", { method: "POST" });
  return r.cleared;
}

// --- shaping the file list for the picker ---

export interface FileOption {
  group: string; // logical download key
  label: string; // the file's own name — see groupFiles
  files: HubFile[]; // every shard of this file — one logical download
  sizeBytes: number;
  projector: boolean; // an mmproj companion, not a model on its own
  aux: boolean; // see HubFile.aux — listed for completeness, never sized
  // Every shard is already on disk. Partly-downloaded sets are NOT local: one
  // shard of three is not a model, so the row keeps its download button (which
  // skips the shards already there).
  local: boolean;
  // ANY shard has been replaced upstream. Unlike `local` this is an OR: the set
  // is one model, so one superseded shard makes the whole thing the old
  // revision, and re-downloading fetches exactly the shards that moved.
  stale: boolean;
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
      opt = { group: f.group, label: baseName(f.group), files: [], sizeBytes: 0, projector: !!f.projector, aux: !!f.aux, local: true, stale: false };
      by.set(f.group, opt);
    }
    opt.files.push(f);
    opt.sizeBytes += f.sizeBytes;
    opt.local = opt.local && !!f.local;
    opt.stale = opt.stale || !!f.stale;
  }
  const out = [...by.values()];
  for (const o of out) o.files.sort((a, b) => (a.shard ?? 0) - (b.shard ?? 0));
  // Aux last, then projectors, then by size. An aux file is not a candidate:
  // sorting the repo's README between two quants by byte count would put it
  // exactly where the eye is looking for the smallest model.
  out.sort((a, b) => Number(a.aux) - Number(b.aux) || Number(a.projector) - Number(b.projector) || a.sizeBytes - b.sizeBytes);
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
