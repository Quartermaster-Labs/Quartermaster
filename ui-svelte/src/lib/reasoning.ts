// Helpers for normalizing model reasoning markup before rendering.

// Harmony control tokens (gpt-oss): the role/turn delimiters. Stripped, leaving
// channel markers for harmonyToThink to interpret. Tolerates a missing closing
// pipe (`<|end>` as well as `<|end|>`) — some templates render them that way.
const HARMONY_CTRL = /<\|(?:start|end|return|constrain)\|?>(?:assistant|user|system)?/gi;
const HARMONY_MSG = /<\|message\|?>/g;
const CHANNEL_RE = /<\|channel\|?>\s*([a-zA-Z]+)\s*(?:<\|message\|?>)?/g;

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
      out += `<think>${body}`; // last segment, still streaming — leave open
    } else {
      out += `<think>${body}</think>`;
    }
  }
  return out;
}
