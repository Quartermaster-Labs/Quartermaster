<script lang="ts">
  import { playgroundPort } from "../stores/playgroundAuth";
  import { FlaskConical } from "lucide-svelte";
  import { isNative } from "../lib/native";
  import { openTab } from "../stores/appTabs";

  // See Sidebar: in the app window this opens a tab rather than leaving for the
  // browser. The href stays real so the browser case is an ordinary link.
  function openPlayground(e: MouseEvent): void {
    if (!isNative) return;
    e.preventDefault();
    openTab(url);
  }

  const url = $derived(
    $playgroundPort ? `${location.protocol}//${location.hostname}:${$playgroundPort}/ui/` : ""
  );
</script>

<div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
  <FlaskConical class="w-10 h-10 opacity-40" strokeWidth={1.5} />
  {#if url}
    <p>The playground now runs as a separate app.</p>
    <!-- data-qm-inapp: same capture-phase problem as the sidebar's entry --
         without it the document-level external link handler wins and this opens
         in the system browser instead of a tab. -->
    <a href={url} target="_blank" rel="noopener" data-qm-inapp onclick={openPlayground} class="px-4 py-2 rounded-md bg-primary text-white font-medium hover:opacity-90 transition-opacity">
      Open Playground
    </a>
  {:else}
    <p>The playground is not enabled. Start the server with <code>-playground-port</code>.</p>
  {/if}
</div>
