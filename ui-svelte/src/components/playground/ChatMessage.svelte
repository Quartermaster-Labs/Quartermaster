<script lang="ts">
  import { renderMarkdown, escapeHtml, renderStreamingMarkdown, createStreamingCache } from "../../lib/markdown";
  import type { RenderedBlock } from "../../lib/markdown";
  import { Copy, Check, Pencil, X, Save, RefreshCw, ChevronDown, ChevronRight, Brain, Code, Search, PenLine } from "lucide-svelte";
  import { getTextContent, getImageUrls } from "../../lib/types";
  import type { ContentPart } from "../../lib/types";
  import RewriteDiff from "./RewriteDiff.svelte";

  interface Props {
    role: "user" | "assistant" | "system" | "tool";
    content: string | ContentPart[];
    reasoning_content?: string;
    reasoningTimeMs?: number;
    genTimeMs?: number;
    searches?: { query: string; results: string }[];
    rewriteInstruction?: string;
    rewriteOriginal?: string;
    isStreaming?: boolean;
    isReasoning?: boolean;
    isSearching?: boolean;
    modelReady?: boolean;
    onEdit?: (newContent: string) => void;
    onRegenerate?: () => void;
  }

  let { role, content, reasoning_content = "", reasoningTimeMs = 0, genTimeMs = 0, searches, rewriteInstruction, rewriteOriginal, isStreaming = false, isReasoning = false, isSearching = false, modelReady = false, onEdit, onRegenerate }: Props = $props();

  let textContent = $derived(getTextContent(content));
  let wordCount = $derived(stripThinking(textContent).trim().split(/\s+/).filter(Boolean).length);
  let imageUrls = $derived(getImageUrls(content));
  let hasImages = $derived(imageUrls.length > 0);
  let canEdit = $derived(onEdit !== undefined && !hasImages);

  let streamingCache = createStreamingCache();
  let renderedParts = $derived.by(() => {
    if (role !== "assistant") {
      return { blocks: [{ id: -1, html: escapeHtml(textContent).replace(/\n/g, '<br>') }] as RenderedBlock[], pendingHtml: "" };
    }
    if (!isStreaming) {
      streamingCache = createStreamingCache();
      return { blocks: [{ id: -1, html: renderMarkdown(textContent) }] as RenderedBlock[], pendingHtml: "" };
    }
    return renderStreamingMarkdown(textContent, streamingCache);
  });
  let copied = $state(false);
  let showRaw = $state(false);
  let isEditing = $state(false);
  let editContent = $state("");
  let showReasoning = $state(false);
  let modalImageUrl = $state<string | null>(null);

  function formatDuration(ms: number): string {
    if (ms < 1000) {
      return `${ms.toFixed(0)}ms`;
    }
    return `${(ms / 1000).toFixed(1)}s`;
  }

  // Wall-clock turn time, minute-aware: "2.5s", "1m 30s".
  function formatTotal(ms: number): string {
    const s = ms / 1000;
    if (s < 60) return `${s.toFixed(1)}s`;
    const m = Math.floor(s / 60);
    return `${m}m ${Math.round(s - m * 60)}s`;
  }

  // Strip inline reasoning so copy gets only the answer. Separate
  // reasoning_content is already excluded; this catches models that emit
  // <think>…</think> (or <reasoning>…) inline in the content.
  function stripThinking(text: string): string {
    return text.replace(/<(think|thinking|reasoning)>[\s\S]*?<\/\1>/gi, "").trimStart();
  }

  async function copyToClipboard() {
    const copyText = stripThinking(textContent);
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(copyText);
      } else {
        // Fallback for non-secure contexts (HTTP)
        const textarea = document.createElement("textarea");
        textarea.value = copyText;
        textarea.style.position = "fixed";
        textarea.style.left = "-9999px";
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        document.body.removeChild(textarea);
      }
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch (err) {
      console.error("Failed to copy:", err);
    }
  }

  function startEdit() {
    editContent = textContent;
    isEditing = true;
  }

  function cancelEdit() {
    isEditing = false;
    editContent = "";
  }

  function saveEdit() {
    if (onEdit && editContent.trim() !== textContent) {
      onEdit(editContent.trim());
    }
    isEditing = false;
    editContent = "";
  }

  function openModal(imageUrl: string) {
    modalImageUrl = imageUrl;
    document.body.style.overflow = "hidden";
  }

  function closeModal(event?: MouseEvent) {
    // Only close if clicking the background, not the image
    if (event && event.target !== event.currentTarget) {
      return;
    }
    modalImageUrl = null;
    document.body.style.overflow = "";
  }

  function handleModalKeyDown(event: KeyboardEvent) {
    if (event.key === "Escape") {
      closeModal();
    }
  }

  function handleKeyDown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      saveEdit();
    } else if (event.key === "Escape") {
      cancelEdit();
    }
  }

  const COPY_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`;
  const CHECK_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>`;

  function codeBlockCopy(node: HTMLElement) {
    function attachButtons() {
      node.querySelectorAll<HTMLPreElement>('pre:not([data-copy-btn])').forEach(pre => {
        pre.setAttribute('data-copy-btn', 'true');
        const btn = document.createElement('button');
        btn.className = 'code-copy-btn';
        btn.title = 'Copy code';
        btn.innerHTML = COPY_SVG;
        btn.addEventListener('click', async () => {
          const text = pre.querySelector('code')?.textContent ?? pre.textContent ?? '';
          try {
            if (navigator.clipboard && window.isSecureContext) {
              await navigator.clipboard.writeText(text);
            } else {
              const ta = document.createElement('textarea');
              ta.value = text;
              ta.style.cssText = 'position:fixed;left:-9999px';
              document.body.appendChild(ta);
              ta.select();
              document.execCommand('copy');
              document.body.removeChild(ta);
            }
            btn.innerHTML = CHECK_SVG;
            btn.classList.add('copied');
            setTimeout(() => { btn.innerHTML = COPY_SVG; btn.classList.remove('copied'); }, 2000);
          } catch (e) {
            console.error('copy failed', e);
          }
        });
        pre.appendChild(btn);
      });
    }
    attachButtons();
    const mo = new MutationObserver(attachButtons);
    mo.observe(node, { childList: true, subtree: true });
    return { destroy: () => mo.disconnect() };
  }
