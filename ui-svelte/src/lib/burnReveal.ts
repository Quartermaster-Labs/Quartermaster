// Paced, "branded" reveal of a streaming chat answer, pressed in by an
// invisible sweep running along the text. Driven by ChatMessage.svelte; four
// parts:
//
//   - batchEnd: how much of the received text to release as the next batch.
//     Everything that arrived while the previous batch was being pressed, cut
//     at the last complete word.
//
//   - planSweep: given the laid-out words of a new batch, when each one lights
//     and the path the mark takes along them. The mark runs at a calm speed
//     while it can, and faster only when a batch would otherwise take longer
//     than MAX_TRACE_MS, so the text trails a fast model by a bounded amount.
//     (Pressing a long batch as one column across all its lines kept the speed
//     fixed, but several lines appearing at once read wrong.)
//
//   - pathAt: the sweep's position on that path at a given time, which is
//     where the next batch picks up.
//
//   - BurnTracker: wraps each word of a batch in a `.qm-burn` span whose CSS
//     animation (index.css) cools it from a hot glow to the normal text colour,
//     started at the word's planned ignition time. The live markdown block is
//     re-rendered through {@html} on every batch, so any span added to it is
//     thrown away a moment later. Rather than fight that, every pass re-wraps
//     whatever is still cooling with an animation-delay of (ignition - now):
//     negative for a word already cooling (it resumes mid-animation), positive
//     for one the sweep has not reached (held transparent by `backwards` fill).

/** How often the reveal checks whether the mark is free for the next batch. */
export const POLL_MS = 40;
/** How long a word takes to cool. Must match the .qm-burn animation. */
export const BURN_MS = 700;
/**
 * The sweep's calmest speed, local px/s, about a 700px line in half a second.
 * Quick on purpose: it only has to read as left-to-right, not be watched.
 */
export const WHEEL_PX_S = 1400;
/** The most a batch may take to trace; past this the sweep speeds up instead. */
export const MAX_TRACE_MS = 350;
// An incomplete trailing word is held back for the next token, unless it has
// grown this long (a URL, a code line, CJK text with no spaces).
const HOLD_MAX = 40;

const isSpace = (c: string) => c === " " || c === "\n" || c === "\t" || c === "\r";

/**
 * The end of the next batch when `shown` characters of `target` are on screen:
 * everything received, minus a trailing word still being streamed. `done` =
 * the stream has ended, so nothing more will arrive to complete it.
 */
export function batchEnd(shown: number, target: string, done: boolean): number {
  const len = target.length;
  if (shown >= len || done) return len;
  let i = len;
  while (i > shown && !isSpace(target[i - 1])) i--;
  if (i > shown) return i;
  return len - shown >= HOLD_MAX ? len : shown;
}

/** A laid-out word, in the wrapper's local px. `y` is the line's centre. */
export interface WordBox {
  x0: number;
  x1: number;
  y: number;
}

/** A point on the mark's path. Two consecutive points at one `t` = a hop. */
export interface PathPt {
  x: number;
  y: number;
  t: number;
}

export interface SweepPlan {
  /** Ignition time per word, same order as the input. */
  ignite: number[];
  path: PathPt[];
  /** When the batch is fully pressed and the mark is free for the next one. */
  end: number;
}

const sameLine = (a: number, b: number) => Math.abs(a - b) < 4;

/**
 * Plan how the batch `words` (in reading order) is pressed, starting at `t0`
 * with the mark at `from` (null = not on screen yet). The mark runs along the
 * text, hopping to the start of each new line, and a word lights as the mark
 * reaches its middle. It moves at `speed`, or faster when the batch would
 * otherwise take longer than `maxMs`: that keeps the text from falling ever
 * further behind a fast model, one line at a time rather than in blocks.
 */
