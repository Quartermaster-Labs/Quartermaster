import { inferenceHeaders } from "./inferenceAuth";

// Client for POST /v1/systemone, the TypeSafe-compatible System One route that
// llama-server serves for decision models. Quartermaster proxies it like any
// other model-dispatched route: the model rides in the JSON body.
//
// The request is a STATE (what is going on), optional FILES (images, or audio on
// omni models) and a set of QUESTIONS about it. The answer is a probability
// distribution per question rather than free text, which is why this is its own
// tab and not a chat. Validation is the server's job; the helpers here only
// stop the obviously-empty fields and shape the body.

export type QuestionType = "choice" | "score" | "noul";

export const QUESTION_TYPES: { id: QuestionType; label: string; hint: string }[] = [
  { id: "choice", label: "Choice", hint: "Pick one of several named options" },
  { id: "score", label: "Score", hint: "Rate on an ordered scale of 2 to 10 levels" },
  { id: "noul", label: "True / false", hint: "Probability that a statement is true" },
];

export const MIN_CHOICES = 2;
export const MIN_LEVELS = 2;
export const MAX_LEVELS = 10;
export const MAX_FILES = 8;

export interface ChoiceRow {
  option: string;
  description: string;
}

// What the builder edits. All three criteria shapes live on the draft at once so
// flipping the type back and forth does not throw away what was typed.
export interface DraftQuestion {
  uid: string;
  id: string;
  type: QuestionType;
  instructions: string;
  choices: ChoiceRow[];
  levels: string[];
  trueDesc: string;
  falseDesc: string;
}

export function newUid(): string {
  return Math.random().toString(36).slice(2, 10);
}

export function newQuestion(id: string, type: QuestionType = "choice"): DraftQuestion {
  return {
    uid: newUid(),
    id,
    type,
    instructions: "",
    choices: [
      { option: "", description: "" },
      { option: "", description: "" },
    ],
    levels: ["", ""],
    trueDesc: "",
    falseDesc: "",
  };
}

// The first "q<n>" no question uses yet.
export function nextQuestionId(existing: { id: string }[]): string {
  const used = new Set(existing.map((q) => q.id.trim()));
  for (let n = existing.length + 1; ; n++) if (!used.has(`q${n}`)) return `q${n}`;
}

// A saved question set comes back from the per-user prefs blob, which any older
// build (or a hand edit) may have written. Repair rather than trust it: a draft
// with a missing array would crash the builder on the first render.
export function normalizeDrafts(raw: unknown): DraftQuestion[] {
  if (!Array.isArray(raw)) return [];
  const out: DraftQuestion[] = [];
  for (const r of raw) {
    if (!r || typeof r !== "object") continue;
    const o = r as Record<string, unknown>;
    const type: QuestionType = o.type === "score" || o.type === "noul" ? o.type : "choice";
    const base = newQuestion(typeof o.id === "string" ? o.id : "", type);
    if (typeof o.uid === "string" && o.uid) base.uid = o.uid;
    if (typeof o.instructions === "string") base.instructions = o.instructions;
    if (Array.isArray(o.choices)) {
      base.choices = o.choices
        .filter((c): c is Record<string, unknown> => !!c && typeof c === "object")
        .map((c) => ({
          option: typeof c.option === "string" ? c.option : "",
          description: typeof c.description === "string" ? c.description : "",
        }));
    }
    if (Array.isArray(o.levels)) base.levels = o.levels.map((l) => (typeof l === "string" ? l : ""));
    if (typeof o.trueDesc === "string") base.trueDesc = o.trueDesc;
    if (typeof o.falseDesc === "string") base.falseDesc = o.falseDesc;
    out.push(base);
  }
  return out;
}

// Text that may be structured: a JSON object or array is sent as JSON, anything
// else (including a bare number or a quoted string, which are far more likely
// prose that happens to parse) is sent as the plain string the user typed.
export function parseLoose(text: string): unknown {
  const t = text.trim();
  if (t.startsWith("{") || t.startsWith("[")) {
    try {
      const v = JSON.parse(t);
      if (v && typeof v === "object") return v;
    } catch {
      /* not JSON after all: fall through to the string */
    }
  }
  return text;
}

// Rows the user left completely blank are dropped, not reported: an "add option"
// click followed by no typing is not a mistake worth stopping a run for.
function filledChoices(q: DraftQuestion): ChoiceRow[] {
  return q.choices.filter((c) => c.option.trim() || c.description.trim());
}

function criteriaFor(q: DraftQuestion): unknown {
  if (q.type === "choice") {
    const o: Record<string, string | null> = {};
    for (const c of filledChoices(q)) o[c.option.trim()] = c.description.trim() || null;
    return o;
  }
  if (q.type === "score") return q.levels.map((l) => l.trim());
  const t = q.trueDesc.trim();
  const f = q.falseDesc.trim();
  if (!t && !f) return undefined;
  const o: Record<string, string> = {};
  if (t) o.true = t;
  if (f) o.false = f;
  return o;
}

export interface SystemOneRequest {
  model: string;
  state: unknown;
  questions: Record<string, { type: QuestionType; instructions?: unknown; criteria?: unknown }>;
  files?: string[];
}

