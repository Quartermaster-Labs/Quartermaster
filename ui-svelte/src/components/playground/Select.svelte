<script lang="ts">
  import { ChevronDown } from "lucide-svelte";

  interface Props {
    value: string;
    options: { value: string; label: string }[];
    disabled?: boolean;
    compact?: boolean;
  }

  let { value = $bindable(), options, disabled = false, compact = false }: Props = $props();

  let open = $state(false);
  let selectedLabel = $derived(options.find((o) => o.value === value)?.label ?? value);

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
    type="button"
    {disabled}
    class="w-full flex items-center justify-between gap-2 rounded border border-card-border bg-surface text-left focus:outline-none focus:border-primary disabled:opacity-50 {compact
      ? 'px-2.5 py-1.5 text-[0.8125rem]'
      : 'px-3 py-2'}"
    onclick={() => (open = !open)}
    onkeydown={(e) => e.key === "Escape" && (open = false)}
  >
    <span class="truncate">{selectedLabel}</span>
    <ChevronDown class="w-4 h-4 shrink-0 transition-transform {open ? 'rotate-180' : ''}" />
  </button>

  {#if open}
    <div
      class="absolute left-0 right-0 top-full mt-1 z-30 max-h-64 overflow-y-auto pretty-scroll rounded-md border border-card-border bg-surface shadow-lg py-1 text-[0.8125rem]"
    >
      {#each options as o (o.value)}
        <button
          type="button"
          class="w-full text-left truncate px-2.5 py-1.5 hover:bg-secondary transition-colors {o.value === value ? 'text-primary' : 'text-txtmain'}"
          onclick={() => select(o.value)}
        >
          {o.label}
        </button>
      {/each}
    </div>
  {/if}
</div>
