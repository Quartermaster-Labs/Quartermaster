<script lang="ts">
  import { cssZoom } from "../../lib/uiZoom";
  import { tip as tooltip } from "../../lib/tooltip";
  import { get } from "svelte/store";
  import { models, backendMetrics, loadModel } from "../../stores/api";
  import { summarizeConversation, generateTitle, COMPACT_AT, KEEP_RECENT } from "../../lib/chatCompact";
  import {
    selectedModelStore,
    selectedTabStore,
    activeSystemPresetStore,
    systemPresetsStore,
    temperatureStore,
    maxTokensStore,
    reasoningBudgetStore,
    webSearchStore,
    qmToolsStore,
    reasoningEffortStore,
    searxngUrlStore,
    searchProvidersStore,
    searchMaxPerTurnStore,
    searchThrottleMsStore,
    searchDedupeStore,
    rewriteStore,
    rewriteInstructionStore,
    shoppingStore,
    shoppingPrefsStore,
    extraToolsStore,
    memoryStore,
  } from "../../stores/playground";
  import { memories, loadMemories } from "../../stores/memories";
  import {
    chatSessions,
    activeChatId,
    generatingChatId,
    saveChatsNow,
    newChatId,
    deriveTitle,
    type ChatSession,
  } from "../../stores/chatHistory";
  import { WEB_SEARCH_TOOL, normalizeProviders, providerReady } from "../../lib/webSearch";
  import { WIKI_TOOL } from "../../lib/wiki";
  import { QM_INSPECT_TOOL, QM_CONFIGURE_TOOL } from "../../lib/qmTools";
  import { YOUTUBE_TOOL, YOUTUBE_SEARCH_TOOL, YOUTUBE_COMMENTS_TOOL } from "../../lib/youtube";
  import { FETCH_PAGE_TOOL } from "../../lib/fetchPage";
  import { CONVERT_CURRENCY_TOOL } from "../../lib/currency";
  import { ALWAYS_TOOLS, EXTRA_TOOLS } from "../../lib/assistantTools";
  import { MEMORY_TOOLS, memoryBlock } from "../../lib/memoryTools";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { getTextContent, getImageUrls } from "../../lib/types";
  import { buildBasePrompt } from "../../lib/systemPrompt";
  import type { ChatMessage, ContentPart } from "../../lib/types";
  import { Paperclip, MessagesSquare, X, Search, Brain, Clock, PenLine, Sparkles, HelpCircle, Wrench, Reply, Quote, ShoppingCart, CloudSun, BrainCircuit, FileText, Loader2, AlertTriangle } from "lucide-svelte";
  import {
    acceptAttr,
    buildFileBlock,
    classifyAttachment,
    estimateTokens,
    extractDocxText,
    extractPdfText,
    oversizeError,
    pickTranscribeModel,
    readTextFile,
    MAX_DOCS_PER_MESSAGE,
    MAX_DOC_BYTES,
    type AttachedDoc,
  } from "../../lib/attachments";
  import { transcribeAudio } from "../../lib/audioApi";
  import ChatMessageComponent from "./ChatMessage.svelte";
  import Composer from "./Composer.svelte";
  import ToolMenu from "./ToolMenu.svelte";
  import { modelCategory } from "../../lib/modelUtils";
  import { EFFORT_OFF, effortOptions, resolveEffort, requestEffort } from "../../lib/effort";
  import { scrollFade } from "../../lib/scrollFade";
  import Select from "../Select.svelte";
  import Toggle from "../Toggle.svelte";
  import { quotePrefix, fmtTokens, TEMP_STEPS, TEMP_LABELS, nearestTempIdx, currentDateLine, REWRITE_SYSTEM, MAX_IMAGES_PER_MESSAGE, validateImageFile, fileToDataUrl, type ToolItem } from "./chatHelpers";

  // Modes are per-conversation: switching chats drops back to a plain chat rather
  // than silently carrying rewrite/shopping into the new one. (They already reset
  // on reload — the stores are session-local, not persisted.)
  let lastModeChatId = "";
  $effect(() => {
    const id = $activeChatId;
    if (id === lastModeChatId) return;
    lastModeChatId = id;
    rewriteStore.set(false);
    shoppingStore.set(false);
  });

  // Composer tool menu: agent MODES only — each one rewires how the assistant
  // answers, and the menu button wears the active mode's icon. Plain capability
  // flags (reasoning, web search, qm tools) belong in the Configs popover.
  const toolMenuItems = $derived<ToolItem[]>([
    {
      key: "rewrite",
      label: "Rewrite",
      description: "Edit pasted text to an instruction, side by side with the original.",
      icon: PenLine,
      active: $rewriteStore,
      onToggle: () => {
        const on = !$rewriteStore;
        // Modes are exclusive: rewrite is a single-shot text edit with no tool
        // loop, so it can't co-exist with the shopping assistant's research.
        if (on) shoppingStore.set(false);
        rewriteStore.set(on);
        showToast(on ? "Rewrite mode on" : "Rewrite mode off");
      },
    },
    {
      key: "shopping",
      label: "Shopping assistant",
      description: "Buying help: pins down the brief, reads real shop pages, returns a compared shortlist.",
      icon: ShoppingCart,
      active: $shoppingStore,
      onToggle: () => {
        const on = !$shoppingStore;
        if (on) rewriteStore.set(false);
        shoppingStore.set(on);
        showToast(on ? "Shopping assistant on" : "Shopping assistant off");
      },
    },
  ]);

  // Ensure a valid active conversation exists, migrating the legacy single-chat
  // store the first time. The store (chatSessions) is the single source of truth
  // for messages — the working view below is derived from it, and generation
  // writes back into it by session id, so a turn survives switching chats.
  function initChats() {
    let sessions = get(chatSessions);
    if (sessions.length === 0) {
      try {
        const legacy = localStorage.getItem("playground-messages");
        const msgs = legacy ? JSON.parse(legacy) : [];
        if (Array.isArray(msgs) && msgs.length > 0) {
          const s: ChatSession = { id: newChatId(), title: deriveTitle(msgs), messages: msgs, updatedAt: Date.now() };
          sessions = [s];
          chatSessions.set(sessions);
          activeChatId.set(s.id);
        }
        localStorage.removeItem("playground-messages");
      } catch {}
    }
    let id = get(activeChatId);
    if (!sessions.some((s) => s.id === id)) {
      // Persisted active chat is gone (or first run) → open the most recently
      // used one, matching the sidebar's top-of-list, not raw array order.
      const recent = sessions.reduce<ChatSession | null>(
        (best, s) => (!best || s.updatedAt > best.updatedAt ? s : best),
        null,
      );
      id = recent ? recent.id : "";
      if (!id) {
        const s: ChatSession = { id: newChatId(), title: "New chat", messages: [], updatedAt: Date.now() };
        chatSessions.set([s]);
        id = s.id;
      }
      activeChatId.set(id);
    }
  }
  initChats();

  // Session the generation loop is writing to (null = idle). One turn at a time;
  // it keeps running when the user switches to another chat. Mirrored to the
  // store so the rail can flag the generating row.
  let genId = $state<string | null>(null);
  $effect(() => {
    generatingChatId.set(genId);
  });

  // Reconnect to a turn the server is still running (tab was closed mid-answer).
  attachIfGenerating();

  // --- store helpers: messages live in chatSessions, keyed by session id ---
  function sessionById(id: string): ChatSession | undefined {
    return get(chatSessions).find((s) => s.id === id);
  }
  // Patch one session. `bump` stamps updatedAt so the rail sorts the row to the
  // top — only on real message activity (send / regenerate), NOT on streaming
  // chunks, title or metadata writes, so a chat never jumps while you read it.
  function patchSession(id: string, fields: Partial<ChatSession>, bump = false) {
    chatSessions.update((ss) => {
      const i = ss.findIndex((s) => s.id === id);
      if (i === -1) return ss; // session deleted — don't resurrect it
      const copy = [...ss];
      copy[i] = { ...copy[i], ...fields, ...(bump ? { updatedAt: Date.now() } : {}) };
      return copy;
    });
  }
  function appendMessage(id: string, msg: ChatMessage) {
    const cur = sessionById(id);
    if (!cur) return;
    const title = cur.titled ? cur.title : deriveTitle([...cur.messages, msg]);
    patchSession(id, { messages: [...cur.messages, msg], title }, true);
  }
  // Patch the streaming last bubble. No updatedAt bump (chunk-rate writes).
  function patchLast(id: string, fn: (m: ChatMessage) => ChatMessage) {
    const cur = sessionById(id);
    if (!cur) return;
    patchSession(id, { messages: cur.messages.map((m, i, a) => (i === a.length - 1 ? fn(m) : m)) });
  }

  // The active conversation, derived from the store (single source of truth).
  let activeSession = $derived($chatSessions.find((s) => s.id === $activeChatId));
  let messages = $derived(activeSession?.messages ?? []);
  let compactedCount = $derived(activeSession?.compactedCount ?? 0);
  // Id of the chat currently being compacted, or "" — an id rather than a bool
  // so the banner shows for a manual /compact too, which has no turn (and so no
  // genId) to hang off.
  let compactingId = $state("");
  // Why the last compaction failed, for the manual toast. A bare "Compaction
  // failed" sent the user hunting through the browser console for what is
  // usually one sentence (model returned 503, produced only reasoning, ...).
  let compactError = "";

  // Live context-window usage for the selected model (from backend KV metrics).
  // The bar fills with kv_cache_usage_ratio; colour steps yellow → orange → red
  // as it nears COMPACT_AT (the auto-compaction threshold).
  let ctxMetrics = $derived($backendMetrics[$selectedModelStore]);
  let ctxN = $derived(ctxMetrics?.n_ctx ?? 0);
  let ctxUsed = $derived(ctxMetrics?.kv_cache_tokens ?? 0);
  let ctxRatio = $derived(ctxN ? Math.min(1, ctxMetrics!.kv_cache_usage_ratio) : 0);
  let ctxColor = $derived(
    ctxRatio >= COMPACT_AT ? "#ef4444" : ctxRatio >= 0.6 ? "#f97316" : "#eab308",
  );

  let userInput = $state("");
  // Messages typed while a turn is streaming; sent one-by-one once it finishes.
  let queued = $state<string[]>([]);

  // Reply target: a past assistant message the next send quotes. The whole
  // conversation is already resent to the model, so the quote is just a short
  // pointer telling it WHICH message the user is answering. Cleared on send or
  // chat switch.
  let replyingTo = $state<string | null>(null);
  function replyTo(idx: number) {
    replyingTo = getTextContent(messages[idx].content).replace(/\s+/g, " ").trim();
  }
  $effect(() => {
    $activeChatId;
    replyingTo = null;
  });

  // Highlight-to-reply: selecting text inside an assistant bubble pops a small
  // Reply button anchored at the selection, which quotes exactly that span.
  let selReply = $state<{ text: string; x: number; y: number } | null>(null);
  function onSelection() {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed) return; // keep popup on button mousedown collapse
    const text = sel.toString().replace(/\s+/g, " ").trim();
    if (!text) {
      selReply = null;
      return;
    }
    // Both ends must sit inside the same assistant message.
    const owner = (n: Node | null) =>
      (n instanceof Element ? n : n?.parentElement)?.closest('[data-role="assistant"]') ?? null;
    const a = owner(sel.anchorNode);
    if (!a || a !== owner(sel.focusNode)) {
      selReply = null;
      return;
    }
    // getBoundingClientRect spans the whole selection (rightmost = widest line);
    // the LAST client rect is the end of the final selected line — anchor there so
    // the button lands by the cursor, not the far edge of the paragraph.
    const range = sel.getRangeAt(0);
    const rects = range.getClientRects();
    const rect = rects[rects.length - 1] ?? range.getBoundingClientRect();
    // The button is `fixed`, so its coordinates are local pixels that the
    // interface zoom scales again - convert out of the rect's visual ones.
    const z = cssZoom(document.body);
    selReply = { text, x: rect.right / z, y: rect.bottom / z };
  }
  function replyToSelection() {
    if (!selReply) return;
    replyingTo = selReply.text;
    selReply = null;
    window.getSelection()?.removeAllRanges();
    inputEl?.focus();
  }

  // Transient toast shown just above the composer (e.g. toggling reasoning/search).
  let toast = $state("");
  let toastTimer: ReturnType<typeof setTimeout> | undefined;
  function showToast(msg: string) {
    toast = msg;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toast = ""), 1500);
  }
  let isStreaming = $derived(genId !== null);
  let isReasoning = $state(false);
  // What the server says the turn is doing right now ("Searching for …"). A tool
  // call only produces a search card once it FINISHES, so this is the only signal
  // during the seconds it runs. Server-driven: the turn loop owns the tools.
  let busyLabel = $state("");
  let isSearching = $derived(busyLabel !== "");
  let reasoningStartTime = $state<number>(0);
  let abortController = $state<AbortController | null>(null);
  let messagesContainer: HTMLDivElement | undefined = $state();
  // Inner content wrapper — the thing that actually changes height (a collapsed
  // tool box, a streamed token); the scroll container itself never resizes.
  let messagesInner: HTMLDivElement | undefined = $state();
  let inputEl: HTMLTextAreaElement | undefined = $state();
  let rewriteFocused = $state(false);
  let rewriteEl: HTMLTextAreaElement | undefined = $state();
  let showSettings = $state(false);
  // Per-chat instructions editor: edit a draft, commit to the active session on Save.
  let showSysPrompt = $state(false);
  let sysPromptDraft = $state("");
  function openSysPrompt() {
    sysPromptDraft = sessionById($activeChatId)?.instructions ?? "";
    showSysPrompt = true;
  }
  function saveSysPrompt() {
    patchSession($activeChatId, { instructions: sysPromptDraft });
    showSysPrompt = false;
  }
  let attachedImages = $state<string[]>([]);
  // Documents (text/code, PDF, DOCX, transcribed audio). Unlike images these
  // become TEXT in the user's message, so they need no vision model and no
  // server support — see lib/attachments.ts.
  let attachedDocs = $state<AttachedDoc[]>([]);
  let fileInput = $state<HTMLInputElement | null>(null);
  let imageError = $state<string | null>(null);

  let hasModels = $derived($models.some((m) => !m.unlisted));
  // Image input needs a vision-capable model; without one the backend 500s
  // ("image input is not supported"). Two ways a model can take images:
  //  - it natively reports vision (single-file multimodal gguf), or
  //  - it has an mmproj "-vision" twin (same family, unlisted): autogen pairs a
  //    sibling mmproj projector into a vision variant. Attaching swaps to it so
  //    the projector loads, then lets the user pick a file.
  let selectedModel = $derived($models.find((m) => m.id === $selectedModelStore));
  let selectedModelVision = $derived(selectedModel?.capabilities?.vision ?? false);
  // Reasoning effort: the ladder this model's template accepts (empty = no
  // ladder, so the control is a plain on/off), the resolved pick for it, and
  // what that means on the wire. See lib/effort.ts.
  let effortLadder = $derived(selectedModel?.capabilities?.reasoning_effort ?? []);
  let effortChoices = $derived(effortOptions(effortLadder));
  let effort = $derived(resolveEffort($reasoningEffortStore, effortLadder));
  // The vision twin shares the base model's family (= same gguf path) and is the
  // sibling that carries vision caps. Family is precise here (one gguf → one
  // family); caps.vision disambiguates from ctx-tier siblings on the same gguf.
  let visionTwin = $derived(
    selectedModelVision
      ? undefined
      : $models.find(
          (m) =>
            !!m.family &&
            m.family === selectedModel?.family &&
            m.id !== selectedModel?.id &&
            m.capabilities?.vision
        )
  );
  let canAttach = $derived(selectedModelVision || !!visionTwin);
  // Budget context for document attachments: the live n_ctx of the loaded
  // backend when there is one, else the model's configured -c from the catalog —
  // files are normally picked before the model has ever been loaded.
  let docCtx = $derived(ctxN > 0 ? ctxN : (selectedModel?.ctx ?? 0));
  let docTokens = $derived(attachedDocs.reduce((n, d) => n + d.tokens, 0));

  // The paperclip is never dead: documents are read into text client-side, so
  // any model can take one. Only IMAGES need vision, and that is decided when a
  // file is actually picked (swapToVision), not when the picker opens — opening
  // it used to swap the model even if the user then chose a PDF.
  function openAttach() {
    fileInput?.click();
  }

  // Attaching an image IS the vision switch: swap to the twin (loading its mmproj
  // projector). Going back to the text-only build is the model picker's job — no
  // separate vision toggle.
  function swapToVision() {
    if (!visionTwin) return;
    selectedModelStore.set(visionTwin.id);
    void loadModel(visionTwin.id).catch(() => {});
    showToast("Switched to the vision variant");
  }
  // Loaded → an empty stream means the model is generating, not swapping in.
  // Use the authoritative catalog state (what the dashboard shows), not just the
  // presence of a metrics row — a stale/early metrics entry made this read true
  // while the model was still loading, mislabeling the wait as "Processing image".
  let modelReady = $derived(
    ($models.find((m) => m.id === $selectedModelStore)?.state) === "ready"
  );
  let userScrolledUp = $state(false);

  // Keep a valid model selected so the composer is never stuck disabled.
  // Only correct the selection when it isn't a listed model; otherwise leave it
  // alone — the chosen model is what loads (swaps in) on the next message, even
  // if it isn't currently loaded. Don't snap to whatever happens to be ready.
  $effect(() => {
    // Only chat-capable models (peers stay; drop non-llm + rerankers) so the
    // default never lands on an image/audio model. Mirrors ModelSelector's filter.
    const chatable = $models.filter(
      (m) => m.peerID || (modelCategory(m) === "llm" && !m.capabilities?.reranker)
    );
    // Default picks from listed models only; but an already-selected unlisted
    // chat model (e.g. the vision twin we just swapped to via attach) is valid —
    // don't snap away from it.
    const listed = chatable.filter((m) => !m.unlisted);
    if (listed.length === 0) return;
    if (!chatable.some((m) => m.id === $selectedModelStore)) {
      const ready = listed.find((m) => m.state === "ready");
      selectedModelStore.set((ready ?? listed[0]).id);
    }
  });

  // Per-chat model memory. Switching conversations re-selects the model that chat
  // last ran on, so a thread stays on the model it was built with. Only fires on an
  // actual chat switch (guarded by lastAppliedChat) — inside a chat the user's own
  // pick wins, and rememberModel() writes it back. A chat with no recorded model
  // (fresh, or created before this existed) keeps the current selection.
  let lastAppliedChat = "";
  $effect(() => {
    const id = $activeChatId;
    if (id === lastAppliedChat) return;
    lastAppliedChat = id;
    const want = get(chatSessions).find((s) => s.id === id)?.model;
    // Don't validate against $models here: the catalog may not have arrived yet,
    // and the default-correction effect above already snaps away from a model that
    // turns out to be gone.
    if (want && want !== get(selectedModelStore)) selectedModelStore.set(want);
  });

  // Bind the current model to the open chat. Called on an explicit pick in the
  // composer's selector and at the start of every turn (which also captures the
  // vision-twin swap and any model the user changed to mid-thread).
  function rememberModel(modelId: string) {
    const id = $activeChatId;
    if (!modelId || !id) return;
    lastAppliedChat = id; // this selection IS the chat's model — don't re-apply over it
    if (sessionById(id)?.model !== modelId) patchSession(id, { model: modelId });
  }

  // Warm-start the selected model while the user is still typing, so the first
  // token doesn't wait on a cold load/swap. Fires once per model, ~500 ms after the
  // composer stops being empty. A ready/starting model is skipped; the request is
  // the same idempotent GET /upstream/<id>/ the dashboard's load button uses.
  let preloadedModel = "";
  $effect(() => {
    const typing = userInput.trim().length > 0;
    const id = $selectedModelStore;
    const state = $models.find((m) => m.id === id)?.state;
    if (!typing || !id || genId !== null || preloadedModel === id) return;
    if (state === "ready" || state === "starting") return;
    const t = setTimeout(() => {
      preloadedModel = id;
      void loadModel(id).catch(() => {
        preloadedModel = ""; // failed load → let the next keystroke retry
      });
    }, 500);
    return () => clearTimeout(t);
  });

  $effect(() => {
    playgroundStores.chatStreaming.set(isStreaming);
  });

  // Drop pending attachments if the user switches to a model that can't take
  // images at all (no native vision and no vision twin), so a stale image can't
  // be sent into a backend that will reject it.
  $effect(() => {
    if (!canAttach && attachedImages.length > 0) {
      attachedImages = [];
      imageError = null;
    }
  });

  // Auto-grow the composer textarea by content only — sized on focus/blur made
  // it visibly jump on click even with no text, which read as a layout glitch.
  // Tracks userInput so it also shrinks back after a send clears the value.
  // Guard scrollHeight === 0 (tab is display:none at mount) — otherwise the
  // height locks at 0px and never recovers when the tab is shown, leaving an
  // invisible, untypeable textarea. CSS min-h-[3rem] on the textarea keeps it
  // from ever measuring smaller than the resting size.
  $effect(() => {
    userInput;
    $selectedTabStore; // re-run when this tab becomes visible again
    if (inputEl) {
      inputEl.style.height = "auto";
      if (inputEl.scrollHeight > 0) {
        inputEl.style.height = Math.min(inputEl.scrollHeight, 480) + "px";
      }
    }
  });

  // Auto-grow the rewrite-instruction bar the same way (rewrite mode only),
  // collapsing back to one row when the user clicks away.
  $effect(() => {
    $rewriteInstructionStore;
    rewriteFocused;
    $selectedTabStore;
    if (rewriteEl) {
      if (!rewriteFocused) {
        rewriteEl.style.height = "1.5rem";
        return;
      }
      rewriteEl.style.height = "auto";
      if (rewriteEl.scrollHeight > 0) {
        rewriteEl.style.height = Math.min(rewriteEl.scrollHeight, 240) + "px";
      }
    }
  });

  // Every scroll event just re-derives "is the view at the bottom" from the live
  // position — no flag marking our own programmatic scrolls. A one-shot flag was
  // worse than nothing here: assigning scrollTop to where it already is fires NO
  // event, so the flag leaked and swallowed the user's next real scroll. Since
  // the pin below is instant (never smooth), there are no intermediate positions
  // to misread: after a pin we are at the bottom, which is exactly what the
  // recomputation reports.
  function handleMessagesScroll() {
    selReply = null; // rects go stale once the list scrolls
    if (!messagesContainer) return;
    const { scrollTop, scrollHeight, clientHeight } = messagesContainer;
    // "At bottom" only means actually at the bottom — a small tolerance absorbs
    // sub-pixel rounding. A wider band (this was 40px) read as a rubber band:
    // the user scrolled up a little, was still inside the band, and the next
    // streamed token snapped them hard to the end. 8px keeps follow-while-at-
    // bottom stable without ever yanking a reader back down.
    userScrolledUp = scrollHeight - scrollTop - clientHeight > 8;
  }

  // Pin the view to the newest content unless the user has scrolled away.
  function pinToBottom() {
    if (!messagesContainer || userScrolledUp) return;
    messagesContainer.scrollTop = messagesContainer.scrollHeight;
  }

  // Auto-scroll when messages change — skip if user scrolled up
  $effect(() => {
    if (messages.length > 0 && messagesContainer && !userScrolledUp) pinToBottom();
  });

  // Re-pin on content height changes only while this chat's assistant is
  // generating: a streamed token grows the content, and without the pin the
  // reply scrolls out of view. Same for a reasoning/tool box toggled mid-stream
  // — it resizes the list without touching `messages`, and with
  // `overflow-anchor: none` on the container the pin is the only way back down.
  // Outside a stream the list is static and height changes (late image loads,
  // lazy mermaid/katex renders, a box the user toggles) must not yank a reader
  // sitting at the old bottom to a moved one: the user owns the scroll
  // position then.
  $effect(() => {
    if (!messagesInner) return;
    const ro = new ResizeObserver(() => {
      if (genId === $activeChatId) pinToBottom();
    });
    ro.observe(messagesInner);
    return () => ro.disconnect();
  });

  // Follow the bottom again when switching conversations.
  $effect(() => {
    $activeChatId;
    userScrolledUp = false;
  });

  // Rewrite mode: prose (userInput) + a "how to help" instruction. The user
  // message carries the instruction; content is the original prose so the
  // assistant turn can render a side-by-side diff against its rewrite.
  async function sendRewrite() {
    const prose = userInput.trim();
    if (!prose || !$selectedModelStore || isStreaming) return;
    const id = $activeChatId;
    userScrolledUp = false;
    appendMessage(id, { role: "user", content: prose, rewriteInstruction: $rewriteInstructionStore.trim() });
    userInput = "";
    await regenerateFromIndex(id, sessionById(id)!.messages.length - 1);
  }

  async function sendMessage() {
    // Composer command, not a message: fold the older turns into the summary on
    // demand. Only an exact "/compact" with nothing attached is intercepted — a
    // message that merely starts with a slash is still a message. Ahead of the
    // rewrite branch, so the command works in rewrite mode too.
    if (/^\/compact$/i.test(userInput.trim()) && attachedImages.length === 0 && attachedDocs.length === 0) {
      userInput = "";
      await runManualCompact($activeChatId);
      return;
    }
    if ($rewriteStore) {
      await sendRewrite();
      return;
    }
    const trimmedInput = userInput.trim();
    const readyDocs = attachedDocs.filter((d) => d.status === "ready");
    if ((!trimmedInput && attachedImages.length === 0 && readyDocs.length === 0) || !$selectedModelStore) return;
    // Don't send half of an attachment set: a file still extracting would silently
    // be dropped from the message the model answers.
    if (attachedDocs.some((d) => d.status === "loading")) {
      showToast("Still reading the attached file");
      return;
    }
    const id = $activeChatId;

    // A turn is in flight. If it's THIS chat's turn, queue the text and send it
    // once the turn drains. If it's another chat generating in the background,
    // the backend only serves one turn at a time — tell the user to wait.
    if (isStreaming) {
      if (genId === id) {
        if (trimmedInput) {
          queued = [...queued, trimmedInput];
          userInput = "";
        }
      } else {
        showToast("Wait for the current response to finish");
      }
      return;
    }

    userScrolledUp = false;

    // Reply to a past message → prepend a quote snippet so the model knows which
    // one this answers (the full thread is already resent as history).
    const quoted = replyingTo ? quotePrefix(replyingTo) + trimmedInput : trimmedInput;
    replyingTo = null;

    // Documents are inlined as <file> blocks AHEAD of the user's own words: the
    // model reads the material first, and the transcript keeps the full text so
    // history, replay and compaction all see exactly what the model saw. The UI
    // parses the blocks back out (ChatMessage) and shows chips, not a wall of
    // text.
    const blocks = readyDocs.map((d) => buildFileBlock(d.name, d.text, d.note)).join("\n\n");
    const text = blocks ? (quoted ? `${blocks}\n\n${quoted}` : blocks) : quoted;

    // Build message content (multimodal if images attached)
    let content: string | ContentPart[];
    if (attachedImages.length > 0) {
      const parts: ContentPart[] = [];
      if (text) {
        parts.push({ type: "text", text });
      }
      for (const url of attachedImages) {
        parts.push({ type: "image_url", image_url: { url } });
      }
      content = parts;
    } else {
      content = text;
    }

    appendMessage(id, { role: "user", content });
    userInput = "";
    attachedImages = [];
    attachedDocs = [];
    imageError = null;

    await regenerateFromIndex(id, sessionById(id)!.messages.length - 1);
  }

  // Stop. Aborting the local fetch only detaches this VIEWER — the turn runs
  // server-side (that is the whole point: a closed tab doesn't kill it), so the
  // backend kept generating and the next send came back 409 "already
  // generating". Tell the server too. Abort first so the UI stops instantly,
  // then cancel the turn; the DELETE carries no signal of its own so the abort
  // above cannot cancel the cancel.
  async function cancelStreaming() {
    const id = genId ?? $activeChatId;
    abortController?.abort();
    if (!id) return;
    try {
      await fetch(`/api/chats/turn?chatId=${encodeURIComponent(id)}`, { method: "DELETE" });
    } catch {
      showToast("Could not stop the generation on the server");
    }
  }

  // Accept/deny a pending qm config change — unblocks the server-side turn, which
  // then applies (or skips) the change and continues generating.
  async function respondApproval(id: string, accept: boolean) {
    const chatId = $activeChatId;
    // Dismiss the card on click, don't wait for the server's resolved delta —
    // that delta is best-effort (dropped for a slow/re-attached subscriber) and
    // a card that outlives the decision still offers buttons for a change the
    // server already applied. The outcome shows up in the tool step instead.
    patchLast(chatId, (m) => (m.approval?.id === id ? { ...m, approval: { ...m.approval, status: accept ? "applied" : "denied" } } : m));
    try {
      await fetch("/api/chats/turn/approve", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ chatId, id, accept }),
      });
    } catch {
      showToast("Could not send the decision");
    }
  }

  // Web search runs as OpenAI tool-calling, which only the chat/completions
  // endpoint speaks. Other endpoints just chat without the tool.

  // Built-in system prompt prepended ahead of the user's own. Grounds the model
  // with basic honesty/tool guidance. Always on. The volatile current-date line is
  // deliberately NOT here — it's appended at the very END of the system block (see
  // currentDateLine) so this prefix stays byte-identical across a midnight rollover
  // and the KV cache isn't invalidated once a day.
  function basePrompt(
    searchAvailable: boolean,
    wikiAvailable: boolean,
    qmAvailable: boolean,
    youtubeAvailable: boolean,
    modelId: string,
    fetchAvailable = false,
    shoppingPrefs: string | false = false,
    assistantAvailable = false,
    extrasAvailable = false,
    memoryAvailable = false,
  ): string {
    // Persona + tool sub-prompts, all from the active system-prompt selection
    // (built-in default, a named preset, or none). A preset bundles its own tool
    // prompts; fixed options use the shipped defaults. Tool directives are only
    // appended when the matching tool is on, so the choice never breaks them.
    return buildBasePrompt(get(activeSystemPresetStore), get(systemPresetsStore), {
      search: searchAvailable,
      wiki: wikiAvailable,
      qm: qmAvailable,
      youtube: youtubeAvailable,
      fetch: fetchAvailable,
      shopping: shoppingPrefs,
      assistant: assistantAvailable,
      extras: extrasAvailable,
      memory: memoryAvailable,
      // The chat pane renders ```mermaid / ```chart blocks (lib/diagrams.ts), so
      // the model is always told it can draw. Rewrite mode never gets here — it
      // ships REWRITE_SYSTEM instead.
      diagrams: true,
      model: modelId,
    });
  }


  async function regenerateFromIndex(id: string, idx: number) {
    // Only one turn in flight at a time. Without this, the "Generate response"
    // pill / edit-save (which call here directly, bypassing send()'s isStreaming
    // guard) could fire while a turn is already streaming — overwriting genId and
    // abortController while the old stream keeps running on its captured signal,
    // so two requests hit the backend at once (slot collision, non-consecutive
    // token positions). Refuse instead.
    if (genId !== null) {
      showToast("Wait for the current response to finish");
      return;
    }
    // Capture the model now — the user may switch chats/models while this turn
    // streams in the background; the turn must stay on the model it started on.
    const modelId = $selectedModelStore;
    if (!modelId || !sessionById(id)) return;
    // The chat now belongs to this model — reopening it re-selects it.
    rememberModel(modelId);

    // Editing/regenerating inside the already-summarized region would make the
    // summary describe messages that no longer match — drop compaction and resend
    // the full history from the start in that case.
    let curSummary = sessionById(id)!.summary ?? "";
    let curCompacted = sessionById(id)!.compactedCount ?? 0;
    if (idx < curCompacted) {
      curSummary = "";
      curCompacted = 0;
      patchSession(id, { summary: undefined, compactedCount: undefined });
    }
    // Remove all messages after the edited user message
    patchSession(id, { messages: sessionById(id)!.messages.slice(0, idx + 1) });

    // Rewrite turn? The user message at idx carries the rewrite instruction;
    // its content is the original prose to diff the model's output against.
    const reqUser = sessionById(id)!.messages[idx];
    const rwInstr = reqUser?.rewriteInstruction;
    const isRewrite = typeof rwInstr === "string";
    const original = isRewrite ? getTextContent(reqUser.content) : "";

    genId = id;
    isReasoning = false;
    busyLabel = "";
    reasoningStartTime = 0;
    abortController = new AbortController();
    const signal = abortController.signal;

    // No tools during a rewrite — it's a self-contained text transform.
    // Web search is opt-in (needs SearXNG); the local help wiki is always on so
    // models can answer quartermaster questions.
    const webEnabled = !isRewrite && $webSearchStore;
    const wikiEnabled = !isRewrite;
    // Quartermaster tools (inspect/configure the running instance). Default on.
    const qmEnabled = !isRewrite && $qmToolsStore;
    // Transcript fetching is always available: it needs no config of its own
    // (yt-dlp is resolved server-side) and only fires when the user actually
    // brings up a video. A missing yt-dlp comes back as a clear tool error.
    const ytEnabled = !isRewrite;
    // Reading a page pairs with searching: search finds the URL, fetch_page reads
    // the real thing off it. On its own (no search) the model has no way to find
    // a URL, so it rides the same toggle. Shopping mode needs it outright — a
    // price from a snippet is not a price.
    const fetchEnabled = !isRewrite && (webEnabled || $shoppingStore);
    // Clock / calculator / unit converter: local, instant, and correcting a
    // failure mode (confident wrong dates and arithmetic) that shows up in
    // ordinary conversation, so they ride along with the wiki as always-on.
    const assistantEnabled = !isRewrite;
    // Weather + feeds hit the network and only matter to some chats — own toggle.
    const extrasEnabled = !isRewrite && $extraToolsStore;
    // Cross-conversation memory. Opt-in: it writes a store the user has to
    // police, and the remembered facts sit in the system prompt of every chat.
    const memoryEnabled = !isRewrite && $memoryStore;
    // Shopping mode: staged buying helper. Carries the standing prefs line.
    const shoppingPrefs: string | false = !isRewrite && $shoppingStore ? $shoppingPrefsStore : false;
    // Stable per-turn tool set the client advertises; the server dispatches them
    // and enforces the per-turn caps (wiki lookups, web-search rate limits).
    const turnTools = [
      ...(webEnabled ? [WEB_SEARCH_TOOL] : []),
      ...(fetchEnabled ? [FETCH_PAGE_TOOL] : []),
      // Shopping only: outside it, converting money is a rare aside a search can
      // answer, and every advertised tool is prefix the KV cache has to carry.
      ...(!isRewrite && $shoppingStore ? [CONVERT_CURRENCY_TOOL] : []),
      ...(assistantEnabled ? ALWAYS_TOOLS : []),
      ...(extrasEnabled ? EXTRA_TOOLS : []),
      ...(memoryEnabled ? MEMORY_TOOLS : []),
      ...(wikiEnabled ? [WIKI_TOOL] : []),
      ...(qmEnabled ? [QM_INSPECT_TOOL, QM_CONFIGURE_TOOL] : []),
      ...(ytEnabled ? [YOUTUBE_TOOL, YOUTUBE_SEARCH_TOOL, YOUTUBE_COMMENTS_TOOL] : []),
    ];

    // Thinking budget: soft cumulative-thinking cap so models can't loop forever
    // before answering. 0 = off. Enforced server-side at round boundaries — once
    // total thinking passes the budget, thinking is turned off for later rounds
    // (never a mid-generation hard close, which derails a tool-using model
    // mid-search). Rewrites think too: the transform is the hard part of the turn
    // (tone, register, what to keep), and a no-reasoning rewrite is visibly worse.
    const reasoningBudget = $reasoningBudgetStore;
    // One assistant bubble holds the whole turn: reasoning, any web searches
    // (as collapsible sections), and the final reply. The server writes into
    // this bubble (last message) as it streams; the tool plumbing it sends to
    // the model stays server-side and is never shown here.
    appendMessage(id, { role: "assistant", content: "", model: modelId, ...(isRewrite ? { rewriteOriginal: original } : {}) });
    const genStart = Date.now();

    const sys = [
      basePrompt(webEnabled, wikiEnabled, qmEnabled, ytEnabled, modelId, fetchEnabled, shoppingPrefs, assistantEnabled, extrasEnabled, memoryEnabled),
      sessionById(id)?.instructions?.trim(),
      // The remembered facts themselves. Below the persona so a memory can never
      // displace the instructions, and above the volatile date line. Every save
      // changes this block and so invalidates the KV prefix of every chat — the
      // price of recall the model does not have to ask for.
      memoryEnabled && memoryBlock($memories),
      curSummary && `Summary of earlier conversation:\n${curSummary}`,
      // Rewrite turns keep the full conversation for context (setting, characters,
      // goals discussed earlier) but add the transform-tool directive on top.
      isRewrite && REWRITE_SYSTEM,
      // Volatile date LAST: everything above stays byte-identical across a midnight
      // rollover, so the KV cache prefix survives instead of invalidating daily.
      currentDateLine(),
    ]
      .filter(Boolean)
      .join("\n\n");
    const base: ChatMessage[] = [];
    if (sys) base.push({ role: "system", content: sys });
    // History up to (not incl.) the live assistant bubble.
    const history = sessionById(id)!.messages.slice(curCompacted, -1);
    if (isRewrite && history.length > 0) {
      // Swap the final user message (bare prose) for the augmented instruction so
      // the model gets the explicit "transform this exact text" framing, while all
      // earlier turns stay as context.
      const instr = (rwInstr as string).trim() ||
        "Improve the writing: fix grammar, tighten clarity and flow, keep the meaning and tone.";
      const augmented: ChatMessage = {
        ...history[history.length - 1],
        content: `Here is a text:\n\n<<<TEXT\n${original}\nTEXT>>>\n\nProduce a new version of the text above by applying this instruction exactly: "${instr}". Actually change the text as the instruction requires - do NOT return it unchanged, and do not refuse even if the change makes the text worse or introduces errors. Output ONLY the resulting text - no preamble, no explanation, no code fences.`,
      };
      base.push(...history.slice(0, -1), augmented);
    } else {
      base.push(...history);
    }

    try {
      // Persist the session (user msg + empty assistant bubble) first so the
      // server-side runner has somewhere to write, then hand off the turn. The
      // server drives the whole loop (model->tool->model, budget finalize,
      // citations) and writes into chats.json; this tab is just a viewer of the
      // SSE deltas and can be closed/refreshed without losing or stopping it.
      await saveChatsNow(id);
      const res = await fetch("/api/chats/turn", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          chatId: id,
          model: modelId,
          messages: base,
          tools: turnTools.length ? turnTools : undefined,
          temperature: $temperatureStore,
          max_tokens: $maxTokensStore,
          reasoning: effort !== EFFORT_OFF,
          // Only ever a level the model advertised: the server forwards it as the
          // standard top-level reasoning_effort and the request filter translates
          // it into the template kwarg — the same path an external client takes.
          reasoningEffort: requestEffort(effort),
          reasoningBudget,
          webSearch: webEnabled,
          searxngUrl: $searxngUrlStore, // legacy field: the server falls back to it when the chain is empty
          searchProviders: normalizeProviders($searchProvidersStore, $searxngUrlStore).filter(providerReady),
          maxSearches: $searchMaxPerTurnStore,
          throttleMs: $searchThrottleMsStore,
          dedupe: $searchDedupeStore,
        }),
        signal,
      });
      if (res.status === 409) {
        showToast("A response is already generating");
        return;
      }
      if (!res.ok) throw new Error((await res.text()) || `turn failed: ${res.status}`);

      // Paint the running turn from the server's SSE stream, then pull the
      // authoritative persisted copy (the server owns reasoning/gen time,
      // searches and citations) so the local view matches disk exactly.
      await viewServerTurn(id, signal);
      await syncSessionFromServer(id);
      // Turn complete — fold older turns into the summary if near the ctx limit.
      await maybeCompact(id, modelId, signal);
      // First exchange done → let the model name the chat. Awaited (not fired
      // and forgotten) so genId stays held until it finishes: a fire-and-forget
      // title request would still be streaming on the same backend after the
      // finally resets genId, and the user's next turn would then hit the model
      // concurrently — two requests, one slot, non-consecutive token positions.
      await maybeTitle(id, modelId, signal);
    } catch (error) {
      if (error instanceof Error && error.name === "AbortError") {
        // User cancelled, keep partial response
        // If we were still reasoning, record the time
        if (isReasoning && reasoningStartTime > 0) {
          const reasoningTimeMs = Date.now() - reasoningStartTime;
          patchLast(id, (m) => ({ ...m, reasoningTimeMs }));
        }
        // Empty-bubble cleanup happens in finally (covers aborts that end the
        // stream gracefully, with no throw to catch).
      } else {
        // Show error in the assistant message
        const errorMessage = error instanceof Error ? error.message : "An error occurred";
        patchLast(id, (m) => ({ ...m, content: m.content + `\n\n**Error:** ${errorMessage}` }));
      }
    } finally {
      const genTimeMs = Date.now() - genStart;
      patchLast(id, (m) => (m.role === "assistant" ? { ...m, genTimeMs } : m));
      // Cancelled/failed/returned before producing anything → drop the blank
      // assistant turn so the chat never keeps an empty bubble (incl. cancelling
      // during model load, which can end the stream without throwing).
      const last = sessionById(id)?.messages.at(-1);
      if (
        last?.role === "assistant" &&
        typeof last.content === "string" &&
        last.content.trim() === "" &&
        !(last.reasoning_content || "").trim() &&
        !(last.searches && last.searches.length)
      ) {
        patchSession(id, { messages: sessionById(id)!.messages.slice(0, -1) });
      }
      genId = null;
      isReasoning = false;
      busyLabel = "";
      abortController = null;
    }

    // Drain a message queued while this turn was streaming. Recurses through
    // regenerateFromIndex, so a backlog sends sequentially. Skip if the user
    // cancelled (aborted) — leftover queue is theirs to resend or clear.
    if (queued.length > 0 && !signal.aborted) {
      const [next, ...rest] = queued;
      queued = rest;
      appendMessage(id, { role: "user", content: next });
      await regenerateFromIndex(id, sessionById(id)!.messages.length - 1);
    }
  }

  // View a server-side turn: stream its SSE deltas onto the assistant bubble
  // until the server signals done/error. Reconnect-safe — on (re)subscribe the
  // server first replays a full snapshot (replace=true) then live deltas, so a
  // refreshed tab syncs and tails. 204 = that chat is no longer generating (the
  // finished answer already lives in chats.json).
  async function viewServerTurn(id: string, signal: AbortSignal) {
    const res = await fetch(`/api/chats/turn/stream?chatId=${encodeURIComponent(id)}`, { signal });
    if (res.status === 204 || !res.body) return;
    if (!res.ok) throw new Error(`stream failed: ${res.status}`);
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let nl: number;
        while ((nl = buf.indexOf("\n\n")) >= 0) {
          const frame = buf.slice(0, nl);
          buf = buf.slice(nl + 2);
          const line = frame.split("\n").find((l) => l.startsWith("data:"));
          if (!line) continue;
          let d: any;
          try {
            d = JSON.parse(line.slice(5).trim());
          } catch {
            continue;
          }
          applyServerDelta(id, d);
          if (d.kind === "done" || d.kind === "error") return;
        }
      }
    } finally {
      try {
        await reader.cancel();
      } catch {}
    }
  }

  // Apply one SSE delta to the live assistant bubble. replace=true carries a full
  // snapshot (sent once on subscribe); otherwise it's an incremental append.
  function applyServerDelta(id: string, d: any) {
    switch (d.kind) {
      case "reasoning":
        if (!isReasoning) {
          isReasoning = true;
          reasoningStartTime = Date.now();
        }
        patchLast(id, (m) => ({
          ...m,
          reasoning_content: d.replace ? d.text || "" : (m.reasoning_content || "") + (d.text || ""),
        }));
        break;
      case "content":
        if (isReasoning) {
          const reasoningTimeMs = Date.now() - reasoningStartTime;
          isReasoning = false;
          patchLast(id, (m) => ({ ...m, reasoningTimeMs }));
        }
        patchLast(id, (m) => ({
          ...m,
          content: d.replace ? d.text || "" : (m.content || "") + (d.text || ""),
        }));
        break;
      case "thinkMs":
        // Duration of one closed inline <think> span (replace=true carries the
        // whole array as a snapshot on subscribe).
        patchLast(id, (m) => ({
          ...m,
          thinkMs: d.replace ? (d.data ?? []) : [...(m.thinkMs ?? []), d.genMs],
        }));
        break;
      case "titles":
        // Reasoning-box titles from the CPU title model, always a full snapshot
        // (computed once at end of turn).
        patchLast(id, (m) => ({
          ...m,
          reasoningTitle: d.data?.reasoningTitle ?? "",
          thinkTitles: d.data?.thinkTitles ?? [],
        }));
        break;
      case "search": {
        const search = d.data?.search;
        const citations = d.data?.citations;
        if (search) {
          patchLast(id, (m) => ({
            ...m,
            ...(citations ? { citations } : {}),
            searches: [...(m.searches ?? []), search],
          }));
          // The model just wrote to the memory store server-side; re-read it so
          // Settings → Memory (and the next turn's injected block) reflect the
          // change without a page reload.
          if (search.kind === "memory") void loadMemories();
        }
        break;
      }
      case "busy":
        // "" = nothing running; the label is shown verbatim.
        busyLabel = d.text || "";
        break;
      case "approval":
        // A qm config change awaiting accept/deny (or its resolved outcome).
        // Lives on the bubble only for the turn; the post-turn sync drops it.
        if (d.data) patchLast(id, (m) => ({ ...m, approval: d.data }));
        break;
      case "done":
        busyLabel = "";
        if (d.genMs) patchLast(id, (m) => (m.role === "assistant" ? { ...m, genTimeMs: d.genMs } : m));
        break;
      case "error":
        patchLast(id, (m) => ({ ...m, content: (m.content || "") + `\n\n**Error:** ${d.msg || "error"}` }));
        break;
    }
  }

  // Replace one session's messages with the server's authoritative copy after a
  // turn finishes — the server-owned fields (searches, citations, reasoning/gen
  // time, compaction boundary) are the truth; the delta view is best-effort.
  async function syncSessionFromServer(id: string) {
    try {
      const r = await fetch("/api/chats");
      if (!r.ok) return;
      const arr = await r.json();
      const sess = Array.isArray(arr) ? arr.find((s: any) => s.id === id) : null;
      if (sess) patchSession(id, { messages: sess.messages, summary: sess.summary, compactedCount: sess.compactedCount });
    } catch {}
  }

  // On (re)mount, reattach to a turn the server is still running for this user —
  // e.g. the tab was closed/refreshed mid-generation. The turn keeps running
  // server-side regardless; this just resumes viewing + the post-turn steps.
  async function attachIfGenerating() {
    if (genId !== null) return; // this tab already owns a turn
    let id = "";
    try {
      const r = await fetch("/api/chats/turn/state");
      if (!r.ok) return;
      const st = await r.json();
      if (!st.running || !st.chatId) return;
      id = st.chatId;
    } catch {
      return;
    }
    genId = id;
    isReasoning = false;
    busyLabel = "";
    reasoningStartTime = 0;
    abortController = new AbortController();
    const signal = abortController.signal;
    const modelId = $selectedModelStore;
    try {
      await viewServerTurn(id, signal);
      await syncSessionFromServer(id);
      if (!signal.aborted && modelId) {
        await maybeCompact(id, modelId, signal);
        await maybeTitle(id, modelId, signal);
      }
    } catch {
      // ignore — partial answer is persisted server-side
    } finally {
      genId = null;
      isReasoning = false;
      busyLabel = "";
      abortController = null;
    }
  }

  // Name the chat from the opening exchange, once. Best-effort: any failure or
  // empty result leaves the first-message heuristic title in place.
  async function maybeTitle(id: string, modelId: string, signal: AbortSignal) {
    const session = sessionById(id);
    if (!session || session.titled) return;
    if (!session.messages.some((m) => m.role === "assistant" && typeof m.content === "string" && m.content.trim())) return;
    const title = await generateTitle(modelId, session.messages as ChatMessage[], signal);
    if (!title) return;
    patchSession(id, { title, titled: true }); // no bump — a late title shouldn't reorder
  }

  // Auto-compact: once the live server KV usage crosses COMPACT_AT, fold every
  // message up to the last KEEP_RECENT into the running summary by advancing the
  // compaction boundary. The messages stay in the list (UI keeps showing them);
  // they just stop being resent to the model. Best-effort: any failure leaves
  // the conversation untouched.
  async function maybeCompact(id: string, modelId: string, signal: AbortSignal) {
    const bm = get(backendMetrics)[modelId];
    if (!bm || !bm.n_ctx || bm.kv_cache_usage_ratio < COMPACT_AT) return;
    await compactNow(id, modelId, signal);
  }

  // The compaction itself, shared by the automatic path and the manual
  // /compact command. Returns how many messages the boundary moved by (0 =
  // nothing to fold), or -1 if the summary call failed — the manual path needs
  // to tell the user which of the three happened, the auto path ignores it.
  async function compactNow(id: string, modelId: string, signal: AbortSignal): Promise<number> {
    const s = sessionById(id);
    if (!s) return 0;
    const msgs = s.messages;
    const curCompacted = s.compactedCount ?? 0;

    // Snap the boundary forward to a user message so the kept slice starts a
    // clean turn (never an orphaned assistant/tool reply whose tool_calls were
    // summarized away).
    let boundary = msgs.length - KEEP_RECENT;
    while (boundary < msgs.length && msgs[boundary].role !== "user") boundary++;
    if (boundary <= curCompacted) return 0; // nothing new to summarize

    // Summarize only the newly-folded slice; `summary` already covers the prefix.
    const fresh = msgs.slice(curCompacted, boundary);
    compactingId = id;
    try {
      const next = await summarizeConversation(modelId, fresh, s.summary ?? "", signal);
      if (signal.aborted) return 0;
      patchSession(id, { summary: next, compactedCount: boundary });
      return boundary - curCompacted;
    } catch (e) {
      if (e instanceof Error && e.name === "AbortError") throw e;
      console.error("compact failed:", e);
      compactError = e instanceof Error ? e.message : String(e);
      return -1;
    } finally {
      compactingId = "";
    }
  }

  // Manual compaction: "/compact" typed into the composer. Same fold as the
  // automatic one, minus the KV-usage threshold — the point of asking for it by
  // hand is to compact BEFORE the window is nearly full (e.g. right before a
  // long tool-heavy turn), so a "not full enough yet" refusal would defeat it.
  async function runManualCompact(id: string) {
    const modelId = $selectedModelStore;
    if (!modelId) {
      showToast("Select a model first");
      return;
    }
    if (isStreaming) {
      showToast("Wait for the current response to finish");
      return;
    }
    if (compactingId) {
      showToast("Already compacting");
      return;
    }
    compactError = "";
    const moved = await compactNow(id, modelId, new AbortController().signal);
    if (moved > 0) {
      showToast(`Compacted ${moved} message${moved === 1 ? "" : "s"}`);
    } else if (moved === 0) {
      showToast(`Nothing to compact; the last ${KEEP_RECENT} messages always stay verbatim`);
    } else {
      showToast(`Compaction failed: ${compactError || "the conversation is unchanged"}`);
    }
  }

  async function editMessage(idx: number, newContent: string) {
    if (isStreaming || !$selectedModelStore) return;
    const id = $activeChatId;
    // Update the user message at the specified index
    patchSession(id, {
      messages: sessionById(id)!.messages.map((msg, i) => (i === idx ? { ...msg, content: newContent } : msg)),
    });
    // Trigger a new chat request with the updated messages
    await regenerateFromIndex(id, idx);
  }

  function handleKeyDown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      sendMessage();
    }
  }


  async function processImageFiles(files: File[]): Promise<void> {
    if (attachedImages.length + files.length > MAX_IMAGES_PER_MESSAGE) {
      imageError = `Maximum ${MAX_IMAGES_PER_MESSAGE} images per message`;
      return;
    }

    for (const file of files) {
      const error = validateImageFile(file);
      if (error) {
        imageError = error;
        return;
      }
    }

    try {
      const dataUrls = await Promise.all(files.map(fileToDataUrl));
      attachedImages = [...attachedImages, ...dataUrls];
    } catch (error) {
      imageError = error instanceof Error ? error.message : "Failed to process images";
    }
  }

  let docSeq = 0;
  function patchDoc(id: string, patch: Partial<AttachedDoc>) {
    attachedDocs = attachedDocs.map((d) => (d.id === id ? { ...d, ...patch } : d));
  }

  // Read one document into the text the model will actually see.
  async function extractDoc(file: File): Promise<{ text: string; note?: string }> {
    if (file.size > MAX_DOC_BYTES) {
      throw new Error(`"${file.name}" is ${(file.size / 1024 / 1024).toFixed(0)}MB, too large to read.`);
    }
    const kind = classifyAttachment(file);
    if (kind === "pdf") {
      const { text, pages } = await extractPdfText(file);
      return { text, note: `${pages} page${pages === 1 ? "" : "s"}` };
    }
    if (kind === "docx") return { text: await extractDocxText(file), note: "Word document" };
    if (kind === "audio") {
      // The only attachment that costs a model load: transcription runs on the
      // ASR backend, which shares the one GPU pool with the chat model.
      const asr = pickTranscribeModel($models);
      if (!asr) throw new Error("No transcription model is installed, so audio can't be attached.");
      if (asr.state !== "ready") showToast(`Loading ${asr.name} to transcribe; this swaps out the chat model`);
      const res = await transcribeAudio(asr.id, file);
      const text = (res.text ?? "").trim();
      if (!text) throw new Error(`Nothing was transcribed from "${file.name}".`);
      return { text, note: `transcribed by ${asr.name}` };
    }
    return { text: await readTextFile(file) };
  }

  // Extraction is async and can be slow (a big PDF, a model swap for audio), so
  // each file gets its chip immediately and fills in when it lands.
  async function processDocFiles(files: File[]): Promise<void> {
    if (attachedDocs.length + files.length > MAX_DOCS_PER_MESSAGE) {
      imageError = `Maximum ${MAX_DOCS_PER_MESSAGE} files per message`;
      return;
    }
    for (const file of files) {
      const id = `d${docSeq++}`;
      const kind = classifyAttachment(file);
      attachedDocs = [
        ...attachedDocs,
        { id, name: file.name, kind: kind === "image" || !kind ? "text" : kind, text: "", tokens: 0, status: "loading" },
      ];
      try {
        const { text, note } = await extractDoc(file);
        const tokens = estimateTokens(text);
        // Budget against what the OTHER attachments claim — not this one's own
        // zero-token placeholder, which is still in the list.
        const used = attachedDocs.filter((d) => d.id !== id).reduce((n, d) => n + d.tokens, 0);
        const tooBig = oversizeError(file.name, tokens, docCtx, used);
        if (tooBig) throw new Error(tooBig);
        patchDoc(id, { text, tokens, note, status: "ready" });
      } catch (e) {
        const msg = e instanceof Error ? e.message : "Could not read this file";
        patchDoc(id, { status: "error", error: msg });
        imageError = msg;
      }
    }
  }

  // One entry point for the picker, drag-drop and paste: sort what arrived, then
  // let each path complain for itself.
  async function processFiles(files: File[]): Promise<void> {
    imageError = null;
    const images: File[] = [];
    const docs: File[] = [];
    const rejected: string[] = [];
    for (const f of files) {
      const kind = classifyAttachment(f);
      if (kind === "image") images.push(f);
      else if (kind) docs.push(f);
      else rejected.push(f.name);
    }
    if (images.length > 0) {
      if (!canAttach) {
        imageError = "This model can't read images. Pick a vision model, or attach a document instead.";
      } else {
        swapToVision();
        await processImageFiles(images);
      }
    }
    if (docs.length > 0) await processDocFiles(docs);
    if (rejected.length > 0 && !imageError) {
      imageError = `Can't read ${rejected.join(", ")}: unsupported file type.`;
    }
  }

  function handleFileSelect(event: Event) {
    const input = event.target as HTMLInputElement;
    if (input.files && input.files.length > 0) {
      void processFiles(Array.from(input.files));
    }
    // Reset the input so the same file can be selected again
    input.value = "";
  }

  function removeImage(idx: number) {
    attachedImages = attachedImages.filter((_, i) => i !== idx);
    imageError = null;
  }

  function removeDoc(id: string) {
    attachedDocs = attachedDocs.filter((d) => d.id !== id);
    imageError = null;
  }

  // Paste a screenshot or a copied file straight into the composer. Only acts on
  // FILE clipboard items — pasted text falls through to the default handler.
  async function handlePaste(event: ClipboardEvent) {
    const items = event.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (const it of items) {
      if (it.kind !== "file") continue;
      const f = it.getAsFile();
      if (f) files.push(f);
    }
    if (files.length === 0) return; // plain text → let the browser handle it
    event.preventDefault();
    await processFiles(files);
  }
