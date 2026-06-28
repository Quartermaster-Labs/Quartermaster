<script lang="ts">
  import { renderMarkdown, renderStreamingMarkdown, createStreamingCache } from "../../lib/markdown";
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
    searches?: { query: string; results: string; at?: number; sources?: { title: string; url: string }[] }[];
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

  // The assistant turn is one string holding, in order: inline <think> blocks
  // (when the backend emits reasoning inline instead of in reasoning_content),
  // answer text, and — across tool rounds — more think blocks and answer text.
  // Searches are recorded separately with the content offset (`at`) where they
  // ran. Build one ordered timeline so think boxes and search blocks render
  // inline between the surrounding text, not pinned to the top.
  type SearchHit = { query: string; results: string; sources?: { title: string; url: string }[] };
  type SubItem = { type: "text"; text: string } | { type: "search"; search: SearchHit };
  type Segment =
    | { kind: "text"; text: string; idx: number }
    | { kind: "think"; items: SubItem[]; open: boolean }
    | { kind: "search"; search: SearchHit };

  // Step 1: tokenize content into think / text parts (think tags anywhere, plus
  // a trailing unclosed tag while streaming). `inner`/`innerStart` give the think
  // body and its content offset so searches can be nested into it later.
  // Field-based reasoning_content has no inline tags → a single text part.
  let parts = $derived.by(() => {
    const res: { kind: "text" | "think"; text: string; start: number; end: number; innerStart: number; open: boolean }[] = [];
    if (role !== "assistant") return res;
    const re = /<(think|thinking|reasoning)>([\s\S]*?)(<\/\1>|$)/gi;
    let last = 0;
    let m: RegExpExecArray | null;
    while ((m = re.exec(textContent))) {
      if (m.index > last) res.push({ kind: "text", text: textContent.slice(last, m.index), start: last, end: m.index, innerStart: last, open: false });
      const closed = m[3] !== "";
      res.push({ kind: "think", text: m[2], start: m.index, end: m.index + m[0].length, innerStart: m.index + m[1].length + 2, open: !closed });
      last = m.index + m[0].length;
      if (!closed) break; // unclosed think runs to the end of the stream
    }
    if (last < textContent.length) res.push({ kind: "text", text: textContent.slice(last), start: last, end: textContent.length, innerStart: last, open: false });
    return res;
  });

  // Step 2: merge searches (by offset) into the part stream. A search inside a
  // think part's range nests inside its reasoning box; one inside a text part
  // splits that text inline.
  let timeline = $derived.by<Segment[]>(() => {
    const out: Segment[] = [];
    if (role !== "assistant") return out;
    const list = searches ?? [];
    const positioned = list.filter((s) => typeof s.at === "number").slice().sort((a, b) => (a.at as number) - (b.at as number));
    for (const s of list) if (typeof s.at !== "number") out.push({ kind: "search", search: s }); // legacy: top
    let ti = 0;
    let si = 0;
    for (const p of parts) {
      if (p.kind === "think") {
        const inner = p.text;
        const items: SubItem[] = [];
        let cur = 0;
        while (si < positioned.length && (positioned[si].at as number) < p.end) {
          const rel = Math.max(cur, Math.min(inner.length, (positioned[si].at as number) - p.innerStart));
          if (rel > cur) items.push({ type: "text", text: inner.slice(cur, rel) });
          items.push({ type: "search", search: positioned[si++] });
          cur = rel;
        }
        if (cur < inner.length) items.push({ type: "text", text: inner.slice(cur) });
        out.push({ kind: "think", items, open: p.open });
        continue;
      }
      let cur = p.start;
      while (si < positioned.length && (positioned[si].at as number) < p.end) {
        const at = Math.max(cur, positioned[si].at as number);
        if (at > cur) out.push({ kind: "text", text: textContent.slice(cur, at), idx: ti++ });
        out.push({ kind: "search", search: positioned[si++] });
        cur = at;
      }
      if (cur < p.end) out.push({ kind: "text", text: textContent.slice(cur, p.end), idx: ti++ });
    }
    while (si < positioned.length) out.push({ kind: "search", search: positioned[si++] });
    // Coalesce adjacent think segments into one box. A reasoning model that emits
    // inline <think> produces a fresh block each tool round (think → search →
    // think → answer); the rounds sit back-to-back in the stream, so merge them
    // into a single reasoning box (any search between them is already nested in).
    const merged: Segment[] = [];
    for (const seg of out) {
      const prev = merged[merged.length - 1];
      if (seg.kind === "think" && prev && prev.kind === "think") {
        prev.items = [...prev.items, ...seg.items];
        prev.open = prev.open || seg.open;
      } else {
        merged.push(seg.kind === "think" ? { ...seg, items: [...seg.items] } : seg);
      }
    }
    return merged;
  });

  // Index of the final text segment — the only one that grows while streaming,
  // so it gets the incremental markdown renderer; earlier ones are static.
  let lastTextIdx = $derived.by(() => {
    let n = -1;
    for (const s of timeline) if (s.kind === "text") n = s.idx;
    return n;
  });
  // All sources gathered across this turn's searches, deduped by URL, for the
  // "Sources" pill list at the end of the message.
  let allSources = $derived.by(() => {
    const seen = new Set<string>();
    const out: { title: string; url: string }[] = [];
    for (const s of searches ?? [])
      for (const src of s.sources ?? [])
        if (src.url && !seen.has(src.url)) {
          seen.add(src.url);
          out.push(src);
        }
    return out;
  });
  // User open/close override for inline reasoning boxes, keyed by timeline index.
  // Defaults to the live `seg.open` (auto-open while reasoning streams); once the
  // user clicks, their choice wins — otherwise the reactive `open` would re-assert
  // every chunk and the box couldn't be closed mid-stream.
  let thinkOverride = $state<Record<number, boolean>>({});
  let openThink = $derived(timeline.some((s) => s.kind === "think" && s.open));
  let hasBodyText = $derived(timeline.some((s) => s.kind === "text" && s.text.trim().length > 0));

  let streamingCache = createStreamingCache();
  // Render a text segment: incremental (streaming) only for the live last one.
  function renderTextSeg(seg: { text: string; idx: number }): { blocks: RenderedBlock[]; pendingHtml: string } {
    if (isStreaming && seg.idx === lastTextIdx) {
      return renderStreamingMarkdown(seg.text, streamingCache);
    }
    return { blocks: [{ id: -1, html: renderMarkdown(seg.text) }], pendingHtml: "" };
  }
  $effect(() => {
    // Reset the streaming cache when a turn finishes so the next one starts clean.
    if (!isStreaming) streamingCache = createStreamingCache();
  });
  let copied = $state(false);
  let showRaw = $state(false);
  let isEditing = $state(false);
  let editContent = $state("");
  let showReasoning = $state(false);
  let modalImageUrl = $state<string | null>(null);

  // Vary the source-pill max width so long titles don't all truncate to one
  // uniform block. Deterministic by title (stable across renders). Classes are
  // spelled out so Tailwind's JIT keeps them.
  const PILL_W = ["max-w-[5rem]", "max-w-[7rem]", "max-w-[9rem]", "max-w-[11rem]"];
  function pillWidth(s: string): string {
    let h = 0;
    for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
    return PILL_W[h % PILL_W.length];
  }

  // Per-domain favicon for the source pills (DuckDuckGo's icon proxy, no key).
  function faviconUrl(url: string): string {
    try {
      return `https://icons.duckduckgo.com/ip3/${new URL(url).hostname}.ico`;
    } catch {
      return "";
    }
  }

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
<div class="flex flex-col {role === 'user' ? 'items-end' : 'items-start'} mb-4">
  <div
    class="relative group rounded-2xl px-3 py-2 text-[0.8125rem] {role === 'user'
      ? 'max-w-[85%] bg-black text-white rounded-br-sm msg-tail-user'
      : (rewriteOriginal != null ? 'w-full' : 'w-full sm:w-4/5') + ' bg-surface border border-card-border rounded-bl-sm msg-tail-bot'}"
  >
    {#if role === "assistant"}
      <!-- Field-based reasoning (backend split it into reasoning_content). Inline
           <think> blocks render in the timeline below instead. -->
      {#if reasoning_content || isReasoning}
        <div class="mb-3 border border-card-border rounded-xl overflow-hidden">
          <button
            class="w-full flex items-center gap-2 px-3 py-2 bg-secondary hover:bg-secondary-hover transition-colors text-sm"
            onclick={() => showReasoning = !showReasoning}
          >
            {#if showReasoning}
              <ChevronDown class="w-4 h-4" />
            {:else}
              <ChevronRight class="w-4 h-4" />
            {/if}
            <Brain class="w-4 h-4 {isReasoning ? 'reason-glow' : ''}" />
            <span class="font-medium {isReasoning ? 'reason-shimmer' : ''}">Reasoning</span>
            <span class="text-txtsecondary ml-2">
              ({reasoning_content.length} chars{#if !isReasoning && reasoningTimeMs > 0}, {formatDuration(reasoningTimeMs)}{/if})
            </span>
          </button>
          <div class="reveal {showReasoning ? 'open' : ''}">
            <div class="reveal-inner">
              <div class="px-3 py-2 bg-secondary/50 text-sm text-txtsecondary prose prose-sm dark:prose-invert max-w-none chat-prose">
                {@html renderMarkdown(reasoning_content)}
              </div>
            </div>
          </div>
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
          <!-- Ordered timeline: inline think boxes, search blocks, and answer text. -->
          {#each timeline as seg, si (si)}
            {#if seg.kind === "search"}
              <details class="not-prose my-2 rounded-xl border border-card-border overflow-hidden">
                <summary class="flex items-center gap-2 px-3 py-2 bg-secondary hover:bg-secondary-hover transition-colors cursor-pointer select-none text-sm">
                  <Search class="w-3.5 h-3.5 shrink-0" />
                  <span class="font-medium truncate">{seg.search.query || "Web search"}</span>
                </summary>
                <div class="reveal">
                  <div class="reveal-inner">
                    <div class="px-3 py-2 bg-secondary/50 text-xs text-txtsecondary whitespace-pre-wrap font-mono max-h-72 overflow-y-auto pretty-scroll">{seg.search.results}</div>
                  </div>
                </div>
              </details>
            {:else if seg.kind === "think"}
              {@const thinkChars = seg.items.reduce((n, it) => n + (it.type === "text" ? it.text.length : 0), 0)}
              {@const isOpen = si in thinkOverride ? thinkOverride[si] : seg.open}
              <details class="not-prose my-2 rounded-xl border border-card-border overflow-hidden" open={isOpen}>
                <summary
                  class="flex items-center gap-2 px-3 py-2 bg-secondary hover:bg-secondary-hover transition-colors cursor-pointer select-none text-sm"
                  onclick={(e) => { e.preventDefault(); thinkOverride[si] = !isOpen; }}
                >
                  <Brain class="w-4 h-4 shrink-0 {seg.open ? 'reason-glow' : ''}" />
                  <span class="font-medium {seg.open ? 'reason-shimmer' : ''}">Reasoning</span>
                  <span class="text-txtsecondary ml-2">({thinkChars} chars)</span>
                </summary>
                <div class="reveal">
                  <div class="reveal-inner">
                <div class="px-3 py-2 bg-secondary/50 text-sm text-txtsecondary flex flex-col gap-2">
                  {#each seg.items as it, ii (ii)}
                    {#if it.type === "search"}
                      <details class="not-prose rounded-lg border border-card-border overflow-hidden font-mono">
                        <summary class="flex items-center gap-2 px-2 py-1.5 bg-secondary hover:bg-secondary-hover transition-colors cursor-pointer select-none text-xs">
                          <Search class="w-3 h-3 shrink-0" />
                          <span class="font-medium truncate">{it.search.query || "Web search"}</span>
                        </summary>
                        <div class="px-2 py-1.5 bg-background/40 text-xs whitespace-pre-wrap max-h-72 overflow-y-auto pretty-scroll">{it.search.results}</div>
                      </details>
                    {:else if it.text}
                      <div class="prose prose-sm dark:prose-invert max-w-none chat-prose">{@html renderMarkdown(it.text)}</div>
                    {/if}
                  {/each}
                </div>
                  </div>
                </div>
              </details>
            {:else if seg.text}
              {@const r = renderTextSeg(seg)}
              {#each r.blocks as block (block.id)}
                {@html block.html}
              {/each}
              {@html r.pendingHtml}
            {/if}
          {/each}
          {#if isSearching}
            <span class="inline-flex items-center gap-2 text-sm text-txtsecondary italic">
              <span class="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
              Searching the web…
            </span>
          {:else if isStreaming && !openThink && !isReasoning && !hasBodyText}
            <!-- No tokens yet — generating if the model is loaded, else swapping in. -->
            <span class="inline-flex items-center gap-2 text-txtsecondary italic">
              <span class="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
              {modelReady ? "Generating…" : "Loading model…"}
            </span>
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
  {#if allSources.length > 0}
    <div class="w-full sm:w-4/5 mt-1.5 flex flex-wrap gap-1.5">
      {#each allSources as src, si (si)}
        <a
          href={src.url}
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex items-center gap-1.5 {pillWidth(src.title)} px-2 py-0.5 rounded-full border border-card-border bg-secondary/50 hover:bg-secondary text-xs text-txtsecondary hover:text-txtmain transition-colors"
          title={src.url}
        >
          {#if faviconUrl(src.url)}
            <img src={faviconUrl(src.url)} alt="" class="w-3.5 h-3.5 shrink-0 rounded-sm" loading="lazy" onerror={(e) => ((e.currentTarget as HTMLImageElement).style.display = "none")} />
          {/if}
          <span class="truncate">{src.title}</span>
        </a>
      {/each}
    </div>
  {/if}
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

  /* Animated expand/reveal for the reasoning + search boxes. grid-rows 0fr→1fr
     animates height without a fixed pixel target. The `display: grid` override
     keeps closed <details> content in the DOM so it can transition (the UA would
     otherwise hide it outright). */
  .reveal {
    display: grid;
    grid-template-rows: 0fr;
    transition: grid-template-rows 0.25s ease;
  }
  .reveal.open,
  details[open] > .reveal {
    grid-template-rows: 1fr;
  }
  .reveal > .reveal-inner {
    overflow: hidden;
    min-height: 0;
  }

  /* Active-reasoning feedback: a brighter band sweeps left→right across the
     "Reasoning" label, and the brain icon pulses a soft glow. */
  .reason-shimmer {
    background: linear-gradient(
      90deg,
      var(--color-txtsecondary) 0%,
      var(--color-txtsecondary) 35%,
      var(--color-primary) 50%,
      var(--color-txtsecondary) 65%,
      var(--color-txtsecondary) 100%
    );
    background-size: 200% 100%;
    -webkit-background-clip: text;
    background-clip: text;
    -webkit-text-fill-color: transparent;
    animation: reason-sweep 1.8s linear infinite;
  }
  @keyframes reason-sweep {
    0% {
      background-position: 200% 0;
    }
    100% {
      background-position: -200% 0;
    }
  }
  .reason-glow {
    color: var(--color-primary);
    animation: reason-glow-pulse 1.8s ease-in-out infinite;
  }
  @keyframes reason-glow-pulse {
    0%,
    100% {
      filter: drop-shadow(0 0 0 transparent);
      opacity: 0.65;
    }
    50% {
      filter: drop-shadow(0 0 4px var(--color-primary));
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .reveal {
      transition: none;
    }
    .reason-shimmer,
    .reason-glow {
      animation: none;
    }
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
     otherwise win the reveal and reopen the gap. */
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
    border: 1px solid var(--color-border, rgba(128, 128, 128, 0.2));
    border-radius: 0.5rem;
    overflow: hidden;
  }

  .prose :global(th),
  .prose :global(td) {
    border: 1px solid var(--color-border, rgba(128, 128, 128, 0.2));
    padding: 0.5rem;
    text-align: left;
  }

  .prose :global(thead) {
    background: #1a1a1a;
  }

  .prose :global(th) {
    font-weight: 600;
    color: #f5f5f5;
    border-bottom: 2px solid #000;
  }

  .prose :global(tbody tr:nth-child(even)) {
    background: color-mix(in srgb, var(--color-txtsecondary) 7%, transparent);
  }

  .prose :global(tbody tr:hover) {
    background: color-mix(in srgb, var(--color-txtsecondary) 12%, transparent);
  }

  /* Highlight.js theme overrides for dark mode */
  :global(.dark) .prose :global(.hljs) {
    background: transparent;
  }
</style>
