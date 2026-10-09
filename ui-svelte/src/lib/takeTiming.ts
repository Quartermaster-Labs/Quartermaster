// Read-along timing for a Speech studio take. The chat's read-aloud highlight
// (lib/speechHighlight) knows exactly which sentence is playing because chat
// synthesises one chunk per request. A take is ONE wav for the whole text and
// the engines report no timing, so here it is estimated:
//
//   1. split the text into sentence spans (long ones further at commas),
//   2. find where speech actually is in the decoded audio, and the pauses in it,
//   3. hand each span a share of the voiced time proportional to its length plus
//      a pause allowance for its closing punctuation,
//   4. snap every boundary to the nearest real pause, re-estimating the rest
//      from that point so one bad guess does not drag every later sentence.
//
// Sentence-level on purpose: word timing from a length model drifts visibly,
// a sentence boundary usually sits on a pause the audio itself shows. The
// alternative (a forced-aligner model) would evict the TTS model on one GPU.

export interface Span {
  /** Character offsets into the take's text, end exclusive. */
  start: number;
  end: number;
}

// A clause past this many characters is cut again at commas: a 400-character
// sentence highlighted as one block is no help following along.
const LONG_SPAN = 160;

/** Sentence (and long-clause) spans of `text`, whitespace between them excluded. */
export function splitSpans(text: string): Span[] {
  const out: Span[] = [];
  const push = (s: number, e: number) => {
    while (s < e && /\s/.test(text[s])) s++;
    while (e > s && /\s/.test(text[e - 1])) e--;
    if (e > s) out.push(...splitLong(text, s, e));
  };
  // A boundary is end punctuation (with any closing quote/bracket) followed by
  // whitespace, or a line break.
  const re = /[.!?…]+["'”’»)\]]*(?=\s)|\n/g;
  let from = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    const end = m.index + m[0].length;
    push(from, end);
    from = end;
  }
  push(from, text.length);
  return out;
}

function splitLong(text: string, s: number, e: number): Span[] {
  if (e - s <= LONG_SPAN) return [{ start: s, end: e }];
  const out: Span[] = [];
  const re = /[,;:–—](?=\s)/g;
  re.lastIndex = s;
  let from = s;
  for (let m = re.exec(text); m && m.index < e; m = re.exec(text)) {
    const cut = m.index + m[0].length;
    // Don't leave a two-word stub on either side of a cut.
    if (cut - from < 40 || e - cut < 40) continue;
    out.push({ start: from, end: cut });
    from = cut;
    while (from < e && /\s/.test(text[from])) from++;
  }
  out.push({ start: from, end: e });
  return out;
}

/** Relative speaking time of a span: letters, plus a pause for its punctuation. */
export function spanWeight(piece: string): number {
  const letters = piece.match(/[\p{L}\p{N}]/gu)?.length ?? 0;
  const tail = piece.trimEnd().replace(/["'”’»)\]]+$/, "");
  const pause = /[.!?…]$/.test(tail) ? 6 : /[,;:–—]$/.test(tail) ? 3 : 2;
  return letters + pause;
}

export interface Pause {
  /** Seconds. */
  start: number;
  end: number;
}

export interface VoiceMap {
  /** First and last voiced instant, seconds. */
  speechStart: number;
  speechEnd: number;
  /** Silences strictly inside the voiced region. */
  pauses: Pause[];
}

const FRAME_S = 0.02;
const MIN_PAUSE_S = 0.12;

/** Where the speech and the pauses are in a mono sample buffer. */
export function findPauses(samples: Float32Array, rate: number): VoiceMap {
  const frame = Math.max(1, Math.round(rate * FRAME_S));
  const n = Math.floor(samples.length / frame);
  const rms = new Float32Array(n);
  for (let i = 0; i < n; i++) {
    let sum = 0;
    for (let j = i * frame, end = j + frame; j < end; j++) sum += samples[j] * samples[j];
    rms[i] = Math.sqrt(sum / frame);
  }
  const duration = samples.length / rate;
  if (n === 0) return { speechStart: 0, speechEnd: duration, pauses: [] };
  // Threshold relative to the loud end of the clip, so a quiet voice and a hot
  // one both split the same way. p90, not max: one click would set the bar.
  const sorted = Array.from(rms).sort((a, b) => a - b);
  const loud = sorted[Math.floor(n * 0.9)];
  const thresh = Math.max(1e-4, loud * 0.1);
  const voiced = (i: number) => rms[i] >= thresh;

  let first = 0;
  while (first < n && !voiced(first)) first++;
  let last = n - 1;
  while (last > first && !voiced(last)) last--;
  if (first >= n) return { speechStart: 0, speechEnd: duration, pauses: [] };

  const pauses: Pause[] = [];
  let runStart = -1;
  for (let i = first; i <= last; i++) {
    if (!voiced(i)) {
      if (runStart < 0) runStart = i;
    } else if (runStart >= 0) {
      if ((i - runStart) * FRAME_S >= MIN_PAUSE_S) pauses.push({ start: runStart * FRAME_S, end: i * FRAME_S });
      runStart = -1;
    }
  }
  return { speechStart: first * FRAME_S, speechEnd: (last + 1) * FRAME_S, pauses };
}

/**
 * Boundary times for each span: N spans -> N+1 instants, [0] the start of the
 * first, [N] the end of the last. Without a voice map (audio not decoded yet)
 * the spans are spread over the whole duration.
 */
export function spanTimes(weights: number[], duration: number, voice?: VoiceMap): number[] {
  const n = weights.length;
  if (n === 0) return [0];
  const t0 = voice ? voice.speechStart : 0;
  const t1 = voice ? Math.max(voice.speechStart, voice.speechEnd) : duration;
  const out = [t0];
  let at = t0;
  let pauseIdx = 0;
  const pauses = voice?.pauses ?? [];
  for (let i = 0; i < n - 1; i++) {
    // Re-estimate from wherever the last boundary actually landed.
    const left = weights.slice(i).reduce((a, b) => a + b, 0);
    const est = at + ((t1 - at) * weights[i]) / Math.max(1e-9, left);
    // Snap to the nearest pause within reach: half this span's own estimated
    // length either way, never past the end of speech, and at least a beat.
    const reach = Math.max(0.35, (est - at) * 0.5);
    let best = -1;
    let bestDist = Infinity;
    for (let p = pauseIdx; p < pauses.length; p++) {
      const mid = (pauses[p].start + pauses[p].end) / 2;
      if (mid <= at) continue;
      if (mid - est > reach) break;
      const d = Math.abs(mid - est);
      if (d <= reach && d < bestDist) {
        best = p;
        bestDist = d;
      }
    }
    let b = est;
    if (best >= 0) {
      b = (pauses[best].start + pauses[best].end) / 2;
      pauseIdx = best + 1;
    }
    b = Math.min(Math.max(b, at), t1);
    out.push(b);
    at = b;
  }
  out.push(t1);
  return out;
}

/** Index of the span playing at `t` (clamped: before speech = 0, after = last). */
export function activeSpan(times: number[], t: number): number {
  const n = times.length - 1;
  if (n <= 0) return -1;
  for (let i = n - 1; i > 0; i--) if (t >= times[i]) return i;
  return 0;
}
