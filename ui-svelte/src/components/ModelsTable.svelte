<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { ChevronRight, Settings, Play, X, MessageCircle, Star, Search } from "lucide-svelte";
  import type { Model } from "../lib/types";
  import {
    buildRows,
    filterRows,
    groupFamilies,
    sortRows,
    pickQuant,
    fmtGB,
    rowLabel,
    isLive,
    type FamilyGroup,
    type ModelRow,
    type QuantEntry,
    type SortDir,
    type SortKey,
    type StateFilter,
  } from "../lib/modelTable";

  interface Props {
    models: Model[];
    search: string;
    // Not named `state`: that shadows the $state rune inside this component.
    stateFilter: StateFilter;
    showUnlisted: boolean;
    display: "id" | "name";
    sortKey: SortKey;
    sortDir: SortDir;
    pending: Record<string, boolean>;
    favorites: string[];
    // Per-model actions, owned by the page (load/cancel/playground/config).
    onLoad: (id: string) => void;
    onCancel: (id: string) => void;
    onPlay?: (m: Model) => void;
    canPlay?: (m: Model) => boolean;
    playLabel?: (m: Model) => string;
    onConfig: (family: string, openFor: string) => void;
    onSort: (key: SortKey) => void;
    onFavorite: (key: string) => void;
  }

  let {
    models,
    search = $bindable(""),
    stateFilter,
    showUnlisted,
    display,
    sortKey,
    sortDir,
    pending,
    favorites,
    onLoad,
    onCancel,
    onPlay,
    canPlay,
    playLabel,
    onConfig,
    onSort,
    onFavorite,
  }: Props = $props();

  // Which quant / variant each row is pointed at, and which rows are expanded.
  // Keyed by row key so a model status tick (the whole list is rebuilt from the
  // store) doesn't reset it.
  let quantPick = $state<Record<string, string>>({});
  let variantPick = $state<Record<string, string>>({});
  let expanded = $state<Record<string, boolean>>({});

  // Search is folded into the Model column header — the column it filters. It
  // collapses back to an icon only when empty, so an active filter can never be
  // hidden behind one.
  let searchOpen = $state(false);
  let searchInput = $state<HTMLInputElement | null>(null);
  function openSearch(): void {
    searchOpen = true;
    requestAnimationFrame(() => searchInput?.focus());
  }
  function closeSearch(): void {
    search = "";
    searchOpen = false;
  }
  // "/" focuses search, the way every table-shaped tool does it.
  function onKeydown(e: KeyboardEvent): void {
    const t = e.target as HTMLElement | null;
    if (e.key === "/" && t?.tagName !== "INPUT" && t?.tagName !== "TEXTAREA") {
      e.preventDefault();
      openSearch();
    }
  }

  let favSet = $derived(new Set(favorites));
  let rows = $derived(
    sortRows(filterRows(buildRows(models), { search, state: stateFilter, showUnlisted }), sortKey, sortDir, {
      favorites: favSet,
      selected: selectedQuant,
    }),
  );
  // Finetunes of one base model are clustered under a heading, but stay their own
  // rows: they carry different weights, sizes and behaviour, so folding them into
  // one row would let "load Qwen3.6-27B" quietly start an uncensored tune.
  let groups = $derived(groupFamilies(rows));
  // Zebra parity is assigned across the whole table, not per group, so the
  // striping doesn't restart mid-list.
  let stripe = $derived(new Map(groups.flatMap((g) => g.rows).map((r, i) => [r.key, i % 2 === 1])));
  // Which rows belong to a family of more than one tune (they get the rail).
  let grouped = $derived(new Set(groups.filter((g) => g.rows.length > 1).flatMap((g) => g.rows.map((r) => r.key))));

  // A family is drawn as a rail spanning exactly its rows, labelled with vertical
  // text — a heading row said where a family started but nothing said where it
  // ended, and it cost a full row of vertical space per family. The rail has to
  // cover expanded quant sub-rows too, so the span is counted, not assumed.
  function familySpan(g: FamilyGroup): number {
    return g.rows.reduce((n, r) => n + 1 + (expanded[r.key] && r.quants.length > 1 ? r.quants.length : 0), 0);
  }

  // The quant a row is showing: the explicit pick when it still exists, else the
  // loaded one, else the largest.
  function selectedQuant(row: ModelRow): QuantEntry {
    return row.quants.find((q) => q.quant === quantPick[row.key]) ?? pickQuant(row);
  }

  // The model a row's numbers and actions refer to: the picked variant of the
  // picked quant, else that quant's base.
  function selectedModel(row: ModelRow): Model {
    const q = selectedQuant(row);
    const key = `${row.key} ${q.quant}`;
    return q.variants.find((v) => v.model.id === variantPick[key])?.model ?? q.base;
  }

  function pickVariant(row: ModelRow, id: string): void {
    variantPick[`${row.key} ${selectedQuant(row).quant}`] = id;
  }
  function variantSelected(row: ModelRow, id: string): boolean {
    return selectedModel(row).id === id;
  }

  const SORTS: { key: SortKey; label: string; num?: boolean }[] = [
    { key: "name", label: "Model" },
    { key: "quant", label: "Quant" },
    { key: "size", label: "Size", num: true },
    { key: "vram", label: "Est VRAM", num: true },
    { key: "ram", label: "Est RAM", num: true },
  ];

  function dotClass(s: string): string {
    if (s === "ready") return "bg-success";
    if (s === "starting") return "bg-warning animate-pulse";
    if (s === "stopping") return "bg-error animate-pulse";
    return "bg-txtsecondary/60";
  }

  // Rows carry no rules — alternating bands do the separating. A loaded row
  // overrides the band entirely (left accent + tint, green ready / amber
  // transitional): it is what the operator came to the page for, and that reads
  // at a glance across a long table.
  function rowTone(live: boolean, s: string, odd: boolean): string {
    if (!live) return `${odd ? "bg-secondary/25" : ""} hover:bg-secondary/50`;
    if (s === "ready") return "bg-success/[0.07] hover:bg-success/[0.12]";
    return "bg-warning/[0.07] hover:bg-warning/[0.12]";
  }

  // The left accent rides the row's FIRST CELL, not the <tr>: under
  // border-separate a border on a table row is not painted at all.
  function rowAccent(live: boolean, s: string): string {
    if (!live) return "border-l-4 border-l-transparent";
    return `border-l-4 ${s === "ready" ? "border-l-success" : "border-l-warning"}`;
  }

  // Sticky lives on the CELLS, not on <thead>: under border-collapse a sticky
  // thead/tr is unreliable and its collapsed bottom border scrolls away with the
  // body regardless — so each th carries the offset, its own opaque background
  // (rows must not show through) and the rule as an inset shadow.
  const thCls = "sticky top-0 z-20 bg-surface shadow-[inset_0_-1px_0_var(--color-card-border)]";
  // Same typography as the sort buttons, minus the button: these columns don't sort.
  const headCls = "py-2 text-micro font-medium uppercase tracking-wide text-txtsecondary";

  // `btn-primary-text` rather than a hardcoded white: the light theme's primary
  // is brown and wants the theme's own on-primary ink.
  // `mono` only for the quant row — those pills carry literal quant strings
  // (UD-Q4_K_XL). The variant pills next to them are labels (Default, game,
  // vision) and read better in the UI face.
  const pillCls = (sel: boolean, mono = false) =>
    `rounded-full border px-2 py-0.5 text-micro transition-colors ${mono ? "font-mono" : "font-medium"} ${
      sel ? "bg-primary border-primary text-btn-primary-text" : "border-card-border text-txtsecondary hover:text-txtmain hover:border-primary/50"
    }`;
