<script lang="ts">
  import { renderMarkdown, renderStreamingMarkdown, createStreamingCache } from "../../lib/markdown";
  import type { RenderedBlock } from "../../lib/markdown";
  import { Copy, Check, Pencil, X, Save, RefreshCw, ChevronRight, Code, Search, BookOpen, PenLine, Wrench, Reply } from "lucide-svelte";
  import { getTextContent, getImageUrls } from "../../lib/types";
  import { harmonyToThink } from "../../lib/reasoning";
  import type { ContentPart, QmApproval } from "../../lib/types";
  import RewriteDiff from "./RewriteDiff.svelte";
  import { autogrow } from "../../lib/autogrow";
  import { openWikiArticle } from "../../stores/wiki";

  interface Props {
    role: "user" | "assistant" | "system" | "tool";
    content: string | ContentPart[];
    reasoning_content?: string;
    reasoningTimeMs?: number;
    thinkMs?: number[];
    genTimeMs?: number;
    searches?: { query: string; results: string; kind?: "web" | "wiki" | "quartermaster"; at?: number; reasoningAt?: number; duringReasoning?: boolean; sources?: { title: string; url: string }[] }[];
    citations?: { n: number; title: string; url: string; wikiId?: string }[];
    approval?: QmApproval;
    onApprove?: (id: string, accept: boolean) => void;
    rewriteInstruction?: string;
    rewriteOriginal?: string;
    isStreaming?: boolean;
    isReasoning?: boolean;
    isSearching?: boolean;
    modelReady?: boolean;
    hasVisionInput?: boolean;
    onEdit?: (newContent: string) => void;
    onRegenerate?: () => void;
    onReply?: () => void;
  }

  let { role, content, reasoning_content = "", reasoningTimeMs = 0, thinkMs, genTimeMs = 0, searches, citations, approval, onApprove, rewriteInstruction, rewriteOriginal, isStreaming = false, isReasoning = false, isSearching = false, modelReady = false, hasVisionInput = false, onEdit, onRegenerate, onReply }: Props = $props();

  // Format a JSON diff value for the approval card (null → "auto", strings bare).
  function fmtVal(v: unknown): string {
    if (v === null || v === undefined || v === "") return "auto";
    return typeof v === "string" ? v : JSON.stringify(v);
  }

  let textContent = $derived(getTextContent(content));
  // Some models (gpt-oss harmony et al.) emit reasoning as channel markup
  // (`<|channel|>analysis<|message|>…<|end|>…<|channel|>final<|message|>…`)
  // that llama.cpp's `--reasoning-format auto` doesn't parse, so it leaks raw
  // into content. Rewrite non-final channels into <think> so the timeline picks
  // them up as reasoning boxes. No-op when no channel markup is present.
  let displayContent = $derived(role === "assistant" ? harmonyToThink(textContent) : textContent);
  let wordCount = $derived(stripThinking(displayContent).trim().split(/\s+/).filter(Boolean).length);
  let imageUrls = $derived(getImageUrls(content));
  let hasImages = $derived(imageUrls.length > 0);
  let canEdit = $derived(onEdit !== undefined && !hasImages);

  // The assistant turn is one string holding, in order: inline <think> blocks
  // (when the backend emits reasoning inline instead of in reasoning_content),
  // answer text, and — across tool rounds — more think blocks and answer text.
  // Searches are recorded separately with the content offset (`at`) where they
  // ran. Build one ordered timeline so think boxes and search blocks render
  // inline between the surrounding text, not pinned to the top.
  type SearchHit = { query: string; results: string; kind?: "web" | "wiki" | "quartermaster"; sources?: { title: string; url: string }[] };
  type SubItem = { type: "text"; text: string } | { type: "search"; search: SearchHit };
  type Segment =
    | { kind: "text"; text: string; idx: number }
    | { kind: "think"; items: SubItem[]; open: boolean; ms: number }
    | { kind: "search"; search: SearchHit };

  // Step 1: tokenize content into think / text parts (think tags anywhere, plus
  // a trailing unclosed tag while streaming). `inner`/`innerStart` give the think
  // body and its content offset so searches can be nested into it later.
  // Field-based reasoning_content has no inline tags → a single text part.
  let parts = $derived.by(() => {
    const res: { kind: "text" | "think"; text: string; start: number; end: number; innerStart: number; open: boolean; ms: number }[] = [];
    if (role !== "assistant") return res;
    const re = /<(think|thinking|reasoning)>([\s\S]*?)(<\/\1>|$)/gi;
    let last = 0;
    let ti = 0; // think-span ordinal, indexes thinkMs (server records one entry per closed span)
    let m: RegExpExecArray | null;
    while ((m = re.exec(displayContent))) {
      if (m.index > last) res.push({ kind: "text", text: displayContent.slice(last, m.index), start: last, end: m.index, innerStart: last, open: false, ms: 0 });
      const closed = m[3] !== "";
      res.push({ kind: "think", text: m[2], start: m.index, end: m.index + m[0].length, innerStart: m.index + m[1].length + 2, open: !closed, ms: thinkMs?.[ti++] ?? 0 });
      last = m.index + m[0].length;
      if (!closed) break; // unclosed think runs to the end of the stream
    }
    if (last < displayContent.length) res.push({ kind: "text", text: displayContent.slice(last), start: last, end: displayContent.length, innerStart: last, open: false, ms: 0 });
    return res;
  });

  // Step 2: merge searches (by offset) into the part stream. A search inside a
  // think part's range nests inside its reasoning box; one inside a text part
  // splits that text inline.
  let timeline = $derived.by<Segment[]>(() => {
    const out: Segment[] = [];
    if (role !== "assistant") return out;
    // Searches fired mid-think nest into the field-based reasoning box (see
    // reasoningItems), not the content timeline — drop them here. But only when
    // there IS a field-based box: inline-<think> models carry reasoning in the
    // content, so their mid-think searches nest into the think segment below via
    // their `at` offset, and dropping them here would lose them entirely.
    const list = (searches ?? []).filter((s) => !(s.duringReasoning && reasoning_content));
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
        out.push({ kind: "think", items, open: p.open, ms: p.ms });
        continue;
      }
      let cur = p.start;
      while (si < positioned.length && (positioned[si].at as number) < p.end) {
        const at = Math.max(cur, positioned[si].at as number);
        if (at > cur) out.push({ kind: "text", text: displayContent.slice(cur, at), idx: ti++ });
        out.push({ kind: "search", search: positioned[si++] });
        cur = at;
      }
      if (cur < p.end) out.push({ kind: "text", text: displayContent.slice(cur, p.end), idx: ti++ });
    }
    while (si < positioned.length) out.push({ kind: "search", search: positioned[si++] });
    // Coalesce think rounds into one box. A reasoning model that emits inline
    // <think> produces a fresh block each tool round (think → search → think →
    // answer). The rounds sit back-to-back, but a search whose offset lands in
    // the text gap between them surfaces as a top-level segment that would split
    // the boxes. Hold such searches: if another think follows, fold them into
    // the box (so it reads think → search → think as one); if answer text
    // follows, flush them ahead of it so post-reasoning searches stay outside.
    const merged: Segment[] = [];
    let pending: Extract<Segment, { kind: "search" }>[] = [];
    for (const seg of out) {
      const prev = merged[merged.length - 1];
      if (seg.kind === "think") {
        if (prev && prev.kind === "think") {
          for (const ps of pending) prev.items.push({ type: "search", search: ps.search });
          prev.items = [...prev.items, ...seg.items];
          prev.open = prev.open || seg.open;
          prev.ms += seg.ms; // coalesced rounds report their combined think time
        } else {
          merged.push(...pending, { ...seg, items: [...seg.items] });
        }
        pending = [];
      } else if (seg.kind === "search" && prev && prev.kind === "think") {
        pending.push(seg); // might be sandwiched between think rounds
      } else {
        merged.push(...pending, seg);
        pending = [];
      }
    }
    merged.push(...pending);
    return merged;
  });

  // Answer-phase tool calls (searches/wiki fired outside reasoning). Collected
  // into one collapsible "Sources" header below the answer instead of dotted
  // inline — the in-`Thought` searches still nest in the reasoning trail.
  let answerSearches = $derived(timeline.filter((s) => s.kind === "search").map((s) => (s as Extract<Segment, { kind: "search" }>).search));

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
  // Field-based reasoning (reasoning_content) with any mid-think searches nested
  // in by their reasoning_content offset, so a search the model ran while it was
  // still thinking renders inside the reasoning box at the right spot — not below
  // it. No searches → a single text item (plain reasoning render).
  let reasoningItems = $derived.by<SubItem[]>(() => {
    const text = reasoning_content;
    const hits = (searches ?? [])
      .filter((s) => s.duringReasoning)
      .slice()
      .sort((a, b) => (a.reasoningAt ?? 0) - (b.reasoningAt ?? 0));
    const items: SubItem[] = [];
    let cur = 0;
    for (const s of hits) {
      const rel = Math.max(cur, Math.min(text.length, s.reasoningAt ?? text.length));
      if (rel > cur) items.push({ type: "text", text: text.slice(cur, rel) });
      items.push({ type: "search", search: s });
      cur = rel;
    }
    if (cur < text.length) items.push({ type: "text", text: text.slice(cur) });
    return items;
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
      return renderStreamingMarkdown(seg.text, streamingCache, citations ?? []);
    }
    return { blocks: [{ id: -1, html: renderMarkdown(seg.text, citations ?? []) }], pendingHtml: "" };
  }
  $effect(() => {
    // Reset the streaming cache when a turn finishes so the next one starts clean.
    if (!isStreaming) streamingCache = createStreamingCache();
  });
  let copied = $state(false);
  let showRaw = $state(false);
  // Vertical offset (px, relative to the bubble top) the reply button tracks to.
  // Snaps to the CENTER of the text line under the cursor (via caret hit-testing)
  // so it steps line-by-line, and is clamped inside the bubble so it can't drift
  // past the text bounds.
  let replyY = $state(0);
  function trackReply(e: MouseEvent) {
    const el = e.currentTarget as HTMLElement;
    const top = el.getBoundingClientRect().top;
    const range = (document as any).caretRangeFromPoint?.(e.clientX, e.clientY) as Range | undefined;
    const rect = range?.getBoundingClientRect();
    const y = rect && rect.height > 0 ? rect.top - top + rect.height / 2 : e.clientY - top;
    replyY = Math.max(10, Math.min(el.clientHeight - 10, y));
  }
  let isEditing = $state(false);
  let editContent = $state("");
  let showReasoning = $state(false);
  let modalImageUrl = $state<string | null>(null);
  let textEl: HTMLDivElement | undefined = $state();
  // A bare textarea has no intrinsic width from its content (only from `cols`,
  // default 20ch), so it collapses the shrink-to-fit user bubble down to ~5
  // words wide. Capture the rendered text's actual width before switching to
  // edit mode and pin the textarea to it, so the bubble stays the size it was.
  let editWidth = $state<number | null>(null);

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
    const copyText = stripThinking(displayContent);
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
    editWidth = textEl?.clientWidth ?? null;
    isEditing = true;
  }

  function cancelEdit() {
    isEditing = false;
    editContent = "";
  }

  function saveEdit() {
    // Save = re-prompt: editMessage slices off later turns and regenerates from
    // here. Always fire (even unchanged text → a retry), never a silent no-op.
    if (onEdit) onEdit(editContent.trim());
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

  // Wiki citation chips (a.cite-wiki, injected as {@html}) can't carry a Svelte
  // handler, so delegate: a click on one opens the Help modal to its article id
  // instead of navigating. Web cites (plain a.cite target=_blank) are untouched.
  function wikiCiteClick(node: HTMLElement) {
    function onClick(e: MouseEvent) {
      const chip = (e.target as HTMLElement | null)?.closest("a.cite-wiki");
      if (!chip) return;
      e.preventDefault();
      const id = chip.getAttribute("data-wiki-id");
      if (id) openWikiArticle.set(id);
    }
    node.addEventListener("click", onClick);
    return { destroy: () => node.removeEventListener("click", onClick) };
  }
</script>

<!-- A tool step (web search / wiki lookup): a clickable label line that
     expands its raw results inline — no box, matching the reasoning trail.
     Top-level so both the reasoning trail and the Sources section can use it. -->
{#snippet searchLine(search: SearchHit)}
  <details class="not-prose group/s">
    <summary class="flex items-center gap-1.5 cursor-pointer select-none list-none [&::-webkit-details-marker]:hidden text-txtsecondary hover:text-txtmain transition-colors">
      {#if search.kind === "wiki"}
        <BookOpen class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Read: {search.query || "help wiki"}</span>
      {:else if search.kind === "quartermaster"}
        <Wrench class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Quartermaster: {search.query || "instance"}</span>
      {:else}
        <Search class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Searched: {search.query || "the web"}</span>
      {/if}
      <ChevronRight class="w-3 h-3 shrink-0 opacity-60 transition-transform group-open/s:rotate-90" />
    </summary>
    <div class="mt-1.5 whitespace-pre-wrap font-mono text-xs bg-background/40 rounded-md px-2 py-1.5 max-h-72 overflow-y-auto pretty-scroll">{search.results}</div>
  </details>
{/snippet}

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
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    onmousemove={role === "assistant" ? trackReply : undefined}
    class="relative group rounded-2xl px-3 py-2 text-[0.8125rem] {role === 'user'
      ? 'max-w-[85%] bg-[#141414] text-[#ededee] rounded-br-sm msg-tail-user'
      : (rewriteOriginal != null ? 'w-full' : 'w-full sm:w-4/5') + ' rounded-bl-sm'}"
  >
    {#if role === "assistant"}
      {#if onReply && !isStreaming}
        <button
          class="absolute left-full ml-2 -translate-y-1/2 z-10 p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary opacity-0 group-hover:opacity-100 transition-opacity"
          style="top: {replyY}px"
          onclick={onReply}
          title="Reply to this message"
        >
          <Reply class="w-4 h-4" />
        </button>
      {/if}
      <!-- DeepSeek-style reasoning trail: one dot per step (thought / searched),
           a gapped connector line between dots, content hanging to the right. -->
      {#snippet reasoningTrail(items: SubItem[])}
        <div class="mt-2 flex flex-col text-sm text-txtsecondary">
          {#each items as it, ii (ii)}
            <div class="flex gap-2.5">
              <div class="flex flex-col items-center pt-[0.3rem]">
                <span class="w-1.5 h-1.5 rounded-full bg-txtsecondary/70 shrink-0"></span>
                <span class="w-px flex-1 my-1 bg-card-border"></span>
              </div>
              <div class="min-w-0 flex-1 {ii < items.length - 1 ? 'pb-3' : ''}">
                {#if it.type === "search"}
                  {@render searchLine(it.search)}
                {:else if it.text}
                  <div class="prose prose-sm dark:prose-invert max-w-none chat-prose italic bg-background/40 rounded-md px-2 py-1.5">{@html renderMarkdown(it.text)}</div>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      {/snippet}
      <!-- Field-based reasoning (backend split it into reasoning_content). Inline
           <think> blocks render in the timeline below instead. -->
      {#if reasoning_content || isReasoning}
        <div class="mb-3">
          <button
            class="flex items-center gap-1.5 text-sm text-txtsecondary hover:text-txtmain transition-colors group"
            onclick={() => showReasoning = !showReasoning}
          >
            <ChevronRight class="w-3.5 h-3.5 transition-transform {showReasoning ? 'rotate-90' : ''}" />
            {#if isSearching}
              <span class="font-medium reason-shimmer-white thinking-dots">Searching</span>
            {:else if isReasoning}
              <span class="font-medium reason-shimmer-white thinking-dots">Thinking</span>
            {:else}
              <span class="font-medium">{#if reasoningTimeMs > 0}Thought for {formatDuration(reasoningTimeMs)}{:else}Thought{/if}</span>
            {/if}
          </button>
          <div class="reveal {showReasoning ? 'open' : ''}">
            <div class="reveal-inner">
              {@render reasoningTrail(reasoningItems)}
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
        <RewriteDiff original={rewriteOriginal} rewritten={stripThinking(displayContent)} {isStreaming} {modelReady} />
      {:else if showRaw}
        <div class="whitespace-pre-wrap font-mono text-sm">{textContent}</div>
      {:else}
        <div class="prose prose-sm dark:prose-invert max-w-none chat-prose" use:codeBlockCopy use:wikiCiteClick>
          <!-- Ordered timeline: inline think boxes, search blocks, and answer text. -->
          {#each timeline as seg, si (si)}
            {#if seg.kind === "search"}
              <!-- Rendered together under the "Sources" header below, not inline. -->
            {:else if seg.kind === "think"}
              {@const isOpen = si in thinkOverride ? thinkOverride[si] : seg.open}
              <details class="not-prose my-2" open={isOpen}>
                <summary
                  class="flex items-center gap-1.5 text-sm text-txtsecondary hover:text-txtmain transition-colors cursor-pointer select-none list-none [&::-webkit-details-marker]:hidden"
                  onclick={(e) => { e.preventDefault(); thinkOverride[si] = !isOpen; }}
                >
                  <ChevronRight class="w-3.5 h-3.5 shrink-0 transition-transform {isOpen ? 'rotate-90' : ''}" />
                  {#if isSearching && seg.open}
                    <span class="font-medium reason-shimmer-white thinking-dots">Searching</span>
                  {:else if seg.open}
                    <span class="font-medium reason-shimmer-white thinking-dots">Thinking</span>
                  {:else}
                    <span class="font-medium">{#if seg.ms > 0}Thought for {formatDuration(seg.ms)}{:else}Thought{/if}</span>
                  {/if}
                </summary>
                <div class="reveal">
                  <div class="reveal-inner">
                    {@render reasoningTrail(seg.items)}
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
          {#if isSearching && !isReasoning && !openThink}
            <!-- Post-reasoning search (answer phase). A mid-think search instead
                 shows its "Searching" label on the reasoning box itself. -->
            <span class="inline-flex items-center gap-2 text-sm italic">
              <span class="w-1.5 h-1.5 bg-primary rounded-full reason-glow"></span>
              <span class="reason-shimmer-white font-medium">Searching the web…</span>
            </span>
          {:else if isStreaming && !openThink && !isReasoning && !hasBodyText && !isSearching}
            <!-- No tokens yet. Order of waits: swap the model in, encode the image
                 (vision turns only), then generate. We get no explicit "encoding"
                 signal from the backend, so infer the vision phase from a
                 loaded-but-silent vision turn.
                 ponytail: heuristic — no real encode event; revisit if llama.cpp
                 ever surfaces one. -->
            <span class="inline-flex items-center gap-2 italic">
              <span class="w-1.5 h-1.5 bg-primary rounded-full reason-glow"></span>
              <span class="reason-shimmer-white font-medium">{!modelReady ? "Loading model…" : hasVisionInput ? "Processing image…" : "Generating…"}</span>
            </span>
          {/if}
        </div>
      {/if}
      {#if approval && approval.status === "pending"}
        <!-- Quartermaster config-change approval: before/after diff the model
             proposed, gated on the user's accept/deny. The turn is blocked
             server-side until a decision (or timeout). Transient by design —
             once decided it vanishes, and the outcome + diff live on in the
             tool step inside the reasoning trail ("Quartermaster: configure X
             — accepted"), which is server-persisted and survives a reload. -->
        <div class="mt-3 rounded-lg border border-card-border bg-background/40 overflow-hidden text-sm">
          <div class="flex items-center gap-1.5 px-3 py-2 border-b border-card-border bg-secondary/40">
            <Wrench class="w-3.5 h-3.5 shrink-0 text-primary" />
            <span class="font-medium">Config change · {approval.target}</span>
          </div>
          <div class="px-3 py-2 flex flex-col gap-1 font-mono text-xs">
            {#each approval.diff as row (row.key)}
              <div class="flex items-center gap-2">
                <span class="text-txtsecondary min-w-[9rem] truncate">{row.key}</span>
                <span class="text-txtsecondary/70 line-through">{fmtVal(row.before)}</span>
                <span class="opacity-50">→</span>
                <span class="text-txtmain font-medium">{fmtVal(row.after)}</span>
              </div>
            {/each}
          </div>
          <div class="flex justify-end gap-2 px-3 py-2 border-t border-card-border">
            <button
              class="px-3 py-1 rounded-md text-xs font-medium border border-card-border hover:bg-secondary transition-colors"
              onclick={() => onApprove?.(approval!.id, false)}
            >Deny</button>
            <button
              class="px-3 py-1 rounded-md text-xs font-medium bg-primary text-white hover:opacity-90 transition-opacity"
              onclick={() => onApprove?.(approval!.id, true)}
            >Accept</button>
          </div>
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
            class="{editWidth ? '' : 'w-full'} px-3 py-2 rounded border border-card-border bg-surface text-txtmain focus:outline-none focus:ring-2 focus:ring-primary resize-none overflow-hidden"
            style={editWidth ? `width:${editWidth}px` : undefined}
            rows="1"
            bind:value={editContent}
            use:autogrow
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
        <div class="prose prose-sm prose-invert max-w-none chat-prose user-msg-prose pr-8" bind:this={textEl} use:codeBlockCopy>
          {@html renderMarkdown(textContent)}
        </div>
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
  <!-- The ONE Sources section for the whole message: every answer-phase tool call
       (the in-`Thought` ones stay nested in their reasoning trail) followed by the
       deduped source pills. Both used to render their own "Sources" header. -->
  {#if allSources.length > 0 || answerSearches.length > 0}
    <details class="w-full sm:w-4/5 mt-1.5 group/pills">
      <summary class="flex items-center gap-1.5 text-sm text-txtsecondary hover:text-txtmain transition-colors cursor-pointer select-none list-none [&::-webkit-details-marker]:hidden">
        <ChevronRight class="w-3.5 h-3.5 shrink-0 transition-transform group-open/pills:rotate-90" />
        <span class="font-medium">Sources</span>
        <span class="opacity-60">({allSources.length || answerSearches.length})</span>
      </summary>
      {#if answerSearches.length > 0}
        <div class="mt-2 flex flex-col gap-1.5 text-sm">
          {#each answerSearches as s, i (i)}
            {@render searchLine(s)}
          {/each}
        </div>
      {/if}
      <div class="mt-2 flex flex-wrap gap-1.5" class:hidden={allSources.length === 0}>
        {#each allSources as src (src.url)}
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
    </details>
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
    border-left-color: #141414;
  }
  .msg-tail-bot::after {
    left: -5px;
    border-right-color: var(--color-surface);
  }

  .chat-prose {
    font-size: 0.8125rem;
    line-height: 1.55;
  }

  /* prose-invert assigns a different shade per element kind (body vs bold vs
     headings vs code…) — on the solid-black user bubble that reads as an
     uneven "gradient" across a message. Pin every prose color var to one
     flat white so the whole bubble's ink matches the bg-black/text-white it
     sits on. */
  .user-msg-prose {
    --tw-prose-invert-body: #ededee;
    --tw-prose-invert-headings: #ededee;
    --tw-prose-invert-lead: #ededee;
    --tw-prose-invert-links: #ededee;
    --tw-prose-invert-bold: #ededee;
    --tw-prose-invert-counters: #ededee;
    --tw-prose-invert-bullets: #ededee;
    --tw-prose-invert-hr: #ededee;
    --tw-prose-invert-quotes: #ededee;
    --tw-prose-invert-quote-borders: #ededee;
    --tw-prose-invert-captions: #ededee;
    --tw-prose-invert-code: #ededee;
    --tw-prose-invert-th-borders: #ededee;
    --tw-prose-invert-td-borders: #ededee;
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

  /* .reason-shimmer / .reason-glow live globally in index.css (shared by every
     live chat status label). */
  @media (prefers-reduced-motion: reduce) {
    .reveal {
      transition: none;
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

  /* @tailwindcss/typography wraps inline <code> in literal backtick
     pseudo-elements (content: "`"). Strip them — the box styling already marks
     code, and the stray backticks clipped ugly at line ends. */
  .prose :global(code)::before,
  .prose :global(code)::after {
    content: none;
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

  /* Tailwind Typography auto-adds open/close quote marks around blockquote
     text via ::before/::after content — redundant on top of the border-left
     indentation, so drop them. */
  .prose :global(blockquote p:first-of-type::before),
  .prose :global(blockquote p:last-of-type::after) {
    content: none;
  }

  .prose :global(a:not(.cite)) {
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
