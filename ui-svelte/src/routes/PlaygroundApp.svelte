<script lang="ts">
  import { onMount } from "svelte";
  import { me, checkMe } from "../stores/playgroundAuth";
  import { loadChats, clearChats, startChat, chatSessions, activeChatId, generatingChatId } from "../stores/chatHistory";
  import { loadImageChats, clearImageChats, imageSessions, activeImageChatId, generatingImageChatId } from "../stores/imageHistory";
  import { loadSpeechChats, clearSpeechChats, speechSessions, activeSpeechChatId, generatingSpeechChatId } from "../stores/speechHistory";
  import { loadPrefs, clearPrefs } from "../stores/prefs";
  import { migrateVoicesCache } from "../lib/voices";
  import { loadMemories, clearMemories } from "../stores/memories";
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
    await checkMe();
    ready = true;
  });

  // Hydrate chat history + settings from the server before showing the shell;
  // clear both on logout.
  $effect(() => {
    if ($me) {
      chatsLoaded = false;
      Promise.all([loadChats(), loadImageChats(), loadSpeechChats(), loadPrefs(), loadMemories()]).then(() => {
        // The TTS voice lists used to live in localStorage; fold anything left
        // there into the now server-backed prefs blob. One-shot, post-hydration.
        migrateVoicesCache();
        applyLaunchParams();
        chatsLoaded = true;
      });
    } else {
      clearChats();
      clearImageChats();
      clearSpeechChats();
      clearPrefs();
      clearMemories();
      chatsLoaded = false;
    }
  });

  // The playground's tab title: <hat> <state> <thread> - Quartermaster Playground.
  // Single writer for this app: App.svelte's connection-dot title is guarded to
  // dashboard mode, and onMount no longer touches it (an effect always wins over
  // a one-shot mount write anyway).
  const APP_ICON = "\u{1F3A9}"; // top hat, standing in for the mark the favicon carries

  // Any tab generating lights the bolt, not just the visible one -- the title is
  // read when the browser tab is in the background, where "which playground tab
  // was open" is exactly what you cannot see.
  const busy = $derived(!!($generatingChatId || $generatingImageChatId || $generatingSpeechChatId));

  // The thread name follows the OPEN tab. Each tab keeps its own sessions +
  // active id; the audio tab has no threads, so it contributes no name.
  const threadTitle = $derived.by(() => {
    let t = "";
    if ($selectedTabStore === "chat") t = $chatSessions.find((c) => c.id === $activeChatId)?.title ?? "";
    else if ($selectedTabStore === "images") t = $imageSessions.find((c) => c.id === $activeImageChatId)?.title ?? "";
    else if ($selectedTabStore === "speech") t = $speechSessions.find((c) => c.id === $activeSpeechChatId)?.title ?? "";
    t = t.trim();
    return t.length > 48 ? t.slice(0, 47) + "\u2026" : t;
  });

  $effect(() => {
    const state = busy ? "\u26A1" : "\u2705";
    // Empty segments drop out, so the login screen and the audio tab get a clean
    // "<hat> <state> - Quartermaster Playground" with no dangling separator.
    document.title = [`${APP_ICON} ${state}`, threadTitle, "Quartermaster Playground"].filter(Boolean).join(" \u2014 ");
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
