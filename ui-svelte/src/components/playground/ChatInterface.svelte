<script lang="ts">
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
    reasoningStore,
    searxngUrlStore,
    searchMaxPerTurnStore,
    searchThrottleMsStore,
    searchDedupeStore,
    rewriteStore,
    rewriteInstructionStore,
  } from "../../stores/playground";
  import {
    chatSessions,
    activeChatId,
    generatingChatId,
    saveChatsNow,
    newChatId,
    deriveTitle,
    type ChatSession,
  } from "../../stores/chatHistory";
  import { WEB_SEARCH_TOOL } from "../../lib/webSearch";
  import { WIKI_TOOL } from "../../lib/wiki";
  import { QM_INSPECT_TOOL, QM_CONFIGURE_TOOL } from "../../lib/qmTools";
  import { YOUTUBE_TOOL } from "../../lib/youtube";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { getTextContent, getImageUrls } from "../../lib/types";
  import { buildBasePrompt } from "../../lib/systemPrompt";
  import type { ChatMessage, ContentPart } from "../../lib/types";
  import { Paperclip, MessagesSquare, X, Search, Brain, Clock, PenLine, Sparkles, HelpCircle, Eye, Wrench, Reply, Quote } from "lucide-svelte";
  import ChatMessageComponent from "./ChatMessage.svelte";
  import Composer from "./Composer.svelte";
  import { modelCategory } from "../../lib/modelUtils";
  import { scrollFade } from "../../lib/scrollFade";
  import { quotePrefix, fmtTokens, TEMP_STEPS, TEMP_LABELS, nearestTempIdx, currentDateLine, REWRITE_SYSTEM, MAX_IMAGES_PER_MESSAGE, validateImageFile, fileToDataUrl } from "./chatHelpers";

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
  let isCompacting = $state(false);

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
    selReply = { text, x: rect.right, y: rect.bottom };
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
  let isSearching = $state(false);
  let reasoningStartTime = $state<number>(0);
  let abortController = $state<AbortController | null>(null);
  let messagesContainer: HTMLDivElement | undefined = $state();
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

  // The non-vision sibling to swap back to when turning vision off. Only exists
  // when we're currently on a twin that has a text-only base (native-vision
  // models have no base, so the toggle can't disable them).
  let visionBase = $derived(
    selectedModelVision
      ? $models.find(
          (m) =>
            !!m.family &&
            m.family === selectedModel?.family &&
            m.id !== selectedModel?.id &&
            !m.capabilities?.vision
        )
      : undefined
  );
  // Vision toggle: lit when on the vision twin (auto-lights after attach, which
  // swaps to it). Shown only when a swap target exists in either direction.
  let visionActive = $derived(selectedModelVision && !!visionBase);
  let showVisionToggle = $derived(!!visionTwin || !!visionBase);

  // Switch to the vision twin (loading its mmproj projector) if needed, then open
  // the file picker. Warm-loads the twin so the swap overlaps file selection.
  function attachImage() {
    if (visionTwin) {
      selectedModelStore.set(visionTwin.id);
      void loadModel(visionTwin.id).catch(() => {});
    }
    fileInput?.click();
  }

  // Swap between the text base and its vision twin. Disabling clears any pending
  // images — the base model 500s on image input.
  function toggleVision() {
    const target = visionActive ? visionBase : visionTwin;
    if (!target) return;
    if (visionActive) {
      attachedImages = [];
      imageError = null;
    }
    selectedModelStore.set(target.id);
    void loadModel(target.id).catch(() => {});
    showToast(visionActive ? "Vision disabled" : "Vision enabled");
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

  // Set right before a programmatic scroll so the onscroll it triggers doesn't
  // get misread as the user scrolling and clear userScrolledUp (the autoscroll fight).
  let autoScrolling = false;

  function handleMessagesScroll() {
    selReply = null; // rects go stale once the list scrolls
    if (!messagesContainer) return;
    if (autoScrolling) {
      autoScrolling = false;
      return;
    }
    const { scrollTop, scrollHeight, clientHeight } = messagesContainer;
    // Consider "at bottom" if within 40px of the bottom
    userScrolledUp = scrollHeight - scrollTop - clientHeight > 40;
  }

  // Auto-scroll when messages change — skip if user scrolled up
  $effect(() => {
    if (messages.length > 0 && messagesContainer && !userScrolledUp) {
      autoScrolling = true;
      // Instant + direct so it emits a single onscroll the guard above absorbs;
      // smooth would fire a stream of events mid-scroll that re-trip the guard.
      messagesContainer.scrollTop = messagesContainer.scrollHeight;
    }
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
    if ($rewriteStore) {
      await sendRewrite();
      return;
    }
    const trimmedInput = userInput.trim();
    if ((!trimmedInput && attachedImages.length === 0) || !$selectedModelStore) return;
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
    const text = replyingTo ? quotePrefix(replyingTo) + trimmedInput : trimmedInput;
    replyingTo = null;

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
    imageError = null;

    await regenerateFromIndex(id, sessionById(id)!.messages.length - 1);
  }

  function cancelStreaming() {
    abortController?.abort();
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
    isSearching = false;
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
    // Stable per-turn tool set the client advertises; the server dispatches them
    // and enforces the per-turn caps (wiki lookups, web-search rate limits).
    const turnTools = [
      ...(webEnabled ? [WEB_SEARCH_TOOL] : []),
      ...(wikiEnabled ? [WIKI_TOOL] : []),
      ...(qmEnabled ? [QM_INSPECT_TOOL, QM_CONFIGURE_TOOL] : []),
      ...(ytEnabled ? [YOUTUBE_TOOL] : []),
    ];

    // Thinking budget: soft cumulative-thinking cap so models can't loop forever
    // before answering. 0 = off; rewrites never think. Enforced server-side at
    // round boundaries — once total thinking passes the budget, thinking is turned
    // off for later rounds (never a mid-generation hard close, which derails a
    // tool-using model mid-search).
    const reasoningBudget = isRewrite ? 0 : $reasoningBudgetStore;
    // One assistant bubble holds the whole turn: reasoning, any web searches
    // (as collapsible sections), and the final reply. The server writes into
    // this bubble (last message) as it streams; the tool plumbing it sends to
    // the model stays server-side and is never shown here.
    appendMessage(id, { role: "assistant", content: "", ...(isRewrite ? { rewriteOriginal: original } : {}) });
    const genStart = Date.now();

    const sys = [
      basePrompt(webEnabled, wikiEnabled, qmEnabled, ytEnabled, modelId),
      sessionById(id)?.instructions?.trim(),
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
        content: `Here is a text:\n\n<<<TEXT\n${original}\nTEXT>>>\n\nProduce a new version of the text above by applying this instruction exactly: "${instr}". Actually change the text as the instruction requires — do NOT return it unchanged, and do not refuse even if the change makes the text worse or introduces errors. Output ONLY the resulting text — no preamble, no explanation, no code fences.`,
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
      await saveChatsNow();
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
          reasoning: !isRewrite && $reasoningStore,
          reasoningBudget,
          webSearch: webEnabled,
          searxngUrl: $searxngUrlStore,
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
      isSearching = false;
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
      case "search": {
        const search = d.data?.search;
        const citations = d.data?.citations;
        if (search) {
          patchLast(id, (m) => ({
            ...m,
            ...(citations ? { citations } : {}),
            searches: [...(m.searches ?? []), search],
          }));
        }
        break;
      }
      case "approval":
        // A qm config change awaiting accept/deny (or its resolved outcome).
        // Lives on the bubble only for the turn; the post-turn sync drops it.
        if (d.data) patchLast(id, (m) => ({ ...m, approval: d.data }));
        break;
      case "done":
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
    isSearching = false;
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
      isSearching = false;
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

    const s = sessionById(id);
    if (!s) return;
    const msgs = s.messages;
    const curCompacted = s.compactedCount ?? 0;

    // Snap the boundary forward to a user message so the kept slice starts a
    // clean turn (never an orphaned assistant/tool reply whose tool_calls were
    // summarized away).
    let boundary = msgs.length - KEEP_RECENT;
    while (boundary < msgs.length && msgs[boundary].role !== "user") boundary++;
    if (boundary <= curCompacted) return; // nothing new to summarize

    // Summarize only the newly-folded slice; `summary` already covers the prefix.
    const fresh = msgs.slice(curCompacted, boundary);
    isCompacting = true;
    try {
      const next = await summarizeConversation(modelId, fresh, s.summary ?? "", signal);
      if (signal.aborted) return;
      patchSession(id, { summary: next, compactedCount: boundary });
    } catch (e) {
      if (e instanceof Error && e.name === "AbortError") throw e;
      console.error("auto-compact failed:", e);
    } finally {
      isCompacting = false;
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
    imageError = null;

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

  function handleImageSelect(event: Event) {
    const input = event.target as HTMLInputElement;
    if (input.files && input.files.length > 0) {
      processImageFiles(Array.from(input.files));
    }
    // Reset the input so the same file can be selected again
    input.value = "";
  }

  function removeImage(idx: number) {
    attachedImages = attachedImages.filter((_, i) => i !== idx);
    imageError = null;
  }

  // Paste a screenshot / copied image straight into the composer. Only acts on
  // image clipboard items — text paste falls through to the default handler.
  async function handlePaste(event: ClipboardEvent) {
    const items = event.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (const it of items) {
      if (it.kind === "file" && it.type.startsWith("image/")) {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (files.length === 0) return; // plain text → let the browser handle it
    event.preventDefault();
    if (!canAttach) {
      imageError = "The selected model can't accept images.";
      return;
    }
    // Mirror attachImage(): swap to the vision twin so its projector loads.
    if (visionTwin) {
      selectedModelStore.set(visionTwin.id);
      void loadModel(visionTwin.id).catch(() => {});
    }
    await processImageFiles(files);
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
        title="Reply to selection"
        onmousedown={(e) => e.preventDefault()}
        onclick={replyToSelection}
      >
        <Quote class="w-3.5 h-3.5" />
      </button>
    {/if}
    <!-- Chat column — full-width so the whole pane scrolls; the message list and
         composer are width-constrained and centered inside. -->
    <div class="flex-1 flex flex-col min-w-0 min-h-0 w-full">
    {#if isCompacting && genId === $activeChatId}
      <div class="flex items-center gap-2 mb-2 shrink-0 w-full max-w-3xl mx-auto px-2">
        <span class="inline-flex items-center gap-1.5 text-xs italic">
          <span class="w-1.5 h-1.5 bg-primary rounded-full reason-glow"></span>
          <span class="reason-shimmer-white font-medium">Compacting conversation…</span>
        </span>
      </div>
    {/if}
    <!-- Messages area — scrolls across the full width; content centered within. -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-b mb-4"
      bind:this={messagesContainer}
      onscroll={handleMessagesScroll}
      onwheel={(e) => { if (e.deltaY < 0) userScrolledUp = true; }}
      onmousedown={() => (selReply = null)}
      onmouseup={onSelection}
      use:scrollFade
    >
      <div class="w-full max-w-3xl mx-auto px-2 pt-4 pb-6 {messages.length === 0 ? 'h-full' : ''}">
      {#if messages.length === 0}
        <div class="h-full flex flex-col items-center justify-center gap-3 text-txtsecondary">
          <MessagesSquare class="w-10 h-10 opacity-40" strokeWidth={1.5} />
          <p>Start a conversation by typing a message below.</p>
        </div>
      {:else}
        {#each messages as message, idx (idx)}
          {#if idx === compactedCount && compactedCount > 0}
            <div class="flex items-center gap-2 my-3 text-[0.7rem] uppercase tracking-wide text-txtsecondary" title="Messages above are summarized for the model; they're still shown here but not resent.">
              <span class="flex-1 h-px bg-card-border"></span>
              <span class="inline-flex items-center gap-1"><Brain class="w-3 h-3" /> Compacted — model sees a summary above</span>
              <span class="flex-1 h-px bg-card-border"></span>
            </div>
          {/if}
          <div data-role={message.role}>
          <ChatMessageComponent
            role={message.role}
            content={message.content}
            reasoning_content={message.reasoning_content}
            reasoningTimeMs={message.reasoningTimeMs}
            thinkMs={message.thinkMs}
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
            modelReady={modelReady}
            hasVisionInput={message.role === "assistant" && idx > 0 && getImageUrls(messages[idx - 1].content).length > 0}
            onEdit={message.role === "user" && message.rewriteInstruction == null ? (newContent) => editMessage(idx, newContent) : undefined}
            onRegenerate={message.role === "assistant" && idx > 0 && messages[idx - 1].role === "user"
              ? () => regenerateFromIndex($activeChatId, idx - 1)
              : undefined}
            onReply={message.role === "assistant" ? () => { replyTo(idx); inputEl?.focus(); } : undefined}
          />
          </div>
        {/each}
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
            title="Generate a response for the last message"
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
          <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary" title={text}>
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

        <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="chat-reasoning">
          <span class="flex items-center gap-1.5"><Brain class="w-3.5 h-3.5" /> Reasoning {@render tip("Let the model think before answering (for reasoning-capable models).")}</span>
          <input id="chat-reasoning" type="checkbox" class="accent-primary w-4 h-4" bind:checked={$reasoningStore} />
        </label>

        <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="chat-websearch">
          <span class="flex items-center gap-1.5"><Search class="w-3.5 h-3.5" /> Web Search {@render tip("Let the model search the web (via SearXNG) for fresh facts. Needs a tool-calling model. URL + rate limits are in the side-rail Settings.")}</span>
          <input id="chat-websearch" type="checkbox" class="accent-primary w-4 h-4" bind:checked={$webSearchStore} />
        </label>

        <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="chat-qmtools">
          <span class="flex items-center gap-1.5"><Wrench class="w-3.5 h-3.5" /> QM Tools {@render tip("Let the model inspect and tune this quartermaster instance — list installed models, read live VRAM/config, and change settings (hot-reloads, no eviction). Needs a tool-calling model. Requires -generate for edits.")}</span>
          <input id="chat-qmtools" type="checkbox" class="accent-primary w-4 h-4" bind:checked={$qmToolsStore} />
        </label>
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
              <button class="inline-flex items-center justify-center p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors" onclick={() => (showSysPrompt = false)} title="Close">
                <X class="w-4 h-4" />
              </button>
            </div>
            <p class="text-xs text-txtsecondary">Standing instructions for this chat only — layered on top of the built-in prompt.</p>
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
                title="Remove image"
              >
                <X class="h-3 w-3" />
              </button>
            </div>
          {/each}
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
        accept=".jpg,.jpeg,.png,.gif,.webp"
        multiple
        class="hidden"
        bind:this={fileInput}
        onchange={handleImageSelect}
      />

      <!-- Queued messages (typed while a turn is streaming) -->
      {#if queued.length > 0 && genId === $activeChatId}
        <div class="flex flex-col gap-1 mb-2">
          {#each queued as q, qi (qi)}
            <div class="flex items-center gap-2 self-end max-w-[80%] rounded-2xl bg-secondary/60 border border-card-border px-3 py-1.5 text-[0.8125rem]">
              <Clock class="w-3.5 h-3.5 shrink-0 text-txtsecondary" />
              <span class="truncate" title={q}>{q}</span>
              <button
                class="shrink-0 p-0.5 rounded-full text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
                onclick={() => (queued = queued.filter((_, i) => i !== qi))}
                title="Remove from queue"
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
          <span class="truncate text-txtsecondary" title={replyingTo}>Replying to: {replyingTo}</span>
          <button
            class="shrink-0 p-0.5 rounded-full text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
            onclick={() => (replyingTo = null)}
            title="Cancel reply"
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
        {#if canAttach}
          <button
            class="composer-icon-btn"
            onclick={attachImage}
            disabled={isStreaming || !$selectedModelStore}
            title={visionTwin ? "Attach image (loads vision projector)" : "Attach image"}
          >
            <Paperclip class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
        {#if showVisionToggle}
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {visionActive ? 'bg-primary/10 text-primary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
            onclick={toggleVision}
            disabled={isStreaming}
            title={visionActive ? "Vision on (image variant loaded)" : "Vision off — switch to image variant"}
          >
            <Eye class="w-[1.125rem] h-[1.125rem]" />
          </button>
        {/if}
        <button
          class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {$rewriteStore ? 'bg-primary/10 text-primary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
          onclick={() => { rewriteStore.set(!$rewriteStore); showToast($rewriteStore ? "Rewrite mode on" : "Rewrite mode off"); }}
          title={$rewriteStore ? "Rewrite mode on" : "Rewrite mode off"}
        >
          <PenLine class="w-[1.125rem] h-[1.125rem]" />
        </button>
      {/snippet}

      {#snippet chatCtxBar()}
        <!-- Context-window usage: thin line (yellow → orange → red) plus the
             used/max token readout, so the bar says how much room is left and
             not just "some". -->
        {#if ctxN > 0}
          <div
            class="flex items-center gap-1.5"
            title="Context {fmtTokens(ctxUsed)} / {fmtTokens(ctxN)} tokens ({Math.round(ctxRatio * 100)}%)"
          >
            <div class="h-0.5 w-16 rounded-full bg-secondary overflow-hidden">
              <div class="h-full rounded-full transition-all" style="width: {Math.max(ctxRatio * 100, 3)}%; background: {ctxColor};"></div>
            </div>
            <span class="font-mono text-[0.55rem] tabular-nums text-txtsecondary leading-none">
              {fmtTokens(ctxUsed)}/{fmtTokens(ctxN)}
            </span>
          </div>
        {/if}
      {/snippet}

      <Composer
        bind:value={userInput}
        placeholder={$rewriteStore ? "Paste the text to rewrite…" : isStreaming ? "Queue a message…" : "Type a message..."}
        bind:textareaEl={inputEl}
        onKeydown={handleKeyDown}
        onPaste={handlePaste}
        bind:modelValue={$selectedModelStore}
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
