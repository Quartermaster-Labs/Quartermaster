<script lang="ts">
  import { ChevronDown } from "lucide-svelte";

  interface Props {
    value: string;
    options: { value: string; label: string; disabled?: boolean }[];
    disabled?: boolean;
    compact?: boolean;
  }

  let { value = $bindable(), options, disabled = false, compact = false }: Props = $props();

  let open = $state(false);
  let selectedLabel = $derived(options.find((o) => o.value === value)?.label ?? value);

  // Drop direction + height, measured against the viewport on every open. The
  // list lives inside popovers (the composer's settings panel) that are NOT
  // scroll containers, so an unclamped 16rem list hanging off the bottom row
  // escapes the h-screen root and gives the whole document scrollbars.
  let btnEl = $state<HTMLButtonElement>();
  let up = $state(false);
  let maxH = $state(256);

  const GAP = 8; // mt-1/mb-1 plus a little breathing room
  const MIN_H = 96;

  // The viewport, narrowed to the nearest clipping ancestor: dropping out of a
  // scrollable settings panel is just as invisible as dropping off the page.
  function bounds(): { top: number; bottom: number } {
    // `zoom` scales layout but not viewport units, so measure the box the page
    // actually paints into rather than window.innerHeight.
    let top = 0;
    let bottom = document.documentElement.clientHeight;
    let el = btnEl?.parentElement ?? null;
    while (el && el !== document.body) {
      const o = getComputedStyle(el).overflowY;
      if (o !== "visible") {
        const r = el.getBoundingClientRect();
        top = Math.max(top, r.top);
        bottom = Math.min(bottom, r.bottom);
      }
      el = el.parentElement;
    }
    return { top, bottom };
  }

  function place() {
    if (!btnEl) return;
    const r = btnEl.getBoundingClientRect();
    const b = bounds();
    const below = b.bottom - r.bottom - GAP;
    const above = r.top - b.top - GAP;
    up = below < Math.min(256, above) && above > below;
    maxH = Math.max(MIN_H, Math.min(256, Math.floor(up ? above : below)));
  }

  function toggle() {
    if (!open) place();
    open = !open;
  }

  function select(v: string) {
    value = v;
    open = false;
  }

  function clickOutside(node: HTMLElement) {
    function onClick(e: MouseEvent) {
      if (!node.contains(e.target as Node)) open = false;
    }
    document.addEventListener("click", onClick, true);
    return { destroy: () => document.removeEventListener("click", onClick, true) };
  }
</script>

<div class="relative w-full" use:clickOutside>
  <button
    bind:this={btnEl}
    type="button"
    {disabled}
    class="w-full flex items-center justify-between gap-2 rounded border border-card-border bg-surface text-left focus:outline-none focus:border-primary disabled:opacity-50 {compact
      ? 'px-2.5 py-1.5 text-[0.8125rem]'
      : 'px-3 py-2'}"
    onclick={toggle}
    onkeydown={(e) => e.key === "Escape" && (open = false)}
  >
    <span class="truncate">{selectedLabel}</span>
    <ChevronDown class="w-4 h-4 shrink-0 transition-transform {open ? 'rotate-180' : ''}" />
  </button>

  {#if open}
    <div
      style="max-height: {maxH}px"
      class="absolute left-0 z-30 min-w-full w-max max-w-[16rem] overflow-y-auto overscroll-contain pretty-scroll rounded-md border border-card-border bg-surface shadow-lg py-1 text-[0.8125rem] {up
        ? 'bottom-full mb-1'
        : 'top-full mt-1'}"
    >
      {#each options as o (o.value)}
        <button
          type="button"
          disabled={o.disabled}
          class="w-full text-left whitespace-nowrap px-2.5 py-1.5 transition-colors {o.disabled
            ? 'text-txtsecondary opacity-40 cursor-not-allowed'
            : `hover:bg-secondary ${o.value === value ? 'text-primary' : 'text-txtmain'}`}"
          onclick={() => !o.disabled && select(o.value)}
        >
          {o.label}
        </button>
      {/each}
    </div>
  {/if}
</div>
