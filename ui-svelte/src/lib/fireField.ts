// The inference animation: a dot-matrix fire, in the lineage of the Doom PSX
// fire. Every column of the bottom (hidden) row is a heat source; each step,
// every cell takes the heat of a jittered cell below it minus a random cooling,
// so heat climbs, flickers and dies out on its own. Nothing is keyframed - the
// look falls out of what the sources are doing, which is why the four states
// blend into each other instead of cutting between animations:
//
//   idle        no source; the odd stray ember rises and dies, and a soft band
//               drifts over the dot grid (the old standby scanner, kept)
//   loading     a low, red ember bed, lit up to the load-progress fraction
//   prefill     the fire catches left to right as the prompt is processed
//   generating  full fire; height and wind follow tokens/sec, and every token
//               that arrives throws a spark
//
// Pure: no DOM beyond the CanvasRenderingContext2D handed to render(), so the
// simulation is unit-testable (fireField.test.ts).

export type FireMode = "idle" | "loading" | "prefill" | "generating";

export interface FireInput {
  mode: FireMode;
  /** 0..1 determinate progress (load or prefill); < 0 = unknown. */
  progress: number;
  /** Live decode rate, tokens/sec. */
  tps: number;
  /** Cumulative output tokens of the request in flight (drives sparks). */
  tokens: number;
}

interface Spark {
  x: number; // column, fractional
  y: number; // row, fractional (0 = top)
  vx: number;
  vy: number; // rows per second, negative = up
  life: number; // seconds left
  max: number;
}

// Decode rate at which the fire reaches full height. Above it the fire does not
// grow; the sparks keep scaling with the real token count.
const TPS_FULL = 80;
// Sources ease toward their target instead of snapping, so a request starting or
// ending reads as the fire catching or dying down.
const IGNITE = 0.12;
const DOUSE = 0.05;

export class FireField {
  cols = 0;
  rows = 0;
  /** rows+1 rows; the last is the hidden source row. Row 0 is the top. */
  heat = new Float32Array(0);
  private source = new Float32Array(0);
  sparks: Spark[] = [];
  private pendingSparks = 0;
  private lastTokens = 0;
  private windPhase = 0;
  /** Seconds of simulated time, for the idle band and the sweep. */
  t = 0;
  private rand: () => number;

  constructor(rand: () => number = Math.random) {
    this.rand = rand;
  }

  resize(cols: number, rows: number): void {
    cols = Math.max(1, Math.floor(cols));
    rows = Math.max(1, Math.floor(rows));
    if (cols === this.cols && rows === this.rows) return;
    this.cols = cols;
    this.rows = rows;
    this.heat = new Float32Array(cols * (rows + 1));
    this.source = new Float32Array(cols);
    this.sparks = [];
  }

  /** Fraction of the columns that act as a source, and how hot, for a mode. */
  private target(inp: FireInput, c: number): number {
    const x = (c + 0.5) / this.cols;
    switch (inp.mode) {
      case "idle":
        return 0;
      case "loading": {
        if (inp.progress >= 0) return x <= inp.progress ? 0.5 : 0;
        // No learned load time: an ember patch sweeping back and forth.
        const pos = (Math.sin(this.t * 1.4) + 1) / 2;
        return Math.abs(x - pos) < 0.12 ? 0.5 : 0;
      }
      case "prefill": {
        if (inp.progress >= 0) return x <= inp.progress ? 0.82 : 0.08;
        const pos = (Math.sin(this.t * 1.8) + 1) / 2;
        return Math.abs(x - pos) < 0.18 ? 0.82 : 0.08;
      }
      case "generating":
        return 0.72 + 0.28 * Math.min(1, Math.max(0, inp.tps) / TPS_FULL);
    }
  }

