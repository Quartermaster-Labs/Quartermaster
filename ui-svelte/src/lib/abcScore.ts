import abcjs from "abcjs";

// ABC score -> a small score model -> MusicXML (for MuseScore) and note events
// (for the library's playback). abcjs does the parsing; this module only walks
// what it parsed. Heavy (abcjs is ~400 KB), so the library imports it lazily.
//
// Written for the dialect SheetSage2 and YuE2 emit, which is also what users
// keep in a library: one tune, a few V: voices written in alternating blocks,
// chord names in quotes, Z<n> multi-measure rests, "% section" comments. Every
// abcjs voice becomes its own MusicXML part; a section comment becomes a
// rehearsal mark, as domina-music's abc_to_musicxml.py does.

// Divisions per quarter note. 96 covers 64ths and triplets of 32nds exactly.
const DIV = 96;
const STEPS = "CDEFGAB";
const SEMI = [0, 2, 4, 5, 7, 9, 11];

export interface ScorePitch {
  step: string;
  octave: number;
  alter: number;
  tieStart?: boolean;
  tieStop?: boolean;
}

export interface ScoreNote {
  rest: boolean;
  // A whole-measure rest: drawn centred, duration is the measure's.
  measureRest?: boolean;
  pitches: ScorePitch[];
  // Sounding length in whole notes (a triplet eighth is 1/12).
  dur: number;
  // Written length in whole notes (that triplet eighth is 1/8).
  written: number;
  tuplet?: { actual: number; normal: number; start?: boolean; stop?: boolean };
  harmony?: string[];
  words?: string[];
  lyrics?: { text: string; syllabic: string }[];
  startChar: number;
  endChar: number;
}

export interface ScoreMeasure {
  notes: ScoreNote[];
  key?: KeyInfo;
  time?: [number, number];
  rehearsal?: string;
}

export interface KeyInfo {
  fifths: number;
  mode: string;
  // Letter -> alteration implied by the signature.
  alters: Record<string, number>;
}

export interface ScorePart {
  name: string;
  clef: { sign: string; line: number; octave?: number };
  measures: ScoreMeasure[];
}

export interface ScoreModel {
  title: string;
  composer: string;
  // Quarter notes per minute.
  bpm: number;
  parts: ScorePart[];
  warnings: string[];
}

type AbcNote = {
  el_type: string;
  duration?: number;
  rest?: { type: string; text?: number };
  pitches?: { pitch: number; accidental?: string; startTie?: object; endTie?: boolean }[];
  chord?: { name: string; position?: string }[];
  lyric?: { syllable: string; divider: string }[];
  startTriplet?: number;
  tripletMultiplier?: number;
  endTriplet?: boolean;
  startChar: number;
  endChar: number;
  // key / meter / clef items
  accidentals?: { acc: string; note: string }[];
  mode?: string;
  value?: { num: string | number; den?: string | number }[];
  type?: string;
};

const ACC: Record<string, number> = {
  sharp: 1,
  flat: -1,
  natural: 0,
  dblsharp: 2,
  dblflat: -2,
  quartersharp: 0.5,
  quarterflat: -0.5,
};

function keyInfo(k: { accidentals?: { acc: string; note: string }[]; mode?: string } | undefined): KeyInfo {
  const alters: Record<string, number> = {};
  let fifths = 0;
  for (const a of k?.accidentals ?? []) {
    const v = ACC[a.acc] ?? 0;
    alters[a.note.toUpperCase()] = v;
    fifths += v > 0 ? 1 : v < 0 ? -1 : 0;
  }
  const m = (k?.mode ?? "").toLowerCase();
  const mode = m === "m" || m.startsWith("min") || m.startsWith("aeo")
    ? "minor"
    : m.startsWith("dor") ? "dorian"
    : m.startsWith("phr") ? "phrygian"
    : m.startsWith("lyd") ? "lydian"
    : m.startsWith("mix") ? "mixolydian"
    : m.startsWith("loc") ? "locrian"
    : "major";
  return { fifths, mode, alters };
}

