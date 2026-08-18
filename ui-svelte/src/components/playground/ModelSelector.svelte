<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { models } from "../../stores/api";
  import { groupModels, modelCategory, matchesCapabilities, prettifyModelName, type ModelCategory } from "../../lib/modelUtils";
  import { baseKey, familyOf } from "../../lib/modelTable";
  import { ChevronDown, Star } from "lucide-svelte";
  import { userPref } from "../../stores/prefs";
  import type { Model } from "../../lib/types";

  // Starred models float to the top of their section. Keyed by the GROUP's root
  // id (not the selected variant): starring is a statement about the model, so
  // it survives switching variant. Server-backed like the rest of playground
  // prefs, so the pins follow the user rather than the browser.
  const favoritesStore = userPref<string[]>("modelFavorites", []);
  let favSet = $derived(new Set($favoritesStore));
  function toggleFavorite(key: string): void {
    favoritesStore.update((f) => (f.includes(key) ? f.filter((k) => k !== key) : [...f, key]));
  }

  interface Props {
    value: string;
    placeholder?: string;
    disabled?: boolean;
    capabilities?: string[];
    matchAny?: boolean;
    // Hard-filter local models to this category (chat=llm, image, tts, ...).
    // Peers are left in — they aren't categorized locally.
    category?: ModelCategory;
    compact?: boolean;
    // Borderless trigger that reads as a clickable label (name + chevron).
    ghost?: boolean;
    // Open the menu upward — for triggers pinned near the viewport bottom.
    dropUp?: boolean;
    onChange?: (value: string) => void;
  }

  let { value = $bindable(), placeholder = "Select a model...", disabled = false, capabilities, matchAny = false, category, compact = false, ghost = false, dropUp = false, onChange }: Props = $props();

  // Hard-filter local models so non-matching ones don't appear at all. Peers
  // stay in — they aren't categorized locally. For "llm" also drop rerankers
  // (they bucket as llm but can't chat).
  let filteredModels = $derived.by(() => {
    if (category) {
      return $models.filter(
        (m) =>
          m.peerID ||
          (modelCategory(m) === category && !(category === "llm" && m.capabilities?.reranker))
      );
    }
    if (capabilities && capabilities.length > 0) {
      return $models.filter((m) => m.peerID || matchesCapabilities(m, capabilities, matchAny));
    }
    return $models;
  });
  let grouped = $derived(groupModels(filteredModels));
  let hasModels = $derived(grouped.local.length > 0 || Object.keys(grouped.peersByProvider).length > 0);

  let open = $state(false);

  type Pick = { value: string; label: string };
  type Group = { base: Pick; variants: Pick[] };

  // Variants share a `family` and are named base-id + suffix; show the suffix.
  function suffix(id: string, base: string): string {
    const s = id.startsWith(base) ? id.slice(base.length).replace(/^[-_:.]/, "") : id;
    return s || id;
  }

  const SEP = /[-_:.]/;

  // Collapse a list into id-prefix groups. A variant is `baseId + sep + suffix`
  // where baseId is itself a listed model; we don't trust `family` because it
  // lumps unrelated quants. Each model attaches to its nearest existing-prefix
  // ancestor, then walks up to the root header. Clean suffix is guaranteed
  // since the root is always a real prefix.
  // ponytail: O(n²) prefix scan — fine for dropdown-sized lists.
  function buildGroups(arr: Model[]): Group[] {
    const ids = arr.map((m) => m.id);
    const parentId = (id: string): string => {
      let best = "";
      for (const cand of ids) {
        if (cand !== id && id.startsWith(cand) && SEP.test(id[cand.length]) && cand.length > best.length) best = cand;
      }
      return best;
    };
    const rootId = (id: string): string => {
      let cur = id;
      for (let p = parentId(cur); p; p = parentId(cur)) cur = p;
      return cur;
    };

    const groups = new Map<string, Group>();
    for (const m of arr) {
      const r = rootId(m.id);
      if (!groups.has(r)) groups.set(r, { base: { value: r, label: r }, variants: [] });
      if (m.id !== r) groups.get(r)!.variants.push({ value: m.id, label: suffix(m.id, r) });
    }
    // Aliases are alternate ids for a model already listed by its real id —
    // selecting one is identical to selecting the base, so don't list them as
    // separate "variants" (that's how an alias like "gemma4-12b-qat" leaked in).
    const out = [...groups.values()];
    for (const g of out) g.variants.sort((a, b) => a.value.localeCompare(b.value));
    return out.sort((a, b) => a.base.value.localeCompare(b.base.value));
  }

  // Starred groups first (buildGroups already sorted alphabetically, and sort is
  // stable), so the pins keep their relative order within each section.
  function pinned(groups: Group[]): Group[] {
    return [...groups].sort((a, b) => Number(favSet.has(b.base.value)) - Number(favSet.has(a.base.value)));
  }

  // A finetune family and the id-groups under it — same axis the Models page
  // uses (familyOf reduces a base id to <model><size>), so "thinkingcap-qwen3.6-
  // 27b" and "qwen3.6-27b-heretic" sit under one heading here too.
  type Family = { key: string; label: string; groups: Group[] };

  // families clusters an ALREADY-ORDERED list without reordering it: a family
  // takes the slot of its best-ranked member, so starring one finetune pulls its
  // relatives up with it and the pin order stays legible.
  function families(groups: Group[]): Family[] {
    const out: Family[] = [];
    const byKey = new Map<string, Family>();
    for (const g of groups) {
      const key = familyOf(baseKey(g.base.value));
      const f = byKey.get(key);
      if (f) {
        f.groups.push(g);
        continue;
      }
      const fresh = { key, label: prettifyModelName(key), groups: [g] };
      byKey.set(key, fresh);
      out.push(fresh);
    }
    return out;
  }

  let sections = $derived.by(() => {
    const out: { label: string; families: Family[] }[] = [];
    const build = (arr: Model[]) => families(pinned(buildGroups(arr)));
    if (grouped.local.length > 0) out.push({ label: "Local", families: build(grouped.local) });
    for (const [peerId, peerModels] of Object.entries(grouped.peersByProvider).sort(([a], [b]) => a.localeCompare(b))) {
      out.push({ label: `Peer: ${peerId}`, families: build(peerModels) });
    }
    return out;
  });

  function select(v: string) {
    value = v;
    open = false;
    onChange?.(v);
  }

  // Variant pill: filled when selected, outlined otherwise.
  const pillCls = (sel: boolean) =>
    `rounded-full border px-2.5 py-1 text-xs transition-colors ${
      sel
        ? "bg-primary border-primary text-white"
        : "border-card-border text-txtsecondary hover:text-txtmain hover:border-primary/50"
    }`;

  // ponytail: mouse + Escape only; no arrow-key listbox nav. Add if asked.
  function clickOutside(node: HTMLElement) {
    function onClick(e: MouseEvent) {
      if (!node.contains(e.target as Node)) open = false;
    }
    document.addEventListener("click", onClick, true);
    return { destroy: () => document.removeEventListener("click", onClick, true) };
  }
