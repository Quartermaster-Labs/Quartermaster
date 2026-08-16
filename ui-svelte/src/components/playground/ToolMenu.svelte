<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { Sparkles, Check } from "lucide-svelte";
  import type { ToolItem } from "./chatHelpers";

  // Composer tool menu: the agent modes (rewrite, shopping…). Settings that just
  // flip a flag on a normal chat — reasoning, web search, qm tools — live in the
  // Configs popover instead; this menu is for "what is the assistant doing".
  let { items, disabled = false }: { items: ToolItem[]; disabled?: boolean } = $props();

  let open = $state(false);
  let toggleEl = $state<HTMLButtonElement>();
  // The button wears the active mode's own icon (shopping cart, pen…) instead of
  // a badge — a dot in the corner reads as an unread notification, an icon reads
  // as "you are in shopping mode".
  const activeItem = $derived(items.find((i) => i.active));
  const ToggleIcon = $derived(activeItem?.icon ?? Sparkles);

  function closeOnOutside(node: HTMLElement) {
    function onClick(e: MouseEvent) {
      const t = e.target as Node;
      if (!node.contains(t) && !toggleEl?.contains(t)) open = false;
    }
    document.addEventListener("click", onClick, true);
    return { destroy: () => document.removeEventListener("click", onClick, true) };
  }
</script>

<div class="relative">
  {#if open}
    <div
      use:closeOnOutside
      class="absolute bottom-full left-0 mb-2 w-72 z-20 flex flex-col gap-0.5 p-1.5 rounded-lg border border-card-border bg-surface shadow-lg text-[0.8125rem]"
    >
      <span class="px-2 pt-1 pb-1.5 text-xs uppercase tracking-wide text-txtsecondary">Tools</span>
      {#each items as item (item.key)}
        {@const Icon = item.icon}
        <button
          type="button"
          class="flex items-start gap-2 w-full text-left px-2 py-1.5 rounded-md transition-colors hover:bg-secondary disabled:opacity-40 disabled:hover:bg-transparent {item.active ? 'text-txtmain' : 'text-txtsecondary'}"
          disabled={item.disabled}
          onclick={() => {
            item.onToggle();
            open = false; // picking a mode is the whole point of the menu
          }}
        >
          <Icon class="w-4 h-4 mt-0.5 shrink-0 {item.active ? 'text-primary' : ''}" />
          <span class="min-w-0 flex-1">
            <span class="block">{item.label}</span>
            <span class="block text-xs text-txtsecondary/80">{item.description}</span>
          </span>
          {#if item.active}
            <Check class="w-4 h-4 mt-0.5 shrink-0 text-primary" />
          {/if}
        </button>
      {/each}
    </div>
  {/if}

  <button
    bind:this={toggleEl}
    class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {activeItem
      ? 'bg-primary/10 text-primary'
      : open
        ? 'bg-secondary text-txtmain shadow-inner'
        : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
    onclick={() => (open = !open)}
    {disabled}
    use:tip={activeItem ? `${activeItem.label} mode` : "Tools"}
  >
    <ToggleIcon class="w-[1.125rem] h-[1.125rem]" />
  </button>
</div>