function meterOf(m: { type?: string; value?: { num: string | number; den?: string | number }[] } | undefined): [number, number] | undefined {
  if (!m) return undefined;
  if (m.type === "common_time") return [4, 4];
  if (m.type === "cut_time") return [2, 2];
  const v = m.value?.[0];
  if (!v) return undefined;
  // "2+3/8" adds up.
  const num = String(v.num).split("+").reduce((s, x) => s + (parseInt(x, 10) || 0), 0);
  const den = parseInt(String(v.den), 10);
  return num > 0 && den > 0 ? [num, den] : undefined;
}

function clefOf(c: { type?: string } | undefined): ScorePart["clef"] {
  const t = c?.type ?? "treble";
  const oct = /([+-])8$/.exec(t);
  const octave = oct ? (oct[1] === "+" ? 1 : -1) : undefined;
  const base = t.replace(/[+-]8$/, "");
  if (base === "bass") return { sign: "F", line: 4, octave };
  if (base === "alto") return { sign: "C", line: 3, octave };
  if (base === "tenor") return { sign: "C", line: 4, octave };
  if (base === "perc") return { sign: "percussion", line: 0 };
  return { sign: "G", line: 2, octave };
}

// "% intro" comment lines, by offset. "%%" lines are directives, not names.
function sectionComments(src: string): { at: number; name: string }[] {
  const out: { at: number; name: string }[] = [];
  const re = /^%(?!%)[ \t]*(\S[^\r\n]*?)[ \t]*$/gm;
  for (let m = re.exec(src); m; m = re.exec(src)) out.push({ at: m.index, name: m[1] });
  return out;
}

