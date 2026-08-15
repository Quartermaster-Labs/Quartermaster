// Model-card rendering for the hub browser.
//
// A model card is the one piece of content in this app that is written by a
// STRANGER. Everything else `renderMarkdown` handles comes from our own models
// or our own wiki; a README comes from whoever published the repo, and anyone
// can publish a repo. That single fact drives this whole module:
//
//   - `renderMarkdown` runs the unified pipeline with `allowDangerousHtml: true`
//     (`markdown.ts`), because model cards and chat replies both legitimately
//     carry raw HTML. Piping a stranger's HTML into `{@html}` unfiltered is a
//     script tag away from owning the dashboard, so the output is sanitized here
//     before it is ever inserted.
//   - Images are the point of rendering a card at all (architecture diagrams,
//     sample outputs, benchmark plots), so they are kept — but every one is
//     rewritten to load through the server's image proxy, never hotlinked.
//
// Sanitizing the OUTPUT rather than the source is deliberate: the markdown
// pipeline is what turns text into tags, so it is the only place where what
// will actually reach the DOM is known.

import { renderMarkdown } from "./markdown";

/** Tags dropped entirely, contents and all. */
const DROP_TAGS = new Set(["script", "style", "iframe", "object", "embed", "applet", "form", "input", "button", "select", "textarea", "link", "meta", "base", "svg", "math"]);

/**
 * Attributes worth keeping. An allowlist, not a blocklist: `on*` handlers are
 * the obvious hazard but far from the only one, and a new HTML attribute that
 * executes something must not become a hole in this app by default.
 */
const KEEP_ATTRS = new Set(["href", "src", "alt", "title", "colspan", "rowspan", "align", "class", "width", "height"]);

/** Only these classes survive — the highlighter's, plus our own citation chips. */
const KEEP_CLASS = /^(hljs|language-|cite|katex)/;

/**
 * proxiedImage routes a remote image through the server rather than hotlinking.
 *
 * The proxy already exists for shopping-report pictures (`/api/imgproxy`,
 * `internal/server/imgproxy.go`) and brings its own SSRF guard on the resolved
 * IP of every dial — which matters more here, not less, since these URLs come
 * out of a stranger's README. It also fixes the mundane failures: http images on
 * an https page are blocked as mixed content, and some hosts refuse a foreign
 * Referer.
 */
export function proxiedImage(url: string): string {
  return `/api/imgproxy?url=${encodeURIComponent(url)}`;
}

/**
 * stripFrontmatter removes the YAML block every HF model card opens with.
 * It is metadata (license, base_model, tags), not prose — rendered, it shows up
 * as a wall of `key: value` lines above the actual card.
 */
export function stripFrontmatter(md: string): string {
  const m = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/.exec(md);
  return m ? md.slice(m[0].length) : md;
}

/**
 * absolutize resolves a repo-relative URL against the repo's own files.
 *
 * Model cards link their images relatively (`![arch](assets/arch.png)`), which
 * resolves against OUR origin and 404s. `/resolve/` rather than `/blob/` is what
 * returns the file itself instead of the HTML page around it.
 */
export function absolutize(url: string, repoID: string): string {
  const u = url.trim();
  if (!u || u.startsWith("#") || /^[a-z][a-z0-9+.-]*:/i.test(u) || u.startsWith("//")) return u;
  const path = u.replace(/^\.?\//, "");
  return `https://huggingface.co/${repoID}/resolve/main/${path}`;
}

/**
 * renderModelCard turns a repo README into HTML that is safe to `{@html}`.
 *
 * Returns "" when there is nothing to show, so the caller can decide whether to
 * render a section at all.
 */
export function renderModelCard(readme: string, repoID: string): string {
  const src = stripFrontmatter(readme ?? "").trim();
  if (!src) return "";
  return sanitizeHTML(renderMarkdown(src), repoID);
}

/**
 * sanitizeHTML walks rendered HTML and strips anything that could execute,
 * navigate somewhere unexpected, or phone home. Exported for its spec.
 *
 * Parsing happens in an inert document (`DOMParser`), so nothing in the input
 * runs while it is being cleaned — assigning to a live element's `innerHTML`
 * first would already have fired `<img onerror>`.
 */
export function sanitizeHTML(html: string, repoID: string): string {
  const doc = new DOMParser().parseFromString(html, "text/html");

  for (const el of Array.from(doc.body.querySelectorAll("*"))) {
    const tag = el.tagName.toLowerCase();
    if (DROP_TAGS.has(tag)) {
      el.remove();
      continue;
    }

    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      if (!KEEP_ATTRS.has(name)) {
        el.removeAttribute(attr.name);
        continue;
      }
      // width/height are kept ONLY on an image, and only as a plain number.
      // Cards size their own badges and diagrams with them (`<img width="200">`);
      // dropping them is why a 20px shield used to render at its 1200px natural
      // size. A unit-bearing or percentage value is discarded rather than
      // parsed — CSS caps the result either way.
      if (name === "width" || name === "height") {
        if (tag !== "img" || !/^\d{1,4}$/.test(attr.value.trim())) el.removeAttribute(attr.name);
        continue;
      }
      if (name === "class") {
        const kept = attr.value.split(/\s+/).filter((c) => KEEP_CLASS.test(c));
        if (kept.length) el.setAttribute("class", kept.join(" "));
        else el.removeAttribute("class");
      }
    }

    if (tag === "a") {
      const href = absolutize(el.getAttribute("href") ?? "", repoID);
      // javascript:, data: and vbscript: hrefs are the classic click-to-execute.
      if (!/^(https?:|#)/i.test(href)) el.removeAttribute("href");
      else el.setAttribute("href", href);
      el.setAttribute("target", "_blank");
      el.setAttribute("rel", "noopener noreferrer nofollow");
    }

    if (tag === "img") {
      const src = absolutize(el.getAttribute("src") ?? "", repoID);
      // A data: image is not a security problem but a broken/inline blob is not
      // worth the payload either; anything non-http simply goes.
      if (!/^https?:/i.test(src)) el.remove();
      else el.setAttribute("src", proxiedImage(src));
      el.setAttribute("loading", "lazy");
    }
  }

  return doc.body.innerHTML;
}
