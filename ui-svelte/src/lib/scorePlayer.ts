import type { NoteEvent } from "./abcScore";

// Plays a score's note events through WebAudio oscillators. Deliberately not
// abcjs's synth: that one fetches its soundfont from a public CDN, and the
// playground must work on a box with no internet. Two simple timbres (a
// plucked triangle for the parts, a soft sine pad for the chords) are enough
// to hear whether a transcription is right.
//
// A lookahead scheduler: every 50 ms, notes starting in the next 300 ms are
// queued on the audio clock, so a long score never builds thousands of nodes
// up front, and stop() only has to silence what is already queued.

export interface ScorePlayer {
  play(from?: number): void;
  stop(): void;
  readonly playing: boolean;
  // Seconds into the score, or 0 when stopped.
  position(): number;
  readonly duration: number;
  setVolume(v: number): void;
}

const LOOKAHEAD = 0.3;
const TICK_MS = 50;

const freq = (midi: number) => 440 * Math.pow(2, (midi - 69) / 12);

export function createScorePlayer(events: NoteEvent[], opts: { volume?: number; onend?: () => void } = {}): ScorePlayer {
  let ctx: AudioContext | null = null;
  let master: GainNode | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let next = 0;
  let t0 = 0;
  let offset = 0;
  let volume = opts.volume ?? 0.8;
  const live = new Set<OscillatorNode>();
  const duration = events.reduce((m, e) => Math.max(m, e.end), 0);

  function voice(e: NoteEvent, at: number, len: number) {
    if (!ctx || !master) return;
    const osc = ctx.createOscillator();
    const g = ctx.createGain();
    const pad = e.part < 0;
    osc.type = pad ? "sine" : "triangle";
    osc.frequency.value = freq(e.midi);
    const peak = pad ? 0.07 : 0.22;
    const attack = pad ? 0.04 : 0.008;
    // Plucked: decays toward a sustain; the release keeps notes from clicking.
    g.gain.setValueAtTime(0, at);
    g.gain.linearRampToValueAtTime(peak, at + attack);
    if (!pad) g.gain.exponentialRampToValueAtTime(peak * 0.45, at + attack + Math.min(0.25, len));
    const off = at + Math.max(len, attack + 0.02);
    // setTargetAtTime starts from whatever the envelope reached, so the release
    // needs no guess at the scheduled value.
    g.gain.setTargetAtTime(0, off, 0.03);
    osc.connect(g).connect(master);
    osc.start(at);
    osc.stop(off + 0.2);
    live.add(osc);
    osc.onended = () => live.delete(osc);
  }

  function tick() {
    if (!ctx) return;
    const now = ctx.currentTime - t0 + offset;
    while (next < events.length && events[next].start < now + LOOKAHEAD) {
      const e = events[next++];
      if (e.end <= offset) continue;
      const start = Math.max(e.start, offset);
      voice(e, t0 + (start - offset), e.end - start);
    }
    if (next >= events.length && now >= duration + 0.1) {
      stop();
      opts.onend?.();
    }
  }

  function play(from = 0) {
    stop();
    const AC = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    ctx ??= new AC();
    void ctx.resume();
    master = ctx.createGain();
    master.gain.value = volume;
    master.connect(ctx.destination);
    offset = Math.max(0, Math.min(from, duration));
    next = events.findIndex((e) => e.end > offset);
    if (next < 0) next = events.length;
    t0 = ctx.currentTime + 0.05;
    tick();
    timer = setInterval(tick, TICK_MS);
  }

  function stop() {
    if (timer) clearInterval(timer);
    timer = null;
    for (const o of live) {
      try {
        o.stop();
      } catch {
        /* already stopped */
      }
    }
    live.clear();
    master?.disconnect();
    master = null;
  }

  return {
    play,
    stop,
    get playing() {
      return timer !== null;
    },
    position() {
      return timer && ctx ? Math.min(duration, Math.max(0, ctx.currentTime - t0 + offset)) : 0;
    },
    duration,
    setVolume(v: number) {
      volume = v;
      if (master) master.gain.value = v;
    },
  };
}
