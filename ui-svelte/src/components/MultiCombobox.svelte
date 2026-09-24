<script lang="ts" module>
  import type { ComboOption } from "./Combobox.svelte";
  export type { ComboOption };

  let seq = 0;
</script>

<script lang="ts">
  import { placePopup, popupStyle, type PopupPos } from "../lib/popupPlace";
  import { Check, ChevronDown, X } from "lucide-svelte";

  interface Props {
    /** Current selection. Replaced, never mutated: `onchange` hands back a new Set. */
    selected: Set<string>;
    options: ComboOption[];
    onchange: (next: Set<string>) => void;
    class?: string;
    placeholder?: string;
    ariaLabel?: string;
    disabled?: boolean;
  }

  let { selected, options, onchange, class: klass = "", placeholder = "", ariaLabel, disabled = false }: Props = $props();

  const listId = `qm-mcombo-${++seq}`;

  // Past this many chips the field collapses to a "+N more" toggle: a whole
  // category ticked at once is 100+ ids, and a wall of chips shoves the rest
  // of the page off screen.
  const CHIP_CAP = 12;

  let query = $state("");
  let expanded = $state(false);
  let open = $state(false);
  let active = $state(-1);
  let anchor = $state<HTMLDivElement | undefined>();
  let input = $state<HTMLInputElement | undefined>();
  let list = $state<HTMLUListElement | undefined>();
  let pos = $state<PopupPos>({ left: 0, top: 0, width: 0, maxHeight: 240, above: false, viewportH: 0 });

  const shown = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((o) => o.value.toLowerCase().includes(q) || (o.detail ?? "").toLowerCase().includes(q));
  });

  // Chips keep the caller's order for catalog ids, then anything selected that
  // the catalog no longer has - a key scoped to a since-deleted model must
  // still show (and be removable), or it is a hidden grant.
  const chips = $derived.by(() => {
    const known = new Set(options.map((o) => o.value));
    return [...options.filter((o) => selected.has(o.value)).map((o) => o.value), ...[...selected].filter((id) => !known.has(id)).sort()];
  });

  const visibleChips = $derived(expanded ? chips : chips.slice(0, CHIP_CAP));

  function place(): void {
    if (anchor) pos = placePopup(anchor);
  }

  function openList(): void {
    if (disabled || open) return;
    place();
    active = -1;
    open = true;
  }

  function closeList(): void {
    open = false;
    active = -1;
  }

  function toggle(id: string): void {
    const next = new Set(selected);
    next.has(id) ? next.delete(id) : next.add(id);
    onchange(next);
  }

  // Acts on the rows the filter is showing, so "type qwen, click Chat" takes
  // every matching chat model and leaves the rest of the category alone.
  function toggleGroup(group: string): void {
    const ids = shown.filter((o) => o.group === group).map((o) => o.value);
    const all = ids.every((id) => selected.has(id));
    const next = new Set(selected);
    for (const id of ids) all ? next.delete(id) : next.add(id);
    onchange(next);
  }

  function groupState(group: string): string {
    const ids = shown.filter((o) => o.group === group);
    return `${ids.filter((o) => selected.has(o.value)).length}/${ids.length}`;
  }

  function onKeydown(e: KeyboardEvent): void {
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp": {
        e.preventDefault();
        if (!open) openList();
        if (!shown.length) return;
        const d = e.key === "ArrowDown" ? 1 : -1;
        active = (((active + d) % shown.length) + shown.length) % shown.length;
        break;
      }
      case "Enter":
        e.preventDefault();
        // Enter toggles and stays open: this is a pick-many list, closing on
        // every pick is exactly the tedium it replaces. With nothing walked
        // onto, a query matching one row takes that row.
        if (open && active >= 0) toggle(shown[active].value);
        else if (shown.length === 1) toggle(shown[0].value);
        break;
      case "Backspace":
        if (!query && chips.length) toggle(chips[chips.length - 1]);
        break;
      case "Escape":
        if (open) {
          e.preventDefault();
          e.stopPropagation();
          closeList();
        }
        break;
      case "Tab":
        closeList();
        break;
    }
  }

  function onInput(): void {
    active = -1;
    if (!open) openList();
  }

  // Chips wrap onto new lines as they are added, moving the field's bottom
  // edge; re-anchor so the list does not overlap it.
  $effect(() => {
    void chips.length;
    if (open) requestAnimationFrame(place);
  });

  $effect(() => {
    if (!open || !list || active < 0) return;
    list.querySelectorAll('[role="option"]')[active]?.scrollIntoView({ block: "nearest" });
  });

  // Same close-on-move rule as Select/Combobox.
  $effect(() => {
    if (!open) return;
    const onScroll = (e: Event): void => {
      const t = e.target;
      if (list && t instanceof Node && list.contains(t)) return;
      if (t instanceof Node && anchor && !t.contains(anchor)) return;
      closeList();
    };
    const onResize = (): void => closeList();
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", onResize);
    };
  });

  function onWindowPointerDown(e: PointerEvent): void {
    if (!open) return;
    const t = e.target as Node;
    if (anchor?.contains(t) || list?.contains(t)) return;
    closeList();
  }
</script>

<svelte:window onpointerdown={onWindowPointerDown} />

<!-- The whole field is the click target: clicking between chips should land in
     the text box, the way a tag input behaves. -->
<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div
  bind:this={anchor}
  class="qm-mcombo {klass}"
  class:is-open={open}
  class:is-disabled={disabled}
  onclick={() => {
    input?.focus();
    openList();
  }}