export function parseScore(abc: string): ScoreModel {
  const tune = abcjs.parseOnly(abc)[0];
  if (!tune) throw new Error("No tune found in this score.");
  const meta = tune.metaText as { title?: string; composer?: string; tempo?: { duration?: number[]; bpm?: number } };
  const tempo = meta.tempo;
  const beat = (tempo?.duration ?? [0.25]).reduce((s, x) => s + x, 0) || 0.25;
  const bpm = tempo?.bpm ? Math.round(tempo.bpm * (beat / 0.25)) : 120;

  type State = {
    part: ScorePart;
    key: KeyInfo;
    time: [number, number];
    cur: ScoreMeasure;
    curStart: number;
    measureAcc: Record<string, number>;
    ties: Record<string, number>;
    tuplet: { actual: number; normal: number; mult: number } | null;
    hyphen: boolean[];
    pendingKey?: KeyInfo;
    pendingTime?: [number, number];
  };
  const states: State[] = [];
  const comments = sectionComments(abc);
  let nextComment = 0;
  let firstTime: [number, number] = [4, 4];

  const newMeasure = (s: State): ScoreMeasure => {
    const m: ScoreMeasure = { notes: [] };
    if (s.pendingKey) m.key = s.pendingKey;
    if (s.pendingTime) m.time = s.pendingTime;
    s.pendingKey = undefined;
    s.pendingTime = undefined;
    return m;
  };
  const closeMeasure = (s: State) => {
    if (s.cur.notes.length) {
      s.part.measures.push(s.cur);
      s.cur = newMeasure(s);
    }
    s.measureAcc = {};
  };

  for (const line of tune.lines) {
    const staffs = line.staff ?? [];
    let partIdx = 0;
    staffs.forEach((staff) => {
      for (const voice of staff.voices ?? []) {
        const idx = partIdx++;
        let s = states[idx];
        const lineKey = staff.key ? keyInfo(staff.key) : undefined;
        const lineTime = meterOf(staff.meter);
        if (!s) {
          const title = (staff as { title?: string[] }).title?.[0];
          const key = lineKey ?? keyInfo(undefined);
          const time = lineTime ?? firstTime;
          if (idx === 0) firstTime = time;
          s = {
            part: { name: title || `Voice ${idx + 1}`, clef: clefOf(staff.clef), measures: [] },
            key,
            time,
            cur: { notes: [], key, time },
            curStart: -1,
            measureAcc: {},
            ties: {},
            tuplet: null,
            hyphen: [],
          };
          states[idx] = s;
        } else {
          if (lineKey && lineKey.fifths !== s.key.fifths) {
            s.key = lineKey;
            if (s.cur.notes.length) s.pendingKey = lineKey;
            else s.cur.key = lineKey;
          }
          if (lineTime && (lineTime[0] !== s.time[0] || lineTime[1] !== s.time[1])) {
            s.time = lineTime;
            if (s.cur.notes.length) s.pendingTime = lineTime;
            else s.cur.time = lineTime;
          }
        }
        for (const raw of voice as unknown as AbcNote[]) {
          walk(s, idx, raw);
        }
      }
    });
  }

  function walk(s: State, idx: number, el: AbcNote) {
    if (el.el_type === "bar") {
      closeMeasure(s);
      return;
    }
    if (el.el_type === "key") {
      const k = keyInfo(el);
      s.key = k;
      if (s.cur.notes.length) s.pendingKey = k;
      else s.cur.key = k;
      return;
    }
    if (el.el_type === "meter") {
      const t = meterOf(el);
      if (!t) return;
      s.time = t;
      if (s.cur.notes.length) s.pendingTime = t;
      else s.cur.time = t;
      return;
    }
    if (el.el_type !== "note") return;

    // Section names land on the first part's measure that starts after them.
    if (idx === 0 && !s.cur.notes.length) {
      while (nextComment < comments.length && comments[nextComment].at < el.startChar) {
        s.cur.rehearsal = comments[nextComment].name;
        nextComment++;
      }
    }

    const measureLen = s.time[0] / s.time[1];
    if (el.rest?.type === "multimeasure") {
      const n = el.rest.text ?? 1;
      if (s.cur.notes.length) closeMeasure(s);
      for (let i = 0; i < n; i++) {
        s.cur.notes.push({ rest: true, measureRest: true, pitches: [], dur: measureLen, written: measureLen, startChar: el.startChar, endChar: el.endChar });
        closeMeasure(s);
      }
      return;
    }

    let mult = 1;
    let tuplet: ScoreNote["tuplet"];
    if (el.startTriplet) {
      const m = el.tripletMultiplier ?? 1;
      const actual = el.startTriplet;
      s.tuplet = { actual, normal: Math.round(actual * m), mult: m };
      tuplet = { actual, normal: s.tuplet.normal, start: true };
    }
    if (s.tuplet) {
      mult = s.tuplet.mult;
      tuplet = tuplet ?? { actual: s.tuplet.actual, normal: s.tuplet.normal };
      if (el.endTriplet) {
        tuplet.stop = true;
        s.tuplet = null;
      }
    }

    const written = el.duration ?? 0;
    const note: ScoreNote = {
      rest: !!el.rest,
      pitches: [],
      dur: written * mult,
      written,
      tuplet,
      startChar: el.startChar,
      endChar: el.endChar,
    };
    if (el.rest && (el.rest.type === "spacer" || el.rest.type === "invisible")) note.rest = true;
    for (const c of el.chord ?? []) {
      if (!c.position || c.position === "default") (note.harmony ??= []).push(c.name);
      else (note.words ??= []).push(c.name);
    }
    if (!el.rest) {
      for (const p of el.pitches ?? []) {
        const step = STEPS[((p.pitch % 7) + 7) % 7];
        const octave = 4 + Math.floor(p.pitch / 7);
        const where = `${step}${octave}`;
        let alter: number;
        if (p.accidental && p.accidental in ACC) {
          alter = ACC[p.accidental];
          s.measureAcc[where] = alter;
        } else if (p.endTie && where in s.ties) {
          alter = s.ties[where];
        } else if (where in s.measureAcc) {
          alter = s.measureAcc[where];
        } else {
          alter = s.key.alters[step] ?? 0;
        }
        const sp: ScorePitch = { step, octave, alter };
        if (p.endTie) {
          sp.tieStop = true;
          delete s.ties[where];
        }
        if (p.startTie) {
          sp.tieStart = true;
          s.ties[where] = alter;
        }
        note.pitches.push(sp);
      }
      if (el.lyric?.length) {
        note.lyrics = el.lyric.map((l, i) => {
          const prev = !!s.hyphen[i];
          const next = l.divider === "-";
          s.hyphen[i] = next;
          return { text: l.syllable, syllabic: prev ? (next ? "middle" : "end") : next ? "begin" : "single" };
        });
      }
    }
    if (s.cur.notes.length === 0) s.curStart = el.startChar;
    s.cur.notes.push(note);
  }

  for (const s of states) closeMeasure(s);
  // MuseScore wants every part the same length.
  const total = Math.max(0, ...states.map((s) => s.part.measures.length));
  for (const s of states) {
    while (s.part.measures.length < total) {
      const len = s.time[0] / s.time[1];
      s.part.measures.push({ notes: [{ rest: true, measureRest: true, pitches: [], dur: len, written: len, startChar: 0, endChar: 0 }] });
    }
  }
  if (!states.length) throw new Error("This score has no notes.");
  return {
    title: meta.title || "",
    composer: meta.composer || "",
    bpm,
    parts: states.map((s) => s.part),
    warnings: tune.warnings ?? [],
  };
}

