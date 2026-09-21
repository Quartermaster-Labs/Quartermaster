// Svelte action that re-parents a node to <body>.
//
// `position: fixed` is only viewport-relative while no ancestor establishes a
// containing block for it. Several containers here do establish one without
// looking like it: the chat scroller carries `mask-image` (.scroll-fade-b), and
// mask/filter/transform/contain all make the element the containing block for
// fixed descendants. An overlay declared inside such a container therefore
// sizes to THAT box and is clipped by its mask — a "full-screen" lightbox that
// only covers the message list.
//
// Moving the node to <body> escapes every one of those. It stays inside the
// zoomed :root, so `inset-0` still resolves in the same local pixels as the
// rest of the UI (see lib/uiZoom.ts) — no vh/vw correction needed.
//
// Usage: <div class="fixed inset-0 …" use:portal> … </div>
export function portal(node: HTMLElement) {
  document.body.appendChild(node);
  return {
    destroy() {
      // Svelte removes the node itself when the {#if} closes; only clean up if
      // it is somehow still attached (e.g. the action is torn down first).
      node.remove();
    },
  };
}
