<script lang="ts">
  import { get } from "svelte/store";
  import { models, backendMetrics } from "../../stores/api";
  import { summarizeConversation, generateTitle, COMPACT_AT, KEEP_RECENT } from "../../lib/chatCompact";
  import {
    selectedModelStore,
    selectedTabStore,
    systemPromptStore,
    temperatureStore,
    maxTokensStore,
    webSearchStore,
    reasoningStore,
    searxngUrlStore,
    rewriteStore,
    rewriteInstructionStore,
  } from "../../stores/playground";
  import {
    chatSessions,
    activeChatId,
    newChatId,
    deriveTitle,
    type ChatSession,
  } from "../../stores/chatHistory";
  import { streamChatCompletion } from "../../lib/chatApi";
  import { WEB_SEARCH_TOOL, searxngSearch, formatSearchResults } from "../../lib/webSearch";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import { getTextContent } from "../../lib/types";
  import type { ChatMessage, ContentPart, ToolCall } from "../../lib/types";
  import { Settings, Paperclip, Square, MessagesSquare, X, Search, Brain, Clock, PenLine } from "lucide-svelte";
  import ChatMessageComponent from "./ChatMessage.svelte";
  import ModelSelector from "./ModelSelector.svelte";
  import { modelCategory } from "../../lib/modelUtils";
  import { scrollFade } from "../../lib/scrollFade";

  // Load (or create) the active conversation, migrating the legacy single-chat
  // store the first time. Returns the messages for the active session.
  function initChats(): ChatMessage[] {
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
      if (sessions.length > 0) {
        id = sessions[0].id;
      } else {
        const s: ChatSession = { id: newChatId(), title: "New chat", messages: [], updatedAt: Date.now() };
        chatSessions.set([s]);
        id = s.id;
      }
      activeChatId.set(id);
    }
    return get(chatSessions).find((s) => s.id === id)?.messages ?? [];
  }

  let messages = $state<ChatMessage[]>(initChats());
  // Auto-compact state for the active session (see lib/chatCompact). The full
  // `messages` list is always kept; `compactedCount` marks how many leading
  // messages are represented by `summary` and therefore NOT resent to the model.
  const initSession = get(chatSessions).find((s) => s.id === get(activeChatId));
  let summary = $state<string>(initSession?.summary ?? "");
  let compactedCount = $state<number>(initSession?.compactedCount ?? 0);
  let isCompacting = $state(false);
  // Which session `messages` currently mirrors. The history list (in the nav
  // rail) drives selection by setting activeChatId; this component reacts by
  // saving the old session and loading the new one.
  let loadedId = $state(get(activeChatId));

  // Write the working `messages` back into its session in the history store.
  function persistCurrent() {
    const id = loadedId;
    const snapshot = $state.snapshot(messages) as ChatMessage[];
    chatSessions.update((sessions) => {
      const idx = sessions.findIndex((s) => s.id === id);
      if (idx === -1) return sessions; // session was deleted — don't resurrect it
      const prev = sessions[idx];
      const title = prev.titled ? prev.title : deriveTitle(snapshot);
      const updated: ChatSession = { id, title, messages: snapshot, updatedAt: Date.now(), summary: summary || undefined, compactedCount: compactedCount || undefined, titled: prev.titled };
      const copy = [...sessions];
      copy[idx] = updated;
      return copy;
    });
  }

  // External selection (history list in the rail sets activeChatId): save the
  // session we're showing, then load the newly-selected one.
  $effect(() => {
    const id = $activeChatId;
    if (id === loadedId) return;
    if (isStreaming) cancelStreaming();
    persistCurrent();
    const s = get(chatSessions).find((x) => x.id === id);
    messages = s ? (structuredClone($state.snapshot(s.messages)) as ChatMessage[]) : [];
    summary = s?.summary ?? "";
    compactedCount = s?.compactedCount ?? 0;
    isReasoning = false;
    reasoningStartTime = 0;
    userScrolledUp = false;
    queued = [];
    loadedId = id;
  });

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
  let isStreaming = $state(false);
  let isReasoning = $state(false);
  let isSearching = $state(false);
  let reasoningStartTime = $state<number>(0);
  let abortController = $state<AbortController | null>(null);
  let messagesContainer: HTMLDivElement | undefined = $state();
  let inputEl: HTMLTextAreaElement | undefined = $state();
  let showSettings = $state(false);
  let attachedImages = $state<string[]>([]);
  let fileInput = $state<HTMLInputElement | null>(null);
  let imageError = $state<string | null>(null);

  let hasModels = $derived($models.some((m) => !m.unlisted));
  let selectedModelName = $derived(
    $models.find((m) => m.id === $selectedModelStore)?.name || $selectedModelStore
  );
  // Loaded → an empty stream means the model is generating, not swapping in.
  let modelReady = $derived(!!$backendMetrics[$selectedModelStore]);
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
    const listed = $models.filter(
      (m) => !m.unlisted && (m.peerID || (modelCategory(m) === "llm" && !m.capabilities?.reranker))
    );
    if (listed.length === 0) return;
    if (!listed.some((m) => m.id === $selectedModelStore)) {
      const ready = listed.find((m) => m.state === "ready");
      selectedModelStore.set((ready ?? listed[0]).id);
    }
  });

  $effect(() => {
    playgroundStores.chatStreaming.set(isStreaming);
  });

  // Auto-grow the composer textarea; tracks userInput so it also shrinks back
  // after a send clears the value. Guard scrollHeight === 0 (tab is display:none
  // at mount) — otherwise the height locks at 0px and never recovers when the
  // tab is shown, leaving an invisible, untypeable textarea.
  $effect(() => {
    userInput;
    $selectedTabStore; // re-run when this tab becomes visible again
    if (inputEl) {
      inputEl.style.height = "auto";
      if (inputEl.scrollHeight > 0) {
        inputEl.style.height = Math.min(inputEl.scrollHeight, 320) + "px";
      }
    }
  });

  function handleMessagesScroll() {
    if (!messagesContainer) return;
    const { scrollTop, scrollHeight, clientHeight } = messagesContainer;
    // Consider "at bottom" if within 40px of the bottom
    userScrolledUp = scrollHeight - scrollTop - clientHeight > 40;
  }

  // Auto-scroll when messages change — skip if user scrolled up
  $effect(() => {
    if (messages.length > 0 && messagesContainer && !userScrolledUp) {
      messagesContainer.scrollTo({
        top: messagesContainer.scrollHeight,
        behavior: isStreaming ? "instant" : "smooth",
      });
    }
  });

  // Persist the active conversation into the history store (throttled to 1.5s).
  let lastSaveTime = 0;
  $effect(() => {
    messages; // track
    const elapsed = Date.now() - lastSaveTime;
    if (elapsed >= 1500) {
      persistCurrent();
      lastSaveTime = Date.now();
      return;
    }
    const timer = setTimeout(() => {
      persistCurrent();
      lastSaveTime = Date.now();
    }, 1500 - elapsed);
    return () => clearTimeout(timer);
  });

  // Rewrite mode: prose (userInput) + a "how to help" instruction. The user
  // message carries the instruction; content is the original prose so the
  // assistant turn can render a side-by-side diff against its rewrite.
  async function sendRewrite() {
    const prose = userInput.trim();
    if (!prose || !$selectedModelStore || isStreaming) return;
    userScrolledUp = false;
    messages = [
      ...messages,
      { role: "user", content: prose, rewriteInstruction: $rewriteInstructionStore.trim() },
    ];
    userInput = "";
    await regenerateFromIndex(messages.length - 1);
  }

  async function sendMessage() {
    if ($rewriteStore) {
      await sendRewrite();
      return;
    }
    const trimmedInput = userInput.trim();
    if ((!trimmedInput && attachedImages.length === 0) || !$selectedModelStore) return;

    // Streaming: queue the text and send it once the current turn drains.
    // (Image attachments aren't queued — the attach button is disabled mid-turn.)
    if (isStreaming) {
      if (trimmedInput) {
        queued = [...queued, trimmedInput];
        userInput = "";
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

    // Add user message
    messages = [...messages, { role: "user", content }];
    userInput = "";
    attachedImages = [];
    imageError = null;

    // Generate response from the new user message
    await regenerateFromIndex(messages.length - 1);
  }

  function cancelStreaming() {
    abortController?.abort();
  }

  // Web search runs as OpenAI tool-calling, which only the chat/completions
  // endpoint speaks. Other endpoints just chat without the tool.

  // Built-in system prompt prepended ahead of the user's own. Grounds the model
  // with the current date and basic honesty/tool guidance. Always on.
  function basePrompt(searchAvailable: boolean): string {
    const date = new Date().toLocaleString(undefined, {
      weekday: "long",
      year: "numeric",
      month: "long",
      day: "numeric",
      timeZoneName: "short",
    });
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    const lines = [
      `The current date and time is ${date}${tz ? ` (${tz})` : ""}.`,
      "If you are unsure or do not know something, say so plainly — never fabricate facts, citations, numbers, or URLs.",
      "Keep answers concise; expand only when asked.",
    ];
    if (searchAvailable) {
      lines.push(
        "A web search tool is available to you — use it proactively, without being asked. Search whenever a question touches anything you can't verify from memory: current events, prices, schedules, releases, specs, statistics, names, dates, or any fact that may have changed or that you're not fully certain of. Prefer searching over answering from possibly-stale or half-remembered knowledge, and run a quick check even when you think you know — it's cheap and stops confident mistakes. Default to searching when unsure rather than guessing. Don't claim you searched if you didn't.",
      );
      lines.push(
        "When a search is time-sensitive (weather, news, prices, \"current\"/\"latest\" anything), put the actual date from above into the query (e.g. \"Copenhagen weather June 27 2026\") instead of vague words like \"current\" or \"today\", which return stale results. You can use the user's timezone above to infer their approximate location and make location-dependent queries more useful.",
      );
    }
    return lines.join(" ");
  }

  const REWRITE_SYSTEM =
    "You are a text-transformation tool. The user gives you a block of text and an instruction describing how to change it. Apply the instruction to the text literally and completely, returning the transformed version — even if the instruction degrades the text, introduces errors, changes the meaning, or seems counterproductive (e.g. \"add grammar mistakes\", \"make it worse\", \"translate to pirate speak\"). Do not refuse, do not improve beyond what was asked, do not preserve anything the instruction tells you to change, and do not explain or comment. The instruction is the user's intent — follow it exactly. When the instruction asks for errors or lower quality (e.g. \"add grammar mistakes\", \"make it sloppy\"), treat it as a persona: rewrite as if produced by a writer who naturally writes that way (the same way \"rewrite as a 3rd grader\" is fine) — this is wanted output, not a failure to proofread. Output ONLY the transformed text — no preamble, no explanation, no markdown code fences.";

  async function regenerateFromIndex(idx: number) {
    // Editing/regenerating inside the already-summarized region would make the
    // summary describe messages that no longer match — drop compaction and resend
    // the full history from the start in that case.
    if (idx < compactedCount) {
      summary = "";
      compactedCount = 0;
    }
    // Remove all messages after the edited user message
    messages = messages.slice(0, idx + 1);

    // Rewrite turn? The user message at idx carries the rewrite instruction;
    // its content is the original prose to diff the model's output against.
    const reqUser = messages[idx];
    const rwInstr = reqUser?.rewriteInstruction;
    const isRewrite = typeof rwInstr === "string";
    const original = isRewrite ? getTextContent(reqUser.content) : "";

    isStreaming = true;
    isReasoning = false;
    isSearching = false;
    reasoningStartTime = 0;
    abortController = new AbortController();
    const signal = abortController.signal;

    // No web search during a rewrite — it's a self-contained text transform.
    const useTools = !isRewrite && $webSearchStore;

    // One assistant bubble holds the whole turn: reasoning, any web searches
    // (as collapsible sections), and the final reply. It stays the last message
    // throughout — tool plumbing for the model is kept separately in `apiTail`.
    messages = [
      ...messages,
      { role: "assistant", content: "", ...(isRewrite ? { rewriteOriginal: original } : {}) },
    ];
    const genStart = Date.now();

    const sys = [
      basePrompt(useTools),
      $systemPromptStore.trim(),
      summary && `Summary of earlier conversation:\n${summary}`,
      // Rewrite turns keep the full conversation for context (setting, characters,
      // goals discussed earlier) but add the transform-tool directive on top.
      isRewrite && REWRITE_SYSTEM,
    ]
      .filter(Boolean)
      .join("\n\n");
    const base: ChatMessage[] = [];
    if (sys) base.push({ role: "system", content: sys });
    // History up to (not incl.) the live assistant bubble.
    const history = messages.slice(compactedCount, -1);
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

    try {
      // ponytail: unbounded by request — model→search→model until the model
      // stops calling tools. No round cap; the user hits Stop to break out.
      for (;;) {
        const stream = streamChatCompletion($selectedModelStore, [...base, ...apiTail], signal, {
          temperature: $temperatureStore,
          max_tokens: $maxTokensStore,
          tools: useTools ? [WEB_SEARCH_TOOL] : undefined,
          reasoning: !isRewrite && $reasoningStore,
        });

        let toolCalls: ToolCall[] | undefined;
        let roundContent = "";

        for await (const chunk of stream) {
          if (chunk.tool_calls) toolCalls = chunk.tool_calls;
          if (chunk.done) break;

          // Handle reasoning content
          if (chunk.reasoning_content) {
            if (!isReasoning) {
              isReasoning = true;
              reasoningStartTime = Date.now();
            }
            messages = messages.map((msg, i) =>
              i === messages.length - 1
                ? { ...msg, reasoning_content: (msg.reasoning_content || "") + chunk.reasoning_content }
                : msg
            );
          }

          // Handle regular content - end reasoning phase when we get content
          if (chunk.content) {
            if (isReasoning) {
              const reasoningTimeMs = Date.now() - reasoningStartTime;
              isReasoning = false;
              messages = messages.map((msg, i) =>
                i === messages.length - 1 ? { ...msg, reasoningTimeMs } : msg
              );
            }
            roundContent += chunk.content;
            messages = messages.map((msg, i) =>
              i === messages.length - 1 ? { ...msg, content: msg.content + chunk.content } : msg
            );
          }
        }

        // No tool calls (or tools off) → the turn is complete.
        if (!useTools || !toolCalls || toolCalls.length === 0) break;

        // Record this round's call so the model sees it next round.
        apiTail.push({ role: "assistant", content: roundContent, tool_calls: toolCalls });

        // Run each requested search; fold the result into the visible bubble and
        // hand the raw text back to the model via apiTail.
        isSearching = true;
        for (const tc of toolCalls) {
          let resultText: string;
          let query = "";
          try {
            query = JSON.parse(tc.function.arguments || "{}").query ?? "";
            const results = await searxngSearch($searxngUrlStore, query, signal);
            resultText = formatSearchResults(query, results);
          } catch (e) {
            if (e instanceof Error && e.name === "AbortError") throw e;
            resultText = `Search failed: ${e instanceof Error ? e.message : String(e)}`;
          }
          apiTail.push({ role: "tool", tool_call_id: tc.id, content: resultText });
          messages = messages.map((msg, i) =>
            i === messages.length - 1
              ? { ...msg, searches: [...(msg.searches ?? []), { query, results: resultText }] }
              : msg
          );
        }
        isSearching = false;
        // loop: next round lets the model read the results and respond.
      }
      // Turn complete — fold older turns into the summary if near the ctx limit.
      await maybeCompact(signal);
      // First exchange done → let the model name the chat.
      void maybeTitle(signal);
    } catch (error) {
      if (error instanceof Error && error.name === "AbortError") {
        // User cancelled, keep partial response
        // If we were still reasoning, record the time
        if (isReasoning && reasoningStartTime > 0) {
          const reasoningTimeMs = Date.now() - reasoningStartTime;
          messages = messages.map((msg, i) =>
            i === messages.length - 1
              ? { ...msg, reasoningTimeMs }
              : msg
          );
        }
        // Cancelled before the model wrote anything → don't leave an empty bubble.
        const last = messages[messages.length - 1];
        if (last?.role === "assistant" && typeof last.content === "string" && last.content.trim() === "") {
          messages = messages.map((msg, i) =>
            i === messages.length - 1
              ? { ...msg, content: "_Cancelled — what did you want instead?_" }
              : msg
          );
        }
      } else {
        // Show error in the assistant message
        const errorMessage = error instanceof Error ? error.message : "An error occurred";
        messages = messages.map((msg, i) =>
          i === messages.length - 1
            ? { ...msg, content: msg.content + `\n\n**Error:** ${errorMessage}` }
            : msg
        );
      }
    } finally {
      const genTimeMs = Date.now() - genStart;
      messages = messages.map((msg, i) =>
        i === messages.length - 1 && msg.role === "assistant" ? { ...msg, genTimeMs } : msg
      );
      isStreaming = false;
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
      messages = [...messages, { role: "user", content: next }];
      await regenerateFromIndex(messages.length - 1);
    }
  }

  // Name the chat from the opening exchange, once. Best-effort: any failure or
  // empty result leaves the first-message heuristic title in place.
  async function maybeTitle(signal: AbortSignal) {
    const id = loadedId;
    const session = get(chatSessions).find((s) => s.id === id);
    if (!session || session.titled) return;
    if (!messages.some((m) => m.role === "assistant" && typeof m.content === "string" && m.content.trim())) return;
    const title = await generateTitle($selectedModelStore, $state.snapshot(messages) as ChatMessage[], signal);
    if (!title) return;
    chatSessions.update((sessions) => {
      const idx = sessions.findIndex((s) => s.id === id);
      if (idx === -1) return sessions;
      const copy = [...sessions];
      copy[idx] = { ...copy[idx], title, titled: true };
      return copy;
    });
  }

  // Auto-compact: once the live server KV usage crosses COMPACT_AT, fold every
  // message up to the last KEEP_RECENT into the running summary by advancing the
  // compaction boundary. The messages stay in the list (UI keeps showing them);
  // they just stop being resent to the model. Best-effort: any failure leaves
  // the conversation untouched.
  async function maybeCompact(signal: AbortSignal) {
    const bm = get(backendMetrics)[$selectedModelStore];
    if (!bm || !bm.n_ctx || bm.kv_cache_usage_ratio < COMPACT_AT) return;

    // Snap the boundary forward to a user message so the kept slice starts a
    // clean turn (never an orphaned assistant/tool reply whose tool_calls were
    // summarized away).
    let boundary = messages.length - KEEP_RECENT;
    while (boundary < messages.length && messages[boundary].role !== "user") boundary++;
    if (boundary <= compactedCount) return; // nothing new to summarize

    // Summarize only the newly-folded slice; `summary` already covers the prefix.
    const fresh = messages.slice(compactedCount, boundary);
    isCompacting = true;
    try {
      const next = await summarizeConversation($selectedModelStore, fresh, summary, signal);
      if (signal.aborted) return;
      summary = next;
      compactedCount = boundary;
      persistCurrent();
    } catch (e) {
      if (e instanceof Error && e.name === "AbortError") throw e;
      console.error("auto-compact failed:", e);
    } finally {
      isCompacting = false;
    }
  }

  async function editMessage(idx: number, newContent: string) {
    if (isStreaming || !$selectedModelStore) return;

    // Update the user message at the specified index
    messages = messages.map((msg, i) =>
      i === idx ? { ...msg, content: newContent } : msg
    );

    // Trigger a new chat request with the updated messages
    await regenerateFromIndex(idx);
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
</script>

<div class="flex flex-col h-full">
  <!-- Empty state for no models configured -->
  {#if !hasModels}
    <div class="flex-1 flex flex-col items-center justify-center gap-3 text-txtsecondary">
      <MessagesSquare class="w-10 h-10 opacity-40" strokeWidth={1.5} />
      <p>No models configured. Add models to your configuration to start chatting.</p>
    </div>
  {:else}
    <!-- Chat column — width-constrained and centered, like claude.ai -->
    <div class="flex-1 flex flex-col min-w-0 min-h-0 w-full max-w-3xl mx-auto">
    {#if isCompacting}
      <div class="flex items-center gap-2 mb-2 shrink-0">
        <span class="inline-flex items-center gap-1.5 text-xs text-txtsecondary">
          <span class="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
          Compacting conversation…
        </span>
      </div>
    {/if}
    <!-- Messages area -->
    <div
      class="flex-1 min-h-0 overflow-y-auto pretty-scroll scroll-fade-y mb-4 px-2"
      bind:this={messagesContainer}
      onscroll={handleMessagesScroll}
      use:scrollFade
    >
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
            isStreaming={isStreaming && idx === messages.length - 1 && message.role === "assistant"}
            isReasoning={isReasoning && idx === messages.length - 1 && message.role === "assistant"}
            isSearching={isSearching && idx === messages.length - 1 && message.role === "assistant"}
            modelReady={modelReady}
            onEdit={message.role === "user" && message.rewriteInstruction == null ? (newContent) => editMessage(idx, newContent) : undefined}
            onRegenerate={message.role === "assistant" && idx > 0 && messages[idx - 1].role === "user"
              ? () => regenerateFromIndex(idx - 1)
              : undefined}
          />
        {/each}
      {/if}
    </div>

    <!-- Input area — narrower than the message column, taller composer. -->
    <div class="shrink-0 relative w-full max-w-2xl mx-auto">
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

          <div class="flex flex-col gap-1">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Model</span>
            <ModelSelector bind:value={$selectedModelStore} placeholder="Select a model..." disabled={isStreaming} category="llm" compact />
          </div>

          <div class="flex flex-col gap-1.5">
            <label class="flex justify-between text-xs uppercase tracking-wide text-txtsecondary" for="temperature">
              <span>Temperature</span>
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
            <label class="text-xs uppercase tracking-wide text-txtsecondary" for="system-prompt">System Prompt</label>
            <textarea
              id="system-prompt"
              class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary resize-none"
              placeholder="You are a helpful assistant..."
              rows="3"
              bind:value={$systemPromptStore}
              disabled={isStreaming}
            ></textarea>
          </div>
        </div>
      {/if}

      <!-- Image preview strip -->
      {#if attachedImages.length > 0}
        <div class="mb-2 flex flex-wrap gap-2">
          {#each attachedImages as imageUrl, idx (idx)}
            <div class="relative group">
              <img
                src={imageUrl}
                alt="Attached image {idx + 1}"
                class="w-20 h-20 object-cover rounded border border-card-border"
              />
              <button
                class="absolute -top-2 -right-2 bg-red-500 text-white rounded-full w-6 h-6 flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity"
                onclick={() => removeImage(idx)}
                title="Remove image"
              >
                ×
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
      {#if queued.length > 0}
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
      <div class="flex flex-col gap-2 rounded-3xl border border-card-border bg-surface px-4 pt-3 pb-3 focus-within:border-primary transition-colors">
        {#if $rewriteStore}
          <div class="flex items-start gap-2 pb-2 border-b border-card-border">
            <PenLine class="w-3.5 h-3.5 mt-1.5 shrink-0 text-primary" />
            <textarea
              class="w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary min-h-[1.5rem] max-h-32 pretty-scroll"
              rows="1"
              placeholder="How should I help? e.g. make it more concise and formal"
              bind:value={$rewriteInstructionStore}
              onkeydown={handleKeyDown}
            ></textarea>
          </div>
        {/if}
        <textarea
          bind:this={inputEl}
          class="w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary pretty-scroll min-h-[3rem] max-h-80"
          rows="2"
          placeholder={$rewriteStore ? "Paste the text to rewrite…" : isStreaming ? "Queue a message…" : "Type a message..."}
          bind:value={userInput}
          onkeydown={handleKeyDown}
        ></textarea>

        <div class="flex items-center justify-between">
          <button
            class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
            onclick={() => fileInput?.click()}
            disabled={isStreaming || !$selectedModelStore}
            title="Attach image"
          >
            <Paperclip class="w-[1.125rem] h-[1.125rem]" />
          </button>

          <span class="flex-1 min-w-0 truncate px-2 text-center text-xs font-medium text-txtsecondary" title={selectedModelName}>
            {selectedModelName}
          </span>

          <div class="flex items-center gap-1">
            <button
              class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {$reasoningStore ? 'bg-primary/10 text-primary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
              onclick={() => { reasoningStore.set(!$reasoningStore); showToast($reasoningStore ? "Reasoning enabled" : "Reasoning disabled"); }}
              title={$reasoningStore ? "Reasoning on" : "Reasoning off"}
            >
              <Brain class="w-[1.125rem] h-[1.125rem]" />
            </button>
            <button
              class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {$webSearchStore ? 'bg-primary/10 text-primary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
              onclick={() => { webSearchStore.set(!$webSearchStore); showToast($webSearchStore ? "Web search enabled" : "Web search disabled"); }}
              title={$webSearchStore ? "Web search on" : "Web search off"}
            >
              <Search class="w-[1.125rem] h-[1.125rem]" />
            </button>
            <button
              class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {$rewriteStore ? 'bg-primary/10 text-primary' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
              onclick={() => { rewriteStore.set(!$rewriteStore); showToast($rewriteStore ? "Rewrite mode on" : "Rewrite mode off"); }}
              title={$rewriteStore ? "Rewrite mode on" : "Rewrite mode off"}
            >
              <PenLine class="w-[1.125rem] h-[1.125rem]" />
            </button>
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
