// Sanitizing a model-authored ```svg block before it becomes live markup.
//
// The chat renders an SVG the model wrote as a picture (`lib/diagrams.ts`), and
// an SVG is not an image format: it is a document, with the same script,
// event-handler and external-fetch surface as HTML. The source is model output
// -- which in this app has read web search results and page text on the way,
// so "our own model wrote it" is NOT the same as "we wrote it". Everything that
// executes, navigates or phones home is stripped here.
//
// Same shape as `hubMarkdown.sanitizeHTML` and for the same reason: parsing
// happens in an inert document, so nothing runs while it is being cleaned --
// assigning the raw source to a live element's `innerHTML` first would already
// have fired an `<image onerror>`.

const SVG_NS = "http://www.w3.org/2000/svg";
const XLINK_NS = "http://www.w3.org/1999/xlink";

/** Dropped entirely, contents and all. */
const DROP_ELEMENTS = new Set([
  "script",
  "foreignobject", // the door back into HTML, and everything HTML can do
  "iframe",
  "object",
  "embed",
  "audio",
  "video",
  "handler", // SVG 1.2 event handler element
  "listener",
  "animation",
  "cursor",
  "font-face-uri",
  "image", // see below: only data: URIs survive, and those come back as <image>
]);

/** SMIL can retarget a link at runtime; an animation of `href` is that attack. */
const ANIMATION_ELEMENTS = new Set([
  "animate",
  "set",
  "animatetransform",
  "animatemotion",
]);

/** URL-bearing attributes. Everything else is compared against `on*` only. */
const URL_ATTRS = new Set([
  "href",
  "xlink:href",
  "src",
  "from",
  "to",
  "values",
  "by",
]);

const DATA_IMAGE = /^data:image\/(png|jpe?g|gif|webp)[;,]/i;

