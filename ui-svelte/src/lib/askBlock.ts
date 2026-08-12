// Click-through answer wizard.
//
// Shopping (and any other mode that has to pin down a brief) ends its turn with
// a question set instead of a numbered list the user has to retype. The model
// writes it as a fenced ```ask block holding JSON; this parses it out, the
// AskWizard renders chips, and the user's clicks are composed back into one
// ordinary user message. No server involvement — it's a turn boundary either way.
export type AskType = "single" | "multi" | "text";

export interface AskQuestion {
  id: string;
  label: string;
  type: AskType;
  options: string[];
  // Free-text field alongside the chips ("something else"). Implicit for `text`.
  allowOther: boolean;
}

// Models routinely put an escape hatch in `options` ("Other", "Other (please
// specify)", "Something else") instead of setting allowOther. Clicking one has to
// open the free-text field, not count as an answer and advance — "Other" on its
// own carries no information, which is exactly the case the field exists for.
const OTHER_WORDS = new Set([
  "other",
  "others",
  "other option",
  "custom",
  "something else",
  "none of these",
  "none of the above",
  "not listed",
  "let me specify",
  "specify",
  "write my own",
]);

/** True when an option is an "escape hatch" that should reveal the free-text field. */
export function isOtherOption(opt: string): boolean {
  const s = opt
    .trim()
    .toLowerCase()
    .replace(/\s*\([^)]*\)\s*$/, "") // "Other (please specify)"
    .replace(/[\s.…:;!?*_\-–—]+$/g, ""); // "Other…", "Other -"
  return OTHER_WORDS.has(s);
}

export interface AskBlock {
  questions: AskQuestion[];
  // The message text with the ```ask fence removed — what actually gets rendered.
  cleaned: string;
}

export interface AskSplit {
  cleaned: string;
  // Parsed questions once the block is complete and valid; null otherwise.
  questions: AskQuestion[] | null;
  // The fence is still being written (streaming). The caller shows a placeholder
  // label — half-typed JSON must never reach the user.
  pending: boolean;
}

const FENCE = /```ask[ \t]*\r?\n([\s\S]*?)```[ \t]*(?:\r?\n|$)/i;
const OPEN_FENCE = /```ask[ \t]*(\r?\n|$)/i;

function asStringList(v: unknown): string[] {
  if (!Array.isArray(v)) return [];
  return v.filter((x): x is string => typeof x === "string" && x.trim() !== "").map((x) => x.trim());
}

/**
 * Pull the ```ask block out of an assistant message. Returns null when there
 * isn't one, or when it isn't usable — a malformed block must fall through to
 * being rendered as an ordinary code fence, never swallow the message.
 */
export function parseAskBlock(content: string): AskBlock | null {
  const m = FENCE.exec(content);
  if (!m) return null;

  let raw: unknown;
  try {
    raw = JSON.parse(m[1]);
  } catch {
    return null;
  }
  const list = Array.isArray(raw) ? raw : (raw as { questions?: unknown })?.questions;
  if (!Array.isArray(list)) return null;

  const questions: AskQuestion[] = [];
  for (const q of list) {
    if (!q || typeof q !== "object") continue;
    const o = q as Record<string, unknown>;
    const label = typeof o.label === "string" ? o.label.trim() : typeof o.question === "string" ? o.question.trim() : "";
    if (!label) continue;
    const options = asStringList(o.options ?? o.choices);
    const t = typeof o.type === "string" ? o.type.toLowerCase() : "";
    const type: AskType = options.length === 0 ? "text" : t === "multi" || t === "multiple" ? "multi" : t === "text" ? "text" : "single";
    questions.push({
      id: typeof o.id === "string" && o.id.trim() ? o.id.trim() : `q${questions.length + 1}`,
      label,
      type,
      options: type === "text" ? [] : options,
      allowOther: type === "text" ? true : o.allowOther !== false && o.other !== false,
    });
  }
  if (questions.length === 0) return null;

  return { questions, cleaned: (content.slice(0, m.index) + content.slice(m.index + m[0].length)).trim() };
}

/**
 * Split an assistant message into prose + question block, including the
 * mid-stream case: an ```ask fence that hasn't closed yet is cut from the prose
 * and reported as `pending` so the UI can show "writing options…" instead of a
 * growing wall of JSON.
 */
export function splitAsk(content: string): AskSplit {
  const done = parseAskBlock(content);
  if (done) return { cleaned: done.cleaned, questions: done.questions, pending: false };

  // A closed-but-broken fence stays in the text (renders as a code block); only
  // an unterminated one is hidden, since it is still being written.
  if (FENCE.test(content)) return { cleaned: content, questions: null, pending: false };
  const open = OPEN_FENCE.exec(content);
  if (open) return { cleaned: content.slice(0, open.index).trimEnd(), questions: null, pending: true };
  return { cleaned: content, questions: null, pending: false };
}

/**
 * Compose the picked answers into the message the user sends back. Questions the
 * user left empty are reported as such — silence would otherwise read to the
 * model as "no budget", and it would invent one.
 */
export function composeAskAnswer(questions: AskQuestion[], answers: Record<string, string[]>): string {
  const lines = questions.map((q) => {
    const picked = (answers[q.id] ?? []).filter((v) => v.trim() !== "");
    return `${q.label}: ${picked.length ? picked.join(", ") : "no preference"}`;
  });
  return lines.join("\n");
}
