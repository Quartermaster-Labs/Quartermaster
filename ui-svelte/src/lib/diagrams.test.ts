// @vitest-environment jsdom
import { describe, it, expect, beforeAll } from "vitest";
import { diagramBlocks } from "./diagrams";

// jsdom has no SVG layout, so mermaid's draw stage always throws here
// (`getBBox is not a function`). That makes it a good stand-in for the real
// failure this guards: whatever happens, the draw must not leave anything
// behind on <body>. The app shell is one viewport tall, so a stray child there
// grows a second, document-level scrollbar.
beforeAll(() => {
  window.matchMedia ??= ((q: string) => ({
    matches: false,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
});

function mount(src: string) {
  const node = document.createElement("div");
  const pre = document.createElement("pre");
  const code = document.createElement("code");
  code.className = "language-mermaid";
  code.textContent = src;
  pre.appendChild(code);
  node.appendChild(pre);
  document.body.appendChild(node);
  return { node, code };
}

describe("diagramBlocks", () => {
  it("leaves nothing on <body> when a draw fails", async () => {
    const { node, code } = mount(
      "graph TD\n  A[Start] --> B{Choice}\n  B -->|yes| C[Go]",
    );
    const baseline = document.body.childElementCount;

    // Watch what the draw parks on <body>. mermaid appends its scratch DOM
    // before it attempts the draw, so this sees it even though the failure path
    // removes it again -- and a scratch div that is not fixed-positioned is
    // exactly what grows the second scrollbar.
    const parked: string[] = [];
    const watch = new MutationObserver((recs) => {
      for (const r of recs) {
        for (const n of r.addedNodes) {
          if (!(n instanceof HTMLElement) || n === node) continue;
          if (getComputedStyle(n).position !== "fixed")
            parked.push(n.outerHTML.slice(0, 80));
        }
      }
    });
    watch.observe(document.body, { childList: true });

    const action = diagramBlocks(node);
    // Long enough to outlast every retry (300ms + 600ms of backoff).
    await new Promise((r) => setTimeout(r, 2500));

    watch.disconnect();
    expect(parked).toEqual([]);
    expect(code.getAttribute("data-diagram")).toBe("error");
    expect(document.body.childElementCount).toBe(baseline);
    // Specifically: no mermaid scratch div and no mermaid error banner.
    expect(document.querySelector("[id^='dqm-diagram-']")).toBeNull();
    expect(document.body.textContent).not.toContain("Syntax error in text");
    // The failure is still reported, in our own note rather than mermaid's.
    expect(node.querySelector(".diagram-error")?.textContent).toMatch(
      /Couldn't draw this diagram/,
    );

    action.destroy();
    node.remove();
  }, 20000);
});
