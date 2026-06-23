<script lang="ts">
  import { models } from "../../stores/api";
  import { persistentStore } from "../../stores/persistent";
  import { selectedModelStore } from "../../stores/playground";
  import { streamChatCompletion, type Endpoint } from "../../lib/chatApi";
  import { WEB_SEARCH_TOOL, searxngSearch, formatSearchResults } from "../../lib/webSearch";
  import { playgroundStores } from "../../stores/playgroundActivity";
  import type { ChatMessage, ContentPart, ToolCall } from "../../lib/types";
  import { Settings, Paperclip, Send, Square, Plus, MessagesSquare, X } from "lucide-svelte";
  import ChatMessageComponent from "./ChatMessage.svelte";
  import ModelSelector from "./ModelSelector.svelte";
  import Select from "./Select.svelte";
  import { scrollFade } from "../../lib/scrollFade";

  const systemPromptStore = persistentStore<string>("playground-system-prompt", "");
  const temperatureStore = persistentStore<number>("playground-temperature", 0.7);
  const endpointStore = persistentStore<Endpoint>("playground-endpoint", "v1/chat/completions");
  const maxTokensStore = persistentStore<number>("playground-max-tokens", 4096);
  const webSearchStore = persistentStore<boolean>("playground-websearch", false);
  const searxngUrlStore = persistentStore<string>("playground-searxng-url", "http://localhost:8888");

  function loadMessages(): ChatMessage[] {
    try {
      const saved = localStorage.getItem("playground-messages");
      return saved ? JSON.parse(saved) : [];
    } catch {
      return [];
    }
  }

  let messages = $state<ChatMessage[]>(loadMessages());
  let userInput = $state("");
  let isStreaming = $state(false);
  let isReasoning = $state(false);
  let reasoningStartTime = $state<number>(0);
  let abortController = $state<AbortController | null>(null);
  let messagesContainer: HTMLDivElement | undefined = $state();
  let inputEl: HTMLTextAreaElement | undefined = $state();
  let showSettings = $state(false);
  let attachedImages = $state<string[]>([]);
  let fileInput = $state<HTMLInputElement | null>(null);
  let imageError = $state<string | null>(null);
  let searxngProbe = $state<{ state: "idle" | "testing" | "ok" | "fail"; msg: string }>({ state: "idle", msg: "" });

  async function testSearxng() {
    searxngProbe = { state: "testing", msg: "" };
    try {
      const results = await searxngSearch($searxngUrlStore, "test");
      searxngProbe = { state: "ok", msg: `OK — ${results.length} result${results.length === 1 ? "" : "s"}` };
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      // A bare "Failed to fetch" is almost always CORS or wrong host/port.
      searxngProbe = { state: "fail", msg: /failed to fetch/i.test(m) ? "Failed — unreachable or CORS blocked" : m };
    }
  }

  let hasModels = $derived($models.some((m) => !m.unlisted));
  let userScrolledUp = $state(false);

  // Mirror the currently loaded (ready) model. Snap to it whenever the selection
  // isn't itself ready, unless the user explicitly picked another from the
  // dropdown (userPinned) — once their pick loads it becomes ready and sticks.
  let userPinned = $state(false);
  $effect(() => {
    const ready = $models.find((m) => m.state === "ready" && !m.unlisted);
    if (!ready) return;
    const selectionReady = $models.some(
      (m) => m.id === $selectedModelStore && m.state === "ready" && !m.unlisted
    );
    if (!selectionReady && !userPinned) selectedModelStore.set(ready.id);
  });

  $effect(() => {
    playgroundStores.chatStreaming.set(isStreaming);
  });

  // Auto-grow the composer textarea; tracks userInput so it also shrinks back
  // after a send clears the value.
  $effect(() => {
    userInput;
    if (inputEl) {
      inputEl.style.height = "auto";
      inputEl.style.height = Math.min(inputEl.scrollHeight, 192) + "px";
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

  // Persist messages to localStorage (throttled to once per 2s)
  let lastSaveTime = 0;
  $effect(() => {
    const json = JSON.stringify(messages);
    const elapsed = Date.now() - lastSaveTime;
    const save = () => {
      try { localStorage.setItem("playground-messages", json); } catch {}
      lastSaveTime = Date.now();
    };
    if (elapsed >= 2000) {
      save();
      return;
    }
    const timer = setTimeout(save, 2000 - elapsed);
    return () => clearTimeout(timer);
  });

  async function sendMessage() {
    const trimmedInput = userInput.trim();
    if ((!trimmedInput && attachedImages.length === 0) || !$selectedModelStore || isStreaming) return;

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

  function newChat() {
    if (isStreaming) {
      cancelStreaming();
    }
    messages = [];
    isReasoning = false;
    reasoningStartTime = 0;
  }

  // Web search runs as OpenAI tool-calling, which only the chat/completions
  // endpoint speaks. Other endpoints just chat without the tool.
  const MAX_TOOL_ROUNDS = 5;

  async function regenerateFromIndex(idx: number) {
    // Remove all messages after the edited user message
    messages = messages.slice(0, idx + 1);

    isStreaming = true;
    isReasoning = false;
    reasoningStartTime = 0;
    abortController = new AbortController();
    const signal = abortController.signal;

    const useTools = $webSearchStore && $endpointStore === "v1/chat/completions";

    try {
      // ponytail: bounded loop — model→tool_calls→search→model, capped at
      // MAX_TOOL_ROUNDS so a misbehaving model can't spin forever.
      for (let round = 0; ; round++) {
        // Add empty assistant message for this round's response
        messages = [...messages, { role: "assistant", content: "" }];

        // Build messages array with optional system prompt
        const apiMessages: ChatMessage[] = [];
        if ($systemPromptStore.trim()) {
          apiMessages.push({ role: "system", content: $systemPromptStore.trim() });
        }
        apiMessages.push(...messages.slice(0, -1)); // all except the empty assistant one

        const stream = streamChatCompletion($selectedModelStore, apiMessages, signal, {
          temperature: $temperatureStore,
          endpoint: $endpointStore,
          max_tokens: $maxTokensStore,
          tools: useTools ? [WEB_SEARCH_TOOL] : undefined,
        });

        let toolCalls: ToolCall[] | undefined;

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
            messages = messages.map((msg, i) =>
              i === messages.length - 1 ? { ...msg, content: msg.content + chunk.content } : msg
            );
          }
        }

        // No tool calls (or tools off) → the turn is complete.
        if (!useTools || !toolCalls || toolCalls.length === 0) break;

        // Attach the tool calls to the assistant message we just streamed.
        const requested = toolCalls;
        messages = messages.map((msg, i) =>
          i === messages.length - 1 ? { ...msg, tool_calls: requested } : msg
        );

        // Run each requested search and append its result as a tool message.
        for (const tc of requested) {
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
          messages = [...messages, { role: "tool", tool_call_id: tc.id, content: resultText }];
        }

        if (round >= MAX_TOOL_ROUNDS) {
          messages = [...messages, { role: "assistant", content: "_Reached the web-search limit for this turn._" }];
          break;
        }
        // loop: next round lets the model read the results and respond.
      }
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
      isStreaming = false;
      isReasoning = false;
      abortController = null;
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
    <!-- Messages area -->
    <div
      class="flex-1 overflow-y-auto pretty-scroll scroll-fade-y mb-4 px-2"
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
          <ChatMessageComponent
            role={message.role}
            content={message.content}
            reasoning_content={message.reasoning_content}
            reasoningTimeMs={message.reasoningTimeMs}
            tool_calls={message.tool_calls}
            isStreaming={isStreaming && idx === messages.length - 1 && message.role === "assistant"}
            isReasoning={isReasoning && idx === messages.length - 1 && message.role === "assistant"}
            onEdit={message.role === "user" ? (newContent) => editMessage(idx, newContent) : undefined}
            onRegenerate={message.role === "assistant" && idx > 0 && messages[idx - 1].role === "user"
              ? () => regenerateFromIndex(idx - 1)
              : undefined}
          />
        {/each}
      {/if}
    </div>

    <!-- Input area -->
    <div class="shrink-0 relative">
      <!-- Settings popover -->
      {#if showSettings}
        <div
          class="absolute bottom-full right-0 mb-2 w-80 max-h-[60vh] overflow-y-auto pretty-scroll z-20 flex flex-col gap-3 p-3 rounded-lg border border-card-border bg-surface shadow-lg text-[0.8125rem]"
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
            <ModelSelector bind:value={$selectedModelStore} placeholder="Select a model..." disabled={isStreaming} compact onChange={() => (userPinned = true)} />
          </div>

          <div class="flex flex-col gap-1">
            <span class="text-xs uppercase tracking-wide text-txtsecondary">Endpoint</span>
            <Select
              bind:value={$endpointStore}
              disabled={isStreaming}
              compact
              options={[
                { value: "v1/chat/completions", label: "/v1/chat/completions" },
                { value: "v1/messages", label: "/v1/messages" },
                { value: "v1/responses", label: "/v1/responses" },
              ]}
            />
          </div>

          <div class="flex flex-col gap-1.5">
            <label class="flex items-center justify-between text-xs uppercase tracking-wide text-txtsecondary" for="websearch">
              <span>Web Search</span>
              <input
                id="websearch"
                type="checkbox"
                class="accent-primary w-4 h-4"
                bind:checked={$webSearchStore}
                disabled={isStreaming}
              />
            </label>
            {#if $webSearchStore}
              <div class="flex gap-1.5">
                <input
                  type="text"
                  class="flex-1 min-w-0 px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
                  placeholder="SearXNG URL (http://localhost:8888)"
                  bind:value={$searxngUrlStore}
                  oninput={() => (searxngProbe = { state: "idle", msg: "" })}
                  disabled={isStreaming}
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
              {/if}
              {#if $endpointStore !== "v1/chat/completions"}
                <p class="text-xs text-amber-500">Web search only works on /v1/chat/completions.</p>
              {:else}
                <p class="text-xs text-txtsecondary">Model must support tool calling. SearXNG needs JSON format + CORS enabled.</p>
              {/if}
            {/if}
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

          <div class="flex flex-col gap-1">
            <label class="flex justify-between text-xs uppercase tracking-wide text-txtsecondary" for="temperature">
              <span>Temperature</span>
              <span class="text-txtmain normal-case">{$temperatureStore.toFixed(2)}</span>
            </label>
            <input
              id="temperature"
              type="range"
              min="0"
              max="2"
              step="0.05"
              class="w-full accent-primary"
              bind:value={$temperatureStore}
              disabled={isStreaming}
            />
            <div class="flex justify-between text-xs text-txtsecondary">
              <span>Precise</span>
              <span>Creative</span>
            </div>
          </div>

          <div class="flex flex-col gap-1">
            <label class="text-xs uppercase tracking-wide text-txtsecondary" for="max-tokens">Max Tokens</label>
            <input
              id="max-tokens"
              type="number"
              min="1"
              class="w-full px-2.5 py-1.5 rounded-md border border-card-border bg-surface focus:outline-none focus:border-primary"
              bind:value={$maxTokensStore}
              disabled={isStreaming}
            />
            <p class="text-xs text-txtsecondary">Required for /v1/messages.</p>
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

      <!-- Composer -->
      <div class="flex flex-col gap-1.5 rounded-lg border border-card-border bg-surface px-2.5 py-2 focus-within:border-primary transition-colors">
        <textarea
          bind:this={inputEl}
          class="w-full bg-transparent text-[0.8125rem] leading-relaxed resize-none focus:outline-none placeholder:text-txtsecondary max-h-48"
          rows="1"
          placeholder="Type a message..."
          bind:value={userInput}
          onkeydown={handleKeyDown}
          disabled={isStreaming || !$selectedModelStore}
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

          <div class="flex items-center gap-1">
            <button
              class="inline-flex items-center justify-center p-1.5 rounded-md transition-colors {showSettings ? 'bg-secondary text-txtmain shadow-inner' : 'text-txtsecondary hover:text-txtmain hover:bg-secondary'}"
              onclick={() => (showSettings = !showSettings)}
              title="Settings"
            >
              <Settings class="w-[1.125rem] h-[1.125rem]" />
            </button>
            <button
              class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors disabled:opacity-40"
              onclick={newChat}
              disabled={messages.length === 0 && !isStreaming}
              title="New chat"
            >
              <Plus class="w-[1.125rem] h-[1.125rem]" />
            </button>

            {#if isStreaming}
              <button
                class="inline-flex items-center justify-center p-1.5 rounded-md text-txtsecondary hover:text-txtmain hover:bg-secondary transition-colors"
                onclick={cancelStreaming}
                title="Stop"
              >
                <Square class="w-[1.125rem] h-[1.125rem]" fill="currentColor" />
              </button>
            {:else}
              <button
                class="inline-flex items-center justify-center p-1.5 rounded-md bg-primary text-white hover:opacity-90 transition-opacity disabled:opacity-40"
                onclick={sendMessage}
                disabled={(!userInput.trim() && attachedImages.length === 0) || !$selectedModelStore}
                title="Send"
              >
                <Send class="w-[1.125rem] h-[1.125rem]" />
              </button>
            {/if}
          </div>
        </div>
      </div>
    </div>
  {/if}
</div>