</script>

{#snippet entry(g: Group)}
  {@const fav = favSet.has(g.base.value)}
  {#if g.variants.length > 0}
    <!-- Model with variants: name header + Default + variant pills. -->
    <div class="px-2.5 py-1.5">
      <div class="flex items-start gap-1.5">
        <button
          type="button"
          class="mt-0.5 shrink-0 p-0.5 transition-colors {fav ? 'text-warning' : 'text-txtsecondary/40 hover:text-txtsecondary'}"
          onclick={(e) => (e.stopPropagation(), toggleFavorite(g.base.value))}
          aria-label={fav ? "Unpin favorite" : "Pin as favorite"}
          use:tip={fav ? "Unpin from the top" : "Pin to the top"}
        >
          <Star class="w-3.5 h-3.5" fill={fav ? "currentColor" : "none"} />
        </button>
        <div class="break-words font-medium text-txtmain">{g.base.label}</div>
      </div>
      <div class="mt-1.5 flex flex-wrap gap-1.5">
        <button type="button" class={pillCls(g.base.value === value)} onclick={() => select(g.base.value)}>Default</button>
        {#each g.variants as v (v.value)}
          <button type="button" class={pillCls(v.value === value)} onclick={() => select(v.value)}>{v.label}</button>
        {/each}
      </div>
    </div>
  {:else}
    <div class="flex items-start gap-1.5 px-2.5 py-1.5 hover:bg-secondary transition-colors">
      <button
        type="button"
        class="mt-0.5 shrink-0 p-0.5 transition-colors {fav ? 'text-warning' : 'text-txtsecondary/40 hover:text-txtsecondary'}"
        onclick={(e) => (e.stopPropagation(), toggleFavorite(g.base.value))}
        aria-label={fav ? "Unpin favorite" : "Pin as favorite"}
        use:tip={fav ? "Unpin from the top" : "Pin to the top"}
      >
        <Star class="w-3.5 h-3.5" fill={fav ? "currentColor" : "none"} />
      </button>
      <button
        type="button"
        class="flex-1 text-left break-words {g.base.value === value ? 'text-primary' : 'text-txtmain'}"
        onclick={() => select(g.base.value)}
      >
        {g.base.label}
      </button>
    </div>
  {/if}
{/snippet}

{#if hasModels}
  <div class="relative {ghost ? 'inline-block max-w-full' : compact ? 'w-full' : 'min-w-0 flex-1 basis-48'}" use:clickOutside>
    {#if ghost}
      <button
        type="button"
        {disabled}
        class="max-w-full flex items-center gap-1 text-xs font-medium text-txtsecondary hover:text-txtmain focus:outline-none disabled:opacity-50 transition-colors"
        onclick={() => (open = !open)}
        onkeydown={(e) => e.key === "Escape" && (open = false)}
        use:tip={value || placeholder}
      >
        <span class="truncate {value ? '' : 'text-txtsecondary'}">{value || placeholder}</span>
        <ChevronDown class="w-3.5 h-3.5 shrink-0 transition-transform {open ? 'rotate-180' : ''}" />
      </button>
    {:else}
      <button
        type="button"
        {disabled}
        class="w-full flex items-center justify-between gap-2 rounded border border-card-border bg-surface text-left focus:outline-none focus:border-primary disabled:opacity-50 {compact
          ? 'px-2.5 py-1.5 text-[0.8125rem]'
          : 'px-3 py-2'}"
        onclick={() => (open = !open)}
        onkeydown={(e) => e.key === "Escape" && (open = false)}
        use:tip={value || placeholder}
      >
        <span class="truncate {value ? '' : 'text-txtsecondary'}">{value || placeholder}</span>
        <ChevronDown class="w-4 h-4 shrink-0 transition-transform {open ? 'rotate-180' : ''}" />
      </button>
    {/if}

    {#if open}
      <div
        class="absolute z-30 max-h-80 overflow-y-auto pretty-scroll rounded-md border border-card-border bg-surface shadow-lg py-1.5 text-[0.8125rem] {ghost
          ? 'left-1/2 -translate-x-1/2 min-w-[22rem] max-w-[30rem]'
          : 'left-0 right-0'} {dropUp ? 'bottom-full mb-1' : 'top-full mt-1'}"
      >
        {#each sections as sec (sec.label)}
          <div class="px-2.5 py-1 text-[0.65rem] uppercase tracking-wide text-txtsecondary">{sec.label}</div>
          {#each sec.families as fam (fam.key)}
            {#if fam.groups.length > 1}
              <!-- Finetune family: one rail + heading over its members, same
                   grouping axis the Models page uses. -->
              <!-- my-2 (not my-0.5): adjacent families collapse to ONE margin,
                   and with the same rail + tint on both a hairline gap reads as
                   a single continuous block. Rounded ends close each family. -->
              <div class="my-2 rounded-r border-l-2 border-l-primary/60 bg-primary/[0.06] pb-1">
                <div
                  class="px-2.5 pt-1 pb-0.5 font-mono text-[0.65rem] uppercase tracking-wide text-primary"
                  use:tip={`${fam.label} - ${fam.groups.length} finetunes of one base model`}
                >
                  {fam.label}
                </div>
                {#each fam.groups as g (g.base.value)}
                  {@render entry(g)}
                {/each}
              </div>
            {:else}
              {@render entry(fam.groups[0])}
            {/if}
          {/each}
        {/each}
      </div>
    {/if}
  </div>
{/if}
