// Helpers for normalizing model reasoning markup before rendering.

// Harmony control tokens (gpt-oss): the role/turn delimiters. Stripped, leaving
// channel markers for harmonyToThink to interpret. Tolerates a missing closing
// pipe (`<|end>` as well as `<|end|>`) — some templates render them that way.
const HARMONY_CTRL = /<\|(?:start|end|return|constrain)\|?>(?:assistant|user|system)?/gi;
const HARMONY_MSG = /<\|message\|?>/g;
const CHANNEL_RE = /<\|channel\|?>\s*([a-zA-Z]+)\s*(?:<\|message\|?>)?/g;

// Conversational openers a reasoning trace almost always starts with. They say
// nothing about the content, so a title built from the raw first sentence reads
// "Okay, so let me think about…" every single time. Stripped repeatedly (a trace
// stacks them: "Okay, so first, the user wants…").
const THINK_FILLER =
  /^(?:ok(?:ay)?|alright|so|hmm+|umm?|well|right|now|then|first(?:ly)?|next|actually|basically|indeed|sure|let(?:'|’)?s(?: see| think)?|let me(?: think| see)?|i(?:'|’)?ll|i need to|i should|i must|i want to|we need to)\b[\s,.:;—–-]*/i;

// A one-line gist of a reasoning block, for the "Thought for 2s · …" header.
// Deliberately local heuristics, not a model call: a title round-trip would
// either swap a model into VRAM or add latency to every collapsed box.
// Returns "" when there is nothing usefully short to say.
export function thinkSummary(text: string, max = 52): string {
  let t = (text ?? "")
    .replace(/```[\s\S]*?(?:```|$)/g, " ") // code fences carry no gist
    .replace(/`([^`]*)`/g, "$1")
    .replace(/^\s{0,3}#{1,6}\s+/gm, "")
    .replace(/^\s{0,3}[-*+]\s+/gm, "")
    .replace(/(\*\*|__|~~)/g, "")
    .replace(/\s+/g, " ")
    .trim();
  for (let i = 0; i < 4 && t; i++) {
    const stripped = t.replace(THINK_FILLER, "");
    if (stripped === t) break;
    t = stripped.trim();
  }
  if (!t) return "";
  // First sentence - but a trace often opens with a stub ("Right."), so walk on
  // until one is long enough to mean something.
  const parts = t.split(/(?<=[.!?])\s+/);
  let s = "";
  for (const p of parts) {
    s = p.trim();
    if (s.length >= 12) break;
  }
  if (!s) return "";
  s = s.replace(/[.,;:\s]+$/, "");
  if (s.length > max) {
    const cut = s.slice(0, max);
    const sp = cut.lastIndexOf(" ");
    s = (sp > max / 2 ? cut.slice(0, sp) : cut).replace(/[.,;:\s]+$/, "") + "…";
  }
  if (s.length < 6) return "";
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// activityLabel names what the model actually DID in a reasoning box, from the
// tool cards nested inside it: "Searched the web", "Read 2 pages". The verb comes
// from the turn's own tool records, so it is true by construction - asking a
// model to describe its activity was tried and does not survive at the size of
// the vendored title model (see internal/server/titlegen.go).
//
// Returns "" when the box ran no tools; the caller then keeps "Thought for Xs".
// Pure so it can be unit-tested.
export function activityLabel(kinds: string[]): string {
  // fetch_page and a youtube transcript are both "went and read a document";
  // everything else that hits the network is a query. The instant local tools
  // (time/calc/units/currency) are deliberately excluded: "Looked something up"
  // for a unit conversion is noise next to a real gist.
  let reads = 0;
  let queries = 0;
  const qk = new Set<string>();
  for (const k of kinds) {
    switch (k) {
      case "page":
      case "youtube":
        reads++;
        break;
      case "web":
      case "wiki":
      case "quartermaster":
      case "youtube-search":
      case "youtube-comments":
      case "feed":
      case "weather":
        queries++;
        qk.add(k);
        break;
    }
  }
  const pages = reads === 1 ? "1 page" : `${reads} pages`;
  if (queries && reads) return `Searched and read ${pages}`;
  if (reads) return `Read ${pages}`;
  if (!queries) return "";
  // A single-source box can name its source; a mixed one cannot without turning
  // into a list, so it stays generic.
  if (qk.size === 1) {
    switch ([...qk][0]) {
      // `wiki` is the embedded quartermaster help wiki (lib/wiki.ts), not Wikipedia.
      case "wiki":
        return "Searched the help wiki";
      case "quartermaster":
        return "Checked the server";
      case "youtube-search":
      case "youtube-comments":
        return "Searched YouTube";
      case "feed":
        return "Checked a feed";
      case "weather":
        return "Checked the weather";
    }
  }
  return queries === 1 ? "Searched the web" : `Searched the web ${queries}×`;
}

// Rewrite harmony channel markup into <think> blocks. Some models (gpt-oss
// harmony et al.) emit reasoning as channel markup
// (`<|channel|>analysis<|message|>…<|end|>…<|channel|>final<|message|>…`) that
// llama.cpp's `--reasoning-format auto` doesn't parse, so it leaks raw into the
// content. Each `<|channel|>NAME<|message|>…` segment runs until the next
// channel marker / control token / end of string; the `final` (and
// `commentary`) channel is the answer, every other channel (analysis, thought,
// reasoning, …) is reasoning. The last non-final segment is left unclosed
// (`<think>…`) so it streams open via the tokenizer's unclosed-think handling.
// No-op when no channel markup is present.
export function harmonyToThink(text: string): string {
  CHANNEL_RE.lastIndex = 0;
  const markers: { end: number; idx: number; channel: string }[] = [];
  let m: RegExpExecArray | null;
  while ((m = CHANNEL_RE.exec(text))) {
    markers.push({ idx: m.index, end: m.index + m[0].length, channel: m[1].toLowerCase() });
  }
  if (markers.length === 0) return text;
  const clean = (s: string) => s.replace(HARMONY_CTRL, "").replace(HARMONY_MSG, "");
  let out = clean(text.slice(0, markers[0].idx));
  for (let i = 0; i < markers.length; i++) {
    const body = clean(text.slice(markers[i].end, i + 1 < markers.length ? markers[i + 1].idx : text.length));
    const isFinal = markers[i].channel === "final" || markers[i].channel === "commentary";
    if (isFinal) {
      out += body;
    } else if (i === markers.length - 1) {
      out += `<think>${body}`; // last segment, still streaming - leave open
    } else {
      out += `<think>${body}</think>`;
    }
  }
  return out;
}