export function planSweep(words: WordBox[], from: { x: number; y: number } | null, t0: number, speed = WHEEL_PX_S, maxMs = MAX_TRACE_MS): SweepPlan {
  if (words.length === 0) return { ignite: [], path: from ? [{ ...from, t: t0 }] : [], end: t0 };

  // Lay the route out in px travelled, then turn distance into time.
  const mid: number[] = [];
  let pos = from ?? { x: words[0].x0, y: words[0].y };
  let d = 0;
  const route: { x: number; y: number; d: number }[] = [{ ...pos, d }];
  for (const w of words) {
    if (!sameLine(w.y, pos.y) || w.x1 < pos.x) {
      pos = { x: w.x0, y: w.y };
      route.push({ ...pos, d });
    }
    mid.push(d + Math.max(0, (w.x0 + w.x1) / 2 - pos.x));
    const to = Math.max(pos.x, w.x1);
    d += to - pos.x;
    pos = { x: to, y: w.y };
    route.push({ ...pos, d });
  }
  const perMs = Math.max(speed / 1000, d / maxMs);
  return {
    ignite: mid.map((m) => t0 + m / perMs),
    path: route.map((p) => ({ x: p.x, y: p.y, t: t0 + p.d / perMs })),
    end: t0 + d / perMs,
  };
}

/** Where the mark is on `path` at time `t` (clamped to its ends). */
export function pathAt(path: PathPt[], t: number): { x: number; y: number } | null {
  if (path.length === 0) return null;
  if (t <= path[0].t) return { x: path[0].x, y: path[0].y };
  for (let i = 1; i < path.length; i++) {
    const b = path[i];
    if (t < b.t) {
      const a = path[i - 1];
      const f = (t - a.t) / (b.t - a.t);
      return { x: a.x + (b.x - a.x) * f, y: a.y + (b.y - a.y) * f };
    }
  }
  const z = path[path.length - 1];
  return { x: z.x, y: z.y };
}

interface Chunk {
  start: number;
  end: number;
  born: number;
  // Ignition times keyed by each word's rendered start offset, ascending. A
  // rebuilt node looks its words up by offset; a word the markdown re-flowed
  // onto a slightly different offset takes the nearest one before it.
  offs: number[];
  times: number[];
  // Latest ignition in the chunk; it is live until that word has cooled.
  until: number;
}

// Subtrees that are not flowing answer text: reasoning boxes (their own
// stream), rendered diagrams and math (splitting their text breaks layout),
// and injected controls such as the code block copy button.
const SKIP = "details, svg, .katex, button, [data-burn-skip]";

/** Every text node under `root` that counts as answer text, in order. */
function textNodes(root: HTMLElement): Text[] {
  const out: Text[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, {
    acceptNode(n) {
      if (n.nodeType === Node.ELEMENT_NODE) {
        return (n as Element).matches(SKIP) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_SKIP;
      }
      // Text sitting directly under the root is the whitespace {@html} leaves
      // between blocks. It is also the boundary Svelte uses to find and remove
      // a block's nodes, so it must never be split.
      return n.parentNode === root ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT;
    },
  });
  for (let n = walker.nextNode(); n; n = walker.nextNode()) out.push(n as Text);
  return out;
}

export class BurnTracker {
  private chunks: Chunk[] = [];
  // Rendered-text length already accounted for (a chunk was stamped up to it).
  private len = -1;

  /**
   * Stamp new text as a chunk (when `record`) and wrap every chunk still
   * cooling. Returns the word spans of the chunk stamped on THIS call, in
   * reading order, for the caller to lay out and plan; they burn from `now`
   * until `setTimes` says otherwise.
   *
   * Offsets are in RENDERED text, not markdown source: the reveal paces the
   * source, but only what the DOM shows can be wrapped. The first call adopts
   * whatever is already there without burning it, so a reload or a thread
   * switch mid-stream does not light up the whole answer.
   */
  update(root: HTMLElement, now: number, record: boolean): HTMLElement[] {
    const nodes = textNodes(root);
    let total = 0;
    for (const t of nodes) total += t.data.length;

    let fresh: Chunk | null = null;
    if (this.len < 0 || !record) this.len = total;
    else if (total > this.len) {
      fresh = { start: this.len, end: total, born: now, offs: [], times: [], until: now };
      this.chunks.push(fresh);
      this.len = total;
    } else if (total < this.len) {
      // The markdown re-flowed shorter (a `**` pair closing into bold, a
      // regenerate). Drop what no longer exists; the rest re-anchors next pass.
      this.len = total;
      this.chunks = this.chunks.filter((c) => c.start < total);
      for (const c of this.chunks) c.end = Math.min(c.end, total);
    }
    this.chunks = this.chunks.filter((c) => now - c.until < BURN_MS);

    const spans: HTMLElement[] = [];
    let off = total;
    const oldest = this.chunks.length ? this.chunks[0].start : total;
    // Walk back from the end: only the tail can hold a cooling chunk.
    for (let i = nodes.length - 1; i >= 0; i--) {
      const node = nodes[i];
      const nodeEnd = off;
      off -= node.data.length;
      if (nodeEnd <= oldest) break;
      // Wrapped on an earlier pass and not re-rendered since: its animation is
      // already running on the right clock.
      if ((node.parentNode as Element | null)?.classList?.contains("qm-burn")) continue;
      this.wrap(node, off, now, fresh, spans);
    }
    return spans.reverse();
  }

