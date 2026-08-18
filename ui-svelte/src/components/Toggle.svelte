<script lang="ts">
  /** On/off switch for boolean settings.

      A real <input type="checkbox"> drives it rather than a role="switch"
      button, so the surrounding <label> keeps working: most call sites wrap the
      control in a label whose text must stay clickable, and native
      focus/keyboard behaviour comes for free. The input is transparent and
      stretched over the track instead of `sr-only` so the switch is also
      clickable on its own, at the sites that have no label.

      Multi-select lists (pick N of M) stay as tick boxes — a switch reads as
      "this feature is on", not "this item is selected". */
  interface Props {
    /** `undefined` reads as off — call sites bind optional config fields. */
    checked: boolean | undefined;
    disabled?: boolean;
    /** "sm" for dense inline rows (config grids), "md" for settings rows. */
    size?: "sm" | "md";
    id?: string;
    ariaLabel?: string;
    /** Layout utilities only (margins, self-alignment) — chrome is baked in. */
    class?: string;
    onchange?: (checked: boolean) => void;
  }

  let {
    checked = $bindable(false),
    disabled = false,
    size = "md",
    id,
    ariaLabel,
    class: klass = "",
    onchange,
  }: Props = $props();

  const track = $derived(size === "sm" ? "h-4 w-7" : "h-5 w-9");
  const knob = $derived(
    size === "sm"
      ? "h-3 w-3 left-0.5 peer-checked:translate-x-3"
      : "h-4 w-4 left-0.5 peer-checked:translate-x-4",
  );
</script>

<span class="relative inline-flex shrink-0 items-center align-middle {klass}">
  <input
    {id}
    type="checkbox"
    class="switch-input peer"
    {disabled}
    aria-label={ariaLabel}
    bind:checked
    onchange={(e) => onchange?.(e.currentTarget.checked)}
  />
  <span
    class="{track} rounded-full bg-control-border transition-colors peer-checked:bg-primary peer-disabled:opacity-45 peer-focus-visible:ring-2 peer-focus-visible:ring-primary peer-focus-visible:ring-offset-1 peer-focus-visible:ring-offset-surface"
  ></span>
  <span
    class="{knob} pointer-events-none absolute rounded-full bg-background shadow transition-transform peer-disabled:opacity-45"
  ></span>
</span>

<style>
  /* Beats the global input[type="checkbox"] chrome (box, border, tick glyph):
     this input is only a hit area — the track and knob spans are the control. */
  .switch-input,
  .switch-input:checked {
    position: absolute;
    inset: 0;
    z-index: 10;
    margin: 0;
    width: 100%;
    height: 100%;
    border: 0;
    border-radius: 999px;
    background: transparent;
    background-image: none;
    cursor: pointer;
  }
  .switch-input:disabled {
    cursor: not-allowed;
  }
</style>
