<script lang="ts">
  import { onMount } from "svelte";
  import { me, checkMe } from "../stores/playgroundAuth";
  import { loadChats, clearChats, startChat } from "../stores/chatHistory";
  import { loadImageChats, clearImageChats } from "../stores/imageHistory";
  import { loadSpeechChats, clearSpeechChats } from "../stores/speechHistory";
  import { loadPrefs, clearPrefs } from "../stores/prefs";
  import { selectedTabStore, selectedModelStore, type PlaygroundTab } from "../stores/playground";
  import { userPref } from "../stores/prefs";
  import Login from "./Login.svelte";
  import PlaygroundShell from "./PlaygroundShell.svelte";

  // Launched from the dashboard's "Chat" button: ?model=<id>&tab=<tab>. Applied
  // after prefs load so it wins over the stored selection, then stripped from the
  // URL so a refresh doesn't re-pin. Each tab has its own model store — chat uses
  // the shared one, images its own per-user pref.
  function applyLaunchParams(): void {
    const p = new URLSearchParams(window.location.search);
    const model = p.get("model");
    const tab = p.get("tab") as PlaygroundTab | null;
    // Ignore a tab that no longer exists (an old dashboard link, e.g. ?tab=rerank)
    // — an unknown value renders no panel at all.
    if (tab && ["chat", "images", "speech", "audio"].includes(tab)) selectedTabStore.set(tab);
    if (model) {
      if (tab === "images") userPref<string>("playground-image-model", "").set(model);
      else {
        selectedModelStore.set(model);
        // Chat launches into a FRESH conversation pinned to this model, rather
        // than dropping the model onto whatever thread happened to be open (which
        // would silently switch that thread's model mid-history).
        startChat(model);
      }
    }
    if (model || tab) history.replaceState(null, "", window.location.pathname + window.location.hash);
  }

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
      Promise.all([loadChats(), loadImageChats(), loadSpeechChats(), loadPrefs()]).then(() => {
        applyLaunchParams();
        chatsLoaded = true;
      });
    } else {
      clearChats();
      clearImageChats();
      clearSpeechChats();
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
