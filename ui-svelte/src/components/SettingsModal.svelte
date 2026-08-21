<script lang="ts">
  import { SlidersHorizontal, X } from "lucide-svelte";
  import Settings from "../routes/Settings.svelte";

  let { open = $bindable(false) }: { open?: boolean } = $props();

  function close() {
    open = false;
  }
</script>

{#if open}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={close}
    onkeydown={(e) => e.key === "Escape" && close()}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-full max-w-4xl h-[36rem] max-h-[calc(85vh/var(--qm-scale))] flex flex-col rounded-lg border border-card-border bg-surface shadow-xl overflow-hidden"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="dialog"
      tabindex="-1"
      aria-label="Settings"
    >
      <div class="flex items-center justify-between px-4 py-3 border-b border-card-border">
        <div class="flex items-center gap-2 text-txtmain">
          <SlidersHorizontal size={18} class="text-primary" />
          <span class="text-sm font-medium">Settings</span>
        </div>
        <button class="text-txtsecondary hover:text-txtmain transition-colors" onclick={close} aria-label="Close">
          <X size={18} />
        </button>
      </div>

      <div class="flex-1 min-h-0 flex">
        <Settings />
      </div>
    </div>
  </div>
{/if}
