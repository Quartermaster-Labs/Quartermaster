import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import remarkRehype from "remark-rehype";
import rehypeKatex from "rehype-katex";
import rehypeStringify from "rehype-stringify";
import hljs from "highlight.js";
import { visit } from "unist-util-visit";
import type { Element, Root } from "hast";

// Custom plugin to highlight code blocks with highlight.js
function rehypeHighlight() {
  return (tree: Root) => {
    visit(tree, "element", (node: Element) => {
      if (node.tagName === "code" && node.properties) {
        const className = node.properties.className;
        const classes = Array.isArray(className)
          ? className.filter((c): c is string => typeof c === "string")
          : [];
        const lang = classes
          .find((c) => c.startsWith("language-"))
          ?.replace("language-", "");

        const text = node.children
          .filter((child): child is { type: "text"; value: string } => child.type === "text")
          .map((child) => child.value)
          .join("");

        // Renderable fences (```mermaid, ```chart) keep their language class and
        // their raw source: `lib/diagrams.ts` turns them into an SVG/canvas after
        // the HTML lands in the DOM. Highlighting them would rewrite the text
        // into markup and there'd be no source left to render.
        if (lang === "mermaid" || lang === "chart") {
          node.properties.className = [`language-${lang}`, ...classes.filter((c) => !c.startsWith("language-"))];
          return;
        }

        if (text) {
          const language = lang && hljs.getLanguage(lang) ? lang : "plaintext";
          const highlighted = hljs.highlight(text, { language }).value;

          // Replace the text node with raw HTML
          node.properties.className = [
            "hljs",
            `language-${language}`,
            ...classes.filter((c) => !c.startsWith("language-")),
          ];
          // Use type assertion since we're modifying the tree structure
          (node.children as unknown) = [
            { type: "raw", value: highlighted },
          ];
        }
      }
    });
  };
}


// Inline citations. The chat models are told to append bracketed source numbers
// (`[1]`, `[2]`) after facts drawn from a web search; this turns those markers
// into clickable superscript chips linking to the source. Set per-render via a
// module global — rendering is synchronous and single-threaded, so no reentrancy.
// ponytail: module-global instead of threading through the shared processor.
export interface Citation {
  n: number;
  title: string;
  url: string;
  // Wiki citations set this (article id) instead of a real `url`; their chip
  // opens the in-app Help modal to the article rather than a browser tab.
  wikiId?: string;
}
let activeCitations: Citation[] = [];

function rehypeCitations() {
  return (tree: Root) => {
    if (activeCitations.length === 0) return;
    const byN = new Map(activeCitations.map((c) => [c.n, c]));
    visit(tree, "text", (node: { type: "text"; value: string }, index, parent) => {
      if (!parent || index == null) return;
      const p = parent as Element;
      // Don't rewrite inside code spans or existing links.
      if (p.type === "element" && (p.tagName === "code" || p.tagName === "a")) return;
      const value = node.value;
      if (!/\[\d+\]/.test(value)) return;

      const out: (Element | { type: "text"; value: string })[] = [];
      const re = /\[(\d+)\]/g;
      let last = 0;
      let m: RegExpExecArray | null;
      while ((m = re.exec(value))) {
        const c = byN.get(Number(m[1]));
        if (!c) continue; // not a known source — leave the literal text alone
        if (m.index > last) out.push({ type: "text", value: value.slice(last, m.index) });
        // Wiki citations have no URL — emit a chip the ChatMessage click handler
        // intercepts (data-wiki-id) to open the Help modal, not a target=_blank link.
        out.push(
          c.wikiId
            ? {
                type: "element",
                tagName: "a",
                properties: {
                  className: ["cite", "cite-wiki"],
                  href: "#",
                  "data-wiki-id": c.wikiId,
                  title: c.title,
                },
                children: [{ type: "text", value: String(c.n) }],
              }
            : {
                type: "element",
                tagName: "a",
                properties: {
                  className: ["cite"],
                  href: c.url,
                  title: c.title,
                  target: "_blank",
                  rel: "noopener noreferrer",
                },
                children: [{ type: "text", value: String(c.n) }],
              },
        );
        last = m.index + m[0].length;
      }
      if (out.length === 0) return;
      if (last < value.length) out.push({ type: "text", value: value.slice(last) });
      parent.children.splice(index, 1, ...(out as Element[]));
      return index + out.length; // skip over the nodes we just inserted
    });
  };
}

