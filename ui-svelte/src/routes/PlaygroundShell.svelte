<script lang="ts">
  import { get } from "svelte/store";
  import {
    selectedTabStore,
    type PlaygroundTab,
    maxTokensStore,
    reasoningBudgetStore,
    webSearchStore,
    searxngUrlStore,
    searchMaxPerTurnStore,
    searchThrottleMsStore,
    searchDedupeStore,
    systemPresetsStore,
    activeSystemPresetStore,
  } from "../stores/playground";
  import {
    DEFAULT_BUILTIN_PROMPT,
    DEFAULT_SEARCH_PROMPT,
    DEFAULT_WIKI_PROMPT,
    DEFAULT_CITE_PROMPT,
    PROMPT_VARS,
    type SystemPreset,
  } from "../lib/systemPrompt";
  import { searxngSearch } from "../lib/webSearch";
  import { openWikiArticle } from "../stores/wiki";
  import { me, logout } from "../stores/playgroundAuth";
  import { themeMode } from "../stores/theme";
  import {
    chatSessions,
    activeChatId,
    generatingChatId,
    newChatId,
    type ChatSession,
  } from "../stores/chatHistory";
  import {
    imageSessions,
    activeImageChatId,
    generatingImageChatId,
    newImageChatId,
    type ImageSession,
  } from "../stores/imageHistory";
  import {
    speechSessions,
    activeSpeechChatId,
    generatingSpeechChatId,
    newSpeechChatId,
    type SpeechSession,
  } from "../stores/speechHistory";
  import { MessageSquare, Image, Volume2, Mic, ListOrdered, Zap, LogOut, Plus, Trash2, Settings, HelpCircle, BookOpen, SlidersHorizontal, Search, FileText, Pencil } from "lucide-svelte";
  import WikiModal from "../components/WikiModal.svelte";
  import ChatInterface from "../components/playground/ChatInterface.svelte";
  import ImageInterface from "../components/playground/ImageInterface.svelte";
  import AudioInterface from "../components/playground/AudioInterface.svelte";
  import SpeechInterface from "../components/playground/SpeechInterface.svelte";
  import RerankInterface from "../components/playground/RerankInterface.svelte";
  import ConcurrencyInterface from "../components/playground/ConcurrencyInterface.svelte";

  type Tab = PlaygroundTab;

  const tabs: { id: Tab; label: string; icon: typeof MessageSquare }[] = [
    { id: "chat", label: "Chats", icon: MessageSquare },
    { id: "images", label: "Images", icon: Image },
    { id: "speech", label: "Speech", icon: Volume2 },
    { id: "audio", label: "Transcription", icon: Mic },
    { id: "rerank", label: "Rerank", icon: ListOrdered },
    { id: "concurrency", label: "Load Test", icon: Zap },
  ];

  let onChats = $derived($selectedTabStore === "chat");
  let onImages = $derived($selectedTabStore === "images");
  let onSpeech = $derived($selectedTabStore === "speech");
  let historyOpen = $state(false);
  let sortedSessions = $derived([...$chatSessions].sort((a, b) => b.updatedAt - a.updatedAt));
  let sortedImageSessions = $derived([...$imageSessions].sort((a, b) => b.updatedAt - a.updatedAt));
  let sortedSpeechSessions = $derived([...$speechSessions].sort((a, b) => b.updatedAt - a.updatedAt));

  // Chat + Images have a history flyout; Speech manages its own threads inline.
  const hasHistory = (id: Tab) => id === "chat" || id === "images";

  function clickTab(id: Tab) {
    if (hasHistory(id)) {
      historyOpen = $selectedTabStore === id ? !historyOpen : true;
    } else {
      historyOpen = false; // non-history tab (speech, audio, …) never shows the flyout
    }
    selectedTabStore.set(id);
  }

  // History actions are pure store ops — ChatInterface reacts to activeChatId,
  // loading/persisting the working messages itself.
  function newChat() {
    const cur = get(chatSessions).find((s) => s.id === get(activeChatId));
    if (cur && cur.messages.length === 0) {
      activeChatId.set(cur.id); // already on a blank chat — don't stack another
      return;
    }
    const s: ChatSession = { id: newChatId(), title: "New chat", messages: [], updatedAt: Date.now() };
    chatSessions.update((ss) => [s, ...ss]);
    activeChatId.set(s.id);
  }

  // Image threads: same pure-store ops as chats. ImageInterface reacts to
  // activeImageChatId, showing the matching thread's turns.
  function newImageChat() {
    const cur = get(imageSessions).find((s) => s.id === get(activeImageChatId));
    if (cur && cur.turns.length === 0) {
      activeImageChatId.set(cur.id);
      return;
    }
    const s: ImageSession = { id: newImageChatId(), title: "New image", turns: [], updatedAt: Date.now() };
    imageSessions.update((ss) => [s, ...ss]);
    activeImageChatId.set(s.id);
  }

  // Speech threads: same pure-store ops as chats/images.
  function newSpeechChat() {
    const cur = get(speechSessions).find((s) => s.id === get(activeSpeechChatId));
    if (cur && cur.turns.length === 0) {
      activeSpeechChatId.set(cur.id);
      return;
    }
    const s: SpeechSession = { id: newSpeechChatId(), title: "New speech", turns: [], updatedAt: Date.now() };
    speechSessions.update((ss) => [s, ...ss]);
    activeSpeechChatId.set(s.id);
  }

  // Small thumbnails for an image thread's history row: the last turn's images
  // (most recent result first), capped at 2 so the row stays compact.
  function imageThumbs(id: string): string[] {
    const s = $imageSessions.find((x) => x.id === id);
    if (!s) return [];
    for (let i = s.turns.length - 1; i >= 0; i--) {
      if (s.turns[i].images.length) return s.turns[i].images.slice(0, 2);
    }
    return [];
  }

  let confirmDeleteId = $state<string | null>(null);
  let confirmDeleteImageId = $state<string | null>(null);
  let confirmDeleteSpeechId = $state<string | null>(null);
  let showSettings = $state(false);
  // Which settings category the modal's side-nav has selected.
  type SettingsCat = "general" | "search" | "prompt";
  let settingsCat = $state<SettingsCat>("general");
  const settingsCats: { id: SettingsCat; label: string; icon: typeof Settings }[] = [
    { id: "general", label: "General", icon: SlidersHorizontal },
    { id: "search", label: "Web Search", icon: Search },
    { id: "prompt", label: "System Prompt", icon: FileText },
  ];
  // Preset editor. A preset bundles the persona (content) AND its three tool
  // sub-prompts (search/wiki/cite). The editor lists the four sections; clicking
  // one opens a big single-section editor (openSection) with revert + save.
  // content: blank = no system prompt. tool fields: blank = shipped default.
  type SectionKey = "content" | "search" | "wiki" | "cite";
  const PRESET_SECTIONS = [
    { key: "content", label: "System Prompt", def: DEFAULT_BUILTIN_PROMPT, blank: "No system prompt", note: "The persona and instructions. Blank means no system prompt.", vars: true },
    { key: "search", label: "Web Search", def: DEFAULT_SEARCH_PROMPT, blank: "Shipped default", note: "Appended when Web Search is on. Blank uses the shipped default.", vars: false },
    { key: "wiki", label: "Wiki", def: DEFAULT_WIKI_PROMPT, blank: "Shipped default", note: "Appended when the help wiki tool is active (always on in chat). Blank uses the shipped default.", vars: false },
    { key: "cite", label: "Citations", def: DEFAULT_CITE_PROMPT, blank: "Shipped default", note: "Appended when either tool is on — how to cite [n]. Blank uses the shipped default.", vars: false },
  ] as const;
  let presetEditor = $state<null | {
    presetId: string | null; // null = new preset
    name: string;
    content: string;
    search: string; // "" = use default
    wiki: string;
    cite: string;
  }>(null);
  // Single-section editor, layered above the preset editor.
  let sectionEditor = $state<null | { key: SectionKey; value: string }>(null);
  function openSection(key: SectionKey) {
    if (!presetEditor) return;
    sectionEditor = { key, value: presetEditor[key] };
  }
  function saveSection() {
    if (!presetEditor || !sectionEditor) return;
    presetEditor[sectionEditor.key] = sectionEditor.value;
    sectionEditor = null;
  }
  function revertSection() {
    // content reverts to the shipped persona text; tool fields revert to blank
    // (which resolves to the shipped default at send time).
    if (!sectionEditor) return;
    sectionEditor.value = sectionEditor.key === "content" ? DEFAULT_BUILTIN_PROMPT : "";
  }
  function newPreset() {
    // Seed from the built-in default so users edit rather than start blank.
    presetEditor = { presetId: null, name: "", content: DEFAULT_BUILTIN_PROMPT, search: "", wiki: "", cite: "" };
  }
  function editPreset(p: SystemPreset) {
    presetEditor = { presetId: p.id, name: p.name, content: p.content, search: p.search ?? "", wiki: p.wiki ?? "", cite: p.cite ?? "" };
  }
  function savePreset() {
    if (!presetEditor) return;
    const e = presetEditor;
    const nn = (s: string) => (s.trim() === "" ? null : s); // blank tool field → default
    const fields = { name: e.name.trim() || "Untitled", content: e.content, search: nn(e.search), wiki: nn(e.wiki), cite: nn(e.cite) };
    if (e.presetId === null) {
      const id = newChatId();
      systemPresetsStore.update((ps) => [...ps, { id, ...fields }]);
      activeSystemPresetStore.set(id); // apply the new preset immediately
    } else {
      systemPresetsStore.update((ps) => ps.map((p) => (p.id === e.presetId ? { ...p, ...fields } : p)));
    }
    presetEditor = null;
  }
  function deletePreset() {
    if (!presetEditor?.presetId) return;
    const id = presetEditor.presetId;
    systemPresetsStore.update((ps) => ps.filter((p) => p.id !== id));
    if (get(activeSystemPresetStore) === id) activeSystemPresetStore.set(null); // fall back to default
    presetEditor = null;
  }
  let showWiki = $state(false);
  let wikiArticleId = $state<string | null>(null);

  // A chat wiki-citation chip requested an article — open the Help modal to it,
  // then clear the signal so re-clicking the same chip (after close) re-triggers.
  $effect(() => {
    const id = $openWikiArticle;
    if (id) {
      wikiArticleId = id;
      showWiki = true;
      openWikiArticle.set(null);
    }
  });
  let confirmLogout = $state(false);
  let searxngProbe = $state<{ state: "idle" | "testing" | "ok" | "fail"; msg: string }>({ state: "idle", msg: "" });

  async function testSearxng() {
    searxngProbe = { state: "testing", msg: "" };
    try {
      const results = await searxngSearch(get(searxngUrlStore), "test");
      searxngProbe = { state: "ok", msg: `OK — ${results.length} result${results.length === 1 ? "" : "s"}` };
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      // A bare "Failed to fetch" is almost always CORS or wrong host/port.
      searxngProbe = { state: "fail", msg: /failed to fetch/i.test(m) ? "Failed — unreachable or CORS blocked" : m };
    }
  }

  function deleteChat(id: string) {
    confirmDeleteId = null;
    const remaining = get(chatSessions).filter((s) => s.id !== id);
    if (id !== get(activeChatId)) {
      chatSessions.set(remaining);
      return;
    }
    if (remaining.length > 0) {
      chatSessions.set(remaining);
      activeChatId.set(remaining[0].id);
    } else {
      const s: ChatSession = { id: newChatId(), title: "New chat", messages: [], updatedAt: Date.now() };
      chatSessions.set([s]);
      activeChatId.set(s.id);
    }
  }

  function deleteImageChat(id: string) {
    confirmDeleteImageId = null;
    const remaining = get(imageSessions).filter((s) => s.id !== id);
    if (id !== get(activeImageChatId)) {
      imageSessions.set(remaining);
      return;
    }
    if (remaining.length > 0) {
      imageSessions.set(remaining);
      activeImageChatId.set(remaining[0].id);
    } else {
      const s: ImageSession = { id: newImageChatId(), title: "New image", turns: [], updatedAt: Date.now() };
      imageSessions.set([s]);
      activeImageChatId.set(s.id);
    }
  }

  function deleteSpeechChat(id: string) {
    confirmDeleteSpeechId = null;
    const remaining = get(speechSessions).filter((s) => s.id !== id);
    if (id !== get(activeSpeechChatId)) {
      speechSessions.set(remaining);
      return;
    }
    if (remaining.length > 0) {
      speechSessions.set(remaining);
      activeSpeechChatId.set(remaining[0].id);
    } else {
      const s: SpeechSession = { id: newSpeechChatId(), title: "New speech", turns: [], updatedAt: Date.now() };
      speechSessions.set([s]);
      activeSpeechChatId.set(s.id);
    }
  }