</script>

<svelte:window onkeydown={onKeydown} />

{#snippet actions(m: Model, base: Model, compact: boolean)}
  <!-- items-stretch, not items-center: the three actions are one control group,
       so the icon-only cogwheel must match the labelled buttons' height rather
       than shrink to its glyph. -->
  <div class="flex items-stretch justify-end gap-1">
    {#if !compact && onPlay && (canPlay?.(m) ?? true)}
      <button
        class="btn btn--sm inline-flex items-center gap-1.5 hover:border-primary hover:text-primary"
        onclick={() => onPlay?.(m)}
        use:tip={"Load and open in the playground"}
      >
        <MessageCircle class="w-3 h-3 shrink-0" />
        {playLabel?.(m) ?? "Chat"}
      </button>
    {/if}
    {#if pending[m.id]}
      <button class="btn btn--sm inline-flex items-center gap-1" onclick={() => onCancel(m.id)}>
        <X class="w-3 h-3" />
        Cancel
      </button>
    {:else}
      <button class="btn btn--sm inline-flex items-center gap-1 hover:border-primary hover:text-primary" onclick={() => onLoad(m.id)}>
        <Play class="w-3 h-3" />
        Load
      </button>
    {/if}
    <button
      class="btn btn--sm inline-flex items-center justify-center px-1.5 hover:border-primary hover:text-primary"
      onclick={() => onConfig(base.id, m.id)}
      aria-label="Edit parameters"
      use:tip={"Edit parameters / variants"}
    >
      <Settings class="w-3.5 h-3.5" />
    </button>
  </div>
{/snippet}

<div class="flex-1 min-h-0 overflow-auto pretty-scroll">
  <!-- overflow-visible is what makes the sticky header work, and it is not
       cosmetic: the global `table` rule (index.css) sets overflow-hidden for its
       rounded corners, and any overflow value turns the table into its own
       clipping container - a sticky th then pins to the TABLE, which scrolls out
       of the wrapper wholesale. The page is full-bleed now, so border-0 drops
       the frame that rule also applies and only the cell rules draw.
       border-separate (also from the global rule) is required too: sticky cells
       are unreliable under border-collapse. The header rule is an inset shadow
       on each th, since a collapsed bottom border would scroll away with it. -->
  <table class="w-full overflow-visible border-0 rounded-none border-separate border-spacing-0 text-left">
    <thead>
      <tr>
        <!-- The non-sortable columns carry a tooltip and an sr-only name rather
             than visible text: the ★ and family-rail columns are only a few
             pixels wide, so a label there wraps or overflows its own column. -->
        <th class="w-8 {thCls} {headCls}" use:tip={"Pinned favorites - click a star to pin a model to the top"}>
          <Star class="mx-auto w-3 h-3" />
          <span class="sr-only">Favorite</span>
        </th>
        <th class="w-6 {thCls} {headCls} px-0" use:tip={"Finetune family - the rail spans every tune of one base model"}>
          <span class="sr-only">Family</span>
        </th>
        {#each SORTS as col (col.key)}
          <th class="font-normal {thCls} {col.num ? 'text-right' : ''} {col.key === 'name' ? '' : 'w-28'}">
            <div class="flex items-center {col.num ? 'justify-end' : ''}">
              <button
                class="inline-flex items-center gap-1 px-3 py-2 text-micro font-medium uppercase tracking-wide text-txtsecondary hover:text-txtmain"
                onclick={() => onSort(col.key)}
                use:tip={"Sort ascending → descending → off"}
              >
                {col.label}
                <span class="text-micro {sortKey === col.key ? 'text-primary' : 'opacity-0'}">{sortDir === "asc" ? "▲" : "▼"}</span>
              </button>
              {#if col.key === "name"}
                {#if searchOpen || search}
                  <div class="relative ml-auto w-64 max-w-full">
                    <input
                      bind:this={searchInput}
                      bind:value={search}
                      type="text"
                      placeholder="Filter models…"
                      onkeydown={(e) => e.key === "Escape" && closeSearch()}
                      onblur={() => (searchOpen = !!search)}
                      class="w-full rounded border border-card-border bg-background pl-2 pr-6 py-1 font-mono text-micro focus:outline-none focus:border-primary"
                    />
                    {#if search}
                      <button
                        class="absolute right-1 top-1/2 -translate-y-1/2 p-0.5 text-txtsecondary hover:text-txtmain"
                        onclick={closeSearch}
                        aria-label="Clear search"
                      >
                        <X class="w-3 h-3" />
                      </button>
                    {/if}
                  </div>
                {:else}
                  <button class="ml-auto mr-2 p-1 text-txtsecondary hover:text-txtmain" onclick={openSearch} aria-label="Search models" use:tip={"Search models  ( / )"}>
                    <Search class="w-3.5 h-3.5" />
                  </button>
                {/if}
              {/if}
            </div>
          </th>
        {/each}
        <th class="w-56 {thCls} {headCls} px-3 text-center">Actions</th>
      </tr>
    </thead>
    <tbody>
      {#each groups as group (group.key)}
        {#each group.rows as row, ri (row.key)}
          {@const q = selectedQuant(row)}
          {@const m = selectedModel(row)}
          {@const multiQuant = row.quants.length > 1}
          {@const odd = stripe.get(row.key) ?? false}
          {@const fav = favSet.has(row.key)}
          {@const inFamily = grouped.has(row.key)}
          <tr class="transition-colors {rowTone(row.live, m.state, odd)}">
            <td class="pl-2 align-top py-2 {rowAccent(row.live, m.state)}">
              <button
                class="p-0.5 transition-colors {fav ? 'text-warning' : 'text-txtsecondary/40 hover:text-txtsecondary'}"
                onclick={() => onFavorite(row.key)}
                aria-label={fav ? "Unpin favorite" : "Pin as favorite"}
                use:tip={fav ? "Unpin from the top" : "Pin to the top"}
              >
                <Star class="w-3.5 h-3.5" fill={fav ? "currentColor" : "none"} />
              </button>
            </td>
            {#if inFamily && ri === 0}
              <!-- Family rail: one cell spanning every row of the family, so the
                   line itself marks both ends. Label runs bottom-up along it. -->
              <td rowspan={familySpan(group)} class="w-6 p-0 border-l-2 border-l-primary/60 bg-primary/[0.06]">
                <div
                  class="mx-auto flex items-center justify-center overflow-hidden font-mono text-micro uppercase tracking-wide text-primary [writing-mode:vertical-rl] rotate-180"
                  use:tip={`${group.label} - ${group.rows.length} finetunes of one base model`}
                >
                  {group.label}
                </div>
              </td>
            {:else if !inFamily}
              <td class="w-6"></td>
            {/if}
            <td class="py-2 pr-3 pl-3 align-top">
              <div class="flex items-center gap-2 min-w-0">
                <span class="inline-block w-2 h-2 rounded-full shrink-0 {dotClass(m.state)}"></span>
                <span class="font-mono text-xs text-txtmain break-all {row.unlisted ? 'opacity-70' : ''}" use:tip={m.id}>
                  {rowLabel(row, display)}
                </span>
                {#if multiQuant}
                  <button
                    class="p-0.5 shrink-0 text-txtsecondary hover:text-txtmain"
                    onclick={() => (expanded[row.key] = !expanded[row.key])}
                    aria-label="Show all quants"
                    use:tip={"Compare every quant of this model"}
                  >
                    <ChevronRight class="w-3.5 h-3.5 transition-transform {expanded[row.key] ? 'rotate-90' : ''}" />
                  </button>
                {/if}
                {#if isLive(m)}
                  <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary shrink-0">{m.state}</span>
                {/if}
              </div>
              {#if multiQuant || q.variants.length > 0}
                <div class="mt-1.5 flex flex-wrap items-center gap-1.5">
                  {#each row.quants as qe (qe.quant)}
                    {#if multiQuant}
                      <button class={pillCls(qe.quant === q.quant, true)} onclick={() => (quantPick[row.key] = qe.quant)} use:tip={`Quant ${qe.quant || 'unknown'}`}>
                        {qe.quant || "-"}
                        {#if qe.live}<span class="ml-1 inline-block w-1.5 h-1.5 rounded-full align-middle {dotClass(qe.base.state)}"></span>{/if}
                      </button>
                    {/if}
                  {/each}
                  {#if q.variants.length > 0}
                    {#if multiQuant}<span class="text-card-border">|</span>{/if}
                    <button class={pillCls(variantSelected(row, q.base.id))} onclick={() => pickVariant(row, q.base.id)}>Default</button>
                    {#each q.variants as v (v.model.id)}
                      <button class={pillCls(variantSelected(row, v.model.id))} onclick={() => pickVariant(row, v.model.id)} use:tip={v.model.id}>
                        {v.label}
                        {#if isLive(v.model)}<span class="ml-1 inline-block w-1.5 h-1.5 rounded-full align-middle {dotClass(v.model.state)}"></span>{/if}
                      </button>
                    {/each}
                  {/if}
                </div>
              {/if}
            </td>
            <td class="px-3 py-2 align-top font-mono text-xs text-txtsecondary">{q.quant || "-"}</td>
            <td class="px-3 py-2 align-top text-right font-mono text-xs tabular-nums text-txtmain">{fmtGB(m.sizeGB)}</td>
            <td class="px-3 py-2 align-top text-right font-mono text-xs tabular-nums text-txtmain">{fmtGB(m.estVramGB)}</td>
            <td class="px-3 py-2 align-top text-right font-mono text-xs tabular-nums {m.estRamGB ? 'text-warning' : 'text-txtsecondary'}">
              {fmtGB(m.estRamGB)}
            </td>
            <td class="px-3 py-2 align-top">{@render actions(m, q.base, false)}</td>
          </tr>

          {#if expanded[row.key] && multiQuant}
            {#each row.quants as qe (qe.quant)}
              <tr class="text-txtsecondary {rowTone(qe.live, qe.base.state, odd)}">
                <td class={rowAccent(qe.live, qe.base.state)}></td>
                {#if !inFamily}<td class="w-6"></td>{/if}
                <td class="py-1.5 pr-3 pl-8">
                  <div class="flex items-center gap-2">
                    <span class="inline-block w-1.5 h-1.5 rounded-full shrink-0 {dotClass(qe.base.state)}"></span>
                    <span class="font-mono text-micro break-all" use:tip={qe.base.id}>{qe.base.id}</span>
                  </div>
                </td>
                <td class="px-3 py-1.5 font-mono text-micro">{qe.quant || "-"}</td>
                <td class="px-3 py-1.5 text-right font-mono text-micro tabular-nums">{fmtGB(qe.base.sizeGB)}</td>
                <td class="px-3 py-1.5 text-right font-mono text-micro tabular-nums">{fmtGB(qe.base.estVramGB)}</td>
                <td class="px-3 py-1.5 text-right font-mono text-micro tabular-nums {qe.base.estRamGB ? 'text-warning' : ''}">
                  {fmtGB(qe.base.estRamGB)}
                </td>
                <td class="px-3 py-1.5">{@render actions(qe.base, qe.base, true)}</td>
              </tr>
            {/each}
          {/if}
        {/each}
      {:else}
        <tr>
          <td colspan="8" class="px-3 py-12 text-center">
            <div class="flex flex-col items-center gap-2 text-txtsecondary">
              <Search class="w-6 h-6 opacity-40" />
              <p class="text-sm text-txtmain">No models match</p>
              <p class="text-label">
                {#if search}Nothing matches <span class="font-mono text-txtmain">{search}</span>.{:else}No models in this category yet.{/if}
              </p>
              {#if search}
                <button class="btn btn--sm mt-1" onclick={closeSearch}>Clear filter</button>
              {/if}
            </div>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