  /** Advance the simulation by dt seconds (one propagation pass). */
  step(inp: FireInput, dt: number, sparks = true): void {
    const { cols, rows, heat, source } = this;
    if (!cols) return;
    this.t += dt;

    // Sources.
    const base = rows * cols;
    for (let c = 0; c < cols; c++) {
      const tgt = this.target(inp, c);
      const k = tgt > source[c] ? IGNITE : DOUSE;
      source[c] += (tgt - source[c]) * k;
      // Flicker lives on the source row, so a steady target still dances.
      heat[base + c] = source[c] > 0.01 ? source[c] * (0.8 + 0.2 * this.rand()) : 0;
    }
    // Idle embers: the fire is out, not gone.
    if (inp.mode === "idle" && this.rand() < 0.18) {
      heat[base + Math.floor(this.rand() * cols)] = 0.32 + 0.12 * this.rand();
    }

    // Wind: a slow gust, harder the faster tokens are flowing. Leftward bias -
    // the old braille stream flowed left, and so does this.
    const gen = inp.mode === "generating" ? Math.min(1, inp.tps / TPS_FULL) : 0;
    this.windPhase += dt * (0.6 + gen * 1.6);
    const wind = -0.25 - gen * 0.35 + Math.sin(this.windPhase) * 0.45;

    // Cooling per row scales with height, so the fire reaches the same share of
    // the field whatever its size.
    const cool = 1.55 / rows;
    for (let r = 0; r < rows; r++) {
      const below = (r + 1) * cols;
      const row = r * cols;
      for (let c = 0; c < cols; c++) {
        const jitter = Math.round(this.rand() * 2 - 1 + wind);
        // Clamped, not wrapped: a lit left edge must not glow on the right.
        const src = Math.min(cols - 1, Math.max(0, c - jitter));
        const h = heat[below + src] - this.rand() * cool;
        heat[row + c] = h > 0 ? h : 0;
      }
    }

    // Sparks: one per token. The server batches token counts every ~200ms, so a
    // burst is released over the next interval rather than all on one frame.
    if (inp.tokens < this.lastTokens || inp.mode !== "generating") {
      this.lastTokens = inp.mode === "generating" ? inp.tokens : 0;
      this.pendingSparks = 0;
    } else if (inp.tokens > this.lastTokens) {
      this.pendingSparks += inp.tokens - this.lastTokens;
      this.lastTokens = inp.tokens;
    }
    if (sparks && this.pendingSparks > 0) {
      const n = Math.min(this.pendingSparks, Math.max(1, Math.ceil(this.pendingSparks * dt * 5)), 6);
      this.pendingSparks -= n;
      for (let i = 0; i < n; i++) {
        const life = 0.5 + this.rand() * 0.6;
        this.sparks.push({
          x: this.rand() * cols,
          y: rows - 0.5,
          vx: wind * 4 + (this.rand() - 0.5) * 3,
          vy: -(rows * (1.3 + this.rand() * 0.9)),
          life,
          max: life,
        });
      }
    } else if (!sparks) {
      this.pendingSparks = 0;
    }
    for (const s of this.sparks) {
      s.x += s.vx * dt;
      s.y += s.vy * dt;
      s.vx += (this.rand() - 0.5) * 6 * dt;
      s.life -= dt;
    }
    this.sparks = this.sparks.filter((s) => s.life > 0 && s.y > -1);
    if (this.sparks.length > 160) this.sparks.splice(0, this.sparks.length - 160);
  }

  /** Visible heat at a cell, 0..1. */
  at(c: number, r: number): number {
    return this.heat[r * this.cols + c];
  }
}

// ---------------------------------------------------------------------------
// Rendering

export interface FirePalette {
  /** Heat stops, ascending; colours as [r, g, b]. */
  stops: [number, [number, number, number]][];
  /** The unlit dot grid. */
  base: [number, number, number, number];
  /** Composite for the glow halo: additive on dark, plain on light. */
  glow: GlobalCompositeOperation;
  glowAlpha: number;
}

export const DARK_PALETTE: FirePalette = {
  stops: [
    [0.0, [74, 20, 8]],
    [0.18, [138, 31, 10]],
    [0.38, [210, 64, 15]],
    [0.58, [255, 106, 43]],
    [0.76, [255, 160, 64]],
    [0.9, [255, 210, 122]],
    [1.0, [255, 244, 214]],
  ],
  base: [180, 180, 190, 0.1],
  glow: "lighter",
  glowAlpha: 0.22,
};

// On the beige light theme a white-hot core would vanish into the page, so heat
// climbs toward a saturated red-orange instead of toward white.
export const LIGHT_PALETTE: FirePalette = {
  stops: [
    [0.0, [228, 196, 160]],
    [0.2, [226, 160, 104]],
    [0.45, [222, 118, 44]],
    [0.7, [214, 80, 22]],
    [1.0, [186, 36, 10]],
  ],
  base: [40, 30, 20, 0.1],
  glow: "source-over",
  glowAlpha: 0.1,
};

const LEVELS = 48;

function lerpStops(p: FirePalette, h: number): [number, number, number] {
  const s = p.stops;
  if (h <= s[0][0]) return s[0][1];
  for (let i = 1; i < s.length; i++) {
    if (h <= s[i][0]) {
      const [h0, c0] = s[i - 1];
      const [h1, c1] = s[i];
      const f = (h - h0) / (h1 - h0);
      return [0, 1, 2].map((k) => Math.round(c0[k] + (c1[k] - c0[k]) * f)) as [number, number, number];
    }
  }
  return s[s.length - 1][1];
}