  /** Give the words `update` just returned their planned ignition times. */
  setTimes(spans: HTMLElement[], ignite: number[], now: number): void {
    const c = this.chunks[this.chunks.length - 1];
    if (!c) return;
    c.offs = [];
    c.times = [];
    spans.forEach((s, i) => {
      c.offs.push(Number(s.dataset.o));
      c.times.push(ignite[i]);
      s.style.animationDelay = `${Math.round(ignite[i] - now)}ms`;
    });
    c.until = Math.max(c.born, ...ignite);
  }

  /** Whether any word is still waiting or cooling (keep passing until not). */
  get active(): boolean {
    return this.chunks.length > 0;
  }

  reset(): void {
    this.chunks = [];
    this.len = -1;
  }

  /**
   * Unwrap every `.qm-burn` span under `root` once the reveal is over. A
   * finished answer otherwise keeps one spent animation per word, and some of
   * them never get dropped: a completed markdown block is not re-rendered, so
   * its spans outlive the reveal indefinitely.
   */
  settle(root: HTMLElement): void {
    this.chunks = [];
    const parents = new Set<Node>();
    for (const s of root.querySelectorAll<HTMLElement>(".qm-burn")) {
      const p = s.parentNode;
      if (!p) continue;
      s.replaceWith(...s.childNodes);
      parents.add(p);
    }
    // Re-join the split text. Never the root: its direct text nodes are the
    // boundaries {@html} uses to find its blocks.
    for (const p of parents) if (p !== root) p.normalize();
  }

  // Split `node` (starting at rendered offset `at`) around every chunk it
  // overlaps and wrap each piece. Right to left, so `node` stays the untouched
  // leading part and earlier offsets stay valid.
  private wrap(node: Text, at: number, now: number, fresh: Chunk | null, out: HTMLElement[]): void {
    const end = at + node.data.length;
    for (let k = this.chunks.length - 1; k >= 0; k--) {
      const c = this.chunks[k];
      const a = Math.max(c.start, at) - at;
      const b = Math.min(c.end, end) - at;
      if (b <= a) continue;
      if (b < node.data.length) node.splitText(b);
      const piece = a > 0 ? node.splitText(a) : node;
      this.wrapWords(piece, at + a, c, now, c === fresh ? out : null);
      if (a === 0) return;
    }
  }

  // One span per word (plus its trailing space), right to left.
  private wrapWords(piece: Text, at: number, c: Chunk, now: number, out: HTMLElement[] | null): void {
    const words = [...piece.data.matchAll(/\S+\s*/g)];
    for (let w = words.length - 1; w >= 0; w--) {
      const m = words[w];
      const start = m.index!;
      const stop = start + m[0].length;
      if (stop < piece.data.length) piece.splitText(stop);
      const word = start > 0 ? piece.splitText(start) : piece;
      const o = at + start;
      const el = document.createElement("span");
      el.className = "qm-burn";
      el.dataset.o = String(o);
      el.style.animationDelay = `${Math.round(igniteAt(c, o) - now)}ms`;
      word.parentNode!.insertBefore(el, word);
      el.appendChild(word);
      out?.push(el);
    }
  }
}

function igniteAt(c: Chunk, off: number): number {
  let t = c.born;
  for (let i = 0; i < c.offs.length && c.offs[i] <= off; i++) t = c.times[i];
  return t;
}
