<script lang="ts">
  import { Database, Wand2 } from "lucide-svelte";
  import KvCache from "./KvCache.svelte";
  import Canon from "./Canon.svelte";

  // Context Management umbrella: features that keep the model's context cheap to
  // reprocess. KV Cache persists/restores slot KV across swaps; Canonicalization
  // stabilizes the prompt prefix so llama-server reuses KV natively. Both panels
  // stay mounted (each polls its own endpoint while Context is the active tab).
  const subTabs = [
    { key: "kvcache" as const, label: "KV Cache", icon: Database },
    { key: "canon" as const, label: "Prompt Canonicalization", icon: Wand2 },
  ];
  let sub = $state<"kvcache" | "canon">("kvcache");
</script>

<div class="flex flex-col h-full">
  <!-- Sub-tabs in the same idiom as the Observe bar above them, one step
       quieter (no uppercase weight fight between two stacked tab rows). -->
  <div class="flex items-stretch gap-x-1 px-3 min-h-10 border-b border-card-border-inner shrink-0">
    {#each subTabs as t (t.key)}
      {@const active = sub === t.key}
      <button
        class="flex items-center gap-2 px-3 font-mono text-xs border-b-2 -mb-px transition-colors {active
          ? 'border-primary text-txtmain'
          : 'border-transparent text-txtsecondary hover:text-txtmain'}"
        onclick={() => (sub = t.key)}
      >
        <t.icon size={14} strokeWidth={active ? 2.4 : 1.8} />
        {t.label}
      </button>
    {/each}
  </div>

  <div class="flex-1 min-h-0">
    <div class="h-full" class:hidden={sub !== "kvcache"}>
      <KvCache />
    </div>
    <div class="h-full" class:hidden={sub !== "canon"}>
      <Canon />
    </div>
  </div>
</div>
