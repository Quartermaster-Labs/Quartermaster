<script lang="ts">
  import { get } from "svelte/store";
  import { models, backendMetrics, loadModel } from "../../stores/api";
  import { summarizeConversation, generateTitle, COMPACT_AT, KEEP_RECENT } from "../../lib/chatCompact";
  import {
    selectedModelStore,
    selectedTabStore,
    systemPromptStore,
    temperatureStore,
    maxTokensStore,
    reasoningBudgetStore,
    webSearchStore,
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
    newChatId,
    deriveTitle,
    type ChatSession,
  } from "../../stores/chatHistory";
  import { streamChatCompletion } from "../../lib/chatApi";
  import { WEB_SEARCH_TOOL, searxngSearch, formatSearchResults } from "../../lib/webSearch";
  import { WIKI_TOOL, searchWiki, formatWikiResults } from "../../lib/wiki";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { getTextContent, getImageUrls } from "../../lib/types";
  import { harmonyToThink } from "../../lib/reasoning";
  import type { ChatMessage, ContentPart, ToolCall } from "../../lib/types";
  import { Settings, Paperclip, Square, MessagesSquare, X, Search, Brain, Clock, PenLine, Sparkles, HelpCircle, Eye } from "lucide-svelte";
  import ChatMessageComponent from "./ChatMessage.svelte";
  import ModelSelector from "./ModelSelector.svelte";
  import { modelCategory } from "../../lib/modelUtils";
  import { scrollFade } from "../../lib/scrollFade";

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

  // --- store helpers: messages live in chatSessions, keyed by session id ---
  function sessionById(id: string): ChatSession | undefined {
    return get(chatSessions).find((s) => s.id === id);
  }
  // Sleep that rejects with an AbortError if the signal fires (so a user Stop
  // during a search throttle-wait breaks out instead of stalling).
  function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      if (signal.aborted) return reject(new DOMException("Aborted", "AbortError"));
      const t = setTimeout(() => {
        signal.removeEventListener("abort", onAbort);
        resolve();
      }, ms);
      const onAbort = () => {
        clearTimeout(t);
        reject(new DOMException("Aborted", "AbortError"));
      };
      signal.addEventListener("abort", onAbort, { once: true });
    });
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
  let ctxRatio = $derived(ctxN ? Math.min(1, ctxMetrics!.kv_cache_usage_ratio) : 0);
  let ctxColor = $derived(
    ctxRatio >= COMPACT_AT ? "#ef4444" : ctxRatio >= 0.6 ? "#f97316" : "#eab308",
  );

  let userInput = $state("");
  // Messages typed while a turn is streaming; sent one-by-one once it finishes.
  let queued = $state<string[]>([]);

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
  // Composer only grows while focused; blur collapses it back to normal size.
  let inputFocused = $state(false);
  let rewriteFocused = $state(false);
  let rewriteEl: HTMLTextAreaElement | undefined = $state();
  let showSettings = $state(false);
  // System-prompt editor modal: edit a draft, commit to the store on Save.
  let showSysPrompt = $state(false);
  let sysPromptDraft = $state("");
  function openSysPrompt() {
    sysPromptDraft = get(systemPromptStore);
    showSysPrompt = true;
  }
  function saveSysPrompt() {
    systemPromptStore.set(sysPromptDraft);
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

  // Discrete temperature steps (Precise → Creative), hand-picked to avoid the
  // useless extremes. The slider edits an index; the actual temp is stored.
  const TEMP_STEPS = [0.3, 0.5, 0.7, 0.9, 1.1];
  const TEMP_LABELS = ["Precise", "Focused", "Balanced", "Creative", "Inventive"];
  function nearestTempIdx(t: number): number {
    let bi = 0,
      bd = Infinity;
    for (let i = 0; i < TEMP_STEPS.length; i++) {
      const d = Math.abs(TEMP_STEPS[i] - t);
      if (d < bd) {
        bd = d;
        bi = i;
      }
    }
    return bi;
  }

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

  // Auto-grow the composer textarea; tracks userInput so it also shrinks back
  // after a send clears the value. Guard scrollHeight === 0 (tab is display:none
  // at mount) — otherwise the height locks at 0px and never recovers when the
  // tab is shown, leaving an invisible, untypeable textarea.
  $effect(() => {
    userInput;
    inputFocused;
    $selectedTabStore; // re-run when this tab becomes visible again
    if (inputEl) {
      // Collapse to normal size when the user clicks away; grow to fit content
      // (up to the max) only while actively editing. CSS transitions the height.
      if (!inputFocused) {
        inputEl.style.height = "3rem";
        return;
      }
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

    // Build message content (multimodal if images attached)
    let content: string | ContentPart[];
    if (attachedImages.length > 0) {
      const parts: ContentPart[] = [];
      if (trimmedInput) {
        parts.push({ type: "text", text: trimmedInput });
      }
      for (const url of attachedImages) {
        parts.push({ type: "image_url", image_url: { url } });
      }
      content = parts;
    } else {
      content = trimmedInput;
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

  // Web search runs as OpenAI tool-calling, which only the chat/completions
  // endpoint speaks. Other endpoints just chat without the tool.

  // Built-in system prompt prepended ahead of the user's own. Grounds the model
  // with basic honesty/tool guidance. Always on. The volatile current-date line is
  // deliberately NOT here — it's appended at the very END of the system block (see
  // currentDateLine) so this prefix stays byte-identical across a midnight rollover
  // and the KV cache isn't invalidated once a day.
  function basePrompt(searchAvailable: boolean, wikiAvailable: boolean): string {
    const lines = [
      "You are a capable, knowledgeable assistant running locally on the user's own machine.",
      "You are served by llama-quartermaster: an all-in-one local inference engine (a llama.cpp/stable-diffusion.cpp front-end) that discovers the user's local model files, auto-computes each model's context/GPU-offload/KV settings, and hot-swaps models in and out of VRAM on demand. The user reaches you through its built-in web playground. There is no cloud service behind you — weights, prompts, and conversations stay on this machine.",
      "If you are unsure or do not know something, say so plainly — never fabricate facts, citations, numbers, or URLs, and clearly separate what you know from what you're inferring or guessing.",
      "Answer directly and lead with the point. Keep answers concise and skip filler and boilerplate caveats; expand only when the topic genuinely needs it or the user asks.",
      "Follow the user's instructions precisely and match their language and tone. If a request is genuinely ambiguous, ask one short clarifying question rather than guessing.",
      "Work through multi-step or tricky problems carefully before committing to a final answer, and double-check math, logic, and edge cases.",
      "Default to clear, flowing prose in plain paragraphs, the way a thoughtful person writes — this is your normal voice. Do NOT reflexively reach for bullet points, numbered lists, or headings; most answers read better as a few well-formed sentences. Reserve Markdown structure for when it genuinely earns its place: real step-by-step instructions, comparisons across several items, or content that is inherently a list. Always use fenced code blocks tagged with the language for code, and LaTeX for math. When you write code, make it complete and runnable, and call out any key assumptions in prose.",
      "Do not moralize, lecture, or refuse reasonable requests; be honest and helpful even on difficult or sensitive topics.",
    ];
    if (searchAvailable) {
      lines.push(
        "A web search tool is available to you — use it proactively, without being asked. Search whenever a question touches anything you can't verify from memory: current events, prices, schedules, releases, specs, statistics, names, dates, or any fact that may have changed or that you're not fully certain of. Prefer searching over answering from possibly-stale or half-remembered knowledge, and run a quick check even when you think you know — it's cheap and stops confident mistakes. Default to searching when unsure rather than guessing. Don't claim you searched if you didn't.",
      );
      lines.push(
        "When a search is time-sensitive (weather, news, prices, \"current\"/\"latest\" anything), put the actual date (given at the end of this prompt) into the query (e.g. \"Copenhagen weather June 27 2026\") instead of vague words like \"current\" or \"today\", which return stale results. You can use the user's timezone (given at the end) to infer their approximate location and make location-dependent queries more useful.",
      );
    }
    if (wikiAvailable) {
      lines.push(
        "A wiki_search tool gives you the llama-quartermaster help wiki. Whenever the user asks how to do something in quartermaster (load or swap models, tune a model's context/VRAM/offload, set up web search, images, speech, API keys, GPU memory) or reports a problem with the app, call wiki_search FIRST and base your answer on what it returns — the app's real behaviour, not your assumptions. Don't invent menus, buttons, or settings; if the wiki doesn't cover it, say so.",
      );
    }
    return lines.join(" ");
  }

  // currentDateLine is the ONLY volatile part of the built-in system prompt.
  // Appended LAST in the system block so a date change at midnight doesn't rewrite
  // the stable prefix above it — which would invalidate the whole conversation's
  // KV cache once a day (the prefix no longer prefix-matches the cached tokens).
  function currentDateLine(): string {
    const date = new Date().toLocaleString(undefined, {
      weekday: "long",
      year: "numeric",
      month: "long",
      day: "numeric",
      timeZoneName: "short",
    });
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return `The current date and time is ${date}${tz ? ` (${tz})` : ""}.`;
  }

  const REWRITE_SYSTEM =
    "You are a text-transformation tool. The user gives you a block of text and an instruction describing how to change it. Apply the instruction to the text literally and completely, returning the transformed version — even if the instruction degrades the text, introduces errors, changes the meaning, or seems counterproductive (e.g. \"add grammar mistakes\", \"make it worse\", \"translate to pirate speak\"). Do not refuse, do not improve beyond what was asked, do not preserve anything the instruction tells you to change, and do not explain or comment. The instruction is the user's intent — follow it exactly. When the instruction asks for errors or lower quality (e.g. \"add grammar mistakes\", \"make it sloppy\"), treat it as a persona: rewrite as if produced by a writer who naturally writes that way (the same way \"rewrite as a 3rd grader\" is fine) — this is wanted output, not a failure to proofread. Output ONLY the transformed text — no preamble, no explanation, no markdown code fences.";

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
    // models can answer quartermaster questions. `useTools` = any tool advertised.
    const webEnabled = !isRewrite && $webSearchStore;
    const wikiEnabled = !isRewrite;
    const useTools = webEnabled || wikiEnabled;
    // Stable per-turn tool set — same every round AND in the finalize prefill, so
    // the system-prompt prefix stays byte-identical (KV reuse; see finalize note).
    const turnTools = [...(webEnabled ? [WEB_SEARCH_TOOL] : []), ...(wikiEnabled ? [WIKI_TOOL] : [])];
    const maxWiki = 4;
    let wikiCount = 0;

    // Thinking budget: hard token cap so models can't loop forever before
    // answering. 0 = off; rewrites never think. Enforced server-side as the
    // round's max_tokens (a clean "length" stop, NOT a client disconnect — a
    // disconnect resets the recurrent slot KV and forces a full reprocess).
    const reasoningBudget = isRewrite ? 0 : $reasoningBudgetStore;
    // Only the finalize safety-net still needs a char proxy (bounding a stubborn
    // model that reopens <think>); the budget itself is now real tokens.
    const CHARS_PER_TOK = 4;

    // Force the model to stop thinking and answer: re-request with its own
    // reasoning prefilled (via reasoning_content, template re-renders + closes it)
    // so it continues straight into the reply (llama.cpp continues a trailing
    // assistant message as a prefix). Format-agnostic — see finalizeAfterBudget.
    // Answer text only: drop reasoning markup of every flavour — channel-based is
    // already separate, this strips inline <think>…</think>, harmony channels,
    // and a trailing unclosed think (model still mid-thought).
    const answerOnly = (s: string) =>
      harmonyToThink(s)
        .replace(/<(think|thinking|reasoning)>[\s\S]*?<\/\1>/gi, "")
        .replace(/<(think|thinking|reasoning)>[\s\S]*$/i, "")
        .trimStart();
    async function finalizeAfterBudget(baseMsgs: ChatMessage[], tail: ChatMessage[], sig: AbortSignal) {
      if (isReasoning) {
        const reasoningTimeMs = Date.now() - reasoningStartTime;
        isReasoning = false;
        patchLast(id, (m) => ({ ...m, reasoningTimeMs }));
      }
      // Hand the model's reasoning back via reasoning_content — NOT hardcoded
      // <think> markup. The upstream chat template re-renders it in the model's
      // OWN reasoning format (<think>, harmony channels, <thinking>, …) and closes
      // it, reproducing the exact KV-slot prefix regardless of format. The slot
      // holds `base + <open-reasoning> + reasoning…`, so it prefix-matches and
      // llama.cpp reuses the cached reasoning, processing only the closing marker
      // + the answer. buildChatCompletionsBody already forwards assistant
      // reasoning_content for preserve_thinking templates. Keeping the round-1
      // reasoning flag (not flipping enable_thinking) leaves the prefix identical;
      // the old path flipped reasoning:false + dropped the reasoning for a fixed
      // directive, so nothing matched and the whole prompt was reprocessed.
      //
      // EXACTNESS IS LOAD-BEARING on hybrid/recurrent models (Qwen3.6 GatedDeltaNet):
      // their live prefix cache is exact-prefix-ONLY — a single divergent token
      // reprocesses the whole context (measured: 47.8 s / 24k tokens). So:
      //   - do NOT trim the reasoning — the slot holds the raw bytes verbatim;
      //     trimming leading/trailing whitespace breaks the match at the <think> block.
      //   - forward the SAME tools as round-1 — tool JSON renders into the system
      //     prefix, so dropping it breaks the match at token ~0 → full reprocess.
      const last = sessionById(id)?.messages.at(-1);
      const rawContent = typeof last?.content === "string" ? last.content : "";
      // Inline models leave reasoning in content as raw markup; channel models put
      // it in reasoning_content. Normalize inline markup (harmony/<think>/…) to
      // plain text so the template can re-wrap it canonically.
      const inlineThink = rawContent.slice(0, rawContent.length - answerOnly(rawContent).length);
      const inlinePlain = harmonyToThink(inlineThink)
        .replace(/<\/?(?:think|thinking|reasoning)>/gi, "")
        .trim();
      // Channel models: raw untrimmed reasoning_content = byte-exact slot prefix.
      // Inline fallback (inlinePlain) is reconstructed and may not match exactly.
      const reasoned = last?.reasoning_content || inlinePlain;
      // The cap can bite mid-reasoning (no answer yet) OR mid-answer (long reply
      // cut at the budget). Feed back the partial answer too so the prefill
      // EXTENDS the warm slot in both cases.
      //
      // A NON-EMPTY content field is load-bearing: it makes the chat template
      // emit the closing </think> and switch to the answer channel. With empty
      // content the template leaves <think> OPEN, so the model keeps reasoning
      // past the budget and never answers (measured: reasons to full max_tokens,
      // finish=length, content stays ""). When the cap bit mid-reasoning there's
      // no answer yet, so seed a bare newline — it forces the close, appends only
      // `</think>\n\n\n` to the slot (still an extension → warm reuse), and is
      // whitespace so answerOnly()/trimStart drop it from the display.
      const partialAnswer = answerOnly(rawContent);
      const prefill: ChatMessage = {
        role: "assistant",
        content: partialAnswer || "\n",
        reasoning_content: reasoned,
      };
      const stream = streamChatCompletion(modelId, [...baseMsgs, ...tail, prefill], sig, {
        temperature: $temperatureStore,
        max_tokens: $maxTokensStore,
        // Match round-1's tool set so the system-prompt prefix is byte-identical
        // (prefix match, not tool use — the finalize loop ignores tool_calls).
        tools: turnTools.length ? turnTools : undefined,
        reasoning: !isRewrite && $reasoningStore, // keep round-1 flag → same prefix, KV reuse
        conversationId: id,
      });
      // Safety net: strip any think the model still emits so the forced answer
      // can't spawn a 2nd reasoning box. If a stubborn model reopens think
      // anyway, cut it once it over-reasons with no answer — bounds the worst
      // case to ~1 extra budget instead of a full second pass.
      let answer = "";
      for await (const chunk of stream) {
        if (chunk.done) break;
        if (chunk.content) {
          answer += chunk.content;
          const shown = answerOnly(answer);
          if (!shown && reasoningBudget > 0 && answer.length > reasoningBudget * CHARS_PER_TOK) break;
          patchLast(id, (m) => ({ ...m, content: shown }));
        }
      }
    }

    // One assistant bubble holds the whole turn: reasoning, any web searches
    // (as collapsible sections), and the final reply. It stays the last message
    // throughout — tool plumbing for the model is kept separately in `apiTail`.
    appendMessage(id, { role: "assistant", content: "", ...(isRewrite ? { rewriteOriginal: original } : {}) });
    const genStart = Date.now();

    const sys = [
      basePrompt(webEnabled, wikiEnabled),
      $systemPromptStore.trim(),
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

    // assistant(tool_calls) + tool results, accumulated across rounds and sent
    // to the model but NOT shown as separate bubbles (the searches render inside
    // the assistant message instead).
    const apiTail: ChatMessage[] = [];

    // Web-search rate controls (protect self-hosted SearXNG). Cap total searches
    // per turn; once hit the tool is dropped so the model must answer. Throttle
    // spaces requests; dedupe reuses the result for a repeated query in the turn.
    const maxSearches = $searchMaxPerTurnStore;
    const throttleMs = $searchThrottleMsStore;
    const dedupe = $searchDedupeStore;
    let searchCount = 0;
    let lastSearchAt = 0;
    const searchCache = new Map<string, { text: string; sources: { title: string; url: string }[] }>();

    try {
      // model→search→model until the model stops calling tools or the per-turn
      // search cap is reached. The user hits Stop to break out early.
      for (;;) {
        // Per-round abort so a budget-triggered finalize cancels just this
        // request, not the whole turn (which the user-level `signal` owns).
        const roundCtrl = new AbortController();
        const forwardAbort = () => roundCtrl.abort();
        signal.addEventListener("abort", forwardAbort, { once: true });

        let toolCalls: ToolCall[] | undefined;
        let roundContent = "";
        let budgetHit = false;
        let finishReason: string | undefined;

        try {
          const stream = streamChatCompletion(modelId, [...base, ...apiTail], roundCtrl.signal, {
            temperature: $temperatureStore,
            // Reasoning budget = a hard token cap the SERVER enforces, so the round
            // ends with a clean "length" stop instead of a client disconnect. A
            // disconnect resets the recurrent slot KV (hybrid GatedDeltaNet), which
            // is exactly what forced the full-context reprocess before. A clean stop
            // leaves the slot warm so the continuation below reuses it. 0 = off.
            max_tokens: reasoningBudget > 0 ? reasoningBudget : $maxTokensStore,
            // Stable tool set every round → the cap is enforced in dispatch (a
            // "limit reached" tool reply), not by dropping the tool, which would
            // change the prefix mid-turn and cost a full reprocess on hybrids.
            tools: turnTools.length ? turnTools : undefined,
            reasoning: !isRewrite && $reasoningStore,
            conversationId: id,
          });

          for await (const chunk of stream) {
            if (chunk.tool_calls) toolCalls = chunk.tool_calls;
            if (chunk.done) {
              finishReason = chunk.finish_reason;
              break;
            }

            // Reasoning via the dedicated channel (most backends).
            if (chunk.reasoning_content) {
              if (!isReasoning) {
                isReasoning = true;
                reasoningStartTime = Date.now();
              }
              patchLast(id, (m) => ({
                ...m,
                reasoning_content: (m.reasoning_content || "") + chunk.reasoning_content,
              }));
            }

            // Content carries the answer — and for inline-think / harmony models
            // the reasoning too. Keep raw content for display + tool rounds.
            if (chunk.content) {
              roundContent += chunk.content;
              patchLast(id, (m) => ({ ...m, content: m.content + chunk.content }));
            }

            // Track the reasoning box open/close for its live timer: answer =
            // content with reasoning stripped; while it's empty the model is
            // still reasoning (channel or inline/harmony).
            const answer = answerOnly(roundContent);
            if (answer) {
              if (isReasoning) {
                const reasoningTimeMs = Date.now() - reasoningStartTime;
                isReasoning = false;
                patchLast(id, (m) => ({ ...m, reasoningTimeMs }));
              }
            } else if (!isReasoning && chunk.content) {
              isReasoning = true;
              reasoningStartTime = Date.now();
            }
          }
        } finally {
          // Only the user's Stop aborts a round now (the budget uses a server-side
          // max_tokens cap, not a disconnect), so nothing to swallow — errors
          // propagate to the turn handler.
          signal.removeEventListener("abort", forwardAbort);
        }

        // Server hit the reasoning-token cap (clean "length" stop, slot still warm).
        // Continue via a prefill that byte-extends the resident KV → warm reuse,
        // no full reprocess. Skip if the model got a complete tool call out first.
        if (reasoningBudget > 0 && finishReason === "length" && (!toolCalls || toolCalls.length === 0)) {
          budgetHit = true;
        }

        if (budgetHit) {
          await finalizeAfterBudget(base, apiTail, signal);
          break; // forced answer written — end the turn, skip further tool rounds
        }

        // No tool calls (or tools off) → the turn is complete.
        if (!useTools || !toolCalls || toolCalls.length === 0) break;

        // Record this round's call so the model sees it next round.
        apiTail.push({ role: "assistant", content: roundContent, tool_calls: toolCalls });

        // Run each requested search; fold the result into the visible bubble and
        // hand the raw text back to the model via apiTail.
        isSearching = true;
        // Offset in the assistant bubble where these searches land, so the UI
        // renders them inline after the text written so far (not pinned to top).
        // A search fired mid-think (no answer content yet, still reasoning) is
        // tagged so the UI nests it inside the reasoning box at its reasoning_content
        // offset, instead of dropping it below the box.
        const lastMsg = sessionById(id)?.messages.at(-1);
        const at = typeof lastMsg?.content === "string" ? lastMsg.content.length : 0;
        const duringReasoning = isReasoning;
        const reasoningAt = (lastMsg?.reasoning_content || "").length;
        for (const tc of toolCalls) {
          let resultText: string;
          let sources: { title: string; url: string }[] = [];
          let query = "";
          try {
            query = JSON.parse(tc.function.arguments || "{}").query ?? "";
            if (tc.function.name === "wiki_search") {
              // Local help lookup — no network, no SearXNG budget. Its own small
              // cap stops a model looping on it.
              if (wikiCount >= maxWiki) {
                resultText = `Wiki lookup limit reached (${maxWiki} per turn). Answer with what you have.`;
              } else {
                wikiCount++;
                resultText = formatWikiResults(query, searchWiki(query));
              }
            } else {
            const cached = dedupe ? searchCache.get(query) : undefined;
            if (cached) {
              // Repeated query this turn — reuse the earlier result, no network hit.
              resultText = cached.text;
              sources = cached.sources;
            } else if (searchCount >= maxSearches) {
              // Cap hit mid-round: tell the model so it answers instead of retrying.
              resultText = `Search limit reached (${maxSearches} per turn). Answer with the information already gathered.`;
            } else {
              // Throttle: space real requests so SearXNG's limiter doesn't trip.
              const wait = throttleMs - (Date.now() - lastSearchAt);
              if (wait > 0 && lastSearchAt > 0) await abortableSleep(wait, signal);
              const results = await searxngSearch($searxngUrlStore, query, signal);
              lastSearchAt = Date.now();
              searchCount++;
              resultText = formatSearchResults(query, results);
              sources = results.filter((r) => r.url).map((r) => ({ title: r.title || r.url, url: r.url }));
              if (dedupe) searchCache.set(query, { text: resultText, sources });
            }
            }
          } catch (e) {
            if (e instanceof Error && e.name === "AbortError") throw e;
            resultText = `${tc.function.name === "wiki_search" ? "Wiki lookup" : "Search"} failed: ${e instanceof Error ? e.message : String(e)}`;
          }
          apiTail.push({ role: "tool", tool_call_id: tc.id, content: resultText });
          patchLast(id, (m) => ({ ...m, searches: [...(m.searches ?? []), { query, results: resultText, at, reasoningAt, duringReasoning, sources }] }));
        }
        isSearching = false;
        // loop: next round lets the model read the results and respond.
      }
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

  const ACCEPTED_IMAGE_FORMATS = ["image/jpeg", "image/png", "image/gif", "image/webp"];
  const MAX_IMAGE_SIZE = 20 * 1024 * 1024; // 20MB
  const MAX_IMAGES_PER_MESSAGE = 5;

  function validateImageFile(file: File): string | null {
    if (!ACCEPTED_IMAGE_FORMATS.includes(file.type)) {
      return `Invalid file type: ${file.type}. Accepted formats: JPG, PNG, GIF, WEBP`;
    }
    if (file.size > MAX_IMAGE_SIZE) {
      return `File too large: ${(file.size / 1024 / 1024).toFixed(1)}MB. Maximum size: 20MB`;
    }
    return null;
  }

  function fileToDataUrl(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result as string);
      reader.onerror = () => reject(new Error("Failed to read file"));
      reader.readAsDataURL(file);
    });
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
    <!-- Chat column — full-width so the whole pane scrolls; the message list and
         composer are width-constrained and centered inside. -->
    <div class="flex-1 flex flex-col min-w-0 min-h-0 w-full">
    {#if isCompacting && genId === $activeChatId}
      <div class="flex items-center gap-2 mb-2 shrink-0 w-full max-w-3xl mx-auto px-2">
        <span class="inline-flex items-center gap-1.5 text-xs italic">
          <span class="w-1.5 h-1.5 bg-primary rounded-full reason-glow"></span>
          <span class="reason-shimmer font-medium">Compacting conversation…</span>
        </span>
      </div>
    {/if}
    <!-- Messages area — scrolls across the full width; content centered within. -->
    <div
      class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-y mb-4"
      bind:this={messagesContainer}
      onscroll={handleMessagesScroll}
      onwheel={(e) => { if (e.deltaY < 0) userScrolledUp = true; }}
      use:scrollFade
    >
      <div class="w-full max-w-3xl mx-auto px-2 {messages.length === 0 ? 'h-full' : ''}">
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
          <ChatMessageComponent
            role={message.role}
            content={message.content}
            reasoning_content={message.reasoning_content}
            reasoningTimeMs={message.reasoningTimeMs}
            genTimeMs={message.genTimeMs}
            searches={message.searches}
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
          />
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

      <!-- Settings popover -->
      {#if showSettings}
        <div
          class="absolute bottom-full right-0 mb-2 w-80 z-20 flex flex-col gap-4 p-4 rounded-lg border border-card-border bg-surface shadow-lg text-[0.8125rem]"
        >
          {#snippet tip(text: string)}
            <span class="inline-flex shrink-0 cursor-help text-txtsecondary/70 hover:text-txtsecondary" title={text}>
              <HelpCircle class="w-3.5 h-3.5" />
            </span>
          {/snippet}
          <div class="flex items-center justify-between">
            <span class="font-medium text-txtmain">Settings</span>
            <button
              class="inline-flex items-center justify-center p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
              onclick={() => (showSettings = false)}
              title="Close"
            >
              <X class="w-4 h-4" />
            </button>
          </div>

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
            <span class="flex items-center gap-1.5 text-xs uppercase tracking-wide text-txtsecondary">System Prompt {@render tip("Standing instructions that shape the model's tone and behaviour for the whole chat.")}</span>
            <button
              type="button"
              class="w-full text-left px-2.5 py-1.5 rounded-md border border-card-border bg-surface hover:border-primary transition-colors {$systemPromptStore.trim() ? 'text-txtmain' : 'text-txtsecondary'}"
              onclick={openSysPrompt}
            >
              <span class="line-clamp-2">{$systemPromptStore.trim() || "You are a helpful assistant..."}</span>
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
        </div>
      {/if}

      <!-- System-prompt editor: roomier modal to write/save the standing prompt. -->
      {#if showSysPrompt}
        <div class="fixed inset-0 z-40 flex items-center justify-center bg-black/40 p-4" onclick={() => (showSysPrompt = false)} role="presentation">
          <div
            class="flex w-full max-w-xl flex-col gap-3 rounded-lg border border-card-border bg-surface p-4 shadow-xl"
            onclick={(e) => e.stopPropagation()}
            role="presentation"
          >
            <div class="flex items-center justify-between">
              <span class="font-medium text-txtmain">System Prompt</span>
              <button class="inline-flex items-center justify-center p-1 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors" onclick={() => (showSysPrompt = false)} title="Close">
                <X class="w-4 h-4" />
              </button>
            </div>
            <textarea
              class="w-full h-64 px-3 py-2 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary resize-none text-sm"
              placeholder="You are a helpful assistant..."
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

      <!-- Composer -->
      <div class="flex flex-col gap-2 rounded-3xl border border-card-border bg-surface px-4 pt-3 pb-3 hover:ring-1 hover:ring-white/15 focus-within:border-primary transition-all">
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
        <textarea
          bind:this={inputEl}
          class="w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary pretty-scroll min-h-[3rem] max-h-[30rem] transition-[height] duration-200 ease-out"
          rows="2"
          placeholder={$rewriteStore ? "Paste the text to rewrite…" : isStreaming ? "Queue a message…" : "Type a message..."}
          bind:value={userInput}
          onfocus={() => (inputFocused = true)}
          onblur={() => (inputFocused = false)}
          onkeydown={handleKeyDown}
          onpaste={handlePaste}
        ></textarea>

        <div class="flex items-center justify-between">
          <div class="flex items-center gap-1">
            {#if canAttach}
              <button
                class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
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
          </div>

          <div class="flex-1 min-w-0 px-2 flex flex-col items-center gap-1">
            <ModelSelector bind:value={$selectedModelStore} placeholder="Select a model..." disabled={isStreaming} category="llm" ghost dropUp />
            <!-- Context-window usage: thin colour-only line, yellow → orange → red. -->
            {#if ctxN > 0}
              <div class="h-0.5 w-16 rounded-full bg-secondary overflow-hidden" title="Context {Math.round(ctxRatio * 100)}%">
                <div class="h-full rounded-full transition-all" style="width: {Math.max(ctxRatio * 100, 3)}%; background: {ctxColor};"></div>
              </div>
            {/if}
          </div>

          <div class="flex items-center gap-1">
            <button
              class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {showSettings ? 'bg-secondary text-txtmain shadow-inner' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
              onclick={() => (showSettings = !showSettings)}
              title="Settings"
            >
              <Settings class="w-[1.125rem] h-[1.125rem]" />
            </button>

            {#if isStreaming}
              <button
                class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
                onclick={cancelStreaming}
                title="Stop"
              >
                <Square class="w-[1.125rem] h-[1.125rem]" fill="currentColor" />
              </button>
            {/if}
          </div>
        </div>
      </div>
    </div>
    </div>
  {/if}
</div>