/** `url(...)`/`@import` pointing anywhere but at this document's own defs. */
const EXTERNAL_URL = /@import|url\s*\(\s*['"]?(?!#)/i;

function isSafeUrl(value: string): boolean {
  const v = value.trim();
  // A fragment is a reference into this same SVG (`url(#grad)`, `<use href="#a">`).
  if (v.startsWith("#")) return true;
  return DATA_IMAGE.test(v);
}

/**
 * sanitizeSvg returns markup safe to inject, or null if `src` is not an SVG at
 * all (or is too broken to parse). Exported for its spec.
 *
 * `idPrefix` namespaces every `id` in the document and rewrites the references
 * that point at them. Two answers in one thread routinely both define
 * `<linearGradient id="a">`, and in one DOM the second silently loses: `url(#a)`
 * resolves to the FIRST match in the document, so an SVG rendered later would
 * pick up an earlier message's gradient.
 */
export function sanitizeSvg(src: string, idPrefix = "qm"): string | null {
  const text = (src ?? "").trim();
  if (!text) return null;

  // XML first: it is the format, and it preserves the case-sensitive attribute
  // names SVG depends on (`viewBox`, `gradientUnits`) exactly as written. Models
  // do emit slightly malformed XML though (an unclosed tag, a stray `&`), and
  // that is not a reason to refuse to draw -- so fall back to the HTML parser,
  // which is error-tolerant and applies its own SVG attribute-case adjustments.
  let root = pickSvgRoot(
    new DOMParser().parseFromString(text, "image/svg+xml"),
    true,
  );
  if (!root)
    root = pickSvgRoot(
      new DOMParser().parseFromString(text, "text/html"),
      false,
    );
  if (!root) return null;

  for (const el of Array.from(root.querySelectorAll("*"))) {
    if (!el.isConnected) continue; // already removed with an ancestor
    const tag = el.localName.toLowerCase();

    // Anything from another namespace is HTML (or MathML) smuggled in through a
    // parser quirk, not part of the drawing.
    if (el.namespaceURI !== SVG_NS) {
      el.remove();
      continue;
    }
    if (DROP_ELEMENTS.has(tag)) {
      // An <image> is worth keeping when it carries its own bytes; anything else
      // is a fetch to somewhere the user never asked to talk to.
      if (tag === "image" && isSafeUrl(hrefOf(el))) {
        cleanAttrs(el);
        continue;
      }
      el.remove();
      continue;
    }
    if (ANIMATION_ELEMENTS.has(tag)) {
      const target = (el.getAttribute("attributeName") ?? "").toLowerCase();
      if (target === "href" || target === "xlink:href") {
        el.remove();
        continue;
      }
    }
    if (tag === "style") {
      if (EXTERNAL_URL.test(el.textContent ?? "")) el.remove();
      else cleanAttrs(el);
      continue;
    }
    cleanAttrs(el);
  }
  cleanAttrs(root);

  namespaceIds(root, idPrefix);
  return root.outerHTML;
}

function pickSvgRoot(doc: Document, xml: boolean): SVGElement | null {
  if (xml && doc.querySelector("parsererror")) return null;
  const el = doc.querySelector("svg");
  if (!el || el.namespaceURI !== SVG_NS) return null;
  return el as SVGElement;
}

function hrefOf(el: Element): string {
  return el.getAttribute("href") ?? el.getAttributeNS(XLINK_NS, "href") ?? "";
}

function cleanAttrs(el: Element): void {
  for (const attr of Array.from(el.attributes)) {
    const name = attr.name.toLowerCase();
    // Event handlers are the obvious one. `on*` is a blocklist rather than an
    // attribute allowlist on purpose here: SVG's presentation attributes are a
    // long, open list (every CSS property is one), and dropping the unknown ones
    // would mean silently redrawing the picture wrong.
    if (name.startsWith("on")) {
      el.removeAttributeNode(attr);
      continue;
    }
    if (URL_ATTRS.has(name) && !isSafeUrl(attr.value)) {
      el.removeAttributeNode(attr);
      continue;
    }
    // A style attribute cannot script, but `url(https://...)` in one is still a
    // beacon that reports the user opened this message.
    if (
      (name === "style" ||
        name === "filter" ||
        name === "fill" ||
        name === "stroke" ||
        name === "mask" ||
        name === "clip-path" ||
        name === "marker-start" ||
        name === "marker-mid" ||
        name === "marker-end") &&
      EXTERNAL_URL.test(attr.value)
    ) {
      el.removeAttributeNode(attr);
    }
  }
}

/** Rewrites every `id` and the references that resolve to it. See sanitizeSvg. */
function namespaceIds(root: SVGElement, prefix: string): void {
  const ids = new Set<string>();
  for (const el of [root, ...Array.from(root.querySelectorAll("[id]"))]) {
    const id = el.getAttribute("id");
    if (id) ids.add(id);
  }
  if (ids.size === 0) return;

  const rename = (id: string) => `${prefix}-${id}`;
  for (const el of [root, ...Array.from(root.querySelectorAll("*"))]) {
    for (const attr of Array.from(el.attributes)) {
      if (attr.name.toLowerCase() === "id") {
        if (ids.has(attr.value)) el.setAttribute(attr.name, rename(attr.value));
        continue;
      }
      const next = rewriteRefs(attr.value, ids, rename);
      if (next !== attr.value) el.setAttribute(attr.name, next);
    }
    if (el.localName.toLowerCase() === "style") {
      const next = rewriteRefs(el.textContent ?? "", ids, rename);
      if (next !== el.textContent) el.textContent = next;
    }
  }
}

function rewriteRefs(
  value: string,
  ids: Set<string>,
  rename: (id: string) => string,
): string {
  if (!value.includes("#")) return value;
  return value.replace(/#([A-Za-z_][\w.:-]*)/g, (whole, id: string) =>
    ids.has(id) ? `#${rename(id)}` : whole,
  );
}
