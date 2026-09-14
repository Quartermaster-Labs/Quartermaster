<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import type { Snippet } from "svelte";
  import { SlidersHorizontal, Square, X } from "lucide-svelte";
  import ModelSelector from "./ModelSelector.svelte";
  import type { ModelCategory } from "../../lib/modelUtils";

  // Shared chat/image composer chrome: main textarea (auto-grow), left/right
  // icon rows, clickable model name + optional context bar, and the settings
  // popover shell. Each tab supplies its own extras via snippets — this only
  // owns the parts that must stay visually identical (padding, icon sizing,
  // popover position) so a tweak to one lands on both.
  let {
    value = $bindable(""),
    placeholder = "",
    textareaDisabled = false,
    textareaEl = $bindable(undefined),
    onFocus,
    onBlur,
    busy = false,
    onStop,
    stopTitle = "Stop",
    modelValue = $bindable(""),
    onModelChange,
    modelPlaceholder = "Select a model...",
    category,
    onKeydown,
    onPaste,
    showSettings = $bindable(false),
    settingsTitle = "Settings",
    topExtra,
    leftButtons,
    extraRightButtons,
    ctxBar,
    settingsPanel,
  }: {
    value?: string;
    placeholder?: string;
    textareaDisabled?: boolean;
    textareaEl?: HTMLTextAreaElement;
    onFocus?: () => void;
    onBlur?: () => void;
    busy?: boolean;
    onStop?: () => void;
    stopTitle?: string;
    modelValue?: string;
    // Fired only on an explicit pick in the dropdown (not on programmatic changes
    // to modelValue) — chat uses it to bind the model to the open conversation.
    onModelChange?: (value: string) => void;
    modelPlaceholder?: string;
    category: ModelCategory;
    onKeydown?: (e: KeyboardEvent) => void;
    onPaste?: (e: ClipboardEvent) => void;
    showSettings?: boolean;
    settingsTitle?: string;
    topExtra?: Snippet;
    leftButtons?: Snippet;
    extraRightButtons?: Snippet;
    ctxBar?: Snippet;
    settingsPanel?: Snippet;
  } = $props();

  // Close the settings popover on any outside click, but ignore the gear toggle
  // (its own onclick handles that) so a click on it doesn't close-then-reopen.
  function closeOnOutside(node: HTMLElement) {
    function onClick(e: MouseEvent) {
      const t = e.target as Node;
      if (!node.contains(t) && !settingsToggleEl?.contains(t)) showSettings = false;
    }
    document.addEventListener("click", onClick, true);
    return { destroy: () => document.removeEventListener("click", onClick, true) };
  }
  let settingsToggleEl = $state<HTMLButtonElement>();

  // Live height of the composer's bottom control row, measured rather than
  // guessed: the row is one line in the image/video tabs and two in chat (the
  // model selector stacks a context bar under itself), so any hardcoded offset
  // would be wrong in one of them. bind:clientHeight installs a ResizeObserver,
  // so this follows the row instead of being sampled once on mount.
  let controlsH = $state(0);
</script>

{#if showSettings}
  <!-- Anchored to the composer's BOTTOM edge, floating OVER the textarea, not
       stacked above the whole composer.
       `bottom-full` anchored it to the composer's TOP, which is not a fixed
       point: the textarea grows with the message up to max-h-[30rem], dragging
       that edge upward until the panel opened entirely off the top of the
       window and became unreachable. Capping the height could never fix that,
       because the panel's bottom edge was already past the viewport. The
       composer's bottom edge, by contrast, is pinned to the bottom of the pane
       (the input area is `shrink-0`), so the panel now opens in the same place
       whatever the composer is doing.
       The offset clears the control row so the gear that opened it, and the
       send button beside it, stay clickable underneath.
       max-h/overflow: the image panel is tall enough to run past the top of a
       short window. It has to scroll itself, because the shell clips rather than
       letting the document grow a pair of scrollbars. 100vh needs the `zoom`
       division (see index.css). -->
  <div
    use:closeOnOutside
    class="absolute right-0 w-80 z-30 flex flex-col gap-3 p-4 rounded-lg border border-card-border bg-surface shadow-lg text-[0.8125rem] overflow-y-auto overscroll-contain pretty-scroll"
    style="bottom: calc({controlsH}px + 1.25rem); max-height: calc(100vh / var(--qm-scale) - {controlsH}px - 4rem)"
  >
    <div class="flex items-center justify-between">
      <span class="font-medium text-txtmain">{settingsTitle}</span>
      <button
        class="inline-flex items-center justify-center p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
        onclick={() => (showSettings = false)}
        use:tip={"Close"}
      >
        <X class="w-4 h-4" />
      </button>
    </div>
    {@render settingsPanel?.()}
  </div>
{/if}

<div class="composer-shell">
  {@render topExtra?.()}
  <textarea
    bind:this={textareaEl}
    class="composer-textarea pretty-scroll min-h-[3rem] max-h-[30rem]"
    rows="2"
    {placeholder}
    disabled={textareaDisabled}
    bind:value
    onfocus={onFocus}
    onblur={onBlur}
    onkeydown={onKeydown}
    onpaste={onPaste}
  ></textarea>

  <div class="flex items-center justify-between" bind:clientHeight={controlsH}>
    <div class="flex-1 min-w-0 flex items-center gap-1">
      {@render leftButtons?.()}
    </div>

    <div class="min-w-0 px-2 flex flex-col items-center gap-1">
      <ModelSelector bind:value={modelValue} placeholder={modelPlaceholder} disabled={busy} {category} onChange={onModelChange} ghost dropUp />
      {@render ctxBar?.()}
    </div>

    <div class="flex-1 min-w-0 flex items-center justify-end gap-1">
      {@render extraRightButtons?.()}
      <button
        bind:this={settingsToggleEl}
        class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {showSettings ? 'bg-secondary text-txtmain shadow-inner' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
        onclick={() => (showSettings = !showSettings)}
        use:tip={settingsTitle}
      >
        <SlidersHorizontal class="w-[1.125rem] h-[1.125rem]" />
      </button>
      {#if busy}
        <button
          class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
          onclick={onStop}
          use:tip={stopTitle}
        >
          <Square class="w-[1.125rem] h-[1.125rem]" fill="currentColor" />
        </button>
      {/if}
    </div>
  </div>
</div>
