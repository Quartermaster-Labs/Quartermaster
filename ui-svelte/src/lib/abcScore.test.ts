import { describe, expect, it } from "vitest";
import { noteEvents, parseChordName, parseScore, pitchMidi, timeline, toMidi, toMusicXML } from "./abcScore";

// The shape SheetSage2 emits: two voices written in alternating blocks, chord
// names in quotes, Z<n> rests for a silent voice, "% section" comments.
const CANON = `X:1
T:canon
M:2/4
L:1/16
Q:1/4=81
V: Vocal clef=treble name="Vocal Melody" snm="Vocal"
V: Ins clef=treble name="Ins Melody" snm="Inst."
K:Bm
% intro
V: Vocal
"Bm"z8|"G"z8|"Em"z8|"F#"z4"F#"z4|
V: Ins
F2G2A2B2|B2c2d2e2|B4A2G2|F3z^A2c2|
% verse
V: Vocal
"Bm"z2B2B2c2|"G"d4B4|"Em"z2B2c2d2|"G6/B"c8|
V: Ins
Z4|
`;

describe("parseScore", () => {
  const m = parseScore(CANON);

  it("keeps one part per voice, named from the header", () => {
    expect(m.parts.map((p) => p.name)).toEqual(["Vocal Melody", "Ins Melody"]);
    expect(m.title).toBe("canon");
    expect(m.bpm).toBe(81);
  });

  it("expands Z4 into four measure rests and pads parts to one length", () => {
    expect(m.parts[0].measures).toHaveLength(8);
    expect(m.parts[1].measures).toHaveLength(8);
    expect(m.parts[1].measures.slice(4).every((x) => x.notes.length === 1 && x.notes[0].measureRest)).toBe(true);
  });

  it("applies the key signature, and a written accidental", () => {
    // K:Bm sharpens F and C.
    const ins = m.parts[1].measures;
    expect(ins[0].notes[0].pitches[0]).toMatchObject({ step: "F", octave: 4, alter: 1 });
    expect(ins[3].notes[2].pitches[0]).toMatchObject({ step: "A", alter: 1 });
    expect(ins[1].notes[1].pitches[0]).toMatchObject({ step: "C", octave: 5, alter: 1 });
  });

  it("turns section comments into rehearsal marks on the first part", () => {
    expect(m.parts[0].measures[0].rehearsal).toBe("intro");
    expect(m.parts[0].measures[4].rehearsal).toBe("verse");
    expect(m.parts[1].measures[4].rehearsal).toBeUndefined();
  });

  // abcjs prints a sharp in a chord name as ♯; parseChordName reads both.
  it("lists every onset once, rests included, both parts merged", () => {
    const tl = timeline(m);
    // Bar 1: the vocal's z8 and the first F2 begin together.
    expect(tl[0]).toMatchObject({ t: 0 });
    expect(tl[0].chars).toHaveLength(2);
    for (let i = 1; i < tl.length; i++) expect(tl[i].t).toBeGreaterThan(tl[i - 1].t);
    // Z4 is one glyph: it appears at its first bar only. 2/4 at 81 bpm is 240/81/2 s a bar.
    const z4 = m.parts[1].measures[4].notes[0].startChar;
    const at = tl.filter((p) => p.chars.includes(z4));
    expect(at).toHaveLength(1);
    expect(at[0].t).toBeCloseTo((4 * 240) / 81 / 2);
  });

  it("separates chord symbols from the notes", () => {
    expect(m.parts[0].measures[0].notes[0].harmony).toEqual(["Bm"]);
    expect(m.parts[0].measures[3].notes.map((n) => n.harmony)).toEqual([["F♯"], ["F♯"]]);
  });
});

