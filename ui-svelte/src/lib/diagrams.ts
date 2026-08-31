// Diagrams and charts in chat answers.
//
// Deliberately NOT a tool: the model has nothing to execute server-side, it just
// writes diagram source. So it emits a fenced block — ```mermaid for diagrams,
// ```chart for a Chart.js config — and the client turns that into a picture
// after `renderMarkdown` has put the HTML in the DOM. `lib/markdown.ts` leaves
// those two languages unhighlighted so the raw source survives to here.
//
// A fenced block that IS an SVG document is handled here too, and is the one
// case with no renderer at all: the browser already draws SVG. What it needs
// instead is a sanitizer (`lib/svgSanitize.ts`), because unlike a mermaid or
// chart source -- which we hand to a library that only ever emits a picture --
// this source becomes live markup in our own document.
//
// Both renderers are lazy: mermaid is a ~500 KB chunk that most chats never
// need, so it's only imported when a diagram actually shows up.
import { get } from "svelte/store";
import { isDarkMode } from "../stores/theme";
import { sanitizeSvg } from "./svgSanitize";
import { cssZoom } from "./uiZoom";

// Chart types we accept from a model-authored config. Chart.js will happily
// build anything its registry knows; the allowlist keeps a malformed/hostile
// block from becoming a surprise.
const CHART_TYPES = new Set(["bar", "line", "pie", "doughnut", "radar", "polarArea", "scatter", "bubble"]);

let mermaidReady: Promise<typeof import("mermaid").default> | null = null;
let mermaidDark: boolean | null = null;
let seq = 0;
let svgSeq = 0;

async function getMermaid(dark: boolean) {
  if (!mermaidReady) {
    mermaidReady = import("mermaid").then((m) => m.default);
  }
  const mermaid = await mermaidReady;
  // initialize() is idempotent and re-callable; re-run it when the app theme
  // flipped since the last render so new diagrams match the page.
  if (mermaidDark !== dark) {
    mermaid.initialize({
      startOnLoad: false,
      // Model-authored source: strict keeps labels sanitized and blocks
      // click-handler/script directives in the diagram.
      securityLevel: "strict",
      theme: dark ? "dark" : "default",
      fontFamily: "inherit",
    });
    mermaidDark = dark;
  }
  return mermaid;
}

async function renderMermaid(host: HTMLElement, src: string, dark: boolean) {
  const mermaid = await getMermaid(dark);
  // parse() first: a syntax error thrown by render() can leave mermaid's own
  // error banner attached to the document body.
  await mermaid.parse(src);
  const { svg } = await mermaid.render(`qm-diagram-${seq++}`, src);
  host.innerHTML = svg;
  const el = host.querySelector("svg");
  if (el) {
    el.removeAttribute("height");
    el.style.maxWidth = "100%";
  }
}

async function renderChart(host: HTMLElement, src: string, dark: boolean) {
  const cfg = JSON.parse(src);
  if (!cfg || typeof cfg !== "object" || !CHART_TYPES.has(cfg.type)) {
    throw new Error(`unsupported chart type: ${cfg?.type}`);
  }
  const { Chart, registerables } = await import("chart.js");
  Chart.register(...registerables);

  const canvas = document.createElement("canvas");
  host.appendChild(canvas);
  const grid = dark ? "rgba(255,255,255,0.12)" : "rgba(0,0,0,0.1)";
  const text = dark ? "#d4d4d8" : "#3f3f46";
  new Chart(canvas, {
    ...cfg,
    options: {
      responsive: true,
      maintainAspectRatio: false,
      ...(cfg.options ?? {}),
      // chart.js sizes the backing store from the canvas's LOCAL css size, so
      // its default ratio misses the interface zoom `--qm-scale` puts on :root
      // and the chart comes out a blurry upscale at any size above 100%. Same
      // correction the dashboard's own charts make (PerformanceChart.svelte);
      // the canvas is in the DOM by now, so its zoom is readable.
      devicePixelRatio: (window.devicePixelRatio || 1) * cssZoom(canvas),
      // Theme last: a model config shouldn't be able to hand us unreadable
      // black-on-black axes.
      plugins: {
        ...(cfg.options?.plugins ?? {}),
        legend: { labels: { color: text }, ...(cfg.options?.plugins?.legend ?? {}) },
      },
      scales:
        cfg.type === "pie" || cfg.type === "doughnut" || cfg.type === "polarArea"
          ? undefined
          : {
              x: { ticks: { color: text }, grid: { color: grid }, ...(cfg.options?.scales?.x ?? {}) },
              y: { ticks: { color: text }, grid: { color: grid }, ...(cfg.options?.scales?.y ?? {}) },
            },
    },
  });
}