// --- MusicXML ------------------------------------------------------------------

const TYPES: [number, string][] = [
  [1, "whole"],
  [1 / 2, "half"],
  [1 / 4, "quarter"],
  [1 / 8, "eighth"],
  [1 / 16, "16th"],
  [1 / 32, "32nd"],
  [1 / 64, "64th"],
];

// A written length as [type, dots], or null when it needs a tie to spell.
function noteType(w: number): [string, number] | null {
  for (const [v, name] of TYPES) {
    for (const [dots, f] of [[0, 1], [1, 1.5], [2, 1.75]] as const) {
      if (Math.abs(w - v * f) < 1e-9) return [name, dots];
    }
  }
  if (Math.abs(w - 2) < 1e-9) return ["breve", 0];
  return null;
}

// Splits a written length into spellable pieces, longest first.
function splitLength(w: number): number[] {
  const out: number[] = [];
  let left = w;
  while (left > 1e-9 && out.length < 16) {
    const fit = TYPES.flatMap(([v]) => [v * 1.5, v]).find((v) => v <= left + 1e-9);
    if (!fit) break;
    out.push(fit);
    left -= fit;
  }
  return out;
}

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const KINDS: [RegExp, string][] = [
  [/^(maj7|M7|Δ7?)$/, "major-seventh"],
  [/^(m7b5|ø7?)$/, "half-diminished"],
  [/^(dim7|o7)$/, "diminished-seventh"],
  [/^(m7|min7|-7)$/, "minor-seventh"],
  [/^(m6|min6)$/, "minor-sixth"],
  [/^(m9|min9)$/, "minor-ninth"],
  [/^(maj9|M9)$/, "major-ninth"],
  [/^(m|min|-)$/, "minor"],
  [/^(dim|o)$/, "diminished"],
  [/^(aug|\+)$/, "augmented"],
  [/^sus4?$/, "suspended-fourth"],
  [/^sus2$/, "suspended-second"],
  [/^7$/, "dominant"],
  [/^9$/, "dominant-ninth"],
  [/^6$/, "major-sixth"],
  [/^(maj|M)?$/, "major"],
];

export interface ChordName {
  step: string;
  alter: number;
  quality: string;
  kind: string;
  bass?: { step: string; alter: number };
}