</script>

<div class="flex flex-col h-full">
  <!-- Empty state for no models configured -->
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <MessagesSquare class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No models configured. Add models to your configuration to start chatting.</p>
    </div>
  {:else}
    <!-- Highlight-to-reply popup, anchored above the current text selection. -->
    {#if selReply}
      <button
        class="fixed z-30 inline-flex items-center justify-center rounded-lg bg-[#141414] text-[#ededee] p-1.5 shadow-lg hover:opacity-90 transition-opacity"
        style="left: {selReply.x + 4}px; top: {selReply.y + 4}px"
        use:tooltip={"Reply to selection"}
        onmousedown={(e) => e.preventDefault()}
        onclick={replyToSelection}
      >
        <Quote class="w-3.5 h-3.5" />
      </button>
    {/if}
    <!-- Chat column — full-width so the whole pane scrolls; the message list and
         composer are width-constrained and centered inside. -->
    <div class="flex-1 flex flex-col min-w-0 min-h-0 w-full">
    <!-- Messages area — scrolls across the full width; content centered within. -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="flex-1 min-h-0 overflow-y-auto [overflow-anchor:none] pretty-scroll scroll-fade-b mb-2"
      bind:this={messagesContainer}
      onscroll={handleMessagesScroll}
      onmousedown={() => (selReply = null)}
      onmouseup={onSelection}
      use:scrollFade
    >
      <div class="w-full max-w-3xl mx-auto px-2 pt-4 pb-2 {messages.length === 0 ? 'h-full' : ''}" bind:this={messagesInner}>
      {#if messages.length === 0}
        <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
          <MessagesSquare class="w-10 h-10 opacity-40" strokeWidth={1.5} />
          <p>Start a conversation by typing a message below.</p>
        </div>
      {:else}
        {#each messages as message, idx (idx)}
          {#if idx === compactedCount && compactedCount > 0}
            <div class="flex items-center gap-2 my-3 text-[0.7rem] uppercase tracking-wide text-txtsecondary" use:tooltip={"Messages above are summarized for the model; they're still shown here but not resent."}>
              <span class="flex-1 h-px bg-card-border"></span>
              <span class="inline-flex items-center gap-1"><Brain class="w-3 h-3" /> Compacted - model sees a summary above</span>
              <span class="flex-1 h-px bg-card-border"></span>
            </div>
          {/if}
          <div data-role={message.role}>
          <ChatMessageComponent
            role={message.role}
            content={message.content}
            model={message.model}
            reasoning_content={message.reasoning_content}
            reasoningTimeMs={message.reasoningTimeMs}
            thinkMs={message.thinkMs}
            reasoningTitle={message.reasoningTitle}
            thinkTitles={message.thinkTitles}
            genTimeMs={message.genTimeMs}
            searches={message.searches}
            citations={message.citations}
            approval={message.approval}
            onApprove={respondApproval}
            rewriteInstruction={message.rewriteInstruction}
            rewriteOriginal={message.rewriteOriginal}
            isStreaming={genId === $activeChatId && idx === messages.length - 1 && message.role === "assistant"}
            isReasoning={isReasoning && genId === $activeChatId && idx === messages.length - 1 && message.role === "assistant"}
            isSearching={isSearching && genId === $activeChatId && idx === messages.length - 1 && message.role === "assistant"}
            {busyLabel}
            modelReady={modelReady}
            hasVisionInput={message.role === "assistant" && idx > 0 && getImageUrls(messages[idx - 1].content).length > 0}
            onEdit={message.role === "user" && message.rewriteInstruction == null ? (newContent) => editMessage(idx, newContent) : undefined}
            onRegenerate={message.role === "assistant" && idx > 0 && messages[idx - 1].role === "user"
              ? () => regenerateFromIndex($activeChatId, idx - 1)
              : undefined}
            onReply={message.role === "assistant" ? () => { replyTo(idx); inputEl?.focus(); } : undefined}
            onAskAnswer={message.role === "assistant" && idx === messages.length - 1 && genId !== $activeChatId
              ? (text) => { userInput = text; void sendMessage(); }
              : undefined}
          />
          </div>
        {/each}
      {/if}
      <!-- Compaction status. Inside the scrolled column and after the last
           message, not a strip at the top of the pane: it is the same kind of
           "the assistant is busy" line as busyLabel, and reading it means
           reading it where the conversation ended. It cannot ride ChatMessage's
           busyLabel prop, because a compaction (manual especially) runs with no
           assistant message in flight to hang the label on. -->
      {#if compactingId === $activeChatId}
        <div class="flex items-center gap-2 mt-1 mb-2">
          <span class="inline-flex items-center gap-1.5 text-xs italic">
            <span class="w-1.5 h-1.5 bg-primary rounded-full reason-glow"></span>
            <span class="reason-shimmer-white font-medium">Compacting conversation…</span>
          </span>
        </div>
      {/if}
      </div>
    </div>

    <!-- Input area — narrower than the message column, taller composer. -->
    <div class="shrink-0 relative w-full max-w-2xl mx-auto">
      <!-- Unanswered user turn (e.g. cancelled during load) → offer to generate. -->
      {#if genId === null && messages.length > 0 && messages[messages.length - 1].role === "user" && messages[messages.length - 1].rewriteInstruction == null}
        <div class="absolute bottom-full left-1/2 -translate-x-1/2 mb-3 z-20">
          <button
            onclick={() => regenerateFromIndex($activeChatId, messages.length - 1)}
            class="inline-flex items-center gap-1.5 rounded-full bg-primary text-white px-3.5 py-1.5 text-xs font-medium shadow-lg hover:opacity-90 transition-opacity"
            use:tooltip={"Generate a response for the last message"}
          >
            <Sparkles class="w-3.5 h-3.5" />
            Generate response
          </button>
        </div>
      {/if}

      <!-- Transient toggle toast -->
      {#if toast}
        <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-3 z-20">
          <div class="rounded-full bg-txtmain text-surface px-3 py-1 text-xs font-medium shadow-lg whitespace-nowrap">
            {toast}
          </div>
        </div>
      {/if}

      {#snippet chatSettingsPanel()}
        {#snippet tip(text: string)}
          <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary" use:tooltip={text}>
            <HelpCircle class="w-3.5 h-3.5" />
          </span>
        {/snippet}

        <div class="flex flex-col gap-1.5">
          <label class="flex justify-between text-xs uppercase tracking-wide text-txtsecondary" for="temperature">
            <span class="flex items-center gap-1.5">Temperature {@render tip("Higher gives the model more freedom to be creative and varied; lower keeps it focused and predictable.")}</span>
            <span class="text-txtmain normal-case">{TEMP_LABELS[nearestTempIdx($temperatureStore)]}</span>
          </label>
          <input
            id="temperature"
            type="range"
            min="0"
            max={TEMP_STEPS.length - 1}
            step="1"
            class="w-full accent-primary"
            value={nearestTempIdx($temperatureStore)}
            oninput={(e) => temperatureStore.set(TEMP_STEPS[+e.currentTarget.value])}
            disabled={isStreaming}
          />
          <div class="flex justify-between text-xs text-txtsecondary">
            <span>Precise</span>
            <span>Creative</span>
          </div>
        </div>

        <div class="flex items-center justify-between gap-2 text-xs uppercase tracking-wide text-txtsecondary">
          <span class="flex items-center gap-1.5"><Brain class="w-3.5 h-3.5" /> Reasoning {@render tip(
            effortLadder.length > 0
              ? "How hard this model thinks before answering. The levels come from the model's own chat template. Changing it rewrites the top of the system prompt, so it re-reads the conversation, so pick one and stay on it. Thinking Budget does not apply at these levels."
              : "Let the model think before answering (for reasoning-capable models). This model's template has no effort levels, so it is on or off.",
          )}</span>
          <Select
            value={effort}
            onchange={(v) => reasoningEffortStore.set(v)}
            ariaLabel="Reasoning effort"
            class="w-32 normal-case"
            options={effortChoices.map((opt) => ({ value: opt.value, label: opt.label }))}
          />
        </div>

        <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="chat-websearch">
          <span class="flex items-center gap-1.5"><Search class="w-3.5 h-3.5" /> Web Search {@render tip("Let the model search the web (via SearXNG) for fresh facts. Needs a tool-calling model. URL + rate limits are in the side-rail Settings.")}</span>
          <Toggle id="chat-websearch" bind:checked={$webSearchStore} />
        </label>

        <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="chat-extratools">
          <span class="flex items-center gap-1.5"><CloudSun class="w-3.5 h-3.5" /> Weather & Feeds {@render tip("Let the model read the live weather (Open-Meteo) and any RSS/Atom feed.")}</span>
          <Toggle id="chat-extratools" bind:checked={$extraToolsStore} />
        </label>

        <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="chat-memory">
          <span class="flex items-center gap-1.5"><BrainCircuit class="w-3.5 h-3.5" /> Memory {@render tip("Let the model remember lasting facts about you across conversations. Remembered facts are added to every chat's system prompt; read, edit and delete them in Settings → Memory.")}</span>
          <Toggle id="chat-memory" bind:checked={$memoryStore} />
        </label>

        <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="chat-qmtools">
          <span class="flex items-center gap-1.5"><Wrench class="w-3.5 h-3.5" /> QM Tools {@render tip("Let the model inspect and tune this Quartermaster instance - list installed models, read live VRAM/config, and change settings (hot-reloads, no eviction). Needs a tool-calling model. Requires -generate for edits.")}</span>
          <Toggle id="chat-qmtools" bind:checked={$qmToolsStore} />
        </label>

        <div class="flex flex-col gap-1">
          <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">Instructions {@render tip("Standing instructions for THIS chat only, layered on top of the built-in prompt. Saved with the conversation.")}</span>
          <button
            type="button"
            class="w-full text-left px-2.5 py-1.5 rounded-md border border-card-border bg-surface hover:border-primary transition-colors {activeSession?.instructions?.trim() ? 'text-txtmain' : 'text-txtsecondary'}"
            onclick={openSysPrompt}
          >
            <span class="line-clamp-2">{activeSession?.instructions?.trim() || "Add instructions for this chat…"}</span>
          </button>
        </div>

        {#if $shoppingStore}
          <div class="flex flex-col gap-1">
            <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">
              <ShoppingCart class="w-3.5 h-3.5" /> Shopping preferences
              {@render tip("Where you buy: country, currency and the shops you prefer. Standing setting - the assistant searches these first instead of asking every time.")}
            </span>
            <input
              type="text"
              class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary text-[0.8125rem]"
              placeholder="e.g. Romania, RON, prefer emag.ro and altex.ro"
              bind:value={$shoppingPrefsStore}
            />
          </div>
        {/if}

      {/snippet}

      <!-- System-prompt editor: roomier modal to write/save the standing prompt. -->
      {#if showSysPrompt}
        <div class="fixed inset-0 z-40 flex items-center justify-center bg-black/40 p-4" onclick={() => (showSysPrompt = false)} role="presentation">
          <div
            class="flex w-full max-w-xl flex-col gap-3 rounded-lg border border-card-border bg-surface p-4 shadow-xl"
            onclick={(e) => e.stopPropagation()}
            role="presentation"
          >
            <div class="flex items-center justify-between">
              <span class="font-medium text-txtmain">Instructions</span>
              <button class="inline-flex items-center justify-center p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors" onclick={() => (showSysPrompt = false)} use:tooltip={"Close"}>
                <X class="w-4 h-4" />
              </button>
            </div>
            <p class="text-xs text-txtsecondary">Standing instructions for this chat only - layered on top of the built-in prompt.</p>
            <textarea
              class="w-full h-64 px-3 py-2 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary resize-none text-sm"
              placeholder="e.g. Answer as a senior Rust engineer. Be terse."
              bind:value={sysPromptDraft}
            ></textarea>
            <div class="flex justify-end gap-2">
              <button class="px-3 py-1.5 rounded-md border border-card-border text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors text-sm" onclick={() => (showSysPrompt = false)}>Cancel</button>
              <button class="px-3 py-1.5 rounded-md bg-primary text-white hover:opacity-90 transition-opacity text-sm" onclick={saveSysPrompt}>Save</button>
            </div>
          </div>
        </div>
      {/if}

      <!-- Image preview strip -->
      {#if attachedImages.length > 0}
        <div class="mb-2 flex flex-wrap gap-2">
          {#each attachedImages as imageUrl, idx (idx)}
            <div class="group relative h-16 w-16 overflow-hidden rounded-lg border border-card-border bg-surface shadow-sm">
              <img
                src={imageUrl}
                alt="Attachment {idx + 1}"
                class="h-full w-full object-cover"
              />
              <button
                class="absolute right-1 top-1 inline-flex h-5 w-5 items-center justify-center rounded-full bg-black/55 text-white backdrop-blur-sm opacity-0 transition-opacity group-hover:opacity-100 hover:bg-black/75"
                onclick={() => removeImage(idx)}
                use:tooltip={"Remove image"}
              >
                <X class="h-3 w-3" />
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <!-- Document attachment chips. A file is text by the time it gets here, so
           the chip carries its size in tokens: the one number that decides
           whether it fits. -->
      {#if attachedDocs.length > 0}
        <div class="mb-2 flex flex-wrap gap-2">
          {#each attachedDocs as doc (doc.id)}
            <div
              class="group flex items-center gap-2 rounded-lg border px-2.5 py-1.5 text-[0.8125rem] max-w-[18rem] {doc.status === 'error'
                ? 'border-red-500/50 bg-red-500/10 text-red-600 dark:text-red-400'
                : 'border-card-border bg-surface text-txtsecondary'}"
              use:tooltip={doc.status === "error" ? doc.error : `${doc.name}${doc.note ? ` · ${doc.note}` : ""}`}
            >
              {#if doc.status === "loading"}
                <Loader2 class="w-3.5 h-3.5 shrink-0 animate-spin text-primary" />
              {:else if doc.status === "error"}
                <AlertTriangle class="w-3.5 h-3.5 shrink-0" />
              {:else}
                <FileText class="w-3.5 h-3.5 shrink-0 text-primary" />
              {/if}
              <span class="truncate">{doc.name}</span>
              {#if doc.status === "ready"}
                <span class="shrink-0 text-txtsecondary/70">{fmtTokens(doc.tokens)} tok</span>
              {:else if doc.status === "loading"}
                <span class="shrink-0 text-txtsecondary/70">reading…</span>
              {/if}
              <button
                class="shrink-0 p-0.5 rounded-full hover:bg-secondary transition-colors"
                onclick={() => removeDoc(doc.id)}
                use:tooltip={"Remove file"}
              >
                <X class="w-3.5 h-3.5" />
              </button>
            </div>
          {/each}
          {#if docTokens > 0 && docCtx > 0}
            <span class="self-center text-[0.75rem] text-txtsecondary/70">
              {fmtTokens(docTokens)} of {fmtTokens(docCtx)} context
            </span>
          {/if}
        </div>
      {/if}

      <!-- Error message -->
      {#if imageError}
        <div class="mb-2 p-2 bg-red-100 dark:bg-red-900/20 text-red-700 dark:text-red-400 rounded text-sm">
          {imageError}
        </div>
      {/if}

      <!-- Hidden file input -->
      <input
        type="file"
        accept={acceptAttr(canAttach)}
        multiple
        class="hidden"
        bind:this={fileInput}
        onchange={handleFileSelect}
      />

      <!-- Queued messages (typed while a turn is streaming) -->
      {#if queued.length > 0 && genId === $activeChatId}
        <div class="flex flex-col gap-1 mb-2">
          {#each queued as q, qi (qi)}
            <div class="flex items-center gap-2 self-end max-w-[80%] rounded-2xl bg-secondary/60 border border-card-border px-3 py-1.5 text-[0.8125rem]">
              <Clock class="w-3.5 h-3.5 shrink-0 text-txtsecondary" />
              <span class="truncate" use:tooltip={q}>{q}</span>
              <button
                class="shrink-0 p-0.5 rounded-full text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
                onclick={() => (queued = queued.filter((_, i) => i !== qi))}
                use:tooltip={"Remove from queue"}
              >
                <X class="w-3.5 h-3.5" />
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <!-- Reply target chip: the past message the next send quotes. -->
      {#if replyingTo}
        <div class="flex items-center gap-2 mb-2 self-start max-w-full rounded-2xl bg-secondary/60 border border-card-border px-3 py-1.5 text-[0.8125rem]">
          <Reply class="w-3.5 h-3.5 shrink-0 text-primary" />
          <span class="truncate text-txtsecondary" use:tooltip={replyingTo}>Replying to: {replyingTo}</span>
          <button
            class="shrink-0 p-0.5 rounded-full text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
            onclick={() => (replyingTo = null)}
            use:tooltip={"Cancel reply"}
          >
            <X class="w-3.5 h-3.5" />
          </button>
        </div>
      {/if}

      <!-- Composer -->
      {#snippet chatTopExtra()}
        {#if $rewriteStore}
          <div class="flex items-start gap-2 pb-2 border-b border-card-border">
            <PenLine class="w-3.5 h-3.5 mt-1.5 shrink-0 text-primary" />
            <textarea
              bind:this={rewriteEl}
              class="w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary min-h-[1.5rem] max-h-60 pretty-scroll"
              rows="1"
              placeholder="How should I help? e.g. make it more concise and formal"
              bind:value={$rewriteInstructionStore}
              onfocus={() => (rewriteFocused = true)}
              onblur={() => (rewriteFocused = false)}
              onkeydown={handleKeyDown}
            ></textarea>
          </div>
        {/if}
      {/snippet}

      {#snippet chatLeftButtons()}
        <ToolMenu items={toolMenuItems} disabled={isStreaming} />
        <!-- Always shown so the composer row doesn't reshuffle per model. It is no
             longer gated on vision: documents are read into text in the browser,
             so every model can take one; only the image half of the accept list
             depends on the model. -->
        <button
          class="composer-icon-btn"
          onclick={openAttach}
          disabled={isStreaming || !$selectedModelStore}
          use:tooltip={!$selectedModelStore
            ? "Pick a model first"
            : canAttach
              ? visionTwin
                ? "Attach a file or image (an image loads the vision projector)"
                : "Attach a file or image"
              : "Attach a file (text, PDF, Word, audio); this model can't read images"}
        >
          <Paperclip class="w-[1.125rem] h-[1.125rem]" />
        </button>
      {/snippet}

      {#snippet chatCtxBar()}
        <!-- Context-window usage: thin line (yellow → orange → red) plus the
             used/max token readout, so the bar says how much room is left and
             not just "some". Clicking it compacts on demand — the same thing
             typing /compact does, but findable: the moment you want it is the
             moment you are looking at this bar. -->
        {#if ctxN > 0}
          <button
            type="button"
            class="flex items-center gap-1.5 hover:opacity-80 transition-opacity"
            onclick={() => runManualCompact($activeChatId)}
            use:tooltip={`Context ${fmtTokens(ctxUsed)} / ${fmtTokens(ctxN)} tokens (${Math.round(ctxRatio * 100)}%) · click to compact now`}
          >
            <div class="h-0.5 w-16 rounded-full bg-secondary overflow-hidden">
              <div class="h-full rounded-full transition-all" style="width: {Math.max(ctxRatio * 100, 3)}%; background: {ctxColor};"></div>
            </div>
            <span class="font-mono text-micro tabular-nums text-txtsecondary leading-none">
              {fmtTokens(ctxUsed)}/{fmtTokens(ctxN)}
            </span>
          </button>
        {/if}
      {/snippet}

      <Composer
        bind:value={userInput}
        placeholder={$rewriteStore ? "Paste the text to rewrite…" : isStreaming ? "Queue a message…" : "Type a message..."}
        bind:textareaEl={inputEl}
        onKeydown={handleKeyDown}
        onPaste={handlePaste}
        bind:modelValue={$selectedModelStore}
        onModelChange={rememberModel}
        modelPlaceholder="Select a model..."
        category="llm"
        busy={isStreaming}
        onStop={cancelStreaming}
        bind:showSettings
        settingsTitle="Configs"
        topExtra={chatTopExtra}
        leftButtons={chatLeftButtons}
        ctxBar={chatCtxBar}
        settingsPanel={chatSettingsPanel}
      />
    </div>
    </div>
  {/if}
</div>
