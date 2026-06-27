<script lang="ts">
  import { persistentStore } from "../stores/persistent";

  interface Props {
    id: string;
    title: string;
    logData: string;
  }

  let { id, title, logData }: Props = $props();

  let filterRegex = $state("");

  // Create persistent stores for this panel (id is intentionally captured at init time)
  // svelte-ignore state_referenced_locally
  const fontSizeStore = persistentStore<"xxs" | "xs" | "small" | "normal">(`logPanel-${id}-fontSize`, "normal");
  // svelte-ignore state_referenced_locally
  const wrapTextStore = persistentStore<boolean>(`logPanel-${id}-wrapText`, false);
  // svelte-ignore state_referenced_locally
  const showFilterStore = persistentStore<boolean>(`logPanel-${id}-showFilter`, false);

  let textWrapClass = $derived($wrapTextStore ? "whitespace-pre-wrap" : "whitespace-pre");

  function toggleFontSize(): void {
    fontSizeStore.update((prev) => {
      switch (prev) {
        case "xxs": return "xs";
        case "xs": return "small";
        case "small": return "normal";
        case "normal": return "xxs";
      }
    });
  }

  function toggleWrapText(): void {
    wrapTextStore.update((prev) => !prev);
  }

  function toggleFilter(): void {
    if ($showFilterStore) {
      showFilterStore.set(false);
      filterRegex = "";
    } else {
      showFilterStore.set(true);
    }
  }

  let fontSizeClass = $derived.by(() => {
    switch ($fontSizeStore) {
      case "xxs": return "text-[0.5rem]";
      case "xs": return "text-[0.75rem]";
      case "small": return "text-[0.875rem]";
      case "normal": return "text-base";
    }
  });

  let filteredLogs = $derived.by(() => {
    if (!filterRegex) return logData;
    try {
      const regex = new RegExp(filterRegex, "i");
      return logData.split("\n").filter((line) => regex.test(line)).join("\n");
    } catch {
      return logData;
    }
  });

  let preElement: HTMLPreElement;
  let userScrolledUp = $state(false);

  function handleScroll() {
    if (!preElement) return;
    const { scrollTop, scrollHeight, clientHeight } = preElement;
    userScrolledUp = scrollHeight - scrollTop - clientHeight > 40;
  }

  // Auto scroll to bottom when logs change, unless user has scrolled up
  $effect(() => {
    if (preElement && filteredLogs && !userScrolledUp) {
      preElement.scrollTop = preElement.scrollHeight;
    }
  });
</script>

<div class="card flex flex-col h-full w-full p-3">
  <div class="pb-3">
    <div class="flex items-center justify-between">
      <h6 class="!pb-0">{title}</h6>

      <div class="flex gap-1 items-center">
        <button class="w-8 h-8 flex items-center justify-center rounded text-txtsecondary hover:text-txtmain hover:bg-secondary-hover transition-colors" onclick={toggleFontSize} title="Change font size">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-4 h-4">
            <path d="M2 4v3h5v12h3V7h5V4H2zm19 5h-9v3h3v7h3v-7h3V9z"/>
          </svg>
        </button>
        <button class="w-8 h-8 flex items-center justify-center rounded text-txtsecondary hover:text-txtmain hover:bg-secondary-hover transition-colors" onclick={toggleWrapText} title="Toggle text wrap">
          {#if $wrapTextStore}
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-4 h-4">
              <path fill-rule="evenodd" d="M3 6.75A.75.75 0 0 1 3.75 6h16.5a.75.75 0 0 1 0 1.5H3.75A.75.75 0 0 1 3 6.75ZM3 12a.75.75 0 0 1 .75-.75h16.5a.75.75 0 0 1 0 1.5H3.75A.75.75 0 0 1 3 12Zm0 5.25a.75.75 0 0 1 .75-.75h16.5a.75.75 0 0 1 0 1.5H3.75a.75.75 0 0 1-.75-.75Z" clip-rule="evenodd" />
            </svg>
          {:else}
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-4 h-4">
              <path fill-rule="evenodd" d="M3 6.75A.75.75 0 0 1 3.75 6h16.5a.75.75 0 0 1 0 1.5H3.75A.75.75 0 0 1 3 6.75ZM3 12a.75.75 0 0 1 .75-.75h10.5a.75.75 0 0 1 0 1.5H3.75A.75.75 0 0 1 3 12Zm0 5.25a.75.75 0 0 1 .75-.75h16.5a.75.75 0 0 1 0 1.5H3.75a.75.75 0 0 1-.75-.75Z" clip-rule="evenodd" />
            </svg>
          {/if}
        </button>
        <button class="w-8 h-8 flex items-center justify-center rounded text-txtsecondary hover:text-txtmain hover:bg-secondary-hover transition-colors" onclick={toggleFilter} title="Toggle filter">
          {#if $showFilterStore}
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-4 h-4">
              <path fill-rule="evenodd" d="M10.5 3.75a6.75 6.75 0 1 0 0 13.5 6.75 6.75 0 0 0 0-13.5ZM2.25 10.5a8.25 8.25 0 1 1 14.59 5.28l4.69 4.69a.75.75 0 1 1-1.06 1.06l-4.69-4.69A8.25 8.25 0 0 1 2.25 10.5Z" clip-rule="evenodd" />
            </svg>
          {:else}
            <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="w-4 h-4">
              <path stroke-linecap="round" stroke-linejoin="round" d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z" />
            </svg>
          {/if}
        </button>
      </div>
    </div>

    {#if $showFilterStore}
      <div class="mt-2 flex gap-2 items-center w-full">
        <input
          type="text"
          class="w-full text-sm border border-card-border bg-surface px-2 py-1 rounded focus:outline-none focus:ring-2 focus:ring-primary"
          placeholder="Filter logs (regex)..."
          bind:value={filterRegex}
        />
        <button class="pl-2 text-txtsecondary hover:text-txtmain transition-colors" onclick={() => (filterRegex = "")} aria-label="Clear filter">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-6 h-6">
            <path fill-rule="evenodd" d="M12 2.25c-5.385 0-9.75 4.365-9.75 9.75s4.365 9.75 9.75 9.75 9.75-4.365 9.75-9.75S17.385 2.25 12 2.25Zm-1.72 6.97a.75.75 0 1 0-1.06 1.06L10.94 12l-1.72 1.72a.75.75 0 1 0 1.06 1.06L12 13.06l1.72 1.72a.75.75 0 1 0 1.06-1.06L13.06 12l1.72-1.72a.75.75 0 1 0-1.06-1.06L12 10.94l-1.72-1.72Z" clip-rule="evenodd" />
          </svg>
        </button>
      </div>
    {/if}
  </div>
  <div class="rounded-md border border-card-border-inner bg-background font-mono text-sm flex-1 overflow-hidden">
    <pre bind:this={preElement} onscroll={handleScroll} class="{textWrapClass} {fontSizeClass} pretty-scroll h-full overflow-auto p-4">{filteredLogs}</pre>
  </div>
</div>
