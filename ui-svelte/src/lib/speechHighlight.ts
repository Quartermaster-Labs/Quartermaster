// Shows where the reader is: the chunk being spoken is tinted orange, everything
// already spoken is dimmed. Both are painted with the CSS Custom Highlight API
// (`CSS.highlights` + `::highlight(...)`) over live `Range`s rather than by
// wrapping text in spans. That matters here — the rendered reply carries copy
// buttons, mermaid/chart canvases, citation chips and Svelte-owned nodes, and
// splitting text nodes underneath all of that to insert markers would fight the
// renderer and break `use:` actions. Ranges touch nothing; feature-detect and do
// nothing at all where the API is missing (Firefox < 140).
//
// The hard part is that the spoken text is NOT what is on screen: speechText()
// flattens markdown to prose (drops code blocks, unwraps links, strips markers
// and citation brackets) before splitForSpeech() cuts it up. So chunks cannot be
// found in the DOM by substring search. Instead both sides are reduced to a
// stream of alphanumeric WORDS and aligned greedily, tolerating words the DOM
// has but the speech doesn't (a citation chip, a dropped code span) and vice
// versa. A mis-alignment is cosmetic, so the aligner prefers giving up on a
// chunk (null, nothing painted) over guessing.

/** Lowercased alphanumeric words — the only thing both sides reliably share. */
export function speechWords(text: string): string[] {
  return text.toLowerCase().match(/[\p{L}\p{N}]+/gu) ?? [];
}

export interface WordSpan {
  /** Index of the first DOM word of the chunk. */
  start: number;
  /** Index of the last DOM word of the chunk, inclusive. */
  end: number;
}

// How far ahead of the cursor a chunk's opening word may be found. Generous
// because whole regions of the DOM (a code block, a collapsed think box that
// slipped through) can sit between two consecutive spoken chunks.
const START_LOOKAHEAD = 600;
// How many DOM words may be skipped between two consecutive words of one chunk.
// Small: inside a chunk the two streams run in lockstep apart from the odd chip.
const INNER_SKIP = 10;
// A chunk whose words mostly failed to match is a bad alignment, not a chunk
// that happens to be missing — painting it would highlight the wrong sentence.
const MIN_MATCH_RATIO = 0.6;

/**
 * Align each chunk's words against the document's word stream, left to right.
 * Returns one span per chunk (null where no confident match was found).
 * Pure — this is the half that carries the tests.
 */
export function alignChunks(domWords: string[], chunks: string[][]): (WordSpan | null)[] {
  const out: (WordSpan | null)[] = [];
  let cursor = 0;
  for (const chunk of chunks) {
    if (!chunk.length) {
      out.push(null);
      continue;
    }
    const span = alignOne(domWords, chunk, cursor);
    out.push(span);
    // Only a confident match advances the cursor. A chunk that failed to align
    // must not drag the search position along with it, or every later chunk
    // inherits the mistake.
    if (span) cursor = span.end + 1;
  }
  return out;
}

function alignOne(domWords: string[], chunk: string[], cursor: number): WordSpan | null {
  const limit = Math.min(domWords.length, cursor + START_LOOKAHEAD);
  for (let s = cursor; s < limit; s++) {
    if (domWords[s] !== chunk[0]) continue;
    // A one-word coincidence ("the") is not a chunk start; require the second
    // word to land nearby too when there is one.
    if (chunk.length > 1 && findNear(domWords, chunk[1], s + 1, INNER_SKIP) < 0) continue;
    const span = walk(domWords, chunk, s);
    if (span) return span;
  }
  return null;
}

/** Follow a chunk word by word from a candidate start, allowing small skips. */
function walk(domWords: string[], chunk: string[], start: number): WordSpan | null {
  let at = start;
  let end = start;
  let matched = 1;
  for (let k = 1; k < chunk.length; k++) {
    const hit = findNear(domWords, chunk[k], at + 1, INNER_SKIP);
    if (hit < 0) continue; // word isn't on screen (stripped chip, dropped code)
    at = hit;
    end = hit;
    matched++;
  }
  return matched / chunk.length >= MIN_MATCH_RATIO ? { start, end } : null;
}

function findNear(domWords: string[], word: string, from: number, skip: number): number {
  const limit = Math.min(domWords.length, from + skip);
  for (let i = from; i < limit; i++) if (domWords[i] === word) return i;
  return -1;
}

// --- DOM side ---------------------------------------------------------------

interface DomWord {
  word: string;
  node: Text;
  /** Character offsets of the word inside `node`. */
  at: number;
  to: number;
}

// Regions whose text is on screen but is never spoken. Reasoning boxes are the
// dangerous one: stripThinking() removes them from the speech text, but they
// stay in the DOM (collapsed, not detached), so without this the aligner can
// find a chunk's opening words inside a thought and highlight that instead.
// `pre` but NOT inline `code`: speechText() drops fenced blocks outright, while
// an inline span keeps its text (only the backticks go), so those words are on
// both sides and skipping them would punch holes in the middle of a chunk.
const SKIP_IN = "details, pre, .diagram-block, .not-prose";

function collectDomWords(root: HTMLElement): DomWord[] {
  const out: DomWord[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      const parent = (node as Text).parentElement;
      if (!parent || parent.closest(SKIP_IN)) return NodeFilter.FILTER_REJECT;
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = n.nodeValue ?? "";
    for (const m of text.matchAll(/[\p{L}\p{N}]+/gu)) {
      out.push({ word: m[0].toLowerCase(), node: n as Text, at: m.index, to: m.index + m[0].length });
    }
  }
  return out;
}

/** One Range per chunk (null where the chunk could not be located on screen). */
export function speechRanges(root: HTMLElement, chunks: string[]): (Range | null)[] {
  const dom = collectDomWords(root);
  const spans = alignChunks(dom.map((w) => w.word), chunks.map(speechWords));
  return spans.map((s) => {
    if (!s || !dom[s.start] || !dom[s.end]) return null;
    const r = document.createRange();
    r.setStart(dom[s.start].node, dom[s.start].at);
    r.setEnd(dom[s.end].node, dom[s.end].to);
    return r;
  });
}

// --- highlight registry -----------------------------------------------------
// `CSS.highlights` is a single global registry, but any number of messages can
// own a speaker button. The owner token means a message clearing its highlight
// on stop can't wipe one another message just started.

const SPOKEN = "tts-spoken";
const ACTIVE = "tts-active";
let owner: object | null = null;

function supported(): boolean {
  return typeof CSS !== "undefined" && !!CSS.highlights && typeof Highlight !== "undefined";
}

/** Paint chunk `active` as current and everything before it as already read. */
export function showSpeechHighlight(o: object, ranges: (Range | null)[], active: number): void {
  if (!supported()) return;
  owner = o;
  const spoken = ranges.slice(0, Math.max(0, active)).filter((r): r is Range => !!r);
  const current = ranges[active];
  CSS.highlights.set(SPOKEN, new Highlight(...spoken));
  if (current) CSS.highlights.set(ACTIVE, new Highlight(current));
  else CSS.highlights.delete(ACTIVE);
}

export function clearSpeechHighlight(o: object): void {
  if (!supported() || owner !== o) return;
  owner = null;
  CSS.highlights.delete(SPOKEN);
  CSS.highlights.delete(ACTIVE);
}
