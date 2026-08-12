// Diagrams and charts in chat answers.
//
// Deliberately NOT a tool: the model has nothing to execute server-side, it just
// writes diagram source. So it emits a fenced block — ```mermaid for diagrams,
// ```chart for a Chart.js config — and the client turns that into a picture
// after `renderMarkdown` has put the HTML in the DOM. `lib/markdown.ts` leaves
// those two languages unhighlighted so the raw source survives to here.
//
// Both renderers are lazy: mermaid is a ~500 KB chunk that most chats never
// need, so it's only imported when a diagram actually shows up.
import { get } from "svelte/store";
import { isDarkMode } from "../stores/theme";

// Chart types we accept from a model-authored config. Chart.js will happily
// build anything its registry knows; the allowlist keeps a malformed/hostile
// block from becoming a surprise.
const CHART_TYPES = new Set(["bar", "line", "pie", "doughnut", "radar", "polarArea", "scatter", "bubble"]);

let mermaidReady: Promise<typeof import("mermaid").default> | null = null;
let mermaidDark: boolean | null = null;
let seq = 0;

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

/**
 * Svelte action: render ```mermaid / ```chart blocks inside `node`.
 *
 * `enabled` is the "message is finished" gate — scanning mid-stream would try to
 * parse a half-written diagram, fail, and burn the block. Pass `!isStreaming`.
 */
export function diagramBlocks(node: HTMLElement, enabled = true) {
  let on = enabled;
  let scanning = false;

  async function scan() {
    if (!on || scanning) return;
    scanning = true;
    try {
      const blocks = node.querySelectorAll<HTMLElement>(
        "code.language-mermaid:not([data-diagram]), code.language-chart:not([data-diagram])"
      );
      const dark = get(isDarkMode);
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
    } finally {
      scanning = false;
    }
  }

  void scan();
  const mo = new MutationObserver(() => void scan());
  mo.observe(node, { childList: true, subtree: true });

  return {
    update(next: boolean) {
      on = next;
      void scan();
    },
    destroy: () => mo.disconnect(),
  };
}
