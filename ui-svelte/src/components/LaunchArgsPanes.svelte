<script lang="ts">
  // The two launch-argument panes (ui-svelte/launch-args.md): the user's text
  // stored verbatim, and the composed command it produces. The text is never
  // parsed back into the form fields; the server composes the layers and says
  // which token came from where.
  import Toggle from "./Toggle.svelte";
  import { tip } from "../lib/tooltip";
  import { HelpCircle } from "lucide-svelte";
  import type { PreviewLayers } from "../stores/api";

  interface Props {
    /** The user's launch-argument text, stored verbatim. */
    customArgs: string;
    /** true = the text stays saved but is not applied. */
    customArgsOff: boolean;
    /** Composed command from /preview, with per-token provenance. */
    layers: PreviewLayers | null;
    /** Shown before the first preview arrives (the saved command). */
    fallback?: string;
    /** Variant pane: the model's text, shown as the placeholder. An empty box
     *  inherits it; the literal "none" runs the variant with none. */
    inherited?: string;
    /** Variant pane: inheritance note instead of the enable toggle. */
    variant?: boolean;
  }
  let {
    customArgs = $bindable(),
    customArgsOff = $bindable(),
    layers,
    fallback = "",
    inherited = "",
    variant = false,
  }: Props = $props();

  const toggleHint =
    "Flags written here are appended to the generated command. A flag that sets something the generator also sets replaces the generated one. Off keeps the text saved but does not apply it.";
  const placeholder = $derived(
    variant
      ? inherited
        ? `inherited: ${inherited}`
        : "empty = inherit this model's launch arguments"
      : "e.g. --load-mode none -cram 2048",
  );
  const suppressed = $derived((layers?.tokens ?? []).filter((t) => t.suppressed).length);
</script>

<details class="group">
  <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
    Custom launch arguments {customArgs && !variant ? (customArgsOff ? "(saved, off)" : "(custom)") : ""}
  </summary>
  {#if variant}
    <p class="text-xs text-txtsecondary mt-2">
      Empty = inherit the model's launch arguments; <code>none</code> = run this variant
      with none.
    </p>
  {:else}
    <label class="flex items-center gap-2 text-sm mt-2">
      <Toggle size="sm" checked={!customArgsOff} onchange={(on) => (customArgsOff = !on)} />
      <span class="text-txtsecondary flex items-center gap-1">
        Enable custom launch arguments
        <span class="inline-flex shrink-0 text-txtsecondary cursor-help hover:text-txtmain" use:tip={toggleHint} aria-label={toggleHint}>
          <HelpCircle size={12} />
        </span>
      </span>
    </label>
  {/if}
  <textarea
    bind:value={customArgs}
    spellcheck="false"
    rows="6"
    disabled={customArgsOff && !variant}
    placeholder={placeholder}
    class="mt-2 w-full bg-background rounded border border-card-border p-3 text-xs font-mono whitespace-pre-wrap break-all resize-y text-txtmain {customArgsOff && !variant
      ? 'opacity-50'
      : ''}"
  ></textarea>
</details>

<details class="group">
  <summary class="cursor-pointer font-semibold text-sm uppercase tracking-wider text-txtsecondary hover:text-txtmain">
    Final launch arguments
  </summary>
  {#if layers?.tokens?.length}
    <div class="mt-2 max-h-56 overflow-auto rounded border border-card-border bg-background p-3 text-xs font-mono leading-5 whitespace-pre-wrap break-all">
      {#each layers.tokens as t, i (i)}<span
          class={t.suppressed
            ? "text-txtsecondary/50 line-through"
            : t.source === "custom"
              ? "text-success font-semibold"
              : "text-txtmain"}
        >{t.text}</span>{#if i < layers.tokens.length - 1}<span> </span>{/if}{/each}
    </div>
    <p class="text-xs text-txtsecondary mt-1">
      {#if layers.ownedKnobs?.length}
        {layers.ownedKnobs.length} setting{layers.ownedKnobs.length === 1 ? "" : "s"} replaced by custom flags{#if suppressed}, {suppressed}
          generated token{suppressed === 1 ? "" : "s"} dropped{/if}.
      {:else}
        No custom flags; the generated command runs as-is.
      {/if}
    </p>
  {:else}
    <p class="text-xs font-mono break-all mt-2 text-txtsecondary">{fallback || "rendering…"}</p>
  {/if}
</details>