const COPY_ICON = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`;
const CHECK_ICON = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>`;

async function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.cssText = "position:fixed;left:-9999px";
  document.body.appendChild(ta);
  ta.select();
  document.execCommand("copy");
  document.body.removeChild(ta);
}

/**
 * Turns one `<pre><code>` holding an SVG document into a preview/source card.
 *
 * The `<pre>` is MOVED inside the card rather than left as a sibling (which is
 * what the diagram path does): the two are one control here -- Preview and
 * Source are modes of the same block, not a picture with the source tacked
 * underneath -- and only one of them is on screen at a time.
 */
function renderSvgBlock(pre: HTMLElement, code: HTMLElement, src: string, id: number): boolean {
  const clean = sanitizeSvg(src, `qm-svg-${id}`);
  if (!clean) return false;

  const fig = document.createElement("figure");
  fig.className = "svg-block";

  const tools = document.createElement("div");
  tools.className = "svg-tools";
  const modes = document.createElement("div");
  modes.className = "svg-modes";
  const preview = document.createElement("button");
  preview.type = "button";
  preview.className = "svg-mode-btn active";
  preview.textContent = "Preview";
  const source = document.createElement("button");
  source.type = "button";
  source.className = "svg-mode-btn";
  source.textContent = "Source";
  modes.append(preview, source);

  // The copy button lives on the TOOLBAR, not floating inside the <pre> the way
  // every other code block's does, because the <pre> is hidden in preview mode
  // and copy has to stay reachable there. `data-copy-btn` is what tells
  // ChatMessage's codeBlockCopy action this block already has one; any button it
  // managed to attach before this ran is removed, or there would be two.
  pre.querySelector(".code-copy-btn")?.remove();
  pre.setAttribute("data-copy-btn", "true");
  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "svg-copy-btn";
  copy.title = "Copy code";
  copy.innerHTML = COPY_ICON;
  copy.addEventListener("click", async () => {
    try {
      await copyText(code.textContent ?? "");
      copy.innerHTML = CHECK_ICON;
      copy.classList.add("copied");
      setTimeout(() => {
        copy.innerHTML = COPY_ICON;
        copy.classList.remove("copied");
      }, 2000);
    } catch (e) {
      console.error("copy failed", e);
    }
  });
  tools.append(modes, copy);

  const out = document.createElement("div");
  out.className = "svg-out";
  out.innerHTML = clean;
  const svg = out.querySelector("svg");
  if (svg) {
    // Cap it to the bubble without distorting it: an SVG that declares a
    // viewBox and no size would otherwise lay out at the full width of the
    // message with an arbitrary height, and one that declares a huge size would
    // push the answer off the screen.
    svg.style.maxWidth = "100%";
    svg.style.height = "auto";
  }

  const show = (showSource: boolean) => {
    out.style.display = showSource ? "none" : "";
    pre.style.display = showSource ? "" : "none";
    source.classList.toggle("active", showSource);
    preview.classList.toggle("active", !showSource);
  };
  preview.addEventListener("click", () => show(false));
  source.addEventListener("click", () => show(true));

  pre.replaceWith(fig);
  fig.append(tools, out, pre);
  show(false); // preview is the default mode
  return true;
}