</script>

<div class="h-screen flex bg-background">
  <!-- Side rail: icons only at rest; expands on hover. Same width hover or with the chat list open. -->
  <nav
    class="group/rail relative z-40 shrink-0 w-14 hover:w-44 transition-[width] duration-200 overflow-hidden flex flex-col gap-1 py-2 border-r border-border bg-surface"
  >
    <div class="relative pb-2 h-9 flex items-center font-mono text-xs uppercase tracking-[0.2em] text-primary leading-tight">
      <span class="w-14 shrink-0 flex items-center justify-center group-hover/rail:hidden">QM</span>
      <span class="hidden group-hover/rail:block absolute left-0 top-1/2 -translate-y-[0.72rem] whitespace-nowrap pl-[1.2rem] leading-tight">Quartermaster<br />Playground</span>
    </div>
    <div class="flex-1 min-h-0 overflow-y-auto overflow-x-hidden pretty-scroll flex flex-col gap-1">
    {#each tabs as tab (tab.id)}
      {@const active = $selectedTabStore === tab.id}
      <button
        onclick={() => clickTab(tab.id)}
        title={tab.label}
        class="w-full flex items-center pr-3 py-2 border-l-2 transition-colors {active
          ? 'border-primary text-txtmain bg-secondary/60'
          : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
      >
        <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center">
          <span class="relative">
            <tab.icon size={18} strokeWidth={active ? 2.4 : 1.8} class="shrink-0" />
            {#if tab.id === "chat" && $generatingChatId}
              <span class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-primary reason-glow" title="A chat is generating"></span>
            {/if}
            {#if tab.id === "images" && $generatingImageChatId}
              <span class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-primary reason-glow" title="An image is generating"></span>
            {/if}
            {#if tab.id === "speech" && $generatingSpeechChatId}
              <span class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-primary reason-glow" title="Speech is generating"></span>
            {/if}
          </span>
        </span>
        <span class="font-mono text-sm whitespace-nowrap opacity-0 group-hover/rail:opacity-100 transition-opacity">
          {tab.label}
        </span>
      </button>

    {/each}
    </div>

    <!-- Settings (placeholder for per-user memory mgmt) above logout, each its
         own row like the tabs. -->
    <button
      onclick={() => { wikiArticleId = null; showWiki = true; }}
      title="Help"
      class="shrink-0 w-full flex items-center pr-3 py-2 border-l-2 border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center"><BookOpen size={18} class="shrink-0" /></span>
      <span class="font-mono text-sm whitespace-nowrap opacity-0 group-hover/rail:opacity-100 transition-opacity">
        Help
      </span>
    </button>
    <button
      onclick={() => (showSettings = true)}
      title="Settings"
      class="shrink-0 w-full flex items-center pr-3 py-2 border-l-2 border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center"><Settings size={18} class="shrink-0" /></span>
      <span class="font-mono text-sm whitespace-nowrap opacity-0 group-hover/rail:opacity-100 transition-opacity">
        Settings
      </span>
    </button>
    <button
      onclick={() => (confirmLogout = true)}
      title="Log out ({$me})"
      class="w-full flex items-center pr-3 py-2 border-l-2 border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center"><LogOut size={18} class="shrink-0" /></span>
      <span class="font-mono text-sm whitespace-nowrap truncate opacity-0 group-hover/rail:opacity-100 transition-opacity">
        {$me}
      </span>
    </button>
  </nav>

  <!-- Tab content -->
  <main class="flex-1 min-w-0 px-4 pb-4">
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "chat"}><ChatInterface /></div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "images"}><ImageInterface /></div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "speech"}><SpeechInterface /></div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "audio"}><AudioInterface /></div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "rerank"}><RerankInterface /></div>
    <div class="h-full" class:tab-hidden={$selectedTabStore !== "concurrency"}><ConcurrencyInterface /></div>
  </main>
