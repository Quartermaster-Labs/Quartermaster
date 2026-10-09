<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { X } from "lucide-svelte";
  import AudioPlayer from "./AudioPlayer.svelte";
  import { splitSpans, spanWeight, findPauses, spanTimes, activeSpan, type VoiceMap } from "../../lib/takeTiming";
  import type { Turn } from "../../stores/speechHistory";

  // The Speech studio's "now playing" stage: the take being listened to, lifted
  // above the grid with its whole text, and the sentence being spoken painted
  // the way the chat's read-aloud paints it (--color-speak, spoken text dimmed).
  // The timing is estimated, not reported - see lib/takeTiming for why.
  let {
    turn,
    volume,
    startAt = 0,
    onclose,
  }: { turn: Turn; volume: number; startAt?: number; onclose: () => void } = $props();

  let spans = $derived(splitSpans(turn.text));
  let weights = $derived(spans.map((s) => spanWeight(turn.text.slice(s.start, s.end))));
  let voice = $state<VoiceMap | undefined>(undefined);
  let duration = $state(0);
  let clock = $state(0);
  let times = $derived(spanTimes(weights, duration, voice));
  // Clock 0 = not started or finished: show the text plain, nothing dimmed,
  // so a finished take reads like a page rather than a half-greyed one.
  let active = $derived(clock > 0 && duration > 0 ? activeSpan(times, clock) : -1);

  // Plain text between the spans (whitespace, line breaks) rendered as-is.
  let pieces = $derived.by(() => {
    const out: { text: string; i: number }[] = [];
    let at = 0;
    spans.forEach((s, i) => {
      if (s.start > at) out.push({ text: turn.text.slice(at, s.start), i: -1 });
      out.push({ text: turn.text.slice(s.start, s.end), i });
      at = s.end;
    });
    if (at < turn.text.length) out.push({ text: turn.text.slice(at), i: -1 });
    return out;
  });

  function onDecoded(samples: Float32Array, rate: number) {
    duration = samples.length / rate;
    voice = findPauses(samples, rate);
  }

  // Keep the spoken sentence in view inside the text box. Scrolls only this box
  // (scrollIntoView would also nudge every scrollable ancestor).
  let textEl = $state<HTMLElement | null>(null);
  $effect(() => {
    const box = textEl;
    if (!box || active < 0) return;
    const el = box.querySelector<HTMLElement>(`[data-span="${active}"]`);
    if (!el) return;
    // The box is `relative`, so it is the span's offsetParent.
    const top = el.offsetTop;
    const bottom = top + el.offsetHeight;
    if (top < box.scrollTop || bottom > box.scrollTop + box.clientHeight) {
      box.scrollTo({ top: Math.max(0, top - box.clientHeight / 3), behavior: "smooth" });
    }
  });
</script>

<div class="shrink-0 mb-2.5 rounded-[0.625rem] border border-primary/50 bg-surface p-4 flex flex-col gap-3 shadow-sm">
  <div class="flex items-center gap-2 min-w-0">
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary shrink-0">Now playing</span>
    <span class="shrink-0 rounded border border-card-border px-1.5 py-px font-mono text-micro text-txtsecondary">{turn.voice || "Default"}</span>
    {#if turn.model}
      <span class="truncate rounded border border-card-border px-1.5 py-px font-mono text-micro text-txtsecondary" use:tip={turn.model}>{turn.model}</span>
    {/if}
    <button class="icon-btn ml-auto !w-6 !h-6" onclick={onclose} use:tip={"Close"} aria-label="Close">
      <X class="w-3.5 h-3.5" />
    </button>
  </div>

  <div
    bind:this={textEl}
    class="relative max-h-[calc(32vh/var(--qm-scale))] overflow-y-auto pretty-scroll text-[0.9375rem] leading-relaxed text-txtmain whitespace-pre-wrap"
  >
    {#each pieces as p}{#if p.i < 0}{p.text}{:else}<span
          data-span={p.i}
          class="rounded-sm transition-colors duration-200 {p.i === active
            ? 'bg-speak-bg text-speak'
            : active >= 0 && p.i < active
              ? 'text-txtsecondary'
              : ''}">{p.text}</span
        >{/if}{/each}
  </div>

  {#if turn.audio}
    <AudioPlayer src={turn.audio} {volume} wide autoplay {startAt} ontime={(t) => (clock = t)} ondecoded={onDecoded} />
  {/if}
</div>