/**
 * Svelte action: render ```mermaid / ```chart blocks inside `node`.
 *
 * Also swaps any code block that holds a whole SVG document for a preview.
 *
 * Runs DURING the stream, per block. The gate is not "the message is finished"
 * but "this block is finished": `markOpenFence` (lib/markdown.ts) tags the one
 * code block the model is still writing, and only that one is skipped — parsing
 * a half-written diagram fails and burns the block, so it is left alone until
 * its fence closes and the streaming renderer moves it into a settled block.
 * A picture the model drew in its first paragraph therefore appears there,
 * rather than after the last token of a long answer.
 */
export function diagramBlocks(node: HTMLElement) {
  let scanning = false;
  let dirty = false;

  // A token landing while we await mermaid must not be dropped: re-run instead.
  // Looped, not recursed — a fast stream can dirty every pass.
  async function scan() {
    if (scanning) {
      dirty = true;
      return;
    }
    scanning = true;
    try {
      do {
        dirty = false;
        await scanOnce();
      } while (dirty);
    } finally {
      scanning = false;
    }
  }

  async function scanOnce() {
    const blocks = node.querySelectorAll<HTMLElement>(
      "pre:not([data-open-fence]) > code.language-mermaid:not([data-diagram]), pre:not([data-open-fence]) > code.language-chart:not([data-diagram])"
    );
    const dark = get(isDarkMode);

    // SVG blocks are matched on their CONTENT, not on a language class: a
    // model asked for a picture writes ```svg, ```xml or ```html for the same
    // markup, and some write no language at all. A block that opens with
    // `<svg` and closes with `</svg>` is an SVG document whatever it was
    // labelled -- and nothing else is.
    for (const code of node.querySelectorAll<HTMLElement>("pre:not([data-open-fence]) > code:not([data-svg])")) {
      const src = (code.textContent ?? "").trim();
      if (!/^<svg[\s>]/i.test(src) || !/<\/svg>$/i.test(src)) continue;
      const pre = code.parentElement;
      if (!pre) continue;
      code.setAttribute("data-svg", "done");
      if (!renderSvgBlock(pre, code, src, svgSeq++)) code.setAttribute("data-svg", "error");
    }
    for (const code of blocks) {
      const pre = code.closest("pre");
      const src = (code.textContent ?? "").trim();
      if (!pre || !src) continue;
      code.setAttribute("data-diagram", "done");

      const fig = document.createElement("figure");
      fig.className = "diagram-block";
      const out = document.createElement("div");
      out.className = "diagram-out";
      fig.appendChild(out);
      try {
        if (code.classList.contains("language-mermaid")) await renderMermaid(out, src, dark);
        else await renderChart(out, src, dark);
      } catch (e) {
        // Keep the source visible instead of swallowing it — a diagram the
        // model got slightly wrong is still readable as text.
        code.setAttribute("data-diagram", "error");
        const msg = document.createElement("div");
        msg.className = "diagram-error";
        msg.textContent = `Couldn't draw this ${code.classList.contains("language-mermaid") ? "diagram" : "chart"}: ${
          e instanceof Error ? e.message.split("\n")[0] : String(e)
        }`;
        pre.before(msg);
        continue;
      }
      // Rendered: swap the code block for the picture, with the source one
      // click away (the code-copy button still works on the hidden <pre>).
      const toggle = document.createElement("button");
      toggle.type = "button";
      toggle.className = "diagram-src-btn";
      toggle.textContent = "Source";
      toggle.addEventListener("click", () => {
        const shown = pre.style.display !== "none";
        pre.style.display = shown ? "none" : "";
        toggle.classList.toggle("open", !shown);
      });
      fig.appendChild(toggle);
      pre.style.display = "none";
      pre.before(fig);
    }
  }

  void scan();
  const mo = new MutationObserver(() => void scan());
  mo.observe(node, { childList: true, subtree: true });

  return {
    destroy: () => mo.disconnect(),
  };
}
