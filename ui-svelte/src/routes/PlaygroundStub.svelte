<script lang="ts">
  import { playgroundPort } from "../stores/playgroundAuth";
  import { FlaskConical } from "lucide-svelte";

  const url = $derived(
    $playgroundPort ? `${location.protocol}//${location.hostname}:${$playgroundPort}/ui/` : ""
  );
</script>

<div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
  <FlaskConical class="w-10 h-10 opacity-40" strokeWidth={1.5} />
  {#if url}
    <p>The playground now runs as a separate app.</p>
    <a href={url} target="_blank" rel="noopener" class="px-4 py-2 rounded-md bg-primary text-white font-medium hover:opacity-90 transition-opacity">
      Open Playground
    </a>
  {:else}
    <p>The playground is not enabled. Start the server with <code>-playground-port</code>.</p>
  {/if}
</div>
