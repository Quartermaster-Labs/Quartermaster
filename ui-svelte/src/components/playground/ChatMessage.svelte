<script lang="ts">
  import { tip } from "../../lib/tooltip";
  import { renderMarkdown, renderStreamingMarkdown, createStreamingCache } from "../../lib/markdown";
  import type { RenderedBlock } from "../../lib/markdown";
  import { Copy, Check, Pencil, X, Save, RefreshCw, ChevronRight, Search, BookOpen, PenLine, Wrench, Reply, Youtube, FileText, ArrowRightLeft, Clock, Calculator, Ruler, CloudSun, Rss, Sparkles, Volume2, Loader2, Square, BrainCircuit } from "lucide-svelte";
  import { generateSpeech } from "../../lib/speechApi";
  import { effectiveTtsModel, chatTtsVoiceStore } from "../../stores/playground";
  import { getTextContent, getImageUrls } from "../../lib/types";
  import { splitFileBlocks } from "../../lib/attachments";
  import { activityLabel, harmonyToThink, thinkSummary } from "../../lib/reasoning";
  import type { ContentPart, QmApproval } from "../../lib/types";
  import RewriteDiff from "./RewriteDiff.svelte";
  import YouTubeEmbed from "./YouTubeEmbed.svelte";
  import { extractYouTubeIds } from "../../lib/youtube";
  import { autogrow } from "../../lib/autogrow";
  import { splitAsk } from "../../lib/askBlock";
  import AskWizard from "./AskWizard.svelte";
  import { splitProducts, repairProductUrls } from "../../lib/productBlock";
  import ProductReport from "./ProductReport.svelte";
  import { diagramBlocks } from "../../lib/diagrams";
  import { openWikiArticle } from "../../stores/wiki";

  interface Props {
    role: "user" | "assistant" | "system" | "tool";
    content: string | ContentPart[];
    // Model that produced this assistant turn (blank on older messages).
    model?: string;
    reasoning_content?: string;
    reasoningTimeMs?: number;
    thinkMs?: number[];
    // Reasoning gists from the server's CPU title model (titlegen.go), landing
    // per box as that box closes. Absent or blank (still being titled, older
    // messages, titling not installed) → the local thinkSummary heuristic.
    reasoningTitle?: string;
    thinkTitles?: string[];
    genTimeMs?: number;
    searches?: { query: string; results: string; kind?: "web" | "wiki" | "quartermaster" | "memory" | "youtube" | "youtube-search" | "youtube-comments" | "page" | "currency" | "time" | "calc" | "units" | "weather" | "feed"; at?: number; reasoningAt?: number; duringReasoning?: boolean; sources?: { title: string; url: string }[] }[];
    citations?: { n: number; title: string; url: string; wikiId?: string }[];
    approval?: QmApproval;
    onApprove?: (id: string, accept: boolean) => void;
    rewriteInstruction?: string;
    rewriteOriginal?: string;
    isStreaming?: boolean;
    isReasoning?: boolean;
    isSearching?: boolean;
    // Live activity label from the server ('Searching for "x"', 'Reading shop.com').
    busyLabel?: string;
    modelReady?: boolean;
    hasVisionInput?: boolean;
    onEdit?: (newContent: string) => void;
    onRegenerate?: () => void;
    onReply?: () => void;
    // Set only on the last finished assistant turn: enables the ```ask wizard,
    // whose picks are sent back as a normal user message.
    onAskAnswer?: (text: string) => void;
  }

  let { role, content, model = "", reasoning_content = "", reasoningTimeMs = 0, thinkMs, reasoningTitle: serverReasoningTitle = "", thinkTitles, genTimeMs = 0, searches, citations, approval, onApprove, rewriteInstruction, rewriteOriginal, isStreaming = false, isReasoning = false, isSearching = false, busyLabel = "", modelReady = false, hasVisionInput = false, onEdit, onRegenerate, onReply, onAskAnswer }: Props = $props();

  // Format a JSON diff value for the approval card (null → "auto", strings bare).
  function fmtVal(v: unknown): string {
    if (v === null || v === undefined || v === "") return "auto";
    return typeof v === "string" ? v : JSON.stringify(v);
  }

  let textContent = $derived(getTextContent(content));
  // A ```ask block turns into the click-through wizard below the answer, and is
  // taken out of the prose so the raw JSON never shows — including mid-stream,
  // where the half-written fence becomes a "writing options" label. The wizard
  // itself only appears on the message the user can reply to (onAskAnswer set =
  // last, finished assistant turn).
  let ask = $derived(role === "assistant" ? splitAsk(textContent) : null);
  // Same treatment for the shopping report's ```products block: lifted out of the
  // prose and rendered as cards below the answer. Split AFTER the ask block so a
  // turn carrying both keeps each one's fence out of the other's prose. Unlike
  // the wizard this is read-only, so it shows on every assistant turn, not just
  // the last one.
  let products = $derived(role === "assistant" ? splitProducts(ask ? ask.cleaned : textContent) : null);
  // Pages this turn actually opened, so a card pointing at a shop's search page
  // can be re-pointed at the product page the assistant already read.
  let fetchedPages = $derived(
    (searches ?? [])
      .filter((s) => s.kind === "page")
      .flatMap((s) => (s.sources ?? []).map((x) => ({ title: x.title || "", url: x.url || "" })))
      .filter((x) => x.url),
  );
  let productReport = $derived(
    products?.report ? { ...products.report, products: repairProductUrls(products.report.products, fetchedPages) } : null,
  );
  // Some models (gpt-oss harmony et al.) emit reasoning as channel markup
  // (`<|channel|>analysis<|message|>…<|end|>…<|channel|>final<|message|>…`)
  // that llama.cpp's `--reasoning-format auto` doesn't parse, so it leaks raw
  // into content. Rewrite non-final channels into <think> so the timeline picks
  // them up as reasoning boxes. No-op when no channel markup is present.
  // Attached documents are inlined into the user's message as <file> blocks (the
  // transcript is the storage — see lib/attachments.ts). Lift them back out so
  // the bubble shows a chip instead of 40k characters of PDF, and so a link
  // inside a document doesn't unfurl as if the user had pasted it.
  let userFiles = $derived(role === "user" ? splitFileBlocks(textContent) : null);
  let displayContent = $derived(
    role === "assistant"
      ? harmonyToThink(products ? products.cleaned : ask ? ask.cleaned : textContent)
      : (userFiles?.rest ?? textContent),
  );
  let wordCount = $derived(stripThinking(displayContent).trim().split(/\s+/).filter(Boolean).length);
  // YouTube links anywhere in the message get a Discord-style card underneath.
  // Derived from displayContent so a link the model wrote in its answer unfurls
  // too, and reasoning-only mentions don't (stripThinking runs first).
  let ytIds = $derived(extractYouTubeIds(stripThinking(displayContent)));
  let imageUrls = $derived(getImageUrls(content));
  let hasImages = $derived(imageUrls.length > 0);
  // Editing rewrites the whole message string; with attachments that would put
  // the raw document text in the textarea (or drop it on save), so it is off for
  // those messages exactly as it already is for images.
  let canEdit = $derived(onEdit !== undefined && !hasImages && (userFiles?.files.length ?? 0) === 0);
  let openFile = $state<string | null>(null);

  // The assistant turn is one string holding, in order: inline <think> blocks
  // (when the backend emits reasoning inline instead of in reasoning_content),
  // answer text, and — across tool rounds — more think blocks and answer text.
  // Searches are recorded separately with the content offset (`at`) where they
  // ran. Build one ordered timeline so think boxes and search blocks render
  // inline between the surrounding text, not pinned to the top.
  type SearchHit = { query: string; results: string; kind?: "web" | "wiki" | "quartermaster" | "memory" | "youtube" | "youtube-search" | "youtube-comments" | "page" | "currency" | "time" | "calc" | "units" | "weather" | "feed"; sources?: { title: string; url: string }[] };
  type SubItem = { type: "text"; text: string } | { type: "search"; search: SearchHit };
  type Segment =
    | { kind: "text"; text: string; idx: number }
    | { kind: "think"; items: SubItem[]; open: boolean; ms: number; title: string }
    | { kind: "search"; search: SearchHit };

  // Step 1: tokenize content into think / text parts (think tags anywhere, plus
  // a trailing unclosed tag while streaming). `inner`/`innerStart` give the think
  // body and its content offset so searches can be nested into it later.
  // Field-based reasoning_content has no inline tags → a single text part.
  let parts = $derived.by(() => {
    const res: { kind: "text" | "think"; text: string; start: number; end: number; innerStart: number; open: boolean; ms: number; title: string }[] = [];
    if (role !== "assistant") return res;
    const re = /<(think|thinking|reasoning)>([\s\S]*?)(<\/\1>|$)/gi;
    let last = 0;
    let ti = 0; // think-span ordinal, indexes thinkMs/thinkTitles (server records one entry per closed span)
    let m: RegExpExecArray | null;
    while ((m = re.exec(displayContent))) {
      if (m.index > last) res.push({ kind: "text", text: displayContent.slice(last, m.index), start: last, end: m.index, innerStart: last, open: false, ms: 0, title: "" });
      const closed = m[3] !== "";
      res.push({ kind: "think", text: m[2], start: m.index, end: m.index + m[0].length, innerStart: m.index + m[1].length + 2, open: !closed, ms: thinkMs?.[ti] ?? 0, title: thinkTitles?.[ti] ?? "" });
      ti++;
      last = m.index + m[0].length;
      if (!closed) break; // unclosed think runs to the end of the stream
    }
    if (last < displayContent.length) res.push({ kind: "text", text: displayContent.slice(last), start: last, end: displayContent.length, innerStart: last, open: false, ms: 0, title: "" });
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
        out.push({ kind: "think", items, open: p.open, ms: p.ms, title: p.title });
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
    // the boxes. Hold EVERY search: if a think follows, fold it into that box
    // (so it reads think → search → think as one, and the box's header names the
    // activity); if answer text follows, flush ahead of it so post-answer
    // searches stay outside and land in the "Sources" fold.
    //
    // Holding forward as well as backward matters for models that write a
    // natural-language preamble before each tool call (Qwen3.8's template asks
    // for exactly that): their rounds read text → search → think, so the search
    // has no preceding think to attach to and used to fall through to the bottom
    // fold — invisible while streaming and easy to miss afterwards.
    const merged: Segment[] = [];
    let pending: Extract<Segment, { kind: "search" }>[] = [];
    for (const seg of out) {
      const prev = merged[merged.length - 1];
      if (seg.kind === "think") {
        const held: SubItem[] = pending.map((ps) => ({ type: "search", search: ps.search }));
        if (prev && prev.kind === "think") {
          prev.items = [...prev.items, ...held, ...seg.items];
          prev.open = prev.open || seg.open;
          prev.ms += seg.ms; // coalesced rounds report their combined think time
          if (!prev.title) prev.title = seg.title; // first round's gist names the merged box
        } else {
          merged.push({ ...seg, items: [...held, ...seg.items] });
        }
        pending = [];
      } else if (seg.kind === "search") {
        pending.push(seg); // might be followed by the think round it belongs to
      } else if (!seg.text.trim()) {
        // Whitespace-only gap (the "\n\n" between a tool call and the next
        // <think>): transparent, so it must not flush the held searches.
        merged.push(seg);
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

  // One-line gist shown beside "Thought for Xs" on a collapsed box, so the
  // header says something about the thought instead of only how long it took.
  // The server's title model only answers once the turn is finished, so the
  // local heuristic carries the box while it streams and the model title takes
  // over when it lands.
  let reasoningTitle = $derived(serverReasoningTitle || thinkSummary(reasoning_content));
  function segTitle(items: SubItem[], server: string): string {
    if (server) return server;
    const first = items.find((i) => i.type === "text" && i.text.trim().length > 0);
    return first && first.type === "text" ? thinkSummary(first.text) : "";
  }

  // "Thought for 2s" is the only true thing to say about a box that only thought.
  // A box that ran tools did something more specific, and the turn's own tool
  // records already say what — so the verb is read off them rather than asked of
  // the title model, which cannot phrase an activity at 80M (see titlegen.go).
  function boxActivity(items: SubItem[]): string {
    return activityLabel(items.flatMap((i) => (i.type === "search" ? [i.search.kind ?? "web"] : [])));
  }
  let reasoningActivity = $derived(boxActivity(reasoningItems));

  // User open/close state for inline reasoning boxes, keyed by timeline index.
  // Absent = collapsed; nothing auto-opens. `seg.open` (span still streaming)
  // drives the summary label only, never the disclosure.
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

  // --- read aloud -----------------------------------------------------------
  // Speaks the answer through the TTS model picked in the chat settings. The
  // model is loaded on demand by the router like any other request, so the first
  // click can take a while (and can evict the chat model — one GPU, one pool).
  let speakState = $state<"idle" | "loading" | "playing">("idle");
  let speakErr = $state("");
  let audioEl: HTMLAudioElement | null = null;
  let audioUrl = "";
  let speakAbort: AbortController | null = null;

  // Long answers: TTS cost is linear in characters and a 5k-word reply would tie
  // up the GPU for minutes, so cut it at a paragraph boundary near the cap.
  const SPEAK_MAX = 4000;

  // Markdown read aloud is unlistenable ("star star note star star"), so flatten
  // it to prose: drop code blocks outright, unwrap links/emphasis, strip list and
  // heading markers, and drop the citation brackets the renderer turns into chips.
  function speechText(md: string): string {
    let t = stripThinking(md)
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/`([^`]*)`/g, "$1")
      .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
      .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
      .replace(/^\s{0,3}#{1,6}\s+/gm, "")
      .replace(/^\s{0,3}[-*+]\s+/gm, "")
      .replace(/^\s{0,3}>\s?/gm, "")
      .replace(/(\*\*|__|\*|_|~~)/g, "")
      .replace(/^\s*\|.*\|\s*$/gm, " ")
      .replace(/\[\d+\]/g, "")
      .replace(/[ \t]+/g, " ")
      .replace(/\n{3,}/g, "\n\n")
      .trim();
    if (t.length > SPEAK_MAX) {
      const cut = t.slice(0, SPEAK_MAX);
      const brk = Math.max(cut.lastIndexOf("\n\n"), cut.lastIndexOf(". "));
      t = (brk > SPEAK_MAX / 2 ? cut.slice(0, brk + 1) : cut).trim();
    }
    return t;
  }

  let speakTitle = $derived(
    speakErr ? speakErr :
    speakState === "playing" ? "Stop" :
    speakState === "loading" ? "Generating speech…" :
    "Read aloud",
  );

  function stopSpeak() {
    speakAbort?.abort();
    speakAbort = null;
    audioEl?.pause();
    audioEl = null;
    if (audioUrl) URL.revokeObjectURL(audioUrl);
    audioUrl = "";
    speakState = "idle";
  }

  async function toggleSpeak() {
    if (speakState !== "idle") {
      stopSpeak();
      return;
    }
    const model = $effectiveTtsModel;
    if (!model) return;
    const text = speechText(displayContent);
    if (!text) return;
    speakErr = "";
    speakState = "loading";
    speakAbort = new AbortController();
    try {
      const blob = await generateSpeech(model, text, $chatTtsVoiceStore, speakAbort.signal);
      if (!speakAbort) return; // stopped while generating
      audioUrl = URL.createObjectURL(blob);
      audioEl = new Audio(audioUrl);
      audioEl.onended = stopSpeak;
      audioEl.onerror = () => { speakErr = "Playback failed"; stopSpeak(); };
      await audioEl.play();
      speakState = "playing";
    } catch (e) {
      if ((e as Error)?.name !== "AbortError") speakErr = (e as Error)?.message ?? "Speech failed";
      stopSpeak();
    }
  }

  // Leaving the page mid-clip must not keep the audio (or its object URL) alive.
  $effect(() => () => stopSpeak());
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
  // The ask wizard sits inside the assistant bubble, so the bubble's group-hover
  // offered "reply to this message" while the cursor was on a form control -
  // answering the questions IS the reply. Suppressed while pointing at it.
  let overAsk = $state(false);
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
      {:else if search.kind === "memory"}
        <BrainCircuit class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Memory: {search.query || "updated"}</span>
      {:else if search.kind === "youtube-search"}
        <Youtube class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Searched YouTube: {search.query || "videos"}</span>
      {:else if search.kind === "youtube-comments"}
        <Youtube class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Read comments: {search.query || "YouTube video"}</span>
      {:else if search.kind === "youtube"}
        <!-- Captions, not viewing: "Watched" claimed the model saw a video it
             only read the transcript of. -->
        <Youtube class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Read transcript: {search.query || "YouTube video"}</span>
      {:else if search.kind === "page"}
        <FileText class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Read page: {search.query || "web page"}</span>
      {:else if search.kind === "currency"}
        <ArrowRightLeft class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Converted: {search.query || "currency"}</span>
      {:else if search.kind === "time"}
        <Clock class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Checked the date: {search.query || "now"}</span>
      {:else if search.kind === "calc"}
        <Calculator class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Calculated: {search.query || "expression"}</span>
      {:else if search.kind === "units"}
        <Ruler class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Converted: {search.query || "units"}</span>
      {:else if search.kind === "weather"}
        <CloudSun class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Weather: {search.query || "forecast"}</span>
      {:else if search.kind === "feed"}
        <Rss class="w-3 h-3 shrink-0" />
        <span class="font-medium truncate">Read feed: {search.query || "feed"}</span>
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
  <!-- Which model wrote this turn — same header as the image tab. A thread can
       switch models mid-conversation, so the composer's selection doesn't answer it. -->
  {#if role === "assistant" && model}
    <span class="flex items-center gap-1 mb-1 px-3 text-[0.6875rem] font-medium text-txtsecondary">
      <Sparkles class="w-3 h-3 shrink-0" />{model}
    </span>
  {/if}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    onmousemove={role === "assistant" ? trackReply : undefined}
    class="relative group rounded-2xl px-3 py-2 text-[0.8125rem] {role === 'user'
      ? 'max-w-[85%] bg-[#141414] text-[#ededee] rounded-br-sm msg-tail-user'
      : (rewriteOriginal != null ? 'w-full' : 'w-full sm:w-4/5') + ' rounded-bl-sm'}"
  >
    {#if role === "assistant"}
      {#if onReply && !isStreaming && !overAsk}
        <button
          class="absolute left-full ml-2 -translate-y-1/2 z-10 p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary opacity-0 group-hover:opacity-100 transition-opacity"
          style="top: {replyY}px"
          onclick={onReply}
          use:tip={"Reply to this message"}
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
              <span class="font-medium reason-shimmer-white thinking-dots">{busyLabel || "Searching"}</span>
            {:else if isReasoning}
              <span class="font-medium reason-shimmer-white thinking-dots">Thinking</span>
            {:else}
              <span class="font-medium shrink-0">{#if reasoningActivity}{reasoningActivity}{:else if reasoningTimeMs > 0}Thought for {formatDuration(reasoningTimeMs)}{:else}Thought{/if}</span>
              {#if reasoningTitle && !showReasoning}
                <span class="opacity-60 truncate text-left min-w-0">· {reasoningTitle}</span>
              {/if}
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
      {:else}
        <div class="prose prose-sm dark:prose-invert max-w-none chat-prose" use:codeBlockCopy use:wikiCiteClick use:diagramBlocks={!isStreaming}>
          <!-- Ordered timeline: inline think boxes, search blocks, and answer text. -->
          {#each timeline as seg, si (si)}
            {#if seg.kind === "search"}
              <!-- Rendered together under the "Sources" header below, not inline. -->
            {:else if seg.kind === "think"}
              <!-- Collapsed unless the user opens it, matching the field-based
                   box above (showReasoning starts false). Following `seg.open`
                   meant every inline think span — i.e. every thought after the
                   first, and every tool round — unfolded itself mid-stream while
                   the first one stayed hidden. The summary still shimmers
                   "Thinking"/"Searching" off seg.open, so live state is visible
                   without dumping the thought into the bubble. -->
              {@const isOpen = thinkOverride[si] === true}
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
                    {@const title = isOpen ? "" : segTitle(seg.items, seg.title)}
                    {@const act = boxActivity(seg.items)}
                    <span class="font-medium shrink-0">{#if act}{act}{:else if seg.ms > 0}Thought for {formatDuration(seg.ms)}{:else}Thought{/if}</span>
                    {#if title}
                      <span class="opacity-60 truncate min-w-0">· {title}</span>
                    {/if}
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
              <span class="reason-shimmer-white thinking-dots font-medium">{busyLabel || "Searching the web"}</span>
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
      <!-- Link unfurls sit inside the bubble, under the text, so they read as
           part of the message rather than a turn of their own. -->
      {#each ytIds as vid (vid)}
        <YouTubeEmbed id={vid} />
      {/each}
      <!-- ```products block → shopping report cards (picture, price, why). -->
      {#if products?.pending}
        <div class="mt-2 inline-flex items-center gap-2 text-sm italic">
          <span class="w-1.5 h-1.5 bg-primary rounded-full reason-glow"></span>
          <span class="reason-shimmer-white font-medium">Building your comparison…</span>
        </div>
      {:else if productReport}
        <ProductReport report={productReport} />
      {/if}
      <!-- ```ask block → click-through answers instead of a numbered list to retype. -->
      {#if ask?.pending}
        <div class="mt-2 inline-flex items-center gap-2 text-sm italic">
          <span class="w-1.5 h-1.5 bg-primary rounded-full reason-glow"></span>
          <span class="reason-shimmer-white font-medium">Writing your options…</span>
        </div>
      {:else if ask?.questions && onAskAnswer && !isStreaming}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div onmouseenter={() => (overAsk = true)} onmouseleave={() => (overAsk = false)}>
          <AskWizard questions={ask.questions} onSubmit={onAskAnswer} />
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
              use:tip={"Regenerate response"}
            >
              <RefreshCw class="w-4 h-4" />
            </button>
          {/if}
          <button
            class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 text-txtsecondary"
            onclick={copyToClipboard}
            use:tip={copied ? "Copied!" : "Copy to clipboard"}
          >
            {#if copied}
              <Check class="w-4 h-4 text-green-500" />
            {:else}
              <Copy class="w-4 h-4" />
            {/if}
          </button>
          <!-- No TTS model installed → no speaker button, rather than a button
               that can only ever explain why it does nothing. -->
          {#if $effectiveTtsModel}
          <button
            class="p-1 rounded hover:bg-black/10 dark:hover:bg-white/10 {speakState === 'idle' ? 'text-txtsecondary' : 'text-primary'}"
            onclick={toggleSpeak}
            use:tip={speakTitle}
          >
            {#if speakState === "loading"}
              <Loader2 class="w-4 h-4 animate-spin" />
            {:else if speakState === "playing"}
              <Square class="w-4 h-4" fill="currentColor" />
            {:else}
              <Volume2 class="w-4 h-4" />
            {/if}
          </button>
          {/if}
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
              use:tip={"Cancel"}
            >
              <X class="w-4 h-4" />
            </button>
            <button
              class="p-1.5 rounded hover:bg-white/20"
              onclick={saveEdit}
              use:tip={"Save"}
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
        {#if userFiles && userFiles.files.length > 0}
          <div class="mb-2 flex flex-col gap-1.5">
            {#each userFiles.files as file, fi (fi)}
              <div class="rounded-lg border border-white/20 bg-white/10 overflow-hidden">
                <button
                  class="flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-[0.8125rem] hover:bg-white/10 transition-colors"
                  onclick={() => (openFile = openFile === `${fi}` ? null : `${fi}`)}
                  use:tip={openFile === `${fi}` ? "Hide contents" : "Show contents"}
                >
                  <FileText class="w-3.5 h-3.5 shrink-0" />
                  <span class="truncate">{file.name}</span>
                  {#if file.note}<span class="shrink-0 opacity-70">· {file.note}</span>{/if}
                  <ChevronRight
                    class="w-3.5 h-3.5 shrink-0 ml-auto transition-transform {openFile === `${fi}` ? 'rotate-90' : ''}"
                  />
                </button>
                {#if openFile === `${fi}`}
                  <pre class="max-h-72 overflow-auto px-2.5 py-2 text-[0.75rem] leading-relaxed whitespace-pre-wrap break-words border-t border-white/20 pretty-scroll">{file.text}</pre>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        {#if displayContent}
          <div class="prose prose-sm prose-invert max-w-none chat-prose user-msg-prose pr-8" bind:this={textEl} use:codeBlockCopy>
            {@html renderMarkdown(displayContent)}
          </div>
        {/if}
        {#each ytIds as vid (vid)}
          <YouTubeEmbed id={vid} onDark />
        {/each}
        {#if canEdit}
          <button
            class="absolute top-1.5 right-1.5 p-1 rounded-full opacity-0 group-hover:opacity-100 transition-all bg-white/10 text-white/70 hover:text-white hover:bg-white/25"
            onclick={startEdit}
            use:tip={"Edit message"}
          >
            <Pencil class="w-3 h-3" />
          </button>
        {/if}
      {/if}
    {/if}
  </div>
  <!-- The ONE Sources section for the whole message: every answer-phase tool call
       (the in-`Thought` ones stay nested in their reasoning trail) followed by the
       deduped source pills. Both used to render their own "Sources" header.
       Held back until the turn finishes: mid-stream it sits pinned under the
       growing answer, and its count/contents keep shifting as tool rounds land. -->
  {#if !isStreaming && (allSources.length > 0 || answerSearches.length > 0)}
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
            use:tip={src.url}
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
      use:tip={"Close"}
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

  /* Rendered ```mermaid / ```chart blocks (lib/diagrams.ts). The chart canvas
     needs a fixed-height box — Chart.js sizes to its parent, and a canvas in an
     auto-height flow collapses to nothing. */
  .prose :global(.diagram-block) {
    margin: 0.75rem 0;
    padding: 0.75rem;
    border: 1px solid var(--color-border);
    border-radius: 0.5rem;
    background: var(--color-surface);
    overflow-x: auto;
  }

  .prose :global(.diagram-block .diagram-out) {
    display: flex;
    justify-content: center;
  }

  .prose :global(.diagram-block canvas) {
    max-width: 100%;
  }

  .prose :global(.diagram-block .diagram-out:has(canvas)) {
    height: 18rem;
    position: relative;
  }

  .prose :global(.diagram-src-btn) {
    margin-top: 0.5rem;
    padding: 0.125rem 0.5rem;
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    border-radius: 0.25rem;
    border: 1px solid var(--color-border);
    color: var(--color-txtsecondary);
    background: transparent;
    cursor: pointer;
  }

  .prose :global(.diagram-src-btn:hover),
  .prose :global(.diagram-src-btn.open) {
    background: var(--color-secondary);
    color: var(--color-txtmain);
  }

  .prose :global(.diagram-error) {
    margin: 0.5rem 0 -0.25rem;
    font-size: 0.75rem;
    color: var(--color-txtsecondary);
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
