// Svelte action for `.scroll-fade-y` containers: keeps the edge-fade honest by
// zeroing the top fade when scrolled to the top and the bottom fade when
// scrolled to the bottom. Without this the static mask leaves the first/last
// row permanently dimmed even when fully revealed.
//
// Usage: <div class="scroll-fade-y" use:scrollFade> … </div>
export function scrollFade(node: HTMLElement) {
  const FADE = "1.25rem";
  const EPS = 1; // px slack for sub-pixel scroll heights

  const update = () => {
    const { scrollTop, scrollHeight, clientHeight } = node;
    const scrollable = scrollHeight - clientHeight > EPS;
    const atTop = !scrollable || scrollTop <= EPS;
    const atBottom = !scrollable || scrollTop + clientHeight >= scrollHeight - EPS;
    node.style.setProperty("--fade-top", atTop ? "0px" : FADE);
    node.style.setProperty("--fade-bottom", atBottom ? "0px" : FADE);
  };

  update();
  node.addEventListener("scroll", update, { passive: true });

  // Recompute when the viewport size or the content (cards added/removed)
  // changes — either can flip whether an edge is reached.
  const ro = new ResizeObserver(update);
  ro.observe(node);
  // childList: cards added/removed. attributeFilter [open]: expanding a nested
  // <details> (e.g. the Sources pills) grows scrollHeight without adding nodes
  // or resizing the container, so nothing else notices — the fade would stay
  // stale and the mask layer wouldn't re-raster (leaving ghost/trail pills).
  const mo = new MutationObserver(update);
  mo.observe(node, { childList: true, subtree: true, attributes: true, attributeFilter: ["open"] });

  return {
    destroy() {
      node.removeEventListener("scroll", update);
      ro.disconnect();
      mo.disconnect();
    },
  };
}
