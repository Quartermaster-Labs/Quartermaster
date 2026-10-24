<script lang="ts">
  import type { Snippet } from "svelte";
  import { History, Plus } from "lucide-svelte";
  import { tip } from "../../lib/tooltip";
  import { historyOpenStore } from "../../stores/playground";
  import { relTime } from "../../lib/playgroundThreads";

  // The h-10 hairline band atop a playground pane, the same band the dashboard
  // panels open with: history toggle, thread title, a mono meta line, then
  // whatever the pane adds on the right (context gauge, config toggle) and New.
  interface Props {
    title: string;
    meta?: string;
    // Last activity; rendered as "· 3m ago" after the meta, kept moving by a
    // minute tick so an idle thread does not say "just now" forever.
    updatedAt?: number;
    newLabel: string;
    onNew: () => void;
    right?: Snippet;
    // Present = the title is a button that turns into a field. Called with the
    // trimmed text; "" means "go back to the automatic title".
    onRename?: (title: string) => void;
  }

  let { title, meta = "", updatedAt, newLabel, onNew, right, onRename }: Props = $props();

  let editing = $state(false);
  let draft = $state("");
  function startEdit() {
    draft = title;
    editing = true;
  }
  // Blur commits, so Enter only has to blur; Escape restores the old text first.
  function commit() {
    if (!editing) return;
    editing = false;
    const t = draft.trim();
    if (t !== title) onRename?.(t);
  }
  function focusSelect(el: HTMLInputElement) {
    el.focus();
    el.select();
  }

  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 30_000);
    return () => clearInterval(t);
  });
  let metaLine = $derived([meta, updatedAt ? relTime(updatedAt, now) : ""].filter(Boolean).join(" · "));
</script>

<div class="flex items-center gap-2 px-3 h-10 border-b border-card-border-inner shrink-0 min-w-0">
  <button
    class="icon-btn shrink-0"
    aria-pressed={$historyOpenStore}
    onclick={() => historyOpenStore.update((v) => !v)}
    use:tip={$historyOpenStore ? "Hide history" : "Show history"}
    aria-label="Toggle history"
  >
    <History class="w-4 h-4" />
  </button>
  {#if editing}
    <input
      use:focusSelect
      bind:value={draft}
      onblur={commit}
      onkeydown={(e) => {
        if (e.key === "Enter") (e.currentTarget as HTMLInputElement).blur();
        else if (e.key === "Escape") {
          e.stopPropagation();
          draft = title;
          (e.currentTarget as HTMLInputElement).blur();
        }
      }}
      maxlength="120"
      aria-label="Thread title"
      class="min-w-0 flex-1 max-w-md h-7 rounded border border-primary bg-background px-1.5 -mx-1.5 text-sm text-txtmain focus:outline-none"
    />
  {:else if onRename}
    <button
      class="truncate min-w-0 text-left text-sm text-txtmain rounded px-1.5 -mx-1.5 h-7 border border-transparent hover:border-card-border cursor-text"
      onclick={startEdit}
      use:tip={"Rename. Clear it to go back to the automatic title"}
    >{title}</button>
  {:else}
    <span class="truncate text-sm text-txtmain min-w-0">{title}</span>
  {/if}
  {#if metaLine}
    <span class="shrink-0 font-mono text-micro text-txtsecondary tabular-nums whitespace-nowrap">· {metaLine}</span>
  {/if}
  <div class="ml-auto flex items-center gap-2 shrink-0">
    {@render right?.()}
    <button class="btn btn--sm inline-flex items-center gap-1 h-7" onclick={onNew}>
      <Plus class="w-3.5 h-3.5" />
      {newLabel}
    </button>
  </div>
</div>