// Every ordinary link the model writes opens in a new tab. The chat IS the app:
// following a shop link in place tears down the playground (and, mid-turn, the
// SSE the answer is streaming over). Citation chips already set this themselves;
// in-app wiki chips (href="#", data-wiki-id) must NOT get it.
function rehypeExternalLinks() {
  return (tree: Root) => {
    visit(tree, "element", (node: Element) => {
      if (node.tagName !== "a" || !node.properties) return;
      const href = node.properties.href;
      if (typeof href !== "string" || !/^https?:\/\//i.test(href)) return;
      node.properties.target = "_blank";
      node.properties.rel = "noopener noreferrer";
    });
  };
}

export function escapeHtml(text: string): string {
  const htmlEntities: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
  return text.replace(/[&<>"']/g, (char) => htmlEntities[char]);
}

// Create the unified processor
const processor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkMath)
  .use(remarkRehype, { allowDangerousHtml: true })
  .use(rehypeKatex)
  .use(rehypeHighlight)
  .use(rehypeCitations)
  .use(rehypeExternalLinks)
  .use(rehypeStringify, { allowDangerousHtml: true });

export function splitCompleteBlocks(text: string): { complete: string; pending: string } {
  if (!text) {
    return { complete: "", pending: "" };
  }

  const lines = text.split("\n");
  let lastCompleteBoundary = -1; // index of last line that ends a complete block
  let inFence = false;
  let fenceChar = "";
  let inMathBlock = false;

  for (let i = 0; i < lines.length; i++) {
    const trimmed = lines[i].trimEnd();

    if (inFence) {
      // Check for closing fence: same character, at least 3, no other content
      if (new RegExp(`^\\s*${fenceChar.replace(/~/g, "\\~")}{3,}\\s*$`).test(trimmed)) {
        inFence = false;
        fenceChar = "";
        lastCompleteBoundary = i;
      }
      continue;
    }

    if (inMathBlock) {
      if (trimmed === "$$" || trimmed === "\\]") {
        inMathBlock = false;
        lastCompleteBoundary = i;
      }
      continue;
    }

    // Check for opening fence
    const fenceMatch = trimmed.match(/^(\s*)(```|~~~)/);
    if (fenceMatch) {
      // Check if it's an opening fence (may have language info after)
      // A line with just ``` or ~~~ could be opening or closing, but since we're not in a fence it's opening
      fenceChar = fenceMatch[2][0]; // '`' or '~'
      inFence = true;
      continue;
    }

    // Check for opening math block
    if (trimmed === "$$" || trimmed === "\\[") {
      inMathBlock = true;
      continue;
    }

    // Outside fences/math: blank line marks a complete boundary
    if (trimmed === "") {
      lastCompleteBoundary = i;
    }
  }

  if (lastCompleteBoundary < 0) {
    return { complete: "", pending: text };
  }

  const completeLines = lines.slice(0, lastCompleteBoundary + 1);
  const pendingLines = lines.slice(lastCompleteBoundary + 1);

  return {
    complete: completeLines.join("\n"),
    pending: pendingLines.join("\n"),
  };
}

export function closePendingBlock(pending: string): string {
  if (!pending) return "";

  const lines = pending.split("\n");
  let inFence = false;
  let fenceStr = "";
  let inMathBlock = false;
  let mathClose = "";

  for (const line of lines) {
    const trimmed = line.trimEnd();

    if (inFence) {
      if (new RegExp(`^\\s*${fenceStr[0] === "~" ? "~~~" : "\\`\\`\\`"}\\s*$`).test(trimmed)) {
        inFence = false;
        fenceStr = "";
      }
      continue;
    }

    if (inMathBlock) {
      if (trimmed === "$$" || trimmed === "\\]") {
        inMathBlock = false;
        mathClose = "";
      }
      continue;
    }

    const fenceMatch = trimmed.match(/^(\s*)(```|~~~)/);
    if (fenceMatch) {
      fenceStr = fenceMatch[2];
      inFence = true;
      continue;
    }

    if (trimmed === "$$") {
      inMathBlock = true;
      mathClose = "$$";
      continue;
    }

    if (trimmed === "\\[") {
      inMathBlock = true;
      mathClose = "\\]";
      continue;
    }
  }

  if (inFence) return pending + "\n" + fenceStr;
  if (inMathBlock) return pending + "\n" + mathClose;
  return pending;
}

export interface RenderedBlock {
  id: number;
  html: string;
}

export interface StreamingCache {
  blocks: RenderedBlock[];
  nextId: number;
  completeKey: string;
}

export function createStreamingCache(): StreamingCache {
  return { blocks: [], nextId: 0, completeKey: "" };
}

export function renderStreamingMarkdown(
  text: string,
  cache: StreamingCache,
  citations: Citation[] = [],
): { blocks: RenderedBlock[]; pendingHtml: string } {
  const { complete, pending } = splitCompleteBlocks(text);

  if (complete) {
    if (cache.completeKey !== complete) {
      if (complete.startsWith(cache.completeKey) && cache.completeKey.length > 0) {
        // Complete section grew - render only the new part as a new block
        const newPart = complete.slice(cache.completeKey.length);
        cache.blocks = [...cache.blocks, { id: cache.nextId++, html: renderMarkdown(newPart, citations) }];
      } else {
        // Complete section changed unexpectedly - re-render as single block
        cache.blocks = [{ id: cache.nextId++, html: renderMarkdown(complete, citations) }];
      }
      cache.completeKey = complete;
    }
  } else if (cache.blocks.length > 0) {
    cache.blocks = [];
    cache.completeKey = "";
  }

  let pendingHtml = "";
  if (pending) {
    const closed = closePendingBlock(pending);
    pendingHtml = renderMarkdown(closed, citations);
  }

  return { blocks: cache.blocks, pendingHtml };
}

// Convert \[...\] to $$...$$ and \(...\) to $...$
export function normalizeLatexDelimiters(text: string): string {
  // Display math: \[...\] → $$...$$  (may span multiple lines)
  text = text.replace(/\\\[([\s\S]*?)\\\]/g, (_match, inner) => `$$${inner}$$`);
  // Inline math: \(...\) → $...$
  text = text.replace(/\\\(([\s\S]*?)\\\)/g, (_match, inner) => `$${inner}$`);
  // Escape currency-style "$" (a "$" directly before a digit, not part of a "$$"
  // display delimiter) so remark-math doesn't pair "$18,000 ... $2,250" into a
  // bogus inline-math span that swallows the text (and any **bold**) between them.
  text = text.replace(/(?<![\\$])\$(?=\d)/g, "\\$");
  return text;
}

export function renderMarkdown(content: string, citations: Citation[] = []): string {
  if (!content) {
    return "";
  }

  activeCitations = citations;
  try {
    const result = processor.processSync(normalizeLatexDelimiters(content));
    return String(result);
  } catch {
    // Fallback to escaped plain text if markdown parsing fails
    return `<p>${escapeHtml(content)}</p>`;
  } finally {
    activeCitations = [];
  }
}