</div>

<!-- Delete confirmation -->
{#if confirmDeleteId}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={() => (confirmDeleteId = null)}
    onkeydown={(e) => e.key === "Escape" && (confirmDeleteId = null)}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-72 flex flex-col gap-3 p-4 rounded-lg border border-card-border bg-surface shadow-lg"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="dialog"
      tabindex="-1"
    >
      <p class="text-sm text-txtmain">Delete this chat? This can't be undone.</p>
      <div class="flex justify-end gap-2">
        <button
          class="px-3 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
          onclick={() => (confirmDeleteId = null)}
        >
          Cancel
        </button>
        <button
          class="px-3 py-1.5 rounded-md text-sm bg-red-500 text-white hover:opacity-90 transition-opacity"
          onclick={() => confirmDeleteId && deleteChat(confirmDeleteId)}
        >
          Delete
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Delete image thread confirmation -->
{#if confirmDeleteImageId}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={() => (confirmDeleteImageId = null)}
    onkeydown={(e) => e.key === "Escape" && (confirmDeleteImageId = null)}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-72 flex flex-col gap-3 p-4 rounded-lg border border-card-border bg-surface shadow-lg"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="dialog"
      tabindex="-1"
    >
      <p class="text-sm text-txtmain">Delete this image thread? This can't be undone.</p>
      <div class="flex justify-end gap-2">
        <button
          class="px-3 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
          onclick={() => (confirmDeleteImageId = null)}
        >
          Cancel
        </button>
        <button
          class="px-3 py-1.5 rounded-md text-sm bg-red-500 text-white hover:opacity-90 transition-opacity"
          onclick={() => confirmDeleteImageId && deleteImageChat(confirmDeleteImageId)}
        >
          Delete
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Delete speech thread confirmation -->
{#if confirmDeleteSpeechId}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={() => (confirmDeleteSpeechId = null)}
    onkeydown={(e) => e.key === "Escape" && (confirmDeleteSpeechId = null)}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-72 flex flex-col gap-3 p-4 rounded-lg border border-card-border bg-surface shadow-lg"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="dialog"
      tabindex="-1"
    >
      <p class="text-sm text-txtmain">Delete this speech thread? This can't be undone.</p>
      <div class="flex justify-end gap-2">
        <button
          class="px-3 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
          onclick={() => (confirmDeleteSpeechId = null)}
        >
          Cancel
        </button>
        <button
          class="px-3 py-1.5 rounded-md text-sm bg-red-500 text-white hover:opacity-90 transition-opacity"
          onclick={() => confirmDeleteSpeechId && deleteSpeechChat(confirmDeleteSpeechId)}
        >
          Delete
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Logout confirmation -->
{#if confirmLogout}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={() => (confirmLogout = false)}
    onkeydown={(e) => e.key === "Escape" && (confirmLogout = false)}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-72 flex flex-col gap-3 p-4 rounded-lg border border-card-border bg-surface shadow-lg"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="dialog"
      tabindex="-1"
    >
      <p class="text-sm text-txtmain">Are you sure you want to log out?</p>
      <div class="flex justify-end gap-2">
        <button
          class="px-3 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
          onclick={() => (confirmLogout = false)}
        >
          Cancel
        </button>
        <button
          class="px-3 py-1.5 rounded-md text-sm bg-red-500 text-white hover:opacity-90 transition-opacity"
          onclick={() => { confirmLogout = false; logout(); }}
        >
          Log out
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Settings — categorized: a left side-nav jumps between panels on the right. -->
{#if showSettings}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={() => (showSettings = false)}
    onkeydown={(e) => e.key === "Escape" && (showSettings = false)}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-full max-w-2xl h-[32rem] flex overflow-hidden rounded-lg border border-card-border bg-surface shadow-lg text-[0.8125rem]"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
      role="dialog"
      tabindex="-1"
    >
      {#snippet tip(text: string)}
        <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary" title={text}>
          <HelpCircle class="w-3.5 h-3.5" />
        </span>
      {/snippet}

      <!-- Category side-nav -->
      <nav class="shrink-0 w-44 flex flex-col gap-0.5 py-3 border-r border-card-border bg-background/40">
        <div class="flex items-center gap-2 px-3 pb-3 text-txtmain">
          <Settings size={16} />
          <span class="text-sm font-medium">Settings</span>
        </div>
        {#each settingsCats as cat (cat.id)}
          {@const active = settingsCat === cat.id}
          <button
            onclick={() => (settingsCat = cat.id)}
            class="w-full flex items-center gap-2.5 px-3 py-2 border-l-2 text-left transition-colors {active
              ? 'border-primary text-txtmain bg-secondary/60'
              : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
          >
            <cat.icon size={15} class="shrink-0" />
            <span class="text-[0.8125rem]">{cat.label}</span>
          </button>
        {/each}
      </nav>

      <!-- Active panel -->
      <div class="flex-1 min-w-0 flex flex-col">
        <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll p-4 flex flex-col gap-4">
          {#if settingsCat === "general"}
            <div class="flex flex-col gap-1">
              <label class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary" for="theme-select">Theme</label>
              <select
                id="theme-select"
                class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                bind:value={$themeMode}
              >
                <option value="system">System</option>
                <option value="light">Light</option>
                <option value="dark">Dark</option>
              </select>
            </div>

            <div class="flex flex-col gap-1">
              <label class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary" for="max-tokens">Max Tokens {@render tip("Cap on how long a single response can be. Higher = longer possible replies.")}</label>
              <input
                id="max-tokens"
                type="number"
                min="1"
                class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                bind:value={$maxTokensStore}
              />
            </div>

            <div class="flex flex-col gap-1">
              <label class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary" for="reasoning-budget">Thinking Budget {@render tip("Max reasoning tokens before the model is forced to answer. Stops it overthinking. 0 = unlimited.")}</label>
              <input
                id="reasoning-budget"
                type="number"
                min="0"
                step="500"
                class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                bind:value={$reasoningBudgetStore}
              />
              <p class="text-xs text-txtsecondary">Max reasoning tokens before the model is forced to answer. 0 = unlimited.</p>
            </div>

            <p class="text-xs text-txtsecondary border-t border-card-border pt-3">Per-user memory management is coming soon.</p>
          {:else if settingsCat === "search"}
            <div class="flex flex-col gap-1.5">
              <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="websearch">
                <span class="flex items-center gap-1.5">Web Search {@render tip("Let the model search the web (via SearXNG) for fresh facts. Needs a tool-calling model.")}</span>
                <input id="websearch" type="checkbox" class="accent-primary w-4 h-4" bind:checked={$webSearchStore} />
              </label>
              {#if $webSearchStore}
                <div class="flex gap-1.5">
                  <input
                    type="text"
                    class="flex-1 min-w-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                    placeholder="SearXNG URL (http://localhost:8888)"
                    bind:value={$searxngUrlStore}
                    oninput={() => (searxngProbe = { state: "idle", msg: "" })}
                  />
                  <button
                    class="shrink-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
                    onclick={testSearxng}
                    disabled={searxngProbe.state === "testing" || !$searxngUrlStore.trim()}
                    title="Probe the SearXNG endpoint"
                  >
                    {searxngProbe.state === "testing" ? "Testing…" : "Test"}
                  </button>
                </div>
                {#if searxngProbe.state === "ok"}
                  <p class="text-xs text-green-500">{searxngProbe.msg}</p>
                {:else if searxngProbe.state === "fail"}
                  <p class="text-xs text-red-500">{searxngProbe.msg}</p>
                {:else}
                  <p class="text-xs text-txtsecondary">Model must support tool calling. SearXNG needs JSON format + CORS enabled.</p>
                {/if}

                <div class="grid grid-cols-2 gap-2">
                  <label class="flex flex-col gap-1 text-xs uppercase tracking-wide text-txtsecondary" for="search-max">
                    <span class="flex items-center gap-1.5">Max / Turn {@render tip("Cap on web searches per message. Once hit, the model must answer with what it found — protects SearXNG from runaway agents.")}</span>
                    <input id="search-max" type="number" min="1" max="50" class="px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$searchMaxPerTurnStore} />
                  </label>
                  <label class="flex flex-col gap-1 text-xs uppercase tracking-wide text-txtsecondary" for="search-throttle">
                    <span class="flex items-center gap-1.5">Throttle ms {@render tip("Minimum gap between searches, so SearXNG's rate limiter doesn't trip. 0 = no delay.")}</span>
                    <input id="search-throttle" type="number" min="0" max="10000" step="100" class="px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary" bind:value={$searchThrottleMsStore} />
                  </label>
                </div>
                <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="search-dedupe">
                  <span class="flex items-center gap-1.5">Dedupe Queries {@render tip("Reuse the result when the model repeats the same query within a turn, instead of searching again.")}</span>
                  <input id="search-dedupe" type="checkbox" class="accent-primary w-4 h-4" bind:checked={$searchDedupeStore} />
                </label>
              {/if}
            </div>
          {:else if settingsCat === "prompt"}
            {#snippet radio(on: boolean)}
              <span class="shrink-0 w-3.5 h-3.5 rounded-full border {on ? 'border-primary' : 'border-card-border'} flex items-center justify-center">
                {#if on}<span class="w-1.5 h-1.5 rounded-full bg-primary"></span>{/if}
              </span>
            {/snippet}

            <div class="flex flex-col gap-2">
              <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">
                System Prompt {@render tip("The standing persona prepended to every chat. Pick the built-in default, one of your presets, or none. Tool/citation instructions are still added automatically when search or wiki are on.")}
              </span>

              <!-- Built-in default -->
              <button
                type="button"
                class="flex items-center gap-2.5 w-full text-left px-2.5 py-2 border-l-2 transition-colors {$activeSystemPresetStore === null ? 'border-primary bg-secondary/60' : 'border-transparent hover:bg-secondary/40'}"
                onclick={() => activeSystemPresetStore.set(null)}
              >
                {@render radio($activeSystemPresetStore === null)}
                <span class="flex flex-col min-w-0">
                  <span class="text-txtmain">Built-in default</span>
                  <span class="text-xs text-txtsecondary truncate">Quartermaster's shipped persona.</span>
                </span>
              </button>

              <!-- User presets -->
              {#each $systemPresetsStore as p (p.id)}
                {@const on = $activeSystemPresetStore === p.id}
                <div class="flex items-center gap-1 w-full border-l-2 transition-colors {on ? 'border-primary bg-secondary/60' : 'border-transparent hover:bg-secondary/40'}">
                  <button type="button" class="flex items-center gap-2.5 flex-1 min-w-0 text-left px-2.5 py-2" onclick={() => activeSystemPresetStore.set(p.id)}>
                    {@render radio(on)}
                    <span class="flex flex-col min-w-0">
                      <span class="text-txtmain truncate">{p.name || "Untitled"}</span>
                      <span class="text-xs text-txtsecondary truncate">{p.content.trim() || "Empty — no system prompt."}</span>
                    </span>
                  </button>
                  <button type="button" class="shrink-0 p-1.5 rounded text-txtsecondary hover:text-txtmain" onclick={() => editPreset(p)} title="Edit preset">
                    <Pencil class="w-3.5 h-3.5" />
                  </button>
                </div>
              {/each}

              <!-- None -->
              <button
                type="button"
                class="flex items-center gap-2.5 w-full text-left px-2.5 py-2 border-l-2 transition-colors {$activeSystemPresetStore === '' ? 'border-primary bg-secondary/60' : 'border-transparent hover:bg-secondary/40'}"
                onclick={() => activeSystemPresetStore.set("")}
              >
                {@render radio($activeSystemPresetStore === "")}
                <span class="flex flex-col min-w-0">
                  <span class="text-txtmain">None</span>
                  <span class="text-xs text-txtsecondary truncate">No system prompt at all.</span>
                </span>
              </button>

              <button
                type="button"
                class="flex items-center justify-center gap-1.5 w-full px-2.5 py-2 rounded-md border border-dashed border-card-border text-txtsecondary hover:text-txtmain hover:border-primary transition-colors"
                onclick={newPreset}
              >
                <Plus class="w-3.5 h-3.5" /> New preset
              </button>

              <p class="text-xs text-txtsecondary border-t border-card-border pt-3">
                Each preset carries its own web-search, wiki, and citation instructions — edit a preset to tune them. The built-in default and its shipped tool prompts stay untouched.
              </p>
            </div>
          {/if}
        </div>

        <div class="shrink-0 flex justify-end border-t border-card-border p-3">
          <button
            class="px-3 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
            onclick={() => (showSettings = false)}
          >
            Close
          </button>
        </div>
      </div>
    </div>
  </div>
{/if}

<!-- Preset editor — persona + the three tool sub-prompts, all in one preset.
     Layers above the settings modal. Blank tool field = use the shipped default. -->
{#if presetEditor}
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" onclick={() => (presetEditor = null)} role="presentation">
    <div
      class="flex w-full max-w-2xl max-h-[85vh] flex-col gap-3 rounded-lg border border-card-border bg-surface p-4 shadow-xl"
      onclick={(e) => e.stopPropagation()}
      role="presentation"
    >
      <span class="font-medium text-txtmain shrink-0">{presetEditor.presetId === null ? "New Preset" : "Edit Preset"}</span>
      <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll flex flex-col gap-3 pr-1">
        <input
          class="w-full px-3 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary text-[0.8125rem]"
          placeholder="Preset name (e.g. Terse Rust expert)"
          bind:value={presetEditor.name}
        />
        <p class="text-xs text-txtsecondary">Click a section to edit it. Tool instructions are appended after the system prompt only when that tool is active.</p>
        {#each PRESET_SECTIONS as sec (sec.key)}
          {@const val = presetEditor[sec.key]}
          <button
            type="button"
            onclick={() => openSection(sec.key)}
            class="flex items-start gap-3 w-full text-left px-3 py-2.5 rounded-md border border-card-border hover:border-primary hover:bg-secondary/40 transition-colors"
          >
            <div class="flex-1 min-w-0">
              <span class="flex items-center gap-1.5 text-[0.8125rem] font-medium text-txtmain">
                {sec.label}
                <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary" title={sec.note}><HelpCircle class="w-3.5 h-3.5" /></span>
              </span>
              <p class="text-xs text-txtsecondary truncate mt-0.5 {val.trim() ? '' : 'italic'}">{val.trim() ? val : sec.blank}</p>
            </div>
            <Pencil class="w-3.5 h-3.5 shrink-0 text-txtsecondary mt-0.5" />
          </button>
        {/each}
      </div>
      <div class="flex items-center justify-between gap-2 shrink-0">
        {#if presetEditor.presetId !== null}
          <button
            class="inline-flex items-center gap-1 px-3 py-1.5 rounded-md border border-card-border text-txtsecondary hover:text-red-500 hover:border-red-500 transition-colors text-sm"
            onclick={deletePreset}
            title="Delete this preset"
          >
            <Trash2 class="w-3.5 h-3.5" /> Delete
          </button>
        {:else}
          <span></span>
        {/if}
        <div class="flex gap-2">
          <button class="px-3 py-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors text-sm" onclick={() => (presetEditor = null)}>Cancel</button>
          <button class="px-3 py-1.5 rounded-md bg-primary text-white hover:opacity-90 transition-opacity text-sm" onclick={savePreset}>Save</button>
        </div>
      </div>
    </div>
  </div>
{/if}

<!-- Single-section editor: a big focused window for one prompt section, layered
     above the preset editor (z-[70] > z-[60]). Revert restores the default. -->
{#if sectionEditor}
  {@const sec = PRESET_SECTIONS.find((s) => s.key === sectionEditor?.key)}
  <div class="fixed inset-0 z-[70] flex items-center justify-center bg-black/60 p-4" onclick={() => (sectionEditor = null)} role="presentation">
    <div
      class="flex w-full max-w-3xl h-[80vh] max-h-[90vh] flex-col gap-3 rounded-lg border border-card-border bg-surface p-4 shadow-xl"
      onclick={(e) => e.stopPropagation()}
      role="presentation"
    >
      <div class="flex items-baseline gap-2 shrink-0">
        <span class="font-medium text-txtmain">{sec?.label}</span>
        <span class="text-xs text-txtsecondary">{sec?.note}</span>
      </div>
      <textarea
        class="flex-1 min-h-0 w-full px-3 py-2 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary resize-none text-[0.8125rem] leading-relaxed pretty-scroll"
        placeholder={sectionEditor.key === "content" ? "Leave empty for no system prompt…" : "Default: " + (sec?.def ?? "")}
        bind:value={sectionEditor.value}
      ></textarea>
      {#if sec?.vars}
        <p class="text-xs text-txtsecondary shrink-0">
          Variables:
          {#each PROMPT_VARS as v, i (v)}<code class="text-txtmain">{v}</code>{i < PROMPT_VARS.length - 1 ? ", " : ""}{/each}
          — replaced when the prompt is sent.
        </p>
      {/if}
      <div class="flex items-center justify-between gap-2 shrink-0">
        <button
          class="px-3 py-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors text-sm"
          onclick={revertSection}
          title="Restore the default for this section"
        >
          Revert to default
        </button>
        <div class="flex gap-2">
          <button class="px-3 py-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors text-sm" onclick={() => (sectionEditor = null)}>Cancel</button>
          <button class="px-3 py-1.5 rounded-md bg-primary text-white hover:opacity-90 transition-opacity text-sm" onclick={saveSection}>Save</button>
        </div>
      </div>
    </div>
  </div>
{/if}

<!-- History popup: previous chats / image threads. Opens off the active Chats or
     Images rail tab; click outside dismisses, selecting a row opens it. -->
{#snippet historyPanel(
  sessions: { id: string; title: string }[],
  activeId: string | null,
  generatingId: string | null,
  onNew: () => void,
  onOpen: (id: string) => void,
  onDelete: (id: string) => void,
  emptyLabel: string,
  thumbsFor?: (id: string) => string[],
)}
  <button
    class="flex items-center justify-center gap-2 w-full px-2.5 py-1.5 rounded-md bg-primary/15 text-primary text-[0.8125rem] font-medium hover:bg-primary/25 transition-colors shrink-0"
    onclick={onNew}
  >
    <Plus class="w-3.5 h-3.5 shrink-0" /> {emptyLabel}
  </button>
  <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll flex flex-col gap-px mt-1.5">
    {#each sessions as session (session.id)}
      {@const sActive = session.id === activeId}
      <div
        class="group/row flex items-center gap-2 w-full px-2 py-1.5 rounded-md text-[0.8125rem] transition-colors {sActive
          ? 'text-txtmain bg-white/5'
          : 'text-txtsecondary hover:text-txtmain hover:bg-white/[0.03]'}"
      >
        {#if session.id === generatingId}
          <span class="w-1.5 h-1.5 shrink-0 rounded-full bg-primary reason-glow" title="Generating…"></span>
        {/if}
        {#if thumbsFor}
          {@const thumbs = thumbsFor(session.id)}
          {#if thumbs.length}
            <span class="flex -space-x-1.5 shrink-0">
              {#each thumbs as th, ti (ti)}
                <img src={th} alt="" class="w-6 h-6 rounded object-cover border border-card-border bg-secondary" />
              {/each}
            </span>
          {/if}
        {/if}
        <button class="flex-1 min-w-0 text-left truncate" onclick={() => onOpen(session.id)} title={session.title || emptyLabel}>
          {session.title || emptyLabel}
        </button>
        <button
          class="shrink-0 p-0.5 rounded text-txtsecondary opacity-0 group-hover/row:opacity-100 hover:text-red-500 transition-opacity"
          onclick={(e) => { e.stopPropagation(); onDelete(session.id); }}
          title="Delete"
        >
          <Trash2 class="w-3.5 h-3.5" />
        </button>
      </div>
    {/each}
  </div>
{/snippet}

{#if historyOpen && (onChats || onImages || onSpeech)}
  <div class="fixed inset-0 z-30" onclick={() => (historyOpen = false)} role="presentation">
    <div
      class="absolute left-44 top-4 w-72 max-h-[80vh] flex flex-col p-2 rounded-lg border border-card-border bg-surface shadow-xl"
      onclick={(e) => e.stopPropagation()}
      role="presentation"
    >
      {#if onChats}
        {@render historyPanel(sortedSessions, $activeChatId, $generatingChatId, () => { newChat(); historyOpen = false; }, (id) => { activeChatId.set(id); historyOpen = false; }, (id) => (confirmDeleteId = id), "New chat")}
      {:else if onImages}
        {@render historyPanel(sortedImageSessions, $activeImageChatId, $generatingImageChatId, () => { newImageChat(); historyOpen = false; }, (id) => { activeImageChatId.set(id); historyOpen = false; }, (id) => (confirmDeleteImageId = id), "New image", imageThumbs)}
      {:else}
        {@render historyPanel(sortedSpeechSessions, $activeSpeechChatId, $generatingSpeechChatId, () => { newSpeechChat(); historyOpen = false; }, (id) => { activeSpeechChatId.set(id); historyOpen = false; }, (id) => (confirmDeleteSpeechId = id), "New speech")}
      {/if}
    </div>
  </div>
{/if}

<WikiModal bind:open={showWiki} articleId={wikiArticleId} />

<style>
  .tab-hidden {
    display: none;
  }
</style>
