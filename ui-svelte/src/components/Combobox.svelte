<script lang="ts" module>
  export interface ComboOption {
    value: string;
    /** Second line under the value — what the id IS, not a restatement of it. */
    detail?: string;
    group?: string;
  }

  /** Per-instance id so the input can point aria-controls at its own list. */
  let seq = 0;
</script>

<script lang="ts">
  import { placePopup, popupStyle, type PopupPos } from "../lib/popupPlace";
  import { ChevronDown } from "lucide-svelte";

  interface Props {
    value: string;
    /** Suggestions only. The typed value is NEVER constrained to this list. */
    options: ComboOption[];
    /** Layout utilities for the wrapper (width, flex-1, …). */
    class?: string;
    /** REPLACES the input's built-in chrome, for a field that has to match
        neighbours styled some other way. Scoped styles outrank Tailwind
        utilities, so this is a swap, not an override. Include your own right
        padding — the caret button sits over that edge. */
    inputClass?: string;
    placeholder?: string;
    disabled?: boolean;
    ariaLabel?: string;
    mono?: boolean;
    onchange?: (value: string) => void;
    /** Fired when the list opens — for lists that fetch themselves lazily. */
    onopen?: () => void;
  }

  let {
    value = $bindable(),
    options,
    class: klass = "",
    inputClass = "",
    placeholder = "",
    disabled = false,
    ariaLabel,
    mono = false,
    onchange,
    onopen,
  }: Props = $props();

  const listId = `qm-combo-${++seq}`;

  let open = $state(false);
  // -1 means "nothing highlighted", which is the state the list OPENS in and
  // returns to on every keystroke: this field is free text, so Enter has to
  // commit what was typed, not whatever suggestion happened to sit under the
  // cursor. A suggestion wins only once the user walked onto it with an arrow.
  let active = $state(-1);
  let anchor = $state<HTMLDivElement | undefined>();
  let input = $state<HTMLInputElement | undefined>();
  let list = $state<HTMLUListElement | undefined>();
  let pos = $state<PopupPos>({ left: 0, top: 0, width: 0, maxHeight: 240, above: false, viewportH: 0 });

  // Substring match on the id, case-insensitively, the way the native datalist
  // this replaced behaved. An exact hit shows the whole list instead of one row
  // echoing the box, so a field that is already filled can still be browsed.
  const shown = $derived.by(() => {
    const q = value.trim().toLowerCase();
    if (!q || options.some((o) => o.value.toLowerCase() === q)) return options;
    return options.filter((o) => o.value.toLowerCase().includes(q) || (o.detail ?? "").toLowerCase().includes(q));
  });

  function place(): void {
    if (anchor) pos = placePopup(anchor);
  }

  function openList(): void {
    if (disabled || open) return;
    onopen?.();
    place();
    active = -1;
    open = true;
  }

  function closeList(refocus = false): void {
    if (!open) return;
    open = false;
    active = -1;
    if (refocus) input?.focus();
  }

  function pick(i: number): void {
    const o = shown[i];
    if (!o) return;
    value = o.value;
    onchange?.(o.value);
    closeList(true);
  }

  function step(delta: number): void {
    if (!shown.length) return;
    if (!open) openList();
    // Travel over n+1 slots, where the extra one is "back to what I typed" at
    // index -1, so arrowing past either end returns the box to free text before
    // wrapping round to the other end of the list.
    const slots = shown.length + 1;
    active = (((active + 1 + delta) % slots) + slots) % slots - 1;
  }

  function onKeydown(e: KeyboardEvent): void {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        step(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        step(-1);
        break;
      case "Enter":
        if (open && active >= 0) {
          e.preventDefault();
          pick(active);
        } else {
          closeList();
        }
        break;
      case "Escape":
        if (open) {
          // Stop here rather than letting it bubble: inside a <dialog>, Escape
          // closes the whole config modal, and losing the form to dismissing a
          // suggestion list is a lost edit.
          e.preventDefault();
          e.stopPropagation();
          closeList(true);
        }
        break;
      case "Tab":
        closeList();
        break;
    }
  }

  function onInput(): void {
    onchange?.(value);
    active = -1;
    if (!open) openList();
    else place();
  }

  // Keep the highlighted row in view during keyboard travel.
  $effect(() => {
    if (!open || !list || active < 0) return;
    list.querySelectorAll('[role="option"]')[active]?.scrollIntoView({ block: "nearest" });
  });

  // Same rule as Select: anything that MOVES the anchor closes the list, but a
  // scroll inside the list itself (or one in an unrelated scroller elsewhere on
  // the page) must not.
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