describe("ties, triplets, lyrics, accidental memory", () => {
  const m = parseScore(`X:1
T:x
M:4/4
L:1/8
K:C
^F2 F2 (3ABc d2-|d2 =F2 z4|
w: one two three four five six-ty
`);
  const [m1, m2] = m.parts[0].measures;

  it("carries an accidental through the measure and resets it at the bar", () => {
    expect(m1.notes[0].pitches[0].alter).toBe(1);
    expect(m1.notes[1].pitches[0].alter).toBe(1);
    expect(m2.notes[1].pitches[0].alter).toBe(0);
  });

  it("marks the triplet and gives it its sounding length", () => {
    const t = m1.notes.slice(2, 5);
    expect(t[0].tuplet).toMatchObject({ actual: 3, normal: 2, start: true });
    expect(t[2].tuplet?.stop).toBe(true);
    expect(t[0].dur).toBeCloseTo(1 / 12);
    expect(t[0].written).toBeCloseTo(1 / 8);
  });

  it("ties across the bar", () => {
    expect(m1.notes[5].pitches[0].tieStart).toBe(true);
    expect(m2.notes[0].pitches[0].tieStop).toBe(true);
  });

  it("reads lyric syllables", () => {
    expect(m1.notes[0].lyrics?.[0]).toEqual({ text: "one", syllabic: "single" });
  });

  it("merges a tie into one event", () => {
    const ev = noteEvents(m, false);
    const d = ev.filter((e) => e.midi === 74);
    expect(d).toHaveLength(1);
    // Two quarters at 120 bpm.
    expect(d[0].end - d[0].start).toBeCloseTo(1);
  });
});

describe("toMusicXML", () => {
  const xml = toMusicXML(parseScore(CANON));

  it("writes a part-wise score MuseScore can open", () => {
    expect(xml).toContain("<score-partwise");
    expect(xml.match(/<score-part id=/g)).toHaveLength(2);
    expect(xml).toContain("<fifths>2</fifths><mode>minor</mode>");
    expect(xml).toContain('<rest measure="yes"/>');
    expect(xml).toContain("<rehearsal>verse</rehearsal>");
    expect(xml).toContain('<sound tempo="81"/>');
  });

  it("spells chord symbols as harmony, slash bass included", () => {
    expect(xml).toContain("<root-step>F</root-step><root-alter>1</root-alter>");
    expect(xml).toContain("<bass><bass-step>B</bass-step></bass>");
  });

  it("keeps every measure's durations summing to the meter", () => {
    // 2/4 at 96 divisions per quarter.
    for (const part of xml.split("<part id=").slice(1)) {
      for (const meas of part.split("<measure ").slice(1)) {
        const durs = [...meas.matchAll(/<note>(?!<chord\/>)[\s\S]*?<duration>(\d+)<\/duration>/g)].map((x) => +x[1]);
        expect(durs.reduce((a, b) => a + b, 0)).toBe(192);
      }
    }
  });

  it("splits a length no single note spells into tied notes", () => {
    const x = toMusicXML(parseScore("X:1\nM:4/4\nL:1/8\nK:C\nC5 z3|\n"));
    expect(x.match(/<step>C<\/step>/g)).toHaveLength(2);
    expect(x).toContain('<tie type="start"/>');
  });
});

describe("helpers", () => {
  it("parses chord names", () => {
    expect(parseChordName("Em7")).toMatchObject({ step: "E", kind: "minor-seventh" });
    expect(parseChordName("Bbmaj7")).toMatchObject({ step: "B", alter: -1, kind: "major-seventh" });
    expect(parseChordName("N.C.")).toBeNull();
  });

  it("maps pitches to MIDI", () => {
    expect(pitchMidi({ step: "C", octave: 4, alter: 0 })).toBe(60);
    expect(pitchMidi({ step: "F", octave: 4, alter: 1 })).toBe(66);
  });

  it("writes a MIDI file", async () => {
    const b = toMidi(CANON);
    const head = new Uint8Array(await b.arrayBuffer()).slice(0, 4);
    expect(String.fromCharCode(...head)).toBe("MThd");
  });
});