>
  {#each visibleChips as id (id)}
    <span class="qm-mcombo-chip">
      <span class="truncate">{id}</span>
      <button
        type="button"
        tabindex="-1"
        aria-label="Remove {id}"
        onclick={(e) => {
          e.stopPropagation();
          toggle(id);
        }}
      ><X size={11} /></button>
    </span>
  {/each}
  {#if chips.length > CHIP_CAP}
    <button
      type="button"
      tabindex="-1"
      class="qm-mcombo-more"
      onclick={(e) => {
        e.stopPropagation();
        expanded = !expanded;
      }}
    >{expanded ? "show less" : `+${chips.length - CHIP_CAP} more`}</button>
  {/if}
  <input
    bind:this={input}
    bind:value={query}
    type="text"
    role="combobox"
    aria-expanded={open}
    aria-controls={listId}
    aria-autocomplete="list"
    aria-activedescendant={open && active >= 0 ? `${listId}-${active}` : undefined}
    aria-label={ariaLabel}
    autocomplete="off"
    spellcheck="false"
    placeholder={chips.length ? "" : placeholder}
    {disabled}
    oninput={onInput}
    onkeydown={onKeydown}
    class="qm-mcombo-input"
  />
  {#if chips.length}
    <button
      type="button"
      tabindex="-1"
      class="qm-mcombo-icon"
      aria-label="Clear selection"
      onclick={(e) => {
        e.stopPropagation();
        onchange(new Set());
      }}
    ><X size={13} /></button>
  {/if}
  <button
    type="button"
    tabindex="-1"
    aria-hidden="true"
    class="qm-mcombo-icon"
    onmousedown={(e) => {
      e.preventDefault();
      e.stopPropagation();
      if (open) closeList();
      else {
        input?.focus();
        openList();
      }
    }}
    onclick={(e) => e.stopPropagation()}
  ><ChevronDown size={13} class="transition-transform {open ? 'rotate-180' : ''}" /></button>

  {#if open}
    <ul
      bind:this={list}
      id={listId}
      role="listbox"
      aria-multiselectable="true"
      aria-label={ariaLabel}
      class="qm-popup pretty-scroll font-mono"
      style={popupStyle(pos)}
    >
      {#each shown as o, i (o.value)}
        {#if o.group && o.group !== shown[i - 1]?.group}
          {@const g = o.group}
          <li role="presentation" class="qm-popup-group">
            <button
              type="button"
              class="qm-mcombo-group"
              onmousedown={(e) => e.preventDefault()}
              onclick={() => toggleGroup(g)}
              title={query ? "Select or clear every match in this category" : "Select or clear the whole category"}
            >{g} <span class="tabular-nums">({groupState(g)})</span></button>
          </li>
        {/if}
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <li
          id="{listId}-{i}"
          role="option"
          aria-selected={selected.has(o.value)}
          class="qm-popup-option"
          class:is-active={i === active}
          class:is-selected={selected.has(o.value)}
          onpointerenter={() => (active = i)}
          onmousedown={(e) => e.preventDefault()}
          onclick={() => toggle(o.value)}
        >
          <span class="qm-mcombo-tick" class:on={selected.has(o.value)}>
            {#if selected.has(o.value)}<Check size={11} />{/if}
          </span>
          <span class="block min-w-0 flex-1 truncate">{o.value}</span>
        </li>
      {:else}
        <li class="qm-popup-empty">No model matches "{query}".</li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .qm-mcombo {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    min-height: 30px;
    padding: 3px 4px 3px 4px;
    border-radius: 4px;
    background: var(--color-background);
    border: 1px solid var(--color-card-border);
    cursor: text;
  }
  .qm-mcombo:hover:not(.is-disabled),
  .qm-mcombo.is-open,
  .qm-mcombo:focus-within {
    border-color: var(--color-primary);
  }
  .qm-mcombo.is-disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .qm-mcombo-input {
    flex: 1 1 8rem;
    min-width: 6rem;
    padding: 1px 4px;
    background: transparent;
    border: none;
    outline: none;
    color: var(--color-txtmain);
    font-family: var(--font-mono);
    font-size: 0.85rem;
  }

  .qm-mcombo-chip {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    max-width: 100%;
    padding: 1px 2px 1px 6px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--color-primary) 18%, transparent);
    color: var(--color-txtmain);
    font-family: var(--font-mono);
    font-size: 0.72rem;
  }
  .qm-mcombo-chip button {
    display: flex;
    padding: 1px;
    border-radius: 3px;
    color: var(--color-txtsecondary);
    cursor: pointer;
  }
  .qm-mcombo-chip button:hover {
    color: var(--color-error);
  }

  .qm-mcombo-more {
    padding: 1px 6px;
    border-radius: 4px;
    font-size: 0.72rem;
    color: var(--color-primary);
    cursor: pointer;
  }
  .qm-mcombo-more:hover {
    text-decoration: underline;
  }

  .qm-mcombo-icon {
    display: flex;
    align-items: center;
    align-self: stretch;
    padding: 0 2px;
    color: var(--color-txtsecondary);
    cursor: pointer;
  }
  .qm-mcombo-icon:hover {
    color: var(--color-txtmain);
  }

  .qm-mcombo-group {
    font: inherit;
    letter-spacing: inherit;
    text-transform: inherit;
    color: inherit;
    cursor: pointer;
  }
  .qm-mcombo-group:hover {
    color: var(--color-primary);
  }

  .qm-mcombo-tick {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 14px;
    height: 14px;
    border-radius: 3px;
    border: 1px solid var(--color-card-border);
  }
  .qm-mcombo-tick.on {
    background: var(--color-primary);
    border-color: var(--color-primary);
    color: var(--color-btn-primary-text);
  }
</style>
