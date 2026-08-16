<script lang="ts">
  import { tip as tooltip } from "../lib/tooltip";
  import { get } from "svelte/store";
  import {
    selectedTabStore,
    type PlaygroundTab,
    maxTokensStore,
    reasoningBudgetStore,
    webSearchStore,
    searxngUrlStore,
    searchProvidersStore,
    searchMaxPerTurnStore,
    searchThrottleMsStore,
    searchDedupeStore,
    systemPresetsStore,
    activeSystemPresetStore,
    chatTtsModelStore,
    effectiveTtsModel,
    ttsModels,
    chatTtsVoiceStore,
    memoryStore,
    selectedModelStore,
  } from "../stores/playground";
  import { cachedVoices, fetchVoices, hasCachedVoices, voiceLabel, voiceSubstitution } from "../lib/voices";
  import { generateSpeech } from "../lib/speechApi";
  import { models } from "../stores/api";
  import {
    DEFAULT_BUILTIN_PROMPT,
    DEFAULT_SEARCH_PROMPT,
    DEFAULT_WIKI_PROMPT,
    DEFAULT_YOUTUBE_PROMPT,
    DEFAULT_CITE_PROMPT,
    PROMPT_VARS,
    type SystemPreset,
  } from "../lib/systemPrompt";
  import {
    SEARCH_PROVIDER_META,
    normalizeProviders,
    providerReady,
    searchViaChain,
    type SearchProviderCfg,
    type SearchProviderId,
  } from "../lib/webSearch";
  import { openWikiArticle } from "../stores/wiki";
  import { memories, saveMemory, deleteMemory } from "../stores/memories";
  import type { MemoryEntry } from "../lib/memoryTools";
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
  import { MessageSquare, Image, Volume2, Mic, LogOut, Plus, Trash2, Settings, HelpCircle, BookOpen, SlidersHorizontal, Search, FileText, Pencil, BrainCircuit, RefreshCw, Square } from "lucide-svelte";
  import WikiModal from "../components/WikiModal.svelte";
  import ChatInterface from "../components/playground/ChatInterface.svelte";
  import Select from "../components/Select.svelte";
  import ImageInterface from "../components/playground/ImageInterface.svelte";
  import AudioInterface from "../components/playground/AudioInterface.svelte";
  import SpeechInterface from "../components/playground/SpeechInterface.svelte";

  type Tab = PlaygroundTab;

  const tabs: { id: Tab; label: string; icon: typeof MessageSquare }[] = [
    { id: "chat", label: "Chats", icon: MessageSquare },
    { id: "images", label: "Images", icon: Image },
    { id: "speech", label: "Speech", icon: Volume2 },
    { id: "audio", label: "Transcription", icon: Mic },
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
  type SettingsCat = "general" | "memory" | "search" | "prompt";
  let settingsCat = $state<SettingsCat>("general");
  const settingsCats: { id: SettingsCat; label: string; icon: typeof Settings }[] = [
    { id: "general", label: "General", icon: SlidersHorizontal },
    { id: "memory", label: "Memory", icon: BrainCircuit },
    { id: "search", label: "Web Search", icon: Search },
    { id: "prompt", label: "System Prompt", icon: FileText },
  ];

  // --- Read-aloud voices --------------------------------------------------
  // The voice list is per TTS model and shared with the Speech tab (same pref
  // key, one person one voice). Rendered from the localStorage cache and only
  // fetched when that model is already loaded: GET /v1/audio/voices proxies to
  // tts-server, so an eager fetch would swap a model out of VRAM just because
  // someone opened Settings. The refresh button is the explicit opt-in.
  let ttsVoices = $state<string[]>([""]);
  let ttsVoicesLoading = $state(false);
  // False while the list is still the [""] placeholder — see the clamp effect.
  let ttsVoicesKnown = $state(false);

  const ttsModelReady = $derived(
    !!$effectiveTtsModel && $models.some((m) => m.id === $effectiveTtsModel && m.state === "ready"),
  );

  let lastVoiceModel: string | null = null;
  let lastVoiceFetch: string | null = null;
  $effect(() => {
    const model = $effectiveTtsModel;
    const ready = ttsModelReady;
    if (model !== lastVoiceModel) {
      lastVoiceModel = model;
      ttsVoices = cachedVoices(model);
      ttsVoicesKnown = hasCachedVoices(model);
    }
    // Fetch when the model BECOMES ready, not only when the selection changes.
    // Settings is normally opened while the TTS model is idle, and the old
    // model-changed guard then never fired again — the dropdown sat on the
    // placeholder for the rest of the session however long the model ran. Same
    // fix the Speech tab already carries.
    const key = `${model}|${ready}`;
    if (model && ready && key !== lastVoiceFetch) {
      lastVoiceFetch = key;
      void refreshTtsVoices();
    }
  });

  // A model whose chat template declares reasoning-effort levels ignores the
  // thinking budget (the server skips it — see turns.go runLoop), so the field
  // is disabled rather than silently doing nothing.
  let hasEffortLadder = $derived(
    ($models.find((m) => m.id === $selectedModelStore)?.capabilities?.reasoning_effort ?? []).length > 0,
  );

  // Keep the stored pick inside the list, so the <select> never sits on a value
  // it can't show (a voice cloned on another model, say) while sending it.
  // Gated on ttsVoicesKnown: clamping against the [""] placeholder wipes the
  // saved voice pref for every model whose list hasn't been fetched yet.
  $effect(() => {
    if (ttsVoicesKnown && ttsVoices.length && !ttsVoices.includes($chatTtsVoiceStore)) {
      chatTtsVoiceStore.set(ttsVoices[0]);
    }
  });

  // What actually reaches the server is safeVoice()'s answer, which is not always
  // the name in the dropdown. Say so: a silent swap and a mislabelled voice pack
  // sound identical from the outside. Depends on ttsVoices/ttsVoicesKnown so it
  // re-derives when the cache behind voiceSubstitution changes.
  const voiceSwapNote = $derived.by(() => {
    void ttsVoices;
    void ttsVoicesKnown;
    return voiceSubstitution($effectiveTtsModel, $chatTtsVoiceStore);
  });

  // Preview the selected voice. Deliberately one short sentence — the point is
  // "is this the voice I want", and every extra word is generation time on a
  // model that had to be loaded to answer at all.
  // Plain ASCII on purpose: an em dash / curly quote reaches the phonemizer as an
  // unknown token, and TTS.cpp's Kokoro runner is happy to abort on one.
  const VOICE_TEST_LINE = "Hello, this is how I will read your messages out loud.";
  let ttsTestBusy = $state(false);
  let ttsTestError = $state("");
  // Lets a second click cancel a generation that is still in flight instead of
  // being swallowed — a TTS request that has to load the model first can run for
  // tens of seconds, and an unresponsive button reads as broken.
  let ttsTestAbort: AbortController | null = null;
  // $state, not a plain let: the button swaps to Stop while it plays.
  let ttsTestAudio = $state<HTMLAudioElement | null>(null);
  // "model|voice" of whatever is playing, so a click after changing the voice
  // reads as "preview THAT one" instead of as a stop.
  let ttsTestKey = "";
  // Bumped per preview attempt; see testVoice.
  let ttsTestSeq = 0;

  function stopVoiceTest() {
    ttsTestAbort?.abort();
    ttsTestAbort = null;
    ttsTestKey = "";
    if (!ttsTestAudio) return;
    ttsTestAudio.pause();
    // A paused Audio still holds the blob URL.
    URL.revokeObjectURL(ttsTestAudio.src);
    ttsTestAudio = null;
    ttsTestKey = "";
  }

  // Only the element that is CURRENTLY the preview may clear the button state:
  // an old element's ended/error can land after the next preview started, and
  // stopping unconditionally would kill the new one.
  function finishPreview(a: HTMLAudioElement) {
    if (ttsTestAudio !== a) {
      URL.revokeObjectURL(a.src);
      return;
    }
    stopVoiceTest();
  }

  // A cancelled preview is a user action, not a failure: a fetch aborted mid-
  // flight and a play() interrupted by our own pause() both reject, and showing
  // either as red text under the picker is how "I clicked while the last one was
  // still going" turned into an error message.
  function isCancellation(e: unknown): boolean {
    if (!(e instanceof Error)) return false;
    return e.name === "AbortError" || e.message.includes("interrupted by a call to pause");
  }

  async function testVoice() {
    const model = get(effectiveTtsModel);
    if (!model) return;
    const voice = get(chatTtsVoiceStore);
    const key = `${model}|${voice}`;

    // A click while the previous preview is still generating OR still playing
    // cancels it. Same voice = stop, different voice = play that one instead.
    // Generation can run for tens of seconds when the model has to load first,
    // and the old code just swallowed clicks in that window.
    if (ttsTestBusy || ttsTestAudio) {
      const sameVoice = ttsTestKey === key;
      stopVoiceTest();
      ttsTestError = "";
      if (sameVoice) return;
    }

    // Seq guards against the aborted request's own unwinding: its catch/finally
    // run AFTER this one started and would otherwise clear busy or post an error
    // belonging to a preview nobody is waiting for.
    const seq = ++ttsTestSeq;
    const abort = new AbortController();
    ttsTestAbort = abort;
    ttsTestBusy = true;
    ttsTestKey = key;
    ttsTestError = "";
    try {
      const blob = await generateSpeech(model, VOICE_TEST_LINE, voice, abort.signal);
      if (seq !== ttsTestSeq) return; // superseded while generating
      const audio = new Audio(URL.createObjectURL(blob));
      audio.onended = () => finishPreview(audio);
      // A decode failure with no handler leaves the button stuck in Stop.
      audio.onerror = () => finishPreview(audio);
      ttsTestAudio = audio;
      await audio.play();
    } catch (e) {
      if (seq !== ttsTestSeq || isCancellation(e)) return;
      ttsTestError = e instanceof Error ? e.message : String(e);
      stopVoiceTest();
    } finally {
      if (seq === ttsTestSeq) ttsTestBusy = false;
    }
  }

  // Leaving the playground mid-preview must not leave audio playing or the blob
  // URL alive.
  $effect(() => () => stopVoiceTest());

  async function refreshTtsVoices() {
    const model = get(effectiveTtsModel);
    if (!model || ttsVoicesLoading) return;
    ttsVoicesLoading = true;
    try {
      ttsVoices = await fetchVoices(model);
      // fetchVoices caches on success and returns DEFAULT_VOICES on failure, so
      // the cache — not the return value — is what says whether we really know.
      ttsVoicesKnown = hasCachedVoices(model);
    } finally {
      ttsVoicesLoading = false;
    }
  }

  // --- Memory panel ------------------------------------------------------
  // The list is server-owned (the chat model writes it too), so every edit is a
  // request, not a local mutation flushed later. memEditId: null = not editing,
  // "" = composing a new one.
  let memEditId = $state<string | null>(null);
  let memDraft = $state("");
  let memBusy = $state(false);
  let memError = $state("");

  function startNewMemory() {
    memEditId = "";
    memDraft = "";
    memError = "";
  }
  function startEditMemory(m: MemoryEntry) {
    memEditId = m.id;
    memDraft = m.text;
    memError = "";
  }
  async function commitMemory() {
    const text = memDraft.trim();
    if (!text || memBusy) return;
    memBusy = true;
    memError = "";
    try {
      await saveMemory(memEditId ? { id: memEditId, text } : { text });
      memEditId = null;
      memDraft = "";
    } catch (e) {
      memError = e instanceof Error ? e.message : "could not save";
    } finally {
      memBusy = false;
    }
  }
  async function removeMemory(id: string) {
    memBusy = true;
    memError = "";
    try {
      await deleteMemory(id);
      if (memEditId === id) memEditId = null;
    } catch (e) {
      memError = e instanceof Error ? e.message : "could not delete";
    } finally {
      memBusy = false;
    }
  }
  // Dates only: a memory's usefulness is measured in weeks, and a wall-clock
  // time on every row is noise.
  function memDate(unix: number): string {
    return unix ? new Date(unix * 1000).toLocaleDateString() : "";
  }
  // Preset editor. A preset bundles the persona (content) AND its tool
  // sub-prompts (search/wiki/youtube/cite). The editor lists the sections;
  // clicking one opens a big single-section editor (openSection) with revert +
  // save. content: blank = no system prompt. tool fields: blank = shipped default.
  type SectionKey = "content" | "search" | "wiki" | "youtube" | "cite";
  const PRESET_SECTIONS = [
    { key: "content", label: "System Prompt", def: DEFAULT_BUILTIN_PROMPT, blank: "No system prompt", note: "The persona and instructions. Blank means no system prompt.", vars: true },
    { key: "search", label: "Web Search", def: DEFAULT_SEARCH_PROMPT, blank: "Shipped default", note: "Appended when Web Search is on. Blank uses the shipped default.", vars: false },
    { key: "wiki", label: "Wiki", def: DEFAULT_WIKI_PROMPT, blank: "Shipped default", note: "Appended when the help wiki tool is active (always on in chat). Blank uses the shipped default.", vars: false },
    { key: "youtube", label: "YouTube", def: DEFAULT_YOUTUBE_PROMPT, blank: "Shipped default", note: "Appended when the YouTube tools are active (transcript, search and comments - always on in chat). Blank uses the shipped default.", vars: false },
    { key: "cite", label: "Citations", def: DEFAULT_CITE_PROMPT, blank: "Shipped default", note: "Appended when a citing tool is on - how to cite [n]. Blank uses the shipped default.", vars: false },
  ] as const;
  let presetEditor = $state<null | {
    presetId: string | null; // null = new preset
    name: string;
    content: string;
    search: string; // "" = use default
    wiki: string;
    youtube: string;
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
    presetEditor = { presetId: null, name: "", content: DEFAULT_BUILTIN_PROMPT, search: "", wiki: "", youtube: "", cite: "" };
  }
  function editPreset(p: SystemPreset) {
    presetEditor = { presetId: p.id, name: p.name, content: p.content, search: p.search ?? "", wiki: p.wiki ?? "", youtube: p.youtube ?? "", cite: p.cite ?? "" };
  }
  function savePreset() {
    if (!presetEditor) return;
    const e = presetEditor;
    const nn = (s: string) => (s.trim() === "" ? null : s); // blank tool field → default
    const fields = { name: e.name.trim() || "Untitled", content: e.content, search: nn(e.search), wiki: nn(e.wiki), youtube: nn(e.youtube), cite: nn(e.cite) };
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

  // Web-search provider chain. Local $state (not the store directly) so a
  // half-typed API key isn't PUT to the server on every keystroke; saveProviders
  // writes the whole list back. Seeded from the store once — prefs are hydrated
  // by PlaygroundApp before this component mounts.
  let providers = $state<SearchProviderCfg[]>(normalizeProviders(get(searchProvidersStore), get(searxngUrlStore)));
  let searchProbe = $state<Record<string, { state: "idle" | "testing" | "ok" | "fail"; msg: string }>>({});

  function providerMeta(id: SearchProviderId) {
    return SEARCH_PROVIDER_META.find((m) => m.id === id)!;
  }

  function saveProviders() {
    searchProvidersStore.set(providers.map((p) => ({ ...p })));
    // Keep the legacy standalone pref in step: it is what an older client and
    // the GET /api/websearch proxy still read.
    const searx = providers.find((p) => p.id === "searxng");
    if (searx?.baseUrl !== undefined) searxngUrlStore.set(searx.baseUrl);
  }

  // Order IS the failover order, so it has to be editable. Two arrow buttons
  // rather than drag-and-drop: five rows, and a drag target in a scrolling
  // settings pane is more fuss than it is worth.
  function moveProvider(i: number, dir: -1 | 1) {
    const j = i + dir;
    if (j < 0 || j >= providers.length) return;
    [providers[i], providers[j]] = [providers[j], providers[i]];
    searchProbe = {};
    saveProviders();
  }

  async function testProvider(p: SearchProviderCfg) {
    searchProbe = { ...searchProbe, [p.id]: { state: "testing", msg: "" } };
    try {
      // Test THIS row alone — a chain test would silently pass on the provider
      // below the one being configured.
      const { results } = await searchViaChain([{ ...p, enabled: true }], "test", 3);
      searchProbe = {
        ...searchProbe,
        [p.id]: { state: "ok", msg: `OK - ${results.length} result${results.length === 1 ? "" : "s"}` },
      };
    } catch (e) {
      const m = (e instanceof Error ? e.message : String(e)).trim();
      searchProbe = {
        ...searchProbe,
        [p.id]: { state: "fail", msg: /failed to fetch/i.test(m) ? "Failed - server unreachable" : m || "Failed" },
      };
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
    class="group/rail relative z-40 shrink-0 {historyOpen ? 'w-44' : 'w-14'} hover:w-44 transition-[width] duration-200 overflow-hidden flex flex-col gap-1 py-2 border-r border-border bg-surface"
  >
    <div class="relative pb-2 h-9 flex items-center font-mono text-xs uppercase tracking-[0.2em] text-primary leading-tight">
      <span class="w-14 shrink-0 flex items-center justify-center group-hover/rail:hidden {historyOpen ? 'hidden' : ''}">QM</span>
      <span class="{historyOpen ? 'block' : 'hidden'} group-hover/rail:block absolute left-0 top-1/2 -translate-y-[0.72rem] whitespace-nowrap pl-[1.2rem] leading-tight">Quartermaster<br />Playground</span>
    </div>
    <div class="flex-1 min-h-0 overflow-y-auto overflow-x-hidden pretty-scroll flex flex-col gap-1">
    {#each tabs as tab (tab.id)}
      {@const active = $selectedTabStore === tab.id}
      <button
        onclick={() => clickTab(tab.id)}
        use:tooltip={tab.label}
        class="w-full flex items-center pr-3 py-2 border-l-2 transition-colors {active
          ? 'border-primary text-txtmain bg-secondary/60'
          : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
      >
        <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center">
          <span class="relative">
            <tab.icon size={18} strokeWidth={active ? 2.4 : 1.8} class="shrink-0" />
            {#if tab.id === "chat" && $generatingChatId}
              <span class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-primary reason-glow" use:tooltip={"A chat is generating"}></span>
            {/if}
            {#if tab.id === "images" && $generatingImageChatId}
              <span class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-primary reason-glow" use:tooltip={"An image is generating"}></span>
            {/if}
            {#if tab.id === "speech" && $generatingSpeechChatId}
              <span class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-primary reason-glow" use:tooltip={"Speech is generating"}></span>
            {/if}
          </span>
        </span>
        <span class="font-mono text-sm whitespace-nowrap {historyOpen ? 'opacity-100' : 'opacity-0'} group-hover/rail:opacity-100 transition-opacity">
          {tab.label}
        </span>
      </button>

    {/each}
    </div>

    <!-- Settings (placeholder for per-user memory mgmt) above logout, each its
         own row like the tabs. -->
    <button
      onclick={() => { wikiArticleId = null; showWiki = true; }}
      use:tooltip={"Help"}
      class="shrink-0 w-full flex items-center pr-3 py-2 border-l-2 border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center"><BookOpen size={18} class="shrink-0" /></span>
      <span class="font-mono text-sm whitespace-nowrap {historyOpen ? 'opacity-100' : 'opacity-0'} group-hover/rail:opacity-100 transition-opacity">
        Help
      </span>
    </button>
    <button
      onclick={() => (showSettings = true)}
      use:tooltip={"Settings"}
      class="shrink-0 w-full flex items-center pr-3 py-2 border-l-2 border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center"><Settings size={18} class="shrink-0" /></span>
      <span class="font-mono text-sm whitespace-nowrap {historyOpen ? 'opacity-100' : 'opacity-0'} group-hover/rail:opacity-100 transition-opacity">
        Settings
      </span>
    </button>
    <button
      onclick={() => (confirmLogout = true)}
      use:tooltip={`Log out (${$me})`}
      class="w-full flex items-center pr-3 py-2 border-l-2 border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <span class="w-14 shrink-0 -ml-0.5 flex items-center justify-center"><LogOut size={18} class="shrink-0" /></span>
      <span class="font-mono text-sm whitespace-nowrap truncate {historyOpen ? 'opacity-100' : 'opacity-0'} group-hover/rail:opacity-100 transition-opacity">
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

<!-- Settings - categorized: a left side-nav jumps between panels on the right. -->
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
        <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary" use:tooltip={text}>
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
              <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">Theme</span>
              <Select
                bind:value={$themeMode}
                ariaLabel="Theme"
                options={[
                  { value: "system", label: "System" },
                  { value: "light", label: "Light" },
                  { value: "dark", label: "Dark" },
                ]}
              />
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
                disabled={hasEffortLadder}
              />
              <p class="text-xs text-txtsecondary">
                {#if hasEffortLadder}
                  Not used by this model: it has its own reasoning-effort levels, set in the chat composer. The budget cuts thinking off
                  between rounds, which fights a level the template already sized.
                {:else}
                  Max reasoning tokens before the model is forced to answer. 0 = unlimited.
                {/if}
              </p>
            </div>

            <!-- Read-aloud TTS. Hidden outright when no TTS model is installed -
                 an empty picker for a feature the box can't do is just noise.
                 <Select> rather than ModelSelector: this panel is a scroll
                 container, and ModelSelector's absolutely-positioned menu is
                 clipped by it, so its options landed further down the page
                 instead of over the row. Select's menu is position:fixed. -->
            {#if $ttsModels.length > 0}
              <div class="flex flex-col gap-1">
                <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">
                  Read Aloud {@render tip("Model behind the speaker button under each chat reply. Loading it can evict the chat model - one GPU, one pool.")}
                </span>
                <!-- Bound to the EFFECTIVE model (auto-picked when nothing is
                     chosen), so the row never reads "none" while the button works. -->
                <Select
                  value={$effectiveTtsModel}
                  onchange={(v) => chatTtsModelStore.set(v)}
                  mono
                  ariaLabel="Read-aloud model"
                  options={$ttsModels.map((m) => ({ value: m.id, label: m.id }))}
                />
              </div>

              <div class="flex flex-col gap-1">
                <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">
                  Voice {@render tip("Speaker the read-aloud button uses. Shared with the Speech tab - one person, one voice. Refresh loads the model to ask it for the full list, including any cloned voices.")}
                </span>
                <div class="flex items-center gap-2">
                  <Select
                    bind:value={$chatTtsVoiceStore}
                    ariaLabel="Voice"
                    class="flex-1 min-w-0"
                    options={ttsVoices.map((v) => ({ value: v, label: voiceLabel(v) }))}
                  />
                  <button
                    type="button"
                    class="p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:border-primary/50 disabled:opacity-50 transition-colors"
                    use:tooltip={ttsTestAudio || ttsTestBusy
                      ? "Stop (or pick another voice to hear that one instead)"
                      : `Hear this voice (loads the TTS model). Says: "${VOICE_TEST_LINE}"`}
                    disabled={!$effectiveTtsModel}
                    onclick={testVoice}
                  >
                    {#if ttsTestAudio || ttsTestBusy}
                      <Square class="w-3.5 h-3.5 {ttsTestBusy ? 'animate-pulse' : ''}" />
                    {:else}
                      <Volume2 class="w-3.5 h-3.5" />
                    {/if}
                  </button>
                  <button
                    type="button"
                    class="p-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:border-primary/50 disabled:opacity-50 transition-colors"
                    use:tooltip={"Refresh voices (loads the TTS model)"}
                    disabled={ttsVoicesLoading || !$effectiveTtsModel}
                    onclick={refreshTtsVoices}
                  >
                    <RefreshCw class="w-3.5 h-3.5 {ttsVoicesLoading ? 'animate-spin' : ''}" />
                  </button>
                </div>
                {#if ttsTestError}
                  <p class="text-xs text-red-400">{ttsTestError}</p>
                {/if}
                <!-- Without this, a substituted voice looks like a mislabelled
                     one: the name stays on screen and someone else speaks. -->
                {#if voiceSwapNote}
                  <p class="text-xs text-warning">{voiceSwapNote}</p>
                {/if}
                <p class="text-xs text-txtsecondary">
                  {#if ttsVoices.length > 1}
                    {ttsVoices.length} voices for this model.
                  {:else}
                    Only this model's default voice is known. Refresh to ask it for its full list.
                  {/if}
                  <!-- Spell out the preview line: otherwise the only way to know
                       what the speaker button says is to press it. -->
                  Preview says <span class="italic">“{VOICE_TEST_LINE}”</span>
                </p>
              </div>
            {/if}
          {:else if settingsCat === "memory"}
            <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="memory-enabled">
              <span class="flex items-center gap-1.5">Memory {@render tip("Let the assistant remember lasting facts about you across conversations. Remembered facts are added to the system prompt of every chat, so keeping the list short keeps chats fast.")}</span>
              <input id="memory-enabled" type="checkbox" class="accent-primary w-4 h-4" bind:checked={$memoryStore} />
            </label>

            <p class="text-xs text-txtsecondary">
              The assistant writes here when you tell it to remember something. Everything is editable - these facts go into every chat, so a wrong one is worth deleting.
            </p>

            {#if memError}
              <p class="text-xs text-red-400">{memError}</p>
            {/if}

            {#if memEditId === ""}
              <div class="flex flex-col gap-2 p-2.5 rounded-md border border-primary/60 bg-background/40">
                <textarea
                  class="w-full min-h-20 px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary resize-y"
                  placeholder="Something worth remembering across conversations…"
                  bind:value={memDraft}
                ></textarea>
                <div class="flex justify-end gap-2">
                  <button type="button" class="px-2.5 py-1 rounded-md text-txtsecondary hover:text-txtmain" onclick={() => (memEditId = null)}>Cancel</button>
                  <button type="button" class="px-2.5 py-1 rounded-md bg-primary text-white disabled:opacity-50" disabled={!memDraft.trim() || memBusy} onclick={commitMemory}>Save</button>
                </div>
              </div>
            {:else}
              <button
                type="button"
                class="flex items-center gap-1.5 self-start px-2.5 py-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:border-primary transition-colors"
                onclick={startNewMemory}
              >
                <Plus class="w-3.5 h-3.5" /> Add a memory
              </button>
            {/if}

            <div class="flex flex-col gap-1.5">
              {#each $memories as m (m.id)}
                {#if memEditId === m.id}
                  <div class="flex flex-col gap-2 p-2.5 rounded-md border border-primary/60 bg-background/40">
                    <textarea
                      class="w-full min-h-20 px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary resize-y"
                      bind:value={memDraft}
                    ></textarea>
                    <div class="flex justify-end gap-2">
                      <button type="button" class="px-2.5 py-1 rounded-md text-txtsecondary hover:text-txtmain" onclick={() => (memEditId = null)}>Cancel</button>
                      <button type="button" class="px-2.5 py-1 rounded-md bg-primary text-white disabled:opacity-50" disabled={!memDraft.trim() || memBusy} onclick={commitMemory}>Save</button>
                    </div>
                  </div>
                {:else}
                  <div class="group flex items-start gap-2 p-2.5 rounded-md border border-card-border bg-background/40">
                    <div class="flex-1 min-w-0 flex flex-col gap-1">
                      <span class="text-txtmain whitespace-pre-wrap break-words">{m.text}</span>
                      <span class="text-xs text-txtsecondary">
                        {m.source === "assistant" ? "Remembered by the assistant" : "Added by you"}{memDate(m.updatedAt) ? ` · ${memDate(m.updatedAt)}` : ""}
                      </span>
                    </div>
                    <button type="button" class="shrink-0 p-1.5 rounded text-txtsecondary hover:text-txtmain" use:tooltip={"Edit"} onclick={() => startEditMemory(m)}>
                      <Pencil class="w-3.5 h-3.5" />
                    </button>
                    <button type="button" class="shrink-0 p-1.5 rounded text-txtsecondary hover:text-red-400" use:tooltip={"Delete"} disabled={memBusy} onclick={() => removeMemory(m.id)}>
                      <Trash2 class="w-3.5 h-3.5" />
                    </button>
                  </div>
                {/if}
              {:else}
                <p class="text-xs text-txtsecondary italic">Nothing remembered yet.</p>
              {/each}
            </div>
          {:else if settingsCat === "search"}
            <div class="flex flex-col gap-1.5">
              <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="websearch">
                <span class="flex items-center gap-1.5">Web Search {@render tip("Let the model search the web for fresh facts. Needs a tool-calling model.")}</span>
                <input id="websearch" type="checkbox" class="accent-primary w-4 h-4" bind:checked={$webSearchStore} />
              </label>
              {#if $webSearchStore}
                <!-- Provider chain. Tried top to bottom; a provider that errors,
                     times out or returns nothing hands off to the next one. -->
                <div class="flex flex-col gap-2 border-t border-card-border pt-2.5">
                  <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">
                    Providers {@render tip("Searched in order, top first. If one times out or returns nothing, the next one is tried - so a rate-limited SearXNG no longer ends the search. Keyed providers only spend quota when the ones above them failed.")}
                  </span>

                  {#each providers as p, i (p.id)}
                    {@const meta = providerMeta(p.id)}
                    {@const probe = searchProbe[p.id]}
                    <div class="rounded-md border {p.enabled ? 'border-card-border' : 'border-card-border/50'} bg-surface/60 p-2 flex flex-col gap-1.5">
                      <div class="flex items-center gap-2">
                        <span class="font-mono text-[0.7rem] text-txtsecondary w-4 shrink-0">{i + 1}</span>
                        <input
                          type="checkbox"
                          class="accent-primary w-4 h-4 shrink-0"
                          aria-label="Enable {meta.label}"
                          bind:checked={p.enabled}
                          onchange={saveProviders}
                        />
                        <span class="flex-1 min-w-0 truncate text-sm {p.enabled ? 'text-txtmain' : 'text-txtsecondary'}">{meta.label}</span>
                        {#if p.enabled && !providerReady(p)}
                          <span class="shrink-0 text-[0.65rem] uppercase tracking-wide text-amber-500" use:tooltip={"Enabled but not configured - it is skipped, not tried."}>
                            needs setup
                          </span>
                        {/if}
                        <button
                          class="shrink-0 px-1 text-txtsecondary hover:text-txtmain disabled:opacity-30"
                          onclick={() => moveProvider(i, -1)}
                          disabled={i === 0}
                          use:tooltip={"Try earlier"}
                          aria-label="Move {meta.label} up">↑</button
                        >
                        <button
                          class="shrink-0 px-1 text-txtsecondary hover:text-txtmain disabled:opacity-30"
                          onclick={() => moveProvider(i, 1)}
                          disabled={i === providers.length - 1}
                          use:tooltip={"Try later"}
                          aria-label="Move {meta.label} down">↓</button
                        >
                      </div>

                      {#if p.enabled}
                        {#if meta.needs === "url"}
                          <input
                            type="text"
                            class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                            placeholder="http://localhost:8888"
                            bind:value={p.baseUrl}
                            oninput={() => (searchProbe = { ...searchProbe, [p.id]: { state: "idle", msg: "" } })}
                            onchange={saveProviders}
                          />
                        {:else if meta.needs === "key" || meta.needs === "key+cx"}
                          <input
                            type="password"
                            autocomplete="off"
                            class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                            placeholder="API key"
                            bind:value={p.key}
                            oninput={() => (searchProbe = { ...searchProbe, [p.id]: { state: "idle", msg: "" } })}
                            onchange={saveProviders}
                          />
                        {/if}
                        {#if meta.needs === "key+cx"}
                          <input
                            type="text"
                            class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                            placeholder="Search engine id (cx)"
                            bind:value={p.cx}
                            oninput={() => (searchProbe = { ...searchProbe, [p.id]: { state: "idle", msg: "" } })}
                            onchange={saveProviders}
                          />
                        {/if}

                        <div class="flex items-center gap-2">
                          <button
                            class="shrink-0 px-2.5 py-1 rounded-md border border-card-border bg-surface text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
                            onclick={() => testProvider(p)}
                            disabled={probe?.state === "testing" || !providerReady(p)}
                            use:tooltip={"Run one query against this provider only"}
                          >
                            {probe?.state === "testing" ? "Testing…" : "Test"}
                          </button>
                          {#if probe?.state === "ok"}
                            <span class="text-xs text-green-500 min-w-0 truncate">{probe.msg}</span>
                          {:else if probe?.state === "fail"}
                            <span class="text-xs text-red-500 min-w-0 truncate" use:tooltip={probe.msg}>{probe.msg}</span>
                          {:else if meta.signupUrl}
                            <a class="text-xs text-txtsecondary hover:text-primary underline" href={meta.signupUrl} target="_blank" rel="noopener noreferrer">Get a key</a>
                          {/if}
                        </div>
                        <p class="text-[0.7rem] leading-snug text-txtsecondary">{meta.hint}</p>
                      {/if}
                    </div>
                  {/each}

                  {#if !providers.some(providerReady)}
                    <p class="text-xs text-amber-500">No provider is configured - web search will fail on every call.</p>
                  {:else}
                    <p class="text-xs text-txtsecondary">Model must support tool calling.</p>
                  {/if}
                </div>

                <div class="grid grid-cols-2 gap-2">
                  <label class="flex flex-col gap-1 text-xs uppercase tracking-wide text-txtsecondary" for="search-max">
                    <span class="flex items-center gap-1.5">Max / Turn {@render tip("Cap on web searches per message. Once hit, the model must answer with what it found - protects SearXNG from runaway agents.")}</span>
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
                      <span class="text-xs text-txtsecondary truncate">{p.content.trim() || "Empty - no system prompt."}</span>
                    </span>
                  </button>
                  <button type="button" class="shrink-0 p-1.5 rounded text-txtsecondary hover:text-txtmain" onclick={() => editPreset(p)} use:tooltip={"Edit preset"}>
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
                Each preset carries its own web-search, wiki, and citation instructions - edit a preset to tune them. The built-in default and its shipped tool prompts stay untouched.
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
                <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary" use:tooltip={sec.note}><HelpCircle class="w-3.5 h-3.5" /></span>
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
            use:tooltip={"Delete this preset"}
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
          - replaced when the prompt is sent.
        </p>
      {/if}
      <div class="flex items-center justify-between gap-2 shrink-0">
        <button
          class="px-3 py-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors text-sm"
          onclick={revertSection}
          use:tooltip={"Restore the default for this section"}
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
  heading: string,
  newTip: string,
  thumbsFor?: (id: string) => string[],
)}
  <div class="flex items-center justify-between gap-2 px-1 shrink-0">
    <span class="text-[0.8125rem] font-medium text-txtmain truncate">{heading}</span>
    <!-- No newTip = no button: speech threads start from the composer, not here. -->
    {#if newTip}
      <button
        class="shrink-0 grid place-items-center w-6 h-6 rounded-md bg-[#141414] text-[#ededee] border border-card-border hover:bg-[#1e1e1e] hover:text-white transition-colors"
        onclick={onNew}
        use:tooltip={newTip}
        aria-label={newTip}
      >
        <Plus class="w-3.5 h-3.5" />
      </button>
    {/if}
  </div>
  <div class="flex-1 min-h-0 overflow-y-auto pretty-scroll flex flex-col gap-px mt-1.5">
    {#each sessions as session (session.id)}
      {@const sActive = session.id === activeId}
      <div
        class="group/row flex items-center gap-2 w-full px-2 py-1.5 rounded-md text-[0.8125rem] transition-colors {sActive
          ? 'text-txtmain bg-white/5'
          : 'text-txtsecondary hover:text-txtmain hover:bg-white/[0.03]'}"
      >
        {#if session.id === generatingId}
          <span class="w-1.5 h-1.5 shrink-0 rounded-full bg-primary reason-glow" use:tooltip={"Generating…"}></span>
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
        <button class="flex-1 min-w-0 text-left truncate" onclick={() => onOpen(session.id)} use:tooltip={session.title || emptyLabel}>
          {session.title || emptyLabel}
        </button>
        <button
          class="shrink-0 p-0.5 rounded text-txtsecondary opacity-0 group-hover/row:opacity-100 hover:text-red-500 transition-opacity"
          onclick={(e) => { e.stopPropagation(); onDelete(session.id); }}
          use:tooltip={"Delete"}
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
      class="absolute left-[12rem] top-4 w-72 max-h-[80vh] flex flex-col p-2 rounded-lg border border-card-border bg-surface shadow-xl"
      onclick={(e) => e.stopPropagation()}
      role="presentation"
    >
      {#if onChats}
        {@render historyPanel(sortedSessions, $activeChatId, $generatingChatId, () => { newChat(); historyOpen = false; }, (id) => { activeChatId.set(id); historyOpen = false; }, (id) => (confirmDeleteId = id), "New chat", "Chat history", "New chat")}
      {:else if onImages}
        {@render historyPanel(sortedImageSessions, $activeImageChatId, $generatingImageChatId, () => { newImageChat(); historyOpen = false; }, (id) => { activeImageChatId.set(id); historyOpen = false; }, (id) => (confirmDeleteImageId = id), "New image", "Image history", "New image", imageThumbs)}
      {:else}
        {@render historyPanel(sortedSpeechSessions, $activeSpeechChatId, $generatingSpeechChatId, () => { newSpeechChat(); historyOpen = false; }, (id) => { activeSpeechChatId.set(id); historyOpen = false; }, (id) => (confirmDeleteSpeechId = id), "New speech", "Speech history", "")}
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