export function buildRequest(model: string, stateText: string, questions: DraftQuestion[], files: string[] = []): SystemOneRequest {
  const qs: SystemOneRequest["questions"] = {};
  for (const q of questions) {
    const criteria = criteriaFor(q);
    qs[q.id.trim()] = {
      type: q.type,
      // Omitted when blank: vllm-d3 asks without instructions, and llama-server
      // is the one that knows which models may.
      ...(q.instructions.trim() ? { instructions: parseLoose(q.instructions) } : {}),
      ...(criteria === undefined ? {} : { criteria }),
    };
  }
  const req: SystemOneRequest = { model, state: stateText.trim() ? parseLoose(stateText) : null, questions: qs };
  if (files.length) req.files = files;
  return req;
}

// Only what is obviously missing. Whether two options are distinguishable, or a
// score scale is sensible, is the model server's call and it says so in a 400.
export function validate(model: string, questions: DraftQuestion[]): string[] {
  const errs: string[] = [];
  if (!model) errs.push("Pick a decision model.");
  // No state or instruction checks: lfm2-d1, lfm2-d1-omni and vllm-d3 take a
  // null state and vllm-d3 a question without instructions. What a model
  // accepts is llama-server's call, and its 400 is shown verbatim.
  if (questions.length === 0) errs.push("Add at least one question.");
  const seen = new Set<string>();
  questions.forEach((q, i) => {
    const n = `Question ${i + 1}`;
    const id = q.id.trim();
    if (!id) errs.push(`${n}: needs an id.`);
    else if (seen.has(id)) errs.push(`${n}: id "${id}" is used twice.`);
    seen.add(id);
    if (q.type === "choice") {
      const rows = filledChoices(q);
      if (rows.some((c) => !c.option.trim())) errs.push(`${n}: an option has a description but no name.`);
      const names = rows.map((c) => c.option.trim()).filter(Boolean);
      if (names.length < MIN_CHOICES) errs.push(`${n}: needs at least ${MIN_CHOICES} options.`);
      if (new Set(names).size !== names.length) errs.push(`${n}: two options share a name.`);
    } else if (q.type === "score") {
      if (q.levels.length < MIN_LEVELS || q.levels.length > MAX_LEVELS) errs.push(`${n}: a score needs ${MIN_LEVELS} to ${MAX_LEVELS} levels.`);
      if (q.levels.some((l) => !l.trim())) errs.push(`${n}: every score level needs a description.`);
    }
  });
  return errs;
}

// --- response ---------------------------------------------------------------

export interface ChoiceAnswer {
  type: "choice";
  choice: string;
  probabilities: Record<string, number>;
  confidence?: number;
}
export interface ScoreAnswer {
  type: "score";
  score: number;
  legend?: Record<string, string>;
  probabilities: Record<string, number>;
  confidence?: number;
}
export interface NoulAnswer {
  type: "noul";
  noul: number;
}
export type Answer = ChoiceAnswer | ScoreAnswer | NoulAnswer;

export interface SystemOneResponse {
  model?: string;
  answers: Record<string, Answer>;
  usage?: { input_tokens?: number; output_tokens?: number };
}

// Highest probability first; ties keep the server's order.
export function sortedProbs(p: Record<string, number> | undefined): [string, number][] {
  return Object.entries(p ?? {}).sort((a, b) => b[1] - a[1]);
}

// Score levels in scale order. The keys are the stringified level numbers.
export function levelProbs(a: ScoreAnswer): { level: number; label: string; p: number }[] {
  return Object.entries(a.probabilities ?? {})
    .map(([k, p]) => ({ level: Number(k), label: a.legend?.[k] ?? "", p }))
    .filter((r) => Number.isFinite(r.level))
    .sort((x, y) => x.level - y.level);
}

// Where the expected score sits along the scale, 0..100. `levels` is how many
// levels the scale has; a one-level scale has nowhere to put a marker.
export function scorePercent(score: number, levels: number): number {
  if (levels < 2 || !Number.isFinite(score)) return 0;
  return Math.max(0, Math.min(100, (score / (levels - 1)) * 100));
}

export function pct(p: number | undefined, digits = 0): string {
  if (typeof p !== "number" || !Number.isFinite(p)) return "-";
  return `${(p * 100).toFixed(digits)}%`;
}

// llama-server answers {error:{message}}; quartermaster's own failures can be
// plain text. Show whichever is there.
export function errorMessage(status: number, body: string): string {
  let msg = body.trim();
  try {
    const j = JSON.parse(body);
    const e = j?.error;
    if (typeof e === "string") msg = e;
    else if (typeof e?.message === "string") msg = e.message;
    else if (typeof j?.message === "string") msg = j.message;
  } catch {
    /* plain text */
  }
  const hint =
    status === 501
      ? " (this model is not a decision model, or has no image support)"
      : status === 404
        ? " (unknown model, or a restricted listener does not expose it)"
        : "";
  return `${status}${msg ? `: ${msg}` : ""}${hint}`;
}

export async function runDecision(req: SystemOneRequest, signal?: AbortSignal): Promise<SystemOneResponse> {
  const res = await fetch("/v1/systemone", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(req),
    signal,
  });
  const text = await res.text();
  if (!res.ok) throw new Error(errorMessage(res.status, text));
  let json: SystemOneResponse;
  try {
    json = JSON.parse(text);
  } catch {
    throw new Error("The server answered with something that is not JSON.");
  }
  if (!json || typeof json.answers !== "object" || json.answers === null) throw new Error("The response carried no answers.");
  return json;
}

// A file as a data URL, unchanged. Images go through normalizeImageFile instead
// (WebP and friends must be re-encoded); audio has no such problem.
export function readAsDataUrl(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result as string);
    r.onerror = () => reject(r.error ?? new Error("could not read file"));
    r.readAsDataURL(file);
  });
}