<div bind:this={anchor} class="qm-combo relative {klass}">
  <!-- Opens on click, not on focus: picking a suggestion puts focus BACK in the
       box, so an open-on-focus handler would reopen the list the pick just
       closed. Keyboard arrival opens it with ArrowDown, the usual verb. -->
  <input
    bind:this={input}
    bind:value
    type="text"
    role="combobox"
    aria-expanded={open}
    aria-controls={listId}
    aria-autocomplete="list"
    aria-activedescendant={open && active >= 0 ? `${listId}-${active}` : undefined}
    aria-label={ariaLabel}
    autocomplete="off"
    spellcheck="false"
    {placeholder}
    {disabled}
    oninput={onInput}
    onkeydown={onKeydown}
    onclick={() => openList()}
    class="{inputClass || 'qm-combo-input'} {mono ? 'font-mono' : ''}"
    class:is-open={open}
  />
  {#if options.length}
    <!-- Mousedown, not click: the window pointerdown handler above would have
         already shut the list by the time a click fired, so the button would
         reopen what it looks like it just closed. -->
    <button
      type="button"
      tabindex="-1"
      aria-hidden="true"
      {disabled}
      onmousedown={(e) => {
        e.preventDefault();
        if (open) closeList(true);
        else {
          input?.focus();
          openList();
        }
      }}
      class="qm-combo-caret"
    >
      <ChevronDown size={13} class="transition-transform {open ? 'rotate-180' : ''}" />
    </button>
  {/if}

  {#if open && shown.length}
    <ul
      bind:this={list}
      id={listId}
      role="listbox"
      aria-label={ariaLabel}
      class="qm-popup pretty-scroll {mono ? 'font-mono' : ''}"
      style={popupStyle(pos)}
    >
      {#each shown as o, i (o.value)}
        {#if o.group && o.group !== shown[i - 1]?.group}
          <li role="presentation" class="qm-popup-group">{o.group}</li>
        {/if}
        <!-- Arrow keys + Enter are handled on the input (the ARIA combobox
             pattern), so a per-option key handler would never fire: focus never
             leaves the text box. -->
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <li
          id="{listId}-{i}"
          role="option"
          aria-selected={o.value === value}
          class="qm-popup-option"
          class:is-active={i === active}
          class:is-selected={o.value === value}
          onpointerenter={() => (active = i)}
          onmousedown={(e) => e.preventDefault()}
          onclick={() => pick(i)}
        >
          <span class="flex-1 min-w-0">
            <span class="block truncate">{o.value}</span>
            {#if o.detail}
              <span class="block truncate text-micro text-txtsecondary">{o.detail}</span>
            {/if}
          </span>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  /* Matches `.cfg-input` (ModelConfigModal) and `.qm-select-trigger`, so a
     Combobox sits flush in a row beside either of them. */
  .qm-combo-input {
    width: 100%;
    padding: 4px 22px 4px 8px;
    border-radius: 4px;
    background: var(--color-background);
    border: 1px solid var(--color-card-border);
    color: var(--color-txtmain);
    font-size: 0.85rem;
  }
  .qm-combo-input:hover:not(:disabled) {
    border-color: var(--color-primary);
  }
  .qm-combo-input:focus,
  .qm-combo-input.is-open {
    outline: none;
    border-color: var(--color-primary);
  }
  .qm-combo-input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .qm-combo-caret {
    position: absolute;
    top: 0;
    right: 0;
    bottom: 0;
    display: flex;
    align-items: center;
    padding: 0 4px;
    color: var(--color-txtsecondary);
    cursor: pointer;
  }
  .qm-combo-caret:hover:not(:disabled) {
    color: var(--color-txtmain);
  }
  .qm-combo-caret:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
