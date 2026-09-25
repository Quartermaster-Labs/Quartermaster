<script lang="ts">
  // Canvas host for lib/fireField.ts. Owns sizing, the frame loop and the theme;
  // the simulation itself is pure and lives in the .ts.
  import { onMount } from "svelte";
  import { FireField, renderFire, paletteRamp, GLOW_REACH, DARK_PALETTE, LIGHT_PALETTE, type FireMode } from "../lib/fireField";
  import { pixelRatio } from "../stores/pixelRatio";
  import { isDarkMode } from "../stores/theme";
  import { cssZoom } from "../lib/uiZoom";

  interface Props {
    mode: FireMode;
    progress?: number;
    tps?: number;
    tokens?: number;
    class?: string;
  }
  let { mode, progress = -1, tps = 0, tokens = 0, class: cls = "" }: Props = $props();

  const PITCH = 6; // CSS px between dot centres
  const DOT = 1.15; // CSS px, unlit dot radius
  // Steps per second. The field is a propagation, so its speed IS the step rate:
  // brisk while working, a slow smoulder at idle (and a cheaper one).
  const ACTIVE_HZ = 32;
  const IDLE_HZ = 14;

  let canvas: HTMLCanvasElement;
  const field = new FireField();
  let w = 0;
  let h = 0;

  const palette = $derived($isDarkMode ? DARK_PALETTE : LIGHT_PALETTE);
  const ramp = $derived(paletteRamp(palette));

  // The backing store covers the interface zoom as well as the display ratio,
  // same rule as PerformanceChart - otherwise the dots are a blurry upscale at
  // any interface size above 100%.
  function resize(): void {
    if (!canvas) return;
    w = canvas.clientWidth;
    h = canvas.clientHeight;
    const ratio = $pixelRatio * cssZoom(canvas);
    canvas.width = Math.max(1, Math.round(w * ratio));
    canvas.height = Math.max(1, Math.round(h * ratio));
    canvas.getContext("2d")?.setTransform(ratio, 0, 0, ratio, 0, 0);
    // Inset the grid by the halo reach (+1px of antialiasing): a halo on an
    // outer dot that overhangs the canvas is clipped to a hard straight edge.
    const inset = 2 * (PITCH * GLOW_REACH + 1);
    field.resize(Math.max(1, Math.floor((w - inset) / PITCH) + 1), Math.max(1, Math.floor((h - inset) / PITCH) + 1));
  }

  $effect(() => {
    void $pixelRatio;
    resize();
  });

  onMount(() => {
    const ro = new ResizeObserver(resize);
    ro.observe(canvas);
    const reduce = window.matchMedia("(prefers-reduced-motion: reduce)");

    let raf = 0;
    let last = performance.now();
    let acc = 0;
    const frame = (now: number) => {
      raf = requestAnimationFrame(frame);
      const dt = Math.min(0.25, (now - last) / 1000);
      last = now;
      // Hidden (display:none behind an app-window tab, or a collapsed layout):
      // nothing to paint, and no reason to burn the steps either.
      if (!w || !h || !canvas.offsetParent) return;
      const hz = reduce.matches ? 6 : mode === "idle" ? IDLE_HZ : ACTIVE_HZ;
      acc += dt;
      if (acc < 1 / hz) return;
      const stepDt = acc;
      acc = 0;
      field.step({ mode, progress, tps, tokens }, stepDt, !reduce.matches);
      const ctx = canvas.getContext("2d");
      if (ctx) renderFire(ctx, field, w, h, { pitch: PITCH, dot: DOT, palette, ramp, idleBand: mode === "idle" && !reduce.matches });
    };
    raf = requestAnimationFrame(frame);
    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
    };
  });
</script>

<canvas bind:this={canvas} class="block w-full {cls}" aria-hidden="true"></canvas>
