// Themed tooltips, as a Svelte action.
//
// The app had ~230 native `title=` attributes. The OS tooltip is unstyleable,
// takes about a second to appear, renders in the system font on a system-yellow
// (Windows) or grey (macOS) chip, and can't wrap the multi-line help strings the
// config panels pass it — so most of the app's explanatory text was delivered in
// a widget that looks nothing like the app.
//
// This is an action rather than a wrapper component so a call site converts in
// place: `title={txt}` -> `use:tip={txt}`, no extra element, no layout change.
//
// ONE shared node in <body>, positioned `fixed` from the target's viewport rect:
// an in-tree absolute tooltip gets clipped by every `overflow-auto` ancestor,
// and the rail, the model table and the config modal are all scroll containers.

let tipEl: HTMLDivElement | null = null;
let showTimer: ReturnType<typeof setTimeout> | undefined;
let activeTarget: HTMLElement | null = null;

const DELAY_MS = 300;
const GAP = 8; // px between the target and the tip
const EDGE = 8; // min distance from the viewport edge

function ensureEl(): HTMLDivElement {
  if (tipEl) return tipEl;
  const el = document.createElement("div");
  el.id = "qm-tooltip";
  el.setAttribute("role", "tooltip");
  el.style.cssText = [
    "position:fixed",
    "z-index:2147483647",
    "max-width:22rem",
    "padding:0.375rem 0.5rem",
    "border-radius:0.375rem",
    "font-size:0.75rem",
    "line-height:1.125rem",
    "white-space:pre-line", // callers pass "\n"-separated help text
    "pointer-events:none",
    "opacity:0",
    "transition:opacity 120ms ease",
    "background:var(--color-surface-2)",
    "color:var(--color-txtmain)",
    "border:1px solid var(--color-card-border)",
    "box-shadow:0 4px 16px rgba(0,0,0,0.25)",
  ].join(";");
  document.body.appendChild(el);
  tipEl = el;
  return el;
}

function place(target: HTMLElement, el: HTMLDivElement): void {
  const r = target.getBoundingClientRect();
  const w = el.offsetWidth;
  const h = el.offsetHeight;
  // Above by default; flip below when the target sits near the top of the
  // viewport (the status rail and sticky table headers both do).
  const above = r.top - GAP - h >= EDGE;
  const top = above ? r.top - GAP - h : r.bottom + GAP;
  let left = r.left + r.width / 2 - w / 2;
  left = Math.max(EDGE, Math.min(left, window.innerWidth - w - EDGE));
  el.style.top = `${Math.round(top)}px`;
  el.style.left = `${Math.round(left)}px`;
}

function hide(): void {
  clearTimeout(showTimer);
  if (activeTarget) activeTarget.removeAttribute("aria-describedby");
  activeTarget = null;
  if (tipEl) {
    tipEl.style.opacity = "0";
    tipEl.style.left = "-9999px"; // park it so a stale rect can't widen the page
  }
}

function show(target: HTMLElement, content: string): void {
  const el = ensureEl();
  // A showModal() <dialog> paints in the browser's top layer, which NO z-index
  // in the normal layer can beat — so for a target inside one, the tip has to
  // live in that dialog. Coordinates are viewport-relative either way.
  const host = target.closest("dialog") ?? document.body;
  if (el.parentNode !== host) host.appendChild(el);
  el.textContent = content;
  el.style.left = "0px";
  el.style.top = "0px";
  activeTarget = target;
  target.setAttribute("aria-describedby", el.id);
  place(target, el);
  el.style.opacity = "1";
}

export function tip(node: HTMLElement, content: string | undefined | null) {
  let text = content ?? "";

  // The native tooltip would otherwise show up alongside ours. Keep the string
  // reachable to assistive tech via aria-label when the node has no other name.
  function syncNative(): void {
    node.removeAttribute("title");
    if (text && !node.getAttribute("aria-label") && !node.textContent?.trim()) {
      node.setAttribute("aria-label", text);
    }
  }

  function onEnter(): void {
    if (!text) return;
    clearTimeout(showTimer);
    showTimer = setTimeout(() => show(node, text), DELAY_MS);
  }
  function onLeave(): void {
    if (activeTarget === node || !activeTarget) hide();
    else clearTimeout(showTimer);
  }
  function onKey(e: KeyboardEvent): void {
    if (e.key === "Escape") onLeave();
  }

  syncNative();
  node.addEventListener("mouseenter", onEnter);
  node.addEventListener("mouseleave", onLeave);
  node.addEventListener("focus", onEnter);
  node.addEventListener("blur", onLeave);
  node.addEventListener("keydown", onKey);
  // A scroll moves the target out from under a `fixed` tip, so drop it rather
  // than chase it. Capture phase: catches scrolls in any ancestor container.
  window.addEventListener("scroll", onLeave, true);

  return {
    update(next: string | undefined | null) {
      text = next ?? "";
      syncNative();
      if (activeTarget === node && tipEl) {
        tipEl.textContent = text;
        place(node, tipEl);
      }
    },
    destroy() {
      onLeave();
      node.removeEventListener("mouseenter", onEnter);
      node.removeEventListener("mouseleave", onLeave);
      node.removeEventListener("focus", onEnter);
      node.removeEventListener("blur", onLeave);
      node.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", onLeave, true);
    },
  };
}