</script>

{#if role === "tool"}
  <details class="mb-4 rounded-lg border border-card-border bg-surface/50 text-[0.8125rem]">
    <summary class="flex items-center gap-2 px-3 py-2 cursor-pointer text-txtsecondary select-none">
      <Search class="w-3.5 h-3.5" />
      Search results
    </summary>
    <div class="px-3 pb-2 whitespace-pre-wrap font-mono text-xs text-txtsecondary">{textContent}</div>
  </details>
{:else}
<div class="flex {role === 'user' ? 'justify-end' : 'justify-start'} mb-4">
  <div
    class="relative group rounded-2xl px-3 py-2 text-[0.8125rem] {role === 'user'
      ? 'max-w-[85%] bg-black text-white rounded-br-sm msg-tail-user'
      : (rewriteOriginal != null ? 'w-full' : 'w-full sm:w-4/5') + ' bg-surface border border-card-border rounded-bl-sm msg-tail-bot'}"
  >
    {#if role === "assistant"}
      {#if searches && searches.length > 0}
        <div class="mb-3 flex flex-col gap-1.5">
          {#each searches as s, si (si)}
            <details class="rounded border border-card-border overflow-hidden">
              <summary class="flex items-center gap-2 px-3 py-2 bg-secondary hover:bg-secondary-hover transition-colors cursor-pointer select-none text-sm">
                <Search class="w-3.5 h-3.5 shrink-0" />
                <span class="font-medium truncate">{s.query || "Web search"}</span>
              </summary>
              <div class="px-3 py-2 bg-secondary/50 text-xs text-txtsecondary whitespace-pre-wrap font-mono max-h-72 overflow-y-auto pretty-scroll">{s.results}</div>
            </details>
          {/each}
        </div>
      {/if}
      {#if isSearching}
        <div class="mb-3 inline-flex items-center gap-2 text-sm text-txtsecondary italic">
          <span class="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
          Searching the web…
        </div>
      {/if}
      {#if reasoning_content || isReasoning}
        <div class="mb-3 border border-card-border rounded overflow-hidden">
          <button
            class="w-full flex items-center gap-2 px-3 py-2 bg-secondary hover:bg-secondary-hover transition-colors text-sm"
            onclick={() => showReasoning = !showReasoning}
          >
            {#if showReasoning}
              <ChevronDown class="w-4 h-4" />
            {:else}
              <ChevronRight class="w-4 h-4" />
            {/if}
            <Brain class="w-4 h-4" />
            <span class="font-medium">Reasoning</span>
            <span class="text-txtsecondary ml-2">
              ({reasoning_content.length} chars{#if !isReasoning && reasoningTimeMs > 0}, {formatDuration(reasoningTimeMs)}{/if})
            </span>
            {#if isReasoning}
              <span class="ml-auto flex items-center gap-1 text-txtsecondary">
                <span class="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
                reasoning...
              </span>
            {/if}
          </button>
          {#if showReasoning}
            <div class="px-3 py-2 bg-secondary/50 text-sm text-txtsecondary whitespace-pre-wrap font-mono">
              {reasoning_content}{#if isReasoning}<span class="inline-block w-1.5 h-4 bg-current animate-pulse ml-0.5"></span>{/if}
            </div>
          {/if}
        </div>
      {/if}
      {#if hasImages}
        <div class="mb-3 flex flex-wrap gap-2">
          {#each imageUrls as imageUrl, idx (idx)}
            <button
              onclick={() => openModal(imageUrl)}
              class="cursor-pointer rounded border border-card-border hover:opacity-80 transition-opacity"
            >
              <img
                src={imageUrl}
                alt="Image {idx + 1}"
                class="max-h-64 rounded"
              />
            </button>
          {/each}
        </div>
      {/if}
      {#if rewriteOriginal != null}
        <RewriteDiff original={rewriteOriginal} rewritten={stripThinking(textContent)} {isStreaming} {modelReady} />
      {:else if showRaw}
        <div class="whitespace-pre-wrap font-mono text-sm">{textContent}</div>
      {:else}
        <div class="prose prose-sm dark:prose-invert max-w-none chat-prose" use:codeBlockCopy>
          {#each renderedParts.blocks as block (block.id)}
            {@html block.html}
          {/each}
          {@html renderedParts.pendingHtml}
          {#if isStreaming && !isReasoning && !isSearching}
            {#if !textContent}
              <!-- No tokens yet — generating if the model is loaded, else swapping in. -->
              <span class="inline-flex items-center gap-2 text-txtsecondary italic">
                <span class="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
                {modelReady ? "Generating…" : "Loading model…"}
              </span>
            {:else}
              <span class="inline-block w-2 h-4 bg-current animate-pulse ml-0.5"></span>
            {/if}
          {/if}
        </div>
      {/if}
      {#if !isStreaming && textContent}
        <div class="flex flex-wrap items-center gap-1 mt-2 pt-1 border-t border-card-border">
          {#if onRegenerate}
            <button
              class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
              onclick={onRegenerate}
              title="Regenerate response"
            >
              <RefreshCw class="w-4 h-4" />
            </button>
          {/if}
          <button
            class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
            onclick={copyToClipboard}
            title={copied ? "Copied!" : "Copy to clipboard"}
          >
            {#if copied}
              <Check class="w-4 h-4 text-green-500" />
            {:else}
              <Copy class="w-4 h-4" />
            {/if}
          </button>
          <button
            class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 {showRaw ? 'text-primary' : 'text-txtsecondary'}"
            onclick={() => showRaw = !showRaw}
            title={showRaw ? "Show rendered" : "Show raw"}
          >
            <Code class="w-4 h-4" />
          </button>
          <span class="ml-auto flex items-center gap-2 self-center text-[0.6875rem] text-txtsecondary tabular-nums">
            <span>{wordCount} {wordCount === 1 ? "word" : "words"}</span>
            {#if genTimeMs > 0}
              <span class="opacity-50">·</span>
              <span>{formatTotal(genTimeMs)}</span>
            {/if}
          </span>
        </div>
      {/if}
    {:else}
      {#if rewriteInstruction != null}
        <div class="flex items-center gap-2 text-xs">
          <PenLine class="w-3.5 h-3.5 shrink-0 opacity-80" />
          <span class="opacity-90">{rewriteInstruction || "Rewrite this text"}</span>
        </div>
      {:else if isEditing}
        <div class="flex flex-col gap-2 min-w-[300px]">
          <textarea
            class="w-full px-3 py-2 rounded border border-card-border bg-surface text-txtmain focus:outline-none focus:ring-2 focus:ring-primary resize-none"
            rows="3"
            bind:value={editContent}
            onkeydown={handleKeyDown}
          ></textarea>
          <div class="flex justify-end gap-2">
            <button
              class="p-1.5 rounded hover:bg-white/20"
              onclick={cancelEdit}
              title="Cancel"
            >
              <X class="w-4 h-4" />
            </button>
            <button
              class="p-1.5 rounded hover:bg-white/20"
              onclick={saveEdit}
              title="Save"
            >
              <Save class="w-4 h-4" />
            </button>
          </div>
        </div>
      {:else}
        {#if hasImages}
          <div class="mb-2 flex flex-wrap gap-2">
            {#each imageUrls as imageUrl, idx (idx)}
              <button
                onclick={() => openModal(imageUrl)}
                class="cursor-pointer rounded border border-white/20 hover:opacity-80 transition-opacity"
              >
                <img
                  src={imageUrl}
                  alt="Image {idx + 1}"
                  class="max-w-[200px] rounded"
                />
              </button>
            {/each}
          </div>
        {/if}
        <div class="whitespace-pre-wrap pr-8">{textContent}</div>
        {#if canEdit}
          <button
            class="absolute top-1.5 right-1.5 p-1 rounded-full opacity-0 group-hover:opacity-100 transition-all bg-white/10 text-white/70 hover:text-white hover:bg-white/25"
            onclick={startEdit}
            title="Edit message"
          >
            <Pencil class="w-3 h-3" />
          </button>
        {/if}
      {/if}
    {/if}
  </div>
</div>
{/if}

<!-- Full-size image modal -->
{#if modalImageUrl}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4"
    onclick={(e) => closeModal(e)}
    onkeydown={handleModalKeyDown}
    role="button"
    tabindex="-1"
  >
    <button
      class="absolute top-4 right-4 p-2 rounded-lg bg-white/10 hover:bg-white/20 text-white transition-colors"
      onclick={() => closeModal()}
      title="Close"
    >
      <X class="w-6 h-6" />
    </button>
    <img
      src={modalImageUrl}
      alt=""
      class="max-w-full max-h-full rounded pointer-events-none"
    />
  </div>
{/if}

<style>
  /* Facebook-Messenger-style speech-bubble tail at the bottom corner. The
     triangle inherits each bubble's fill so it reads as part of the bubble. */
  .msg-tail-user::after,
  .msg-tail-bot::after {
    content: "";
    position: absolute;
    bottom: 0;
    width: 0;
    height: 0;
    border: 6px solid transparent;
    border-bottom: 0;
  }
  .msg-tail-user::after {
    right: -5px;
    border-left-color: #000;
  }
  .msg-tail-bot::after {
    left: -5px;
    border-right-color: var(--color-surface);
  }

  .chat-prose {
    font-size: 0.8125rem;
    line-height: 1.55;
  }

  .prose :global(pre) {
    position: relative;
    background-color: var(--color-surface);
    border: 1px solid var(--color-border, rgba(128, 128, 128, 0.2));
    border-radius: 0.375rem;
    padding: 0.75rem;
    padding-right: 2.5rem;
    overflow-x: auto;
    margin: 0.5rem 0;
  }

  .prose :global(.code-copy-btn) {
    position: absolute;
    top: 0.375rem;
    right: 0.375rem;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0.25rem;
    border-radius: 0.25rem;
    border: 1px solid var(--color-border);
    background: var(--color-surface);
    color: var(--color-txtsecondary);
    cursor: pointer;
    transition: background-color 0.15s;
    line-height: 0;
  }

  .prose :global(.code-copy-btn:hover) {
    background: var(--color-secondary);
  }

  .prose :global(.code-copy-btn.copied) {
    color: var(--color-success);
    opacity: 1;
  }

  .prose :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.875em;
  }

  .prose :global(pre code) {
    background: none;
    padding: 0;
  }

  .prose :global(code:not(pre code)) {
    background-color: var(--color-surface);
    padding: 0.125rem 0.25rem;
    border-radius: 0.25rem;
    border: 1px solid var(--color-border, rgba(128, 128, 128, 0.2));
  }

  .prose :global(p) {
    margin: 0.5rem 0;
  }

  .prose :global(p:first-child) {
    margin-top: 0;
  }

  .prose :global(p:last-child) {
    margin-bottom: 0;
  }

  .prose :global(ul),
  .prose :global(ol) {
    margin: 0.5rem 0;
    padding-left: 1.5rem;
  }

  .prose :global(li) {
    margin: 0.25rem 0;
  }

  /* Models lean on huge markdown headers; flatten them to a slight size bump
     + primary color instead of the prose defaults. */
  .prose :global(h1),
  .prose :global(h2),
  .prose :global(h3),
  .prose :global(h4),
  .prose :global(h5),
  .prose :global(h6) {
    margin: 0.85rem 0 0.15rem 0;
    font-weight: 600;
    color: var(--color-primary);
    line-height: 1.3;
  }

  /* Pin the following block tight to its heading — its own top margin would
     otherwise win the collapse and reopen the gap. */
  .prose :global(h1 + *),
  .prose :global(h2 + *),
  .prose :global(h3 + *),
  .prose :global(h4 + *),
  .prose :global(h5 + *),
  .prose :global(h6 + *) {
    margin-top: 0;
  }

  .prose :global(h1) { font-size: 1.15em; }
  .prose :global(h2) { font-size: 1.08em; }
  .prose :global(h3),
  .prose :global(h4),
  .prose :global(h5),
  .prose :global(h6) { font-size: 1em; }

  .prose :global(h1:first-child),
  .prose :global(h2:first-child),
  .prose :global(h3:first-child),
  .prose :global(h4:first-child) {
    margin-top: 0;
  }

  .prose :global(blockquote) {
    border-left: 3px solid var(--color-primary);
    padding-left: 1rem;
    margin: 0.5rem 0;
    font-style: italic;
  }

  .prose :global(a) {
    color: var(--color-primary);
    text-decoration: underline;
  }

  .prose :global(table) {
    width: 100%;
    border-collapse: collapse;
    margin: 0.5rem 0;
  }

  .prose :global(th),
  .prose :global(td) {
    border: 1px solid var(--color-border, rgba(128, 128, 128, 0.2));
    padding: 0.5rem;
    text-align: left;
  }

  .prose :global(th) {
    background-color: var(--color-surface);
    font-weight: 600;
  }

  /* Highlight.js theme overrides for dark mode */
  :global(.dark) .prose :global(.hljs) {
    background: transparent;
  }
</style>
