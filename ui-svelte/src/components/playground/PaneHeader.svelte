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
  }

  let { title, meta = "", updatedAt, newLabel, onNew, right }: Props = $props();

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
  <span class="truncate text-sm text-txtmain min-w-0">{title}</span>
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
