<script lang="ts" module>
  export interface SelectOption {
    value: string;
    label: string;
    /** Second line under the label — for the "what does this option do" case. */
    detail?: string;
    /** Optgroup heading. Consecutive options sharing a group sit under one
        heading; the array stays flat so option indices survive keyboard travel. */
    group?: string;
    disabled?: boolean;
  }

  /** Per-instance id so the trigger can point aria-controls at its own list. */
  let seq = 0;
</script>

<script lang="ts">
  import { placePopup, popupStyle, type PopupPos } from "../lib/popupPlace";
  import { ChevronDown, Check } from "lucide-svelte";
  import { tip } from "../lib/tooltip";

  interface Props {
    /** `undefined` is treated as the empty value — call sites bind optional fields. */
    value: string | undefined;
    options: SelectOption[];
    /** Layout utilities only (width, ml-auto, …) — the chrome is baked in. */
    class?: string;
    /** Shown when `value` matches no option. */
    placeholder?: string;
    disabled?: boolean;
    ariaLabel?: string;
    /** Render the closed value + the list in the mono face (quants, paths, ids). */
    mono?: boolean;
    /** Hover/focus hint on the trigger (the `tip` action, not a native title). */
    tooltip?: string;
    onchange?: (value: string) => void;
    /** Fired when the list opens — for lists that fetch themselves lazily. */
    onopen?: () => void;
  }

  let {
    value = $bindable(),
    options,
    class: klass = "",
    placeholder = "",
    disabled = false,
    ariaLabel,
    mono = false,
    tooltip = "",
    onchange,
    onopen,
  }: Props = $props();

  const listId = `qm-select-${++seq}`;

  let open = $state(false);
  let active = $state(-1);
  let trigger = $state<HTMLButtonElement | undefined>();
  let list = $state<HTMLUListElement | undefined>();

  // Where the list sits. placePopup() owns the measuring, the zoom correction
  // and the flip rule - see lib/popupPlace.ts.
  let pos = $state<PopupPos>({ left: 0, top: 0, width: 0, maxHeight: 240, above: false, viewportH: 0 });

  const selected = $derived(options.findIndex((o) => o.value === (value ?? "")));
  const label = $derived(selected >= 0 ? options[selected].label : placeholder);

  function place(): void {
    if (trigger) pos = placePopup(trigger);
  }

  function openList(): void {
    if (disabled) return;
    onopen?.();
    place();
    active = selected >= 0 ? selected : options.findIndex((o) => !o.disabled);
    open = true;
  }

  function closeList(refocus = true): void {
    if (!open) return;
    open = false;
    if (refocus) trigger?.focus();
  }

  function pick(i: number): void {
    const o = options[i];
    if (!o || o.disabled) return;
    value = o.value;
    onchange?.(o.value);
    closeList();
  }

  function step(delta: number): void {
    if (!options.length) return;
    let i = active;
    for (let n = 0; n < options.length; n++) {
      i = (i + delta + options.length) % options.length;
      if (!options[i].disabled) break;
    }
    active = i;
  }

  function onKeydown(e: KeyboardEvent): void {
    if (!open) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        openList();
      }
      return;
    }
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        closeList();
        break;
      case "ArrowDown":
        e.preventDefault();
        step(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        step(-1);
        break;
      case "Home":
        e.preventDefault();
        active = options.findIndex((o) => !o.disabled);
        break;
      case "End":
        e.preventDefault();
        active = options.length - 1;
        break;
      case "Enter":
      case " ":
        e.preventDefault();
        pick(active);
        break;
      case "Tab":
        closeList(false);
        break;
    }
  }

  // Keep the highlighted row in view during keyboard travel.
  $effect(() => {
    if (!open || !list || active < 0) return;
    // [role=option] rather than li: group headings are list items too, and would
    // otherwise shift every index past the first group.
    list.querySelectorAll('[role="option"]')[active]?.scrollIntoView({ block: "nearest" });
  });

  // Anything that moves the trigger under the popup closes it: re-measuring on
  // every scroll frame would be smoother but this list is never long-lived.
  // Only a scroller that CONTAINS the trigger can move it, though — the capture
  // listener sees every scroll on the page, and the log panel's auto-follow
  // scrolls its <pre> on every streamed chunk, which shut this list again the
  // instant it opened.
  $effect(() => {
    if (!open) return;
    const onScroll = (e: Event): void => {
      const t = e.target;
      if (list && t instanceof Node && list.contains(t)) return;
      if (t instanceof Node && trigger && !t.contains(trigger)) return;
      closeList(false);
    };
    const onResize = (): void => closeList(false);
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
    if (trigger?.contains(t) || list?.contains(t)) return;
    closeList(false);
  }
</script>

<svelte:window onpointerdown={onWindowPointerDown} />

<div class="qm-select relative {klass}">
  <button
    bind:this={trigger}
    type="button"
    role="combobox"
    aria-expanded={open}
    aria-controls={listId}
    aria-haspopup="listbox"
    aria-label={ariaLabel}
    {disabled}
    onclick={() => (open ? closeList() : openList())}
    onkeydown={onKeydown}
    use:tip={tooltip}
    class="qm-select-trigger {mono ? 'font-mono' : ''}"
    class:is-open={open}
  >
    <span class="truncate {selected < 0 ? 'text-txtsecondary' : ''}">{label || " "}</span>
    <ChevronDown size={13} class="shrink-0 text-txtsecondary transition-transform {open ? 'rotate-180' : ''}" />
  </button>

  {#if open}
    <ul
      bind:this={list}
      id={listId}
      role="listbox"
      tabindex="-1"
      aria-label={ariaLabel}
      class="qm-popup pretty-scroll {mono ? 'font-mono' : ''}"
      style={popupStyle(pos)}
      onkeydown={onKeydown}
    >
      {#each options as o, i (o.value)}
        {#if o.group && o.group !== options[i - 1]?.group}
          <li role="presentation" class="qm-popup-group">{o.group}</li>
        {/if}
        <!-- Keyboard selection is handled on the listbox (arrows + Enter), which
             is the ARIA combobox pattern — a per-option key handler would never
             fire, since focus stays on the trigger. -->
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <li
          role="option"
          aria-selected={i === selected}
          aria-disabled={o.disabled}
          class="qm-popup-option"
          class:is-active={i === active}
          class:is-selected={i === selected}
          class:is-disabled={o.disabled}
          onpointerenter={() => !o.disabled && (active = i)}
          onclick={() => pick(i)}
        >
          <span class="flex-1 min-w-0">
            <span class="block truncate">{o.label}</span>
            {#if o.detail}
              <span class="block truncate text-micro text-txtsecondary">{o.detail}</span>
            {/if}
          </span>
          {#if i === selected}<Check size={12} class="shrink-0 text-primary" />{/if}
        </li>
      {/each}
      {#if options.length === 0}
        <li class="qm-popup-empty">No options</li>
      {/if}
    </ul>
  {/if}
</div>

<style>
  /* Matches `.cfg-input` (ModelConfigModal) so a Select sits flush beside the
     text/number inputs it shares a row with. */
  .qm-select-trigger {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    width: 100%;
    padding: 4px 6px 4px 8px;
    border-radius: 4px;
    background: var(--color-background);
    border: 1px solid var(--color-card-border);
    color: var(--color-txtmain);
    font-size: 0.85rem;
    text-align: left;
    cursor: pointer;
  }
  .qm-select-trigger > :global(span) {
    flex: 1;
    min-width: 0;
  }
  .qm-select-trigger:hover:not(:disabled) {
    border-color: var(--color-primary);
  }
  .qm-select-trigger.is-open {
    border-color: var(--color-primary);
  }
  .qm-select-trigger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
