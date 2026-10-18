<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { Play, Pause } from "lucide-svelte";

  // Custom audio clip player matching the playground design language (warm
  // palette, primary fill) — replaces the ugly native <audio controls>. Shows a
  // real waveform decoded from the clip; the played portion fills primary.
  // Parent auto-play still works via the exported play().
  //
  // The Speech studio's spotlight drives a read-along off this player, which is
  // what the optional hooks are for: `onplayrequest` turns a grid card's play
  // button into "open this take in the spotlight from here" instead of playing
  // in place, `startAt`/`autoplay` let the spotlight pick up where the card was,
  // and `ontime`/`ondecoded` feed the highlight its clock and its pause map.
  let {
    src,
    volume = 1,
    label = "",
    wide = false,
    startAt = 0,
    autoplay = false,
    onplayrequest,
    ontime,
    ondecoded,
  }: {
    src: string;
    volume?: number;
    label?: string;
    wide?: boolean;
    startAt?: number;
    autoplay?: boolean;
    onplayrequest?: (at: number) => void;
    /** Playback position; 0 again once the clip has ended. */
    ontime?: (t: number) => void;
    ondecoded?: (samples: Float32Array, rate: number) => void;
  } = $props();

  const BARS = 48;

  let audioEl: HTMLAudioElement | null = $state(null);
  let playing = $state(false);
  let cur = $state(0);
  let dur = $state(0);
  let seeking = $state(false);
  let trackEl: HTMLDivElement | null = $state(null);
  let peaks = $state<number[]>([]);

  // Parent calls this on the component instance (bind:this) to auto-play.
  export function play() {
    audioEl?.play().catch(() => {});
  }

  export function pause() {
    audioEl?.pause();
  }

  function toggle() {
    if (!audioEl) return;
    if (playing) audioEl.pause();
    else if (onplayrequest) onplayrequest(audioEl.currentTime);
    else audioEl.play().catch(() => {});
  }

  function onMeta() {
    // wav duration is finite; guard NaN/Infinity from odd encodes.
    dur = Number.isFinite(audioEl?.duration ?? NaN) ? audioEl!.duration : 0;
    if (!audioEl) return;
    if (startAt > 0 && startAt < dur) {
      audioEl.currentTime = startAt;
      cur = startAt;
    }
    if (autoplay) audioEl.play().catch(() => {});
  }

  const frac = $derived(dur > 0 ? Math.min(1, cur / dur) : 0);

  // Keep the element's playback volume in sync with the composer setting.
  $effect(() => {
    if (audioEl) audioEl.volume = Math.min(1, Math.max(0, volume));
  });

  // Decode the clip once into per-bar peaks for the waveform. Falls back to a
  // flat bar row if the browser can't decode (peaks stays empty).
  $effect(() => {
    const url = src;
    let cancelled = false;
    (async () => {
      try {
        const buf = await (await fetch(url)).arrayBuffer();
        const AC = window.AudioContext || (window as any).webkitAudioContext;
        const ctx = new AC();
        const audio = await ctx.decodeAudioData(buf);
        ctx.close();
        if (cancelled) return;
        peaks = computePeaks(audio.getChannelData(0), BARS);
        ondecoded?.(audio.getChannelData(0), audio.sampleRate);
      } catch {
        if (!cancelled) peaks = [];
      }
    })();
    return () => {
      cancelled = true;
    };
  });

  function computePeaks(data: Float32Array, n: number): number[] {
    const block = Math.floor(data.length / n) || 1;
    const out: number[] = [];
    let max = 0;
    for (let i = 0; i < n; i++) {
      let peak = 0;
      const start = i * block;
      for (let j = 0; j < block; j++) {
        const v = Math.abs(data[start + j] || 0);
        if (v > peak) peak = v;
      }
      out.push(peak);
      if (peak > max) max = peak;
    }
    return max > 0 ? out.map((v) => v / max) : out;
  }

  const bars = $derived(peaks.length ? peaks : new Array(BARS).fill(0));

  function seekTo(clientX: number) {
    if (!trackEl || !audioEl || dur <= 0) return;
    const r = trackEl.getBoundingClientRect();
    const f = Math.min(1, Math.max(0, (clientX - r.left) / r.width));
    audioEl.currentTime = f * dur;
    cur = f * dur;
    ontime?.(cur);
  }

  function onPointerDown(e: PointerEvent) {
    seeking = true;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    seekTo(e.clientX);
  }
  function onPointerMove(e: PointerEvent) {
    if (seeking) seekTo(e.clientX);
  }
  function onPointerUp() {
    seeking = false;
  }

  function fmt(s: number): string {
    if (!Number.isFinite(s) || s < 0) s = 0;
    const m = Math.floor(s / 60);
    const sec = Math.floor(s % 60);
    return `${m}:${sec.toString().padStart(2, "0")}`;
  }
</script>

<div class="flex items-center gap-2.5 {wide ? 'w-full' : 'w-72'} max-w-full">
  <audio
    bind:this={audioEl}
    {src}
    onloadedmetadata={onMeta}
    ontimeupdate={() => { if (!seeking) cur = audioEl?.currentTime ?? 0; ontime?.(audioEl?.currentTime ?? 0); }}
    onplay={() => (playing = true)}
    onpause={() => (playing = false)}
    onended={() => { playing = false; cur = 0; ontime?.(0); }}
  ></audio>

  <button
    class="shrink-0 grid place-items-center w-9 h-9 rounded-full bg-[#141414] text-white hover:opacity-90 active:opacity-80 transition-opacity"
    onclick={toggle}
    use:tip={playing ? "Pause" : "Play"}
  >
    {#if playing}
      <Pause class="w-4 h-4" fill="currentColor" />
    {:else}
      <Play class="w-4 h-4 translate-x-px" fill="currentColor" />
    {/if}
  </button>

  <div class="flex-1 min-w-0 flex flex-col gap-1">
    <!-- Waveform: bar heights = clip peaks; played bars fill primary. Click / drag to seek. -->
    <div
      bind:this={trackEl}
      class="relative h-7 cursor-pointer touch-none"
      onpointerdown={onPointerDown}
      onpointermove={onPointerMove}
      onpointerup={onPointerUp}
      role="slider"
      tabindex="0"
      aria-label="Seek"
      aria-valuemin={0}
      aria-valuemax={Math.round(dur)}
      aria-valuenow={Math.round(cur)}
    >
      <!-- SVG bars: preserveAspectRatio="none" scales every rect's x by the same
           factor, so all bars render at identical width (DOM flex bars land on
           fractional pixels → antialias to uneven widths). -->
      <svg class="w-full h-full" viewBox="0 0 {BARS * 2} 100" preserveAspectRatio="none">
        {#each bars as p, i}
          {@const h = 15 + p * 85}
          <rect
            x={i * 2 + 0.4}
            y={(100 - h) / 2}
            width="1.2"
            height={h}
            rx="0.6"
            class={i / BARS < frac ? "text-primary" : "text-secondary"}
            fill="currentColor"
          />
        {/each}
      </svg>
    </div>
    <div class="flex justify-between items-center gap-2 text-[0.6875rem] text-txtsecondary tabular-nums leading-none">
      <span>{fmt(cur)}</span>
      {#if label}<span class="truncate normal-nums">{label}</span>{/if}
      <span>{fmt(dur)}</span>
    </div>
  </div>
</div>
