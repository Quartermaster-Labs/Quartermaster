<script lang="ts">
  import { diffWords } from "../../lib/wordDiff";

  interface Props {
    original: string;
    rewritten: string;
    isStreaming?: boolean;
    modelReady?: boolean;
  }

  let { original, rewritten, isStreaming = false, modelReady = false }: Props = $props();

  let ops = $derived(diffWords(original, rewritten));
  // Left = original (equal + removed); right = rewritten (equal + added).
  let leftOps = $derived(ops.filter((o) => o.type !== "insert"));
  let rightOps = $derived(ops.filter((o) => o.type !== "delete"));
</script>

<div class="grid grid-cols-2 text-[0.8125rem]">
  <!-- Original -->
  <div class="min-w-0 pr-4 border-r border-card-border">
    <div class="mb-1.5 text-xs font-medium text-txtsecondary">Original</div>
    <div class="whitespace-pre-wrap break-words leading-relaxed">
      {#each leftOps as op, i (i)}
        {#if op.type === "delete"}<span class="bg-red-500/20 text-red-700 dark:text-red-300 rounded-[2px]">{op.value}</span>{:else}<span>{op.value}</span>{/if}
      {/each}
    </div>
  </div>

  <!-- Rewritten -->
  <div class="min-w-0 pl-4">
    <div class="mb-1.5 flex items-center gap-2 text-xs font-medium text-txtsecondary">
      Rewritten{#if isStreaming}<span class="inline-block w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>{/if}
    </div>
    <div class="whitespace-pre-wrap break-words leading-relaxed">
      {#if !rewritten && isStreaming}
        <span class="inline-flex items-center gap-2 text-txtsecondary italic">
          <span class="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
          {modelReady ? "Rewriting…" : "Loading model…"}
        </span>
      {:else}
        {#each rightOps as op, i (i)}
          {#if op.type === "insert"}<span class="bg-green-500/20 text-green-700 dark:text-green-300 rounded-[2px]">{op.value}</span>{:else}<span>{op.value}</span>{/if}
        {/each}
      {/if}
    </div>
  </div>
</div>
