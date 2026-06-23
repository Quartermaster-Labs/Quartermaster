<script lang="ts">
  import { models } from "../../stores/api";
  import { groupModels } from "../../lib/modelUtils";
  import { ChevronDown } from "lucide-svelte";
  import type { Model } from "../../lib/types";

  interface Props {
    value: string;
    placeholder?: string;
    disabled?: boolean;
    capabilities?: string[];
    matchAny?: boolean;
    compact?: boolean;
    onChange?: (value: string) => void;
  }

  let { value = $bindable(), placeholder = "Select a model...", disabled = false, capabilities, matchAny = false, compact = false, onChange }: Props = $props();

  let grouped = $derived(groupModels($models, capabilities, matchAny));
  let hasMatching = $derived(grouped.localMatching.length > 0);
  let hasModels = $derived(hasMatching || grouped.local.length > 0 || Object.keys(grouped.peersByProvider).length > 0);

  let open = $state(false);

  type Item = { value: string; label: string; alias?: boolean };

  // Flatten the grouped models (+ aliases) into render-ready sections.
  let sections = $derived.by(() => {
    const withAliases = (arr: Model[]): Item[] =>
      arr.flatMap((m) => {
        const items: Item[] = [{ value: m.id, label: m.id }];
        if (m.aliases) for (const a of m.aliases) items.push({ value: a, label: a, alias: true });
        return items;
      });

    const out: { label: string; items: Item[] }[] = [];
    if (hasMatching) out.push({ label: "Matching Capabilities", items: withAliases(grouped.localMatching) });
    if (grouped.local.length > 0) out.push({ label: "Local", items: withAliases(grouped.local) });
    for (const [peerId, peerModels] of Object.entries(grouped.peersByProvider).sort(([a], [b]) => a.localeCompare(b))) {
      out.push({ label: `Peer: ${peerId}`, items: peerModels.map((m) => ({ value: m.id, label: m.id })) });
    }
    return out;
  });

  function select(v: string) {
    value = v;
    open = false;
    onChange?.(v);
  }

  // ponytail: mouse + Escape only; no arrow-key listbox nav. Add if asked.
  function clickOutside(node: HTMLElement) {
    function onClick(e: MouseEvent) {
      if (!node.contains(e.target as Node)) open = false;
    }
    document.addEventListener("click", onClick, true);
    return { destroy: () => document.removeEventListener("click", onClick, true) };
  }
</script>

{#if hasModels}
  <div class="relative {compact ? 'w-full' : 'min-w-0 flex-1 basis-48'}" use:clickOutside>
    <button
      type="button"
      {disabled}
      class="w-full flex items-center justify-between gap-2 rounded border border-card-border bg-surface text-left focus:outline-none focus:border-primary disabled:opacity-50 {compact
        ? 'px-2.5 py-1.5 text-[0.8125rem]'
        : 'px-3 py-2'}"
      onclick={() => (open = !open)}
      onkeydown={(e) => e.key === "Escape" && (open = false)}
    >
      <span class="truncate {value ? '' : 'text-txtsecondary'}">{value || placeholder}</span>
      <ChevronDown class="w-4 h-4 shrink-0 transition-transform {open ? 'rotate-180' : ''}" />
    </button>

    {#if open}
      <div
        class="absolute left-0 right-0 top-full mt-1 z-30 max-h-64 overflow-y-auto pretty-scroll rounded-md border border-card-border bg-surface shadow-lg py-1 text-[0.8125rem]"
      >
        {#each sections as sec (sec.label)}
          <div class="px-2.5 py-1 text-[0.65rem] uppercase tracking-wide text-txtsecondary">{sec.label}</div>
          {#each sec.items as it (it.value)}
            <button
              type="button"
              class="w-full text-left truncate px-2.5 py-1.5 hover:bg-secondary transition-colors {it.value === value
                ? 'text-primary'
                : 'text-txtmain'} {it.alias ? 'pl-5' : ''}"
              onclick={() => select(it.value)}
            >
              {it.alias ? "↳ " : ""}{it.label}
            </button>
          {/each}
        {/each}
      </div>
    {/if}
  </div>
{/if}