export function parseChordName(name: string): ChordName | null {
  const m = /^([A-G])([#b♯♭]?)([^/]*?)(?:\/([A-G])([#b♯♭]?))?$/.exec(name.trim());
  if (!m) return null;
  const alt = (a: string) => (a === "#" || a === "♯" ? 1 : a === "b" || a === "♭" ? -1 : 0);
  const quality = m[3];
  const kind = KINDS.find(([re]) => re.test(quality))?.[1] ?? (/^(m|min|-)/.test(quality) ? "minor" : "major");
  return { step: m[1], alter: alt(m[2]), quality, kind, bass: m[4] ? { step: m[4], alter: alt(m[5]) } : undefined };
}

function harmonyXml(name: string): string {
  const c = parseChordName(name);
  if (!c) return `<direction placement="above"><direction-type><words>${esc(name)}</words></direction-type></direction>`;
  let x = `<harmony><root><root-step>${c.step}</root-step>${c.alter ? `<root-alter>${c.alter}</root-alter>` : ""}</root>`;
  x += `<kind text="${esc(c.quality)}">${c.kind}</kind>`;
  if (c.bass) x += `<bass><bass-step>${c.bass.step}</bass-step>${c.bass.alter ? `<bass-alter>${c.bass.alter}</bass-alter>` : ""}</bass>`;
  return x + "</harmony>";
}

function noteXml(n: ScoreNote, measureLen: number): string {
  if (n.measureRest) {
    return `<note><rest measure="yes"/><duration>${Math.round(measureLen * 4 * DIV)}</duration></note>`;
  }
  // A length no single note can spell is written as tied pieces.
  const spelled = noteType(n.written);
  const pieces = spelled || n.tuplet ? [n.written] : splitLength(n.written);
  const scale = n.written ? n.dur / n.written : 1;
  let out = "";
  pieces.forEach((w, pi) => {
    const first = pi === 0;
    const last = pi === pieces.length - 1;
    const t = noteType(w) ?? ["quarter", 0];
    const dur = Math.max(1, Math.round(w * scale * 4 * DIV));
    const chordTones = n.rest ? [null] : n.pitches;
    chordTones.forEach((p, ci) => {
      let x = "<note>";
      if (ci > 0) x += "<chord/>";
      if (!p) x += "<rest/>";
      else x += `<pitch><step>${p.step}</step>${p.alter ? `<alter>${p.alter}</alter>` : ""}<octave>${p.octave}</octave></pitch>`;
      x += `<duration>${dur}</duration>`;
      const tieStop = p && ((first && p.tieStop) || !first);
      const tieStart = p && ((last && p.tieStart) || !last);
      if (tieStop) x += `<tie type="stop"/>`;
      if (tieStart) x += `<tie type="start"/>`;
      x += `<type>${t[0]}</type>` + "<dot/>".repeat(t[1]);
      if (p && p.alter && first) {
        const acc = p.alter === 1 ? "sharp" : p.alter === -1 ? "flat" : p.alter === 2 ? "double-sharp" : p.alter === -2 ? "flat-flat" : "";
        if (acc) x += `<accidental>${acc}</accidental>`;
      }
      if (n.tuplet) x += `<time-modification><actual-notes>${n.tuplet.actual}</actual-notes><normal-notes>${n.tuplet.normal}</normal-notes></time-modification>`;
      const nots: string[] = [];
      if (tieStop) nots.push(`<tied type="stop"/>`);
      if (tieStart) nots.push(`<tied type="start"/>`);
      if (ci === 0 && n.tuplet?.start && first) nots.push(`<tuplet type="start" bracket="yes"/>`);
      if (ci === 0 && n.tuplet?.stop && last) nots.push(`<tuplet type="stop"/>`);
      if (nots.length) x += `<notations>${nots.join("")}</notations>`;
      if (ci === 0 && first) {
        (n.lyrics ?? []).forEach((l, li) => {
          if (l.text) x += `<lyric number="${li + 1}"><syllabic>${l.syllabic}</syllabic><text>${esc(l.text)}</text></lyric>`;
        });
      }
      out += x + "</note>";
    });
  });
  return out;
}

export function toMusicXML(model: ScoreModel): string {
  const parts = model.parts;
  let x = `<?xml version="1.0" encoding="UTF-8" standalone="no"?>\n`;
  x += `<!DOCTYPE score-partwise PUBLIC "-//Recordare//DTD MusicXML 4.0 Partwise//EN" "http://www.musicxml.org/dtds/partwise.dtd">\n`;
  x += `<score-partwise version="4.0">\n`;
  if (model.title) x += `<work><work-title>${esc(model.title)}</work-title></work>\n`;
  x += `<identification>${model.composer ? `<creator type="composer">${esc(model.composer)}</creator>` : ""}<encoding><software>Quartermaster</software></encoding></identification>\n`;
  x += `<part-list>\n`;
  parts.forEach((p, i) => {
    x += `<score-part id="P${i + 1}"><part-name>${esc(p.name)}</part-name></score-part>\n`;
  });
  x += `</part-list>\n`;
  parts.forEach((p, pi) => {
    x += `<part id="P${pi + 1}">\n`;
    let time: [number, number] = [4, 4];
    let key: KeyInfo | undefined;
    p.measures.forEach((m, mi) => {
      if (m.time) time = m.time;
      const measureLen = time[0] / time[1];
      const filled = m.notes.reduce((s, n) => s + (n.measureRest ? measureLen : n.dur), 0);
      const pickup = mi === 0 && filled < measureLen - 1e-9;
      x += `<measure number="${pickup ? 0 : mi + (p.measures[0] && isPickup(p, 0) ? 0 : 1)}"${pickup ? ` implicit="yes"` : ""}>`;
      const attrs: string[] = [];
      if (mi === 0) attrs.push(`<divisions>${DIV}</divisions>`);
      if (m.key && (mi === 0 || m.key.fifths !== key?.fifths || m.key.mode !== key?.mode)) {
        key = m.key;
        attrs.push(`<key><fifths>${m.key.fifths}</fifths><mode>${m.key.mode}</mode></key>`);
      } else if (mi === 0) {
        attrs.push(`<key><fifths>0</fifths><mode>major</mode></key>`);
      }
      if (m.time || mi === 0) attrs.push(`<time><beats>${time[0]}</beats><beat-type>${time[1]}</beat-type></time>`);
      if (mi === 0) {
        const c = p.clef;
        attrs.push(c.sign === "percussion" ? `<clef><sign>percussion</sign></clef>` : `<clef><sign>${c.sign}</sign><line>${c.line}</line>${c.octave ? `<clef-octave-change>${c.octave}</clef-octave-change>` : ""}</clef>`);
      }
      if (attrs.length) x += `<attributes>${attrs.join("")}</attributes>`;
      if (mi === 0 && pi === 0) x += `<direction placement="above"><direction-type><metronome><beat-unit>quarter</beat-unit><per-minute>${model.bpm}</per-minute></metronome></direction-type><sound tempo="${model.bpm}"/></direction>`;
      if (m.rehearsal) x += `<direction placement="above"><direction-type><rehearsal>${esc(m.rehearsal)}</rehearsal></direction-type></direction>`;
      for (const n of m.notes) {
        for (const h of n.harmony ?? []) x += harmonyXml(h);
        for (const w of n.words ?? []) x += `<direction placement="above"><direction-type><words>${esc(w)}</words></direction-type></direction>`;
        x += noteXml(n, measureLen);
      }
      x += `</measure>\n`;
    });
    x += `</part>\n`;
  });
  return x + `</score-partwise>\n`;
}

function isPickup(p: ScorePart, mi: number): boolean {
  const m = p.measures[mi];
  const t = m.time ?? [4, 4];
  const len = t[0] / t[1];
  return m.notes.reduce((s, n) => s + (n.measureRest ? len : n.dur), 0) < len - 1e-9;
}

// --- playback events -------------------------------------------------------------

export interface NoteEvent {
  // Seconds from the start.
  start: number;
  end: number;
  midi: number;
  // Which part sounds it; -1 is the chord accompaniment.
  part: number;
  startChar: number;
  endChar: number;
}

const CHORD_TONES: Record<string, number[]> = {
  major: [0, 4, 7],
  minor: [0, 3, 7],
  diminished: [0, 3, 6],
  augmented: [0, 4, 8],
  "suspended-fourth": [0, 5, 7],
  "suspended-second": [0, 2, 7],
  dominant: [0, 4, 7, 10],
  "major-seventh": [0, 4, 7, 11],
  "minor-seventh": [0, 3, 7, 10],
  "half-diminished": [0, 3, 6, 10],
  "diminished-seventh": [0, 3, 6, 9],
  "major-sixth": [0, 4, 7, 9],
  "minor-sixth": [0, 3, 7, 9],
  "dominant-ninth": [0, 4, 7, 10],
  "major-ninth": [0, 4, 7, 11],
  "minor-ninth": [0, 3, 7, 10],
};

export function pitchMidi(p: ScorePitch): number {
  return 12 * (p.octave + 1) + SEMI[STEPS.indexOf(p.step)] + Math.round(p.alter);
}

// Every sounding note, tied notes merged, plus held chords under the named
// chord symbols (one octave below middle C, root first) when `chords` is set.
export function noteEvents(model: ScoreModel, chords = true): NoteEvent[] {
  const secPerWhole = (4 * 60) / model.bpm;
  const out: NoteEvent[] = [];
  const chordAt: { t: number; name: string }[] = [];
  let end = 0;
  model.parts.forEach((p, pi) => {
    let t = 0;
    let time: [number, number] = [4, 4];
    const open: Record<number, NoteEvent> = {};
    for (const m of p.measures) {
      if (m.time) time = m.time;
      const measureLen = time[0] / time[1];
      for (const n of m.notes) {
        const len = n.measureRest ? measureLen : n.dur;
        for (const h of n.harmony ?? []) if (!chordAt.some((c) => Math.abs(c.t - t) < 1e-9)) chordAt.push({ t, name: h });
        if (!n.rest) {
          for (const pt of n.pitches) {
            const midi = pitchMidi(pt);
            const s = t * secPerWhole;
            const e = (t + len) * secPerWhole;
            if (pt.tieStop && open[midi]) {
              open[midi].end = e;
            } else {
              const ev: NoteEvent = { start: s, end: e, midi, part: pi, startChar: n.startChar, endChar: n.endChar };
              out.push(ev);
              open[midi] = ev;
            }
            if (!pt.tieStart) delete open[midi];
          }
        }
        t += len;
      }
    }
    end = Math.max(end, t);
  });
  if (chords) {
    chordAt.sort((a, b) => a.t - b.t);
    chordAt.forEach((c, i) => {
      const name = parseChordName(c.name);
      if (!name) return;
      const until = i + 1 < chordAt.length ? chordAt[i + 1].t : end;
      const root = 48 + SEMI[STEPS.indexOf(name.step)] + name.alter;
      for (const iv of (CHORD_TONES[name.kind] ?? CHORD_TONES.major).slice(0, 3)) {
        out.push({ start: c.t * secPerWhole, end: until * secPerWhole, midi: root + iv, part: -1, startChar: 0, endChar: 0 });
      }
    });
  }
  return out.sort((a, b) => a.start - b.start);
}

// --- playhead timeline -------------------------------------------------------------

export interface TimePoint {
  // Seconds from the start.
  t: number;
  // Every element (note or rest, any part) that begins at t, by its offset
  // in the ABC text, so the viewer can find where it was engraved.
  chars: number[];
}

// When each written element starts, rests included, so the playhead keeps
// moving through a silent bar. One point per distinct onset, all parts merged.
// Measure rests a voice was padded with have no text (startChar 0 and
// endChar 0) and are left out, and a Z4 expanded into four bars counts once,
// at its first bar: all four are the one glyph on the page.
export function timeline(model: ScoreModel): TimePoint[] {
  const secPerWhole = (4 * 60) / model.bpm;
  const at = new Map<number, number[]>();
  for (const p of model.parts) {
    let t = 0;
    let time: [number, number] = [4, 4];
    let last = -1;
    for (const m of p.measures) {
      if (m.time) time = m.time;
      const measureLen = time[0] / time[1];
      for (const n of m.notes) {
        if ((n.startChar || n.endChar) && n.startChar !== last) {
          last = n.startChar;
          // Rounded so two parts landing on one beat share a point.
          const key = Math.round(t * secPerWhole * 1e4) / 1e4;
          const list = at.get(key) ?? [];
          list.push(n.startChar);
          at.set(key, list);
        }
        t += n.measureRest ? measureLen : n.dur;
      }
    }
  }
  return [...at.entries()].sort((a, b) => a[0] - b[0]).map(([t, chars]) => ({ t, chars }));
}

// --- MIDI + file names -------------------------------------------------------------

export function toMidi(abc: string): Blob {
  const files = abcjs.synth.getMidiFile(abc, { midiOutputType: "binary" }) as unknown as Uint8Array[];
  return new Blob([files[0] as BlobPart], { type: "audio/midi" });
}

export function fileStem(title: string): string {
  return title.replace(/[^\w\- ]+/g, "").trim().slice(0, 60).replace(/\s+/g, "-") || "score";
}

export { abcjs };
