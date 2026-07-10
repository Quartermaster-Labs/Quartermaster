<script lang="ts">
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
</script>

{#if showSettings}
  <div class="absolute bottom-full right-0 mb-2 w-80 z-20 flex flex-col gap-3 p-4 rounded-lg border border-card-border bg-surface shadow-lg text-[0.8125rem]">
    <div class="flex items-center justify-between">
      <span class="font-medium text-txtmain">{settingsTitle}</span>
      <button
        class="inline-flex items-center justify-center p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
        onclick={() => (showSettings = false)}
        title="Close"
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

  <div class="flex items-center justify-between">
    <div class="flex items-center gap-1">
      {@render leftButtons?.()}
    </div>

    <div class="flex-1 min-w-0 px-2 flex flex-col items-center gap-1">
      <ModelSelector bind:value={modelValue} placeholder={modelPlaceholder} disabled={busy} {category} ghost dropUp />
      {@render ctxBar?.()}
    </div>

    <div class="flex items-center gap-1">
      {@render extraRightButtons?.()}
      <button
        class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {showSettings ? 'bg-secondary text-txtmain shadow-inner' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
        onclick={() => (showSettings = !showSettings)}
        title={settingsTitle}
      >
        <SlidersHorizontal class="w-[1.125rem] h-[1.125rem]" />
      </button>
      {#if busy}
        <button
          class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
          onclick={onStop}
          title={stopTitle}
        >
          <Square class="w-[1.125rem] h-[1.125rem]" fill="currentColor" />
        </button>
      {/if}
    </div>
  </div>
</div>
