<script lang="ts">
  import { tip } from "../lib/tooltip";
  import { persistentStore } from "../stores/persistent";
  import Select from "./Select.svelte";
  import { Type, WrapText, Search, Copy, Check, X, ArrowDown, HelpCircle } from "lucide-svelte";

  interface Props {
    id: string;
    title: string;
    /** One line explaining what this stream actually carries (shown as a ? hint). */
    subtitle?: string;
    logData: string;
  }

  let { id, title, subtitle, logData }: Props = $props();

  let filterRegex = $state("");

  // Font size is a real px value, not a class enum: ctrl+scroll over the log
  // nudges it like an editor, and the dropdown offers the same range.
  const FONT_MIN = 8;
  const FONT_MAX = 20;
  const FONT_PRESETS = [9, 10, 11, 12, 13, 14, 16, 18];

  // Create persistent stores for this panel (id is intentionally captured at init time)
  // svelte-ignore state_referenced_locally
  const fontPxStore = persistentStore<number>(`logPanel-${id}-fontPx`, 12);
  // svelte-ignore state_referenced_locally
  const wrapTextStore = persistentStore<boolean>(`logPanel-${id}-wrapText`, false);
  // svelte-ignore state_referenced_locally
  const showFilterStore = persistentStore<boolean>(`logPanel-${id}-showFilter`, false);

  let textWrapClass = $derived($wrapTextStore ? "whitespace-pre-wrap" : "whitespace-pre");

  function clampFont(px: number): number {
    return Math.min(FONT_MAX, Math.max(FONT_MIN, Math.round(px)));
  }

  // ctrl+scroll can land between presets, so the current size joins the list
  // rather than leaving the picker showing a value it doesn't offer.
  const fontOptions = $derived(
    (FONT_PRESETS.includes($fontPxStore) ? FONT_PRESETS : [$fontPxStore, ...FONT_PRESETS].sort((a, b) => a - b)).map(
      (px) => ({ value: String(px), label: `${px}px` }),
    ),
  );

  // ctrl/⌘ + wheel over the log body zooms it instead of scrolling the page.
  function handleWheel(e: WheelEvent): void {
    if (!e.ctrlKey && !e.metaKey) return;
    e.preventDefault();
    fontPxStore.update((px) => clampFont(px + (e.deltaY < 0 ? 1 : -1)));
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

  // A half-typed regex ("[" etc.) shows every line rather than nothing, and
  // flags itself in the toolbar instead of silently doing nothing.
  const filtered = $derived.by(() => {
    if (!filterRegex) return { text: logData, bad: false };
    try {
      const regex = new RegExp(filterRegex, "i");
      return { text: logData.split("\n").filter((line) => regex.test(line)).join("\n"), bad: false };
    } catch {
      return { text: logData, bad: true };
    }
  });

  const filteredLogs = $derived(filtered.text);

  // Level colouring. A proxy line is "[hh:mm:ss] [LEVEL] message" (the
  // timestamp is optional), so the prefix is dimmed and the body takes the
  // level's colour; upstream backend lines carry no level tag and render
  // plain. Built as one HTML string rather than a keyed {#each}: the log is a
  // few thousand lines and re-renders on every streamed chunk.
  const LEVEL_RE = /^(.{0,20}?\[(DEBUG|INFO|WARN|ERROR)\]\s?)(.*)$/;

  const LEVEL_CLASS: Record<string, string> = {
    DEBUG: "text-txtsecondary/70",
    INFO: "",
    WARN: "text-warning",
    ERROR: "text-error",
  };

  function escapeHtml(text: string): string {
    return text.replace(/[&<>]/g, (c) => (c === "&" ? "&amp;" : c === "<" ? "&lt;" : "&gt;"));
  }

  const highlighted = $derived.by(() =>
    filteredLogs
      .split("\n")
      .map((line) => {
        const m = LEVEL_RE.exec(line);
        if (!m) return escapeHtml(line);
        const cls = LEVEL_CLASS[m[2]] ?? "";
        const prefixCls = m[2] === "WARN" || m[2] === "ERROR" ? cls : "text-txtsecondary/60";
        return `<span class="${prefixCls}">${escapeHtml(m[1])}</span><span class="${cls}">${escapeHtml(m[3])}</span>`;
      })
      .join("\n"),
  );
  const badRegex = $derived(filtered.bad);
  const totalLines = $derived(logData ? logData.split("\n").length : 0);
  const lineCount = $derived(filteredLogs ? filteredLogs.split("\n").length : 0);

  let copied = $state(false);
  let copyTimer: ReturnType<typeof setTimeout> | null = null;

  async function copyLogs(): Promise<void> {
    try {
      await navigator.clipboard.writeText(filteredLogs);
      copied = true;
      if (copyTimer) clearTimeout(copyTimer);
      copyTimer = setTimeout(() => (copied = false), 1200);
    } catch {
      // clipboard blocked (insecure context) — nothing useful to show
    }
  }

  let preElement: HTMLPreElement;
  let userScrolledUp = $state(false);

  function handleScroll() {
    if (!preElement) return;
    const { scrollTop, scrollHeight, clientHeight } = preElement;
    userScrolledUp = scrollHeight - scrollTop - clientHeight > 40;
  }

  function jumpToBottom(): void {
    if (!preElement) return;
    preElement.scrollTop = preElement.scrollHeight;
    userScrolledUp = false;
  }

  // Auto scroll to bottom when logs change, unless user has scrolled up
  $effect(() => {
    if (preElement && highlighted && !userScrolledUp) {
      preElement.scrollTop = preElement.scrollHeight;
    }
  });
</script>

<div class="card flex flex-col h-full w-full p-0">
  <!-- Toolbar -->
  <div class="flex items-center gap-2 px-3 py-2 border-b border-card-border-inner">
    <h6 class="truncate">{title}</h6>
    {#if subtitle}
      <span
        class="shrink-0 inline-flex text-txtsecondary cursor-help hover:text-txtmain"
        use:tip={subtitle}
        aria-label={subtitle}><HelpCircle size={12} /></span>
    {/if}
    <span class="text-micro font-medium uppercase tracking-wide text-txtsecondary shrink-0">
      {lineCount.toLocaleString()} lines{#if filterRegex && lineCount !== totalLines}<span class="text-primary"> / {totalLines.toLocaleString()}</span>{/if}
    </span>

    <div class="flex gap-1 items-center ml-auto shrink-0">
      <div class="flex items-center gap-1 text-txtsecondary">
        <Type size={14} />
        <Select
          value={String($fontPxStore)}
          onchange={(v) => fontPxStore.set(clampFont(Number(v)))}
          options={fontOptions}
          mono
          tooltip="Font size - ctrl+scroll over the log also works"
          ariaLabel="Font size"
          class="w-20"
        />
      </div>
      <button class="icon-btn" aria-pressed={$wrapTextStore} onclick={toggleWrapText} use:tip={"Toggle text wrap"}><WrapText size={15} /></button>
      <button class="icon-btn" aria-pressed={$showFilterStore} onclick={toggleFilter} use:tip={"Filter (regex)"}><Search size={15} /></button>
      <button class="icon-btn" onclick={copyLogs} use:tip={"Copy visible log"}>
        {#if copied}<Check size={15} class="text-success" />{:else}<Copy size={15} />{/if}
      </button>
    </div>
  </div>

  {#if $showFilterStore}
    <div class="flex gap-2 items-center px-3 py-2 border-b border-card-border-inner">
      <div class="relative flex-1">
        <Search size={13} class="absolute left-2 top-1/2 -translate-y-1/2 text-txtsecondary pointer-events-none" />
        <input
          type="text"
          class="w-full font-mono text-xs border border-card-border bg-surface pl-7 pr-6 py-1 rounded text-txtmain placeholder:text-txtsecondary/60 focus:outline-none focus:ring-2 focus:ring-primary {badRegex ? 'border-error' : ''}"
          placeholder="filter lines (regex)"
          bind:value={filterRegex}
        />
        {#if filterRegex}
          <button class="absolute right-1.5 top-1/2 -translate-y-1/2 text-txtsecondary hover:text-txtmain" onclick={() => (filterRegex = "")} aria-label="Clear filter">
            <X size={13} />
          </button>
        {/if}
      </div>
      {#if badRegex}<span class="font-mono text-[0.65rem] text-error shrink-0">invalid regex</span>{/if}
    </div>
  {/if}

  <div class="relative flex-1 overflow-hidden bg-background font-mono">
    <pre
      bind:this={preElement}
      onscroll={handleScroll}
      onwheel={handleWheel}
      style="font-size: {$fontPxStore}px; line-height: 1.45"
      class="{textWrapClass} pretty-scroll h-full overflow-auto p-3">{@html highlighted}</pre>

    <!-- Auto-follow is suspended while scrolled up; this jumps back and resumes. -->
    {#if userScrolledUp}
      <button
        class="absolute bottom-3 right-4 inline-flex items-center gap-1 rounded-full border border-card-border bg-surface px-2.5 py-1 font-mono text-[0.65rem] uppercase tracking-wide text-txtsecondary shadow-sm hover:text-primary hover:border-primary transition-colors"
        onclick={jumpToBottom}
      ><ArrowDown size={12} /> Live</button>
    {/if}
  </div>
</div>
