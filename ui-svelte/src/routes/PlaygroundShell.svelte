<script lang="ts">
  import { get } from "svelte/store";
  import {
    selectedTabStore,
    type PlaygroundTab,
    maxTokensStore,
    reasoningBudgetStore,
    webSearchStore,
    searxngUrlStore,
  } from "../stores/playground";
  import { searxngSearch } from "../lib/webSearch";
  import { me, logout } from "../stores/playgroundAuth";
  import {
    chatSessions,
    activeChatId,
    generatingChatId,
    newChatId,
    type ChatSession,
  } from "../stores/chatHistory";
  import { MessageSquare, Image, Volume2, Mic, ListOrdered, Zap, LogOut, Plus, Trash2, Settings, HelpCircle } from "lucide-svelte";
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
  let historyOpen = $state(false);
  let sortedSessions = $derived([...$chatSessions].sort((a, b) => b.updatedAt - a.updatedAt));

  function clickTab(id: Tab) {
    if (id === "chat") {
      historyOpen = onChats ? !historyOpen : true;
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

  let confirmDeleteId = $state<string | null>(null);
  let showSettings = $state(false);
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
</script>

<div class="h-screen flex bg-background dot-bg">
  <!-- Side rail: icons only at rest; expands on hover. Same width hover or with the chat list open. -->
  <nav
    class="group/rail shrink-0 w-14 hover:w-44 transition-[width] duration-200 overflow-hidden flex flex-col gap-1 p-2 border-r border-border bg-surface"
    onmouseleave={() => (historyOpen = false)}
  >
    <div class="px-2 pb-2 h-9 font-mono text-[0.6rem] uppercase tracking-[0.2em] text-primary leading-tight">
      <span class="group-hover/rail:hidden">QM</span>
      <span class="hidden group-hover/rail:block">Quartermaster<br />Playground</span>
    </div>
    <div class="flex-1 min-h-0 overflow-y-auto overflow-x-hidden pretty-scroll flex flex-col gap-1">
    {#each tabs as tab (tab.id)}
      {@const active = $selectedTabStore === tab.id}
      <button
        onclick={() => clickTab(tab.id)}
        title={tab.label}
        class="flex items-center gap-3 px-2.5 py-2 rounded-md border-l-2 transition-colors {tab.id === 'chat' && historyOpen ? 'group-hover/rail:rounded-b-none' : ''} {active
          ? 'border-primary text-primary bg-secondary/60'
          : 'border-transparent text-txtsecondary hover:text-txtmain hover:bg-secondary/40'}"
      >
        <span class="relative shrink-0">
          <tab.icon size={18} strokeWidth={active ? 2.4 : 1.8} class="shrink-0" />
          {#if tab.id === "chat" && $generatingChatId}
            <span class="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-primary reason-glow" title="A chat is generating"></span>
          {/if}
        </span>
        <span class="font-mono text-sm whitespace-nowrap opacity-0 group-hover/rail:opacity-100 transition-opacity">
          {tab.label}
        </span>
      </button>

      {#if tab.id === "chat" && onChats}
        <!-- Chat history, nested under Chats. Only revealed while the rail is
             hovered AND toggled open — so the rail still collapses on mouse-leave.
             Grid-rows 0fr→1fr animates the open, sliding the tabs below down. -->
        <div class="grid {historyOpen ? 'grid-rows-[0fr] group-hover/rail:grid-rows-[1fr]' : 'grid-rows-[0fr]'} transition-[grid-template-rows] duration-200 ease-out -mt-1">
          <div class="overflow-hidden">
            <div class="flex flex-col gap-0.5 px-1.5 pt-2 pb-1.5 w-full rounded-b-lg bg-background">
              <button
                class="flex items-center justify-center gap-2 w-full px-2.5 py-1.5 rounded-md bg-primary/15 text-primary text-[0.8125rem] font-medium hover:bg-primary/25 transition-colors"
                onclick={newChat}
                title="Start a new chat"
              >
                <Plus class="w-3.5 h-3.5 shrink-0" />
                New chat
              </button>
              <div class="max-h-[40vh] overflow-y-auto pretty-scroll flex flex-col gap-px mt-0.5">
                {#each sortedSessions as session (session.id)}
                  {@const sActive = session.id === $activeChatId}
                  <div
                    class="group/row flex items-center gap-2 w-full px-2 py-1.5 rounded-md text-[0.8125rem] transition-colors {sActive
                      ? 'text-txtmain bg-white/5'
                      : 'text-txtsecondary hover:text-txtmain hover:bg-white/[0.03]'}"
                  >
                    {#if session.id === $generatingChatId}
                      <span class="w-1.5 h-1.5 shrink-0 rounded-full bg-primary reason-glow" title="Generating…"></span>
                    {/if}
                    <button class="flex-1 min-w-0 text-center truncate text-[0.75rem]" onclick={() => activeChatId.set(session.id)} title={session.title || "New chat"}>
                      {session.title || "New chat"}
                    </button>
                    <button
                      class="shrink-0 p-0.5 rounded text-txtsecondary opacity-0 group-hover/row:opacity-100 hover:text-red-500 transition-opacity"
                      onclick={(e) => { e.stopPropagation(); confirmDeleteId = session.id; }}
                      title="Delete chat"
                    >
                      <Trash2 class="w-3.5 h-3.5" />
                    </button>
                  </div>
                {/each}
              </div>
            </div>
          </div>
        </div>
      {/if}
    {/each}
    </div>

    <!-- Settings (placeholder for per-user memory mgmt) above logout, each its
         own row like the tabs. -->
    <button
      onclick={() => (showSettings = true)}
      title="Settings"
      class="shrink-0 flex items-center gap-3 px-2.5 py-2 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <Settings size={18} class="shrink-0" />
      <span class="font-mono text-sm whitespace-nowrap opacity-0 group-hover/rail:opacity-100 transition-opacity">
        Settings
      </span>
    </button>
    <button
      onclick={() => (confirmLogout = true)}
      title="Log out ({$me})"
      class="flex items-center gap-3 px-2.5 py-2 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary/40 transition-colors"
    >
      <LogOut size={18} class="shrink-0" />
      <span class="font-mono text-sm whitespace-nowrap truncate opacity-0 group-hover/rail:opacity-100 transition-opacity">
        {$me}
      </span>
    </button>
  </nav>

  <!-- Tab content -->
  <main class="flex-1 min-w-0 p-4">
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

<!-- Settings — placeholder; per-user memory management lands here later. -->
{#if showSettings}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
    onclick={() => (showSettings = false)}
    onkeydown={(e) => e.key === "Escape" && (showSettings = false)}
    role="button"
    tabindex="-1"
  >
    <div
      class="w-96 max-h-[80vh] overflow-y-auto pretty-scroll flex flex-col gap-4 p-4 rounded-lg border border-card-border bg-surface shadow-lg text-[0.8125rem]"
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
      <div class="flex items-center gap-2 text-txtmain">
        <Settings size={16} />
        <span class="text-sm font-medium">Settings</span>
      </div>

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
        {/if}
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

      <div class="flex justify-end">
        <button
          class="px-3 py-1.5 rounded-md text-sm text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
          onclick={() => (showSettings = false)}
        >
          Close
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .tab-hidden {
    display: none;
  }
</style>