/** Precomputed per-heat-level colours for one palette. */
export function paletteRamp(p: FirePalette): string[] {
  const out: string[] = [];
  for (let i = 0; i < LEVELS; i++) {
    const [r, g, b] = lerpStops(p, i / (LEVELS - 1));
    out.push(`rgb(${r},${g},${b})`);
  }
  return out;
}

export interface RenderOpts {
  pitch: number; // CSS px between dot centres
  dot: number; // CSS px, radius of an unlit dot
  palette: FirePalette;
  ramp: string[];
  idleBand: boolean;
}

/** Draw the field. ctx is expected to be transformed to CSS px already. */
export function renderFire(ctx: CanvasRenderingContext2D, f: FireField, w: number, h: number, o: RenderOpts): void {
  const { cols, rows } = f;
  const { pitch, dot, palette, ramp } = o;
  ctx.clearRect(0, 0, w, h);
  const ox = (w - (cols - 1) * pitch) / 2;
  const oy = (h - (rows - 1) * pitch) / 2;
  const [br, bg, bb, ba] = palette.base;
  // Standby band: the old idle scanner, now a soft light drifting over the grid.
  const bandX = ((Math.sin(f.t * 0.55) + 1) / 2) * (cols - 1);

  // Pass 1: unlit grid + lit cores.
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const heat = f.at(c, r);
      const x = ox + c * pitch;
      const y = oy + r * pitch;
      if (heat < 0.05) {
        let a = ba;
        if (o.idleBand) {
          const d = (c - bandX) / (cols * 0.06);
          a += ba * 2.2 * Math.exp(-d * d);
        }
        ctx.fillStyle = `rgba(${br},${bg},${bb},${a})`;
        ctx.beginPath();
        ctx.arc(x, y, dot, 0, Math.PI * 2);
        ctx.fill();
        continue;
      }
      const lv = Math.min(LEVELS - 1, Math.floor(heat * (LEVELS - 1)));
      ctx.fillStyle = ramp[lv];
      ctx.globalAlpha = Math.min(1, 0.35 + heat * 1.1);
      ctx.beginPath();
      ctx.arc(x, y, dot * (1 + heat * 0.6), 0, Math.PI * 2);
      ctx.fill();
      ctx.globalAlpha = 1;
    }
  }

  // Pass 2: glow halos on the hot cells, composited as light.
  ctx.globalCompositeOperation = palette.glow;
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const heat = f.at(c, r);
      if (heat < 0.5) continue;
      const lv = Math.min(LEVELS - 1, Math.floor(heat * (LEVELS - 1)));
      ctx.fillStyle = ramp[lv];
      ctx.globalAlpha = palette.glowAlpha * (heat - 0.4);
      ctx.beginPath();
      ctx.arc(ox + c * pitch, oy + r * pitch, pitch * 0.95, 0, Math.PI * 2);
      ctx.fill();
    }
  }

  // Sparks: snapped to the grid so they read as LEDs, with a two-cell trail.
  for (const s of f.sparks) {
    const k = s.life / s.max;
    for (let t = 0; t < 3; t++) {
      const c = Math.round(s.x - (s.vx / Math.abs(s.vy || 1)) * t);
      const r = Math.round(s.y + t);
      if (c < 0 || c >= cols || r < 0 || r >= rows) continue;
      const heat = Math.min(1, 0.55 + 0.45 * k) * (1 - t * 0.35);
      const lv = Math.min(LEVELS - 1, Math.floor(heat * (LEVELS - 1)));
      ctx.fillStyle = ramp[lv];
      ctx.globalAlpha = heat;
      ctx.beginPath();
      ctx.arc(ox + c * pitch, oy + r * pitch, t === 0 ? pitch * 0.55 : pitch * 0.35, 0, Math.PI * 2);
      ctx.fill();
    }
  }
  ctx.globalAlpha = 1;
  ctx.globalCompositeOperation = "source-over";

  // Hot cores on top of their own halos (additive halos wash them out).
  for (const s of f.sparks) {
    const c = Math.round(s.x);
    const r = Math.round(s.y);
    if (c < 0 || c >= cols || r < 0 || r >= rows) continue;
    ctx.fillStyle = ramp[LEVELS - 1];
    ctx.globalAlpha = Math.min(1, 0.4 + s.life / s.max);
    ctx.beginPath();
    ctx.arc(ox + c * pitch, oy + r * pitch, dot * 1.3, 0, Math.PI * 2);
    ctx.fill();
  }
  ctx.globalAlpha = 1;
}
