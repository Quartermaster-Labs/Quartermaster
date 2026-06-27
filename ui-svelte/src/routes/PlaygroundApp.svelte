<script lang="ts">
  import { onMount } from "svelte";
  import { me, checkMe } from "../stores/playgroundAuth";
  import { loadChats, clearChats } from "../stores/chatHistory";
  import { loadPrefs, clearPrefs } from "../stores/prefs";
  import Login from "./Login.svelte";
  import PlaygroundShell from "./PlaygroundShell.svelte";

  let ready = $state(false); // initial /auth/me check done
  let chatsLoaded = $state(false);

  onMount(async () => {
    document.title = "Quartermaster Playground";
    await checkMe();
    ready = true;
  });

  // Hydrate chat history + settings from the server before showing the shell;
  // clear both on logout.
  $effect(() => {
    if ($me) {
      chatsLoaded = false;
      Promise.all([loadChats(), loadPrefs()]).then(() => (chatsLoaded = true));
    } else {
      clearChats();
      clearPrefs();
      chatsLoaded = false;
    }
  });
</script>

{#if !ready}
  <div class="h-screen bg-background"></div>
{:else if !$me}
  <Login />
{:else if !chatsLoaded}
  <div class="h-screen flex items-center justify-center bg-background text-txtsecondary font-mono text-sm">
    Loading…
  </div>
{:else}
  <PlaygroundShell />
{/if}
