// Builds the public site (GitHub Pages) into .site/ at the repo root.
//
//   npm run site                       # build into .site/
//   npm run site -- --serve            # build, then serve it on :4321
//   npm run site -- --adopt .shots/current   # pull fresh screenshots in first
//
// Two halves, one corpus:
//
//   landing page  — site/content.mjs, hand-written marketing copy
//   /docs/*       — internal/server/wiki_articles.json, the SAME articles the
//                   app's Help modal renders and the `wiki_search` tool reads
//
// So the guide on the website cannot drift from the guide in the app: there is
// one file, and three renderers (Go tool, Svelte modal, this). docs/ in the repo
// is a fourth (ui-svelte/scripts/wiki-docs.mjs) — markdown for people reading on
// GitHub, HTML for people who haven't cloned anything.
//
// Output is NOT committed. It is a build product with no reader inside the repo,
// and .github/workflows/pages.yml regenerates it on every push — committing it
// would only create something that can go stale.
import { cp, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import remarkRehype from "remark-rehype";
import rehypeStringify from "rehype-stringify";
import { visit } from "unist-util-visit";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const UI = path.resolve(HERE, "..");
const ROOT = path.resolve(UI, "..");

const ARTICLES = path.join(ROOT, "internal", "server", "wiki_articles.json");
const CATEGORIES = path.join(UI, "src", "lib", "wiki-categories.json");
const IMAGES = path.join(ROOT, "docs", "assets");
const FONTS = path.join(UI, "node_modules", "@fontsource");

const { REPO, UPSTREAM, HERO, PILLS, SECTIONS, STORY, ICONS, FEATURES, GALLERY, INSTALL } =
  await import("./content.mjs");

// ── args ───────────────────────────────────────────────────────────────────

const argv = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = argv.indexOf(name);
  return i === -1 ? fallback : argv[i + 1];
};
const OUT = path.resolve(flag("--out", path.join(ROOT, ".site")));
const SITE_URL = (flag("--url", "https://quartermaster-labs.github.io/quartermaster")).replace(/\/+$/, "");
const ADOPT = flag("--adopt", null);

// ── html helpers ───────────────────────────────────────────────────────────

const esc = (s) =>
  String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

const icon = (name, size = 20) =>
  `<svg viewBox="0 0 24 24" width="${size}" height="${size}" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] ?? ""}</svg>`;

// Applied before first paint so a light-mode visitor never sees a dark flash.
// Kept as one inline statement rather than a file: an external script is a
// second round-trip, and this must run before the body renders.
const THEME_BOOT =
  `<script>try{var t=localStorage.getItem("qm-site-theme");if(t)document.documentElement.dataset.theme=t}catch(e){}</script>`;

const THEME_TOGGLE = `<button class="icon-btn" id="theme" type="button" aria-label="Toggle colour theme">
      <svg class="sun" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M6.3 17.7l-1.4 1.4M19.1 4.9l-1.4 1.4"/></svg>
      <svg class="moon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/></svg>
    </button>`;

const THEME_SCRIPT = `<script>
document.getElementById("theme").addEventListener("click", function () {
  var root = document.documentElement;
  var dark = root.dataset.theme
    ? root.dataset.theme === "dark"
    : matchMedia("(prefers-color-scheme: dark)").matches;
  root.dataset.theme = dark ? "light" : "dark";
  try { localStorage.setItem("qm-site-theme", root.dataset.theme); } catch (e) {}
});
</script>`;

const MARK = `<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS.box}</svg>`;

// `depth` is how far the page sits below the site root, so every href stays
// relative — that way the site works at github.io/quartermaster/, at a custom
// domain, and from a file:// preview without a base-URL setting to get wrong.
function page({ title, description, body, depth = 0, extraScripts = "", ogImage = null }) {
  const up = depth === 0 ? "" : "../".repeat(depth);
  return `<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(title)}</title>
<meta name="description" content="${esc(description)}">
<meta property="og:title" content="${esc(title)}">
<meta property="og:description" content="${esc(description)}">
<meta property="og:type" content="website">
<meta property="og:url" content="${SITE_URL}/">
${ogImage ? `<meta property="og:image" content="${SITE_URL}/assets/img/${ogImage}">\n<meta name="twitter:card" content="summary_large_image">` : ""}
<link rel="icon" href="${up}assets/favicon.svg">
<link rel="stylesheet" href="${up}assets/styles.css">
${THEME_BOOT}
</head>
<body>
<header class="site-header">
  <div class="wrap">
    <a class="brand" href="${up}index.html">${MARK} quartermaster</a>
    <nav class="site-nav">
      <a href="${up}index.html#features" class="hide-sm">Features</a>
      <a href="${up}index.html#install" class="hide-sm">Install</a>
      <a href="${up}docs/index.html">Docs</a>
      <a href="${REPO}">GitHub</a>
      ${THEME_TOGGLE}
    </nav>
  </div>
</header>
${body}
<footer class="site-footer">
  <div class="wrap">
    <span>MIT licensed. A fork of <a href="${UPSTREAM}">llama-swap</a>, diverged into its own project.</span>
    <span class="spacer"><a href="${REPO}">Source</a></span>
    <span><a href="${REPO}/releases">Releases</a></span>
    <span><a href="${up}docs/index.html">User guide</a></span>
  </div>
</footer>
${THEME_SCRIPT}
${extraScripts}
</body>
</html>
`;
}

// ── markdown ───────────────────────────────────────────────────────────────

const slug = (s) =>
  s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");

// Headings get an id and a hover anchor so a docs section is linkable, and any
// link to a sibling article's .md file is retargeted at the HTML we emit for it
// (the article bodies are written for the in-repo docs/ tree, which is markdown).
function rehypeDocsLinks() {
  return (tree) => {
    visit(tree, "element", (node) => {
      if (node.tagName === "h2" || node.tagName === "h3") {
        const text = [];
        visit(node, "text", (t) => text.push(t.value));
        const id = slug(text.join(" "));
        if (!id) return;
        node.properties.id = id;
        node.children.push({
          type: "element",
          tagName: "a",
          properties: { className: ["anchor"], href: `#${id}`, ariaHidden: "true", tabIndex: -1 },
          children: [{ type: "text", value: "#" }],
        });
      }
      if (node.tagName === "a" && typeof node.properties?.href === "string") {
        node.properties.href = node.properties.href.replace(/^(?!https?:)([^#?]*)\.md(?=$|[#?])/, "$1.html");
      }
    });
  };
}

const md = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkRehype)
  .use(rehypeDocsLinks)
  .use(rehypeStringify);

const toHtml = async (source) => String(await md.process(source));

// First sentence of the body, markdown stripped — the <meta description> and the
// blurb under each card in the docs index.
function summarize(body, max = 165) {
  const text = body
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/^#{1,6}\s+.*$/gm, " ")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/[*_`>|-]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (text.length <= max) return text;
  const cut = text.slice(0, max);
  return cut.slice(0, cut.lastIndexOf(" ")) + "…";
}

// ── release lookup ─────────────────────────────────────────────────────────

const fmtSize = (n) => `${(n / 1024 / 1024).toFixed(0)} MB`;

// The installer asset name carries the version, so there is no stable
// /releases/latest/download/<name> URL to hard-code — we resolve the real one at
// build time. The Pages workflow also runs on `release: published`, so the
// button is rebuilt when a release lands. No gh, no releases, or a network
// failure all degrade to the releases page rather than failing the build.
function latestRelease() {
  try {
    const list = JSON.parse(
      execFileSync("gh", ["release", "list", "--limit", "20", "--json", "tagName,isDraft,isPrerelease"], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "ignore"],
      }),
    );
    const rel = list.find((r) => !r.isDraft && /^v\d+\.\d+/.test(r.tagName));
    if (!rel) return null;
    const view = JSON.parse(
      execFileSync("gh", ["release", "view", rel.tagName, "--json", "tagName,publishedAt,assets"], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "ignore"],
      }),
    );
    const exe = view.assets.find((a) => a.name.endsWith(".exe"));
    if (!exe) return null;
    return { tag: view.tagName, url: exe.url, size: exe.size, prerelease: rel.isPrerelease };
  } catch {
    return null;
  }
}

// ── landing page ───────────────────────────────────────────────────────────

// One centred masthead per section: accent eyebrow with its icon, the heading,
// and an optional sub-lede. Sections are keyed by DOM id so the copy lives in
// content.mjs and the markup is identical everywhere.
function sectionHead(key) {
  const s = SECTIONS[key];
  if (!s) return "";
  return `<div class="section-head" data-reveal>
      <span class="eyebrow">${icon(s.icon, 15)} ${esc(s.eyebrow)}</span>
      <h2>${esc(s.title)}</h2>
      ${s.sub ? `<p>${esc(s.sub)}</p>` : ""}
    </div>`;
}

function renderHero(release, hero) {
  const primary = release
    ? `<a class="btn btn-primary" href="${release.url}">${icon("windows", 18)} Download for Windows</a>`
    : `<a class="btn btn-primary" href="${REPO}/releases/latest">${icon("windows", 18)} Download</a>`;
  const note = release
    ? `${esc(release.tag)}${release.prerelease ? " (pre-release)" : ""} · ${fmtSize(release.size)} · <a href="${REPO}/releases">all releases</a> · Docker and source below`
    : `See <a href="${REPO}/releases">releases</a> for builds, or install with Docker or from source below.`;

  const pills = PILLS.map((p) => `<li>${esc(p)}</li>`).join("");

  // The mesh is three blurred gradient blobs drifting on long, offset CSS
  // keyframes — no canvas, no rAF loop, nothing repainting on the main thread,
  // and `prefers-reduced-motion` parks them where they start.
  return `<section class="hero">
  <div class="mesh" aria-hidden="true"><i></i><i></i><i></i></div>
  <div class="wrap">
    <span class="eyebrow"><b>◆</b> ${esc(HERO.eyebrow)}</span>
    <h1>${esc(hero[0])} <span class="accent">${esc(hero[1])}</span> ${esc(hero[2])}</h1>
    <p class="lede">${esc(HERO.lede)}</p>
    <div class="cta-row">
      ${primary}
      <a class="btn btn-ghost" href="#install">Other install options</a>
      <a class="btn btn-ghost" href="docs/index.html">${icon("book", 18)} Read the guide</a>
    </div>
    <ul class="pills">${pills}</ul>
    <p class="cta-note">${note}</p>
  </div>
</section>`;
}

// `light` is the sibling capture (dashboard-light.png next to dashboard.png)
// when docs/assets has one; the CSS picks by theme. The dark one keeps the alt
// text and the light one is decorative, so a screen reader hears it once.
function shotFrame(file, alt, label, light = null) {
  const img = (src, cls, a) =>
    `<img src="assets/img/${esc(src)}"${cls ? ` class="${cls}"` : ""} alt="${esc(a)}" loading="lazy" decoding="async">`;
  const body = light
    ? img(file, "only-dark", alt) + img(light, "only-light", "")
    : img(file, "", alt);
  return `<div class="shot">
    <div class="shot-bar"><i></i><i></i><i></i><span>${esc(label)}</span></div>
    ${body}
  </div>`;
}

// Collapsed vertical slats that expand on hover. The first is open in the
// markup, so with JS off (or before it runs) the section is still a screenshot
// with a caption rather than a row of empty bars — the expansion is an
// enhancement, not the thing that makes it legible.
function renderGallery(shots) {
  if (!shots.length) return "";
  const img = (src, cls, alt) =>
    `<img src="assets/img/${esc(src)}"${cls ? ` class="${cls}"` : ""} alt="${esc(alt)}" loading="lazy" decoding="async">`;

  const slats = shots
    .map((s, i) => {
      const alt = `${s.label} — ${s.caption}`;
      const pic = s.light ? img(s.file, "only-dark", alt) + img(s.light, "only-light", "") : img(s.file, "", alt);
      return `<button class="slat${i === 0 ? " is-open" : ""}" type="button" aria-expanded="${i === 0}">
      <span class="slat-spine">${icon(s.icon ?? "monitor", 18)}<b>${esc(s.label)}</b></span>
      <span class="slat-panel">
        <span class="slat-shot">${pic}</span>
        <span class="slat-caption"><b>${esc(s.label)}</b>${esc(s.caption)}</span>
      </span>
    </button>`;
    })
    .join("\n");

  return `<section id="screens">
  <div class="wrap">
    ${sectionHead("screens")}
    <div class="slats" data-reveal>${slats}</div>
  </div>
</section>`;
}

// Hover opens on a pointer; click and keyboard focus open everywhere else, so
// the strip works on a phone and from the Tab key, not just under a mouse.
const GALLERY_SCRIPT = `<script>
(function () {
  var slats = [].slice.call(document.querySelectorAll(".slats .slat"));
  if (!slats.length) return;
  function open(el) {
    slats.forEach(function (s) {
      var on = s === el;
      s.classList.toggle("is-open", on);
      s.setAttribute("aria-expanded", String(on));
    });
  }
  slats.forEach(function (s) {
    s.addEventListener("mouseenter", function () { if (matchMedia("(hover: hover)").matches) open(s); });
    s.addEventListener("focus", function () { open(s); });
    s.addEventListener("click", function () { open(s); });
  });
})();
</script>`;

function renderFeatures() {
  const cards = FEATURES.map(
    (f, i) => `<article class="card" data-reveal style="--d:${(i % 3) * 60}ms">
      <div class="ico">${icon(f.icon)}</div>
      <h3>${esc(f.title)}${f.neu ? '<span class="new">new in fork</span>' : ""}</h3>
      <p>${esc(f.body)}</p>
    </article>`,
  ).join("\n");

  return `<section id="features">
  <div class="wrap">
    ${sectionHead("features")}
    <div class="grid">${cards}</div>
  </div>
</section>`;
}

// The one section written in the first person. It sits between the screenshots
// and the docs so the page has somewhere to stop being a spec sheet.
function renderStory() {
  return `<section id="story">
  <div class="wrap narrow">
    ${sectionHead("story")}
    <div class="story" data-reveal>${STORY.map((p) => `<p>${esc(p)}</p>`).join("\n      ")}</div>
    <p class="story-foot"><a class="btn btn-ghost" href="${UPSTREAM}">See llama-swap, where it started</a></p>
  </div>
</section>`;
}

// A shell block that looks like a shell: a prompt glyph on each command, dimmed
// trailing comments, and a Copy button. The prompt and the comment styling are
// markup, never text — the button copies `code` verbatim, so what lands on the
// clipboard is exactly what you'd type.
function terminal(code, label) {
  const lines = code.split("\n");
  const html = lines
    .map((line, i) => {
      const cont = i > 0 && /\\\s*$/.test(lines[i - 1]);
      const m = line.match(/^(.*?)(\s{2,}#.*)$/);
      const body = m ? `${esc(m[1])}<span class="cm">${esc(m[2])}</span>` : esc(line);
      return `<span class="ln${cont ? " cont" : ""}">${body}</span>`;
    })
    .join("");
  return `<div class="term">
        <div class="term-bar"><i></i><i></i><i></i><span>${esc(label)}</span>
          <button class="copy" type="button" data-copy="${esc(code)}">${icon("copy", 13)}<b>Copy</b></button>
        </div>
        <pre><code>${html}</code></pre>
      </div>`;
}

const COPY_SCRIPT = `<script>
(function () {
  [].forEach.call(document.querySelectorAll(".copy"), function (btn) {
    btn.addEventListener("click", function () {
      var text = btn.getAttribute("data-copy");
      var done = function () {
        btn.classList.add("ok");
        setTimeout(function () { btn.classList.remove("ok"); }, 1400);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, fallback);
      } else fallback();
      function fallback() {
        // http:// origins and older Safari have no async clipboard — the
        // deprecated path still works there and this is one short string.
        var ta = document.createElement("textarea");
        ta.value = text; ta.setAttribute("readonly", "");
        ta.style.cssText = "position:fixed;top:-1000px";
        document.body.appendChild(ta); ta.select();
        try { document.execCommand("copy"); done(); } catch (e) {}
        document.body.removeChild(ta);
      }
    });
  });
})();
</script>`;

function renderInstall(release) {
  const cards = INSTALL.map((c, i) => {
    let action = "";
    if (c.download) {
      action = release
        ? `<a class="btn btn-primary" href="${release.url}">Download ${esc(release.tag)} · ${fmtSize(release.size)}</a>`
        : `<a class="btn btn-primary" href="${REPO}/releases/latest">Go to releases</a>`;
    } else if (c.code) {
      action = terminal(c.code, c.title.toLowerCase());
    }
    return `<article class="card" data-reveal style="--d:${i * 60}ms">
      <div class="ico">${icon(c.icon)}</div>
      <h3>${esc(c.title)}</h3>
      <p>${esc(c.body)}</p>
      ${action}
    </article>`;
  }).join("\n");

  return `<section id="install">
  <div class="wrap">
    ${sectionHead("install")}
    <div class="grid">${cards}</div>
  </div>
</section>`;
}

// Scroll reveals, with three separate ways of never leaving content hidden:
//
//   1. the hiding class is added by JS, so no-JS keeps the page fully visible;
//   2. `prefers-reduced-motion` skips the whole thing;
//   3. a 2.5s failsafe unhides everything even if the observer never fires.
//
// (3) is not paranoia — the site this design borrows from ships unguarded
// reveals, and its own full-page screenshot comes back blank below the fold.
const REVEAL_SCRIPT = `<script>
(function () {
  var els = [].slice.call(document.querySelectorAll("[data-reveal]"));
  if (!els.length) return;
  if (matchMedia("(prefers-reduced-motion: reduce)").matches || !("IntersectionObserver" in window)) return;
  var show = function (el) { el.classList.add("in"); };
  els.forEach(function (el) { el.classList.add("reveal"); });
  var io = new IntersectionObserver(function (entries) {
    entries.forEach(function (e) {
      if (!e.isIntersecting) return;
      show(e.target);
      io.unobserve(e.target);
    });
  }, { rootMargin: "0px 0px -8% 0px", threshold: 0.02 });
  els.forEach(function (el) { io.observe(el); });
  setTimeout(function () { els.forEach(show); }, 2500);
})();
</script>`;

function renderDocsTeaser(articles, categories) {
  const byId = new Map(articles.map((a) => [a.id, a]));
  const cards = categories
    .map((c) => {
      const links = c.ids
        .map((id) => byId.get(id))
        .filter(Boolean)
        .map((a) => `<li><a href="docs/${a.id}.html">${esc(a.title)}</a></li>`)
        .join("");
      return `<article class="card" data-reveal>
      <h3>${esc(c.title)}</h3>
      <ul class="topic-list">${links}</ul>
    </article>`;
    })
    .join("\n");

  return `<section id="docs">
  <div class="wrap">
    ${sectionHead("docs")}
    <div class="grid">${cards}</div>
  </div>
</section>`;
}

// ── docs pages ─────────────────────────────────────────────────────────────

function docsNav(articles, categories, currentId) {
  const byId = new Map(articles.map((a) => [a.id, a]));
  const known = new Set(categories.flatMap((c) => c.ids));
  const groups = [
    ...categories.map((c) => ({ title: c.title, items: c.ids.map((id) => byId.get(id)).filter(Boolean) })),
    { title: "More", items: articles.filter((a) => !known.has(a.id)) },
  ].filter((g) => g.items.length);

  return groups
    .map(
      (g) => `<h4>${esc(g.title)}</h4>
    <ul>${g.items
      .map(
        (a) =>
          `<li><a href="${a.id}.html"${a.id === currentId ? ' aria-current="page"' : ""}>${esc(a.title)}</a></li>`,
      )
      .join("")}</ul>`,
    )
    .join("\n");
}

async function renderArticlePage(a, articles, categories) {
  const body = await toHtml(a.body);
  return page({
    title: `${a.title} — quartermaster`,
    description: summarize(a.body),
    depth: 1,
    body: `<main class="wrap docs-layout">
  <aside class="docs-nav">${docsNav(articles, categories, a.id)}</aside>
  <article class="prose">
    <h1>${esc(a.title)}</h1>
    ${body}
    <p class="docs-foot">This page is generated from the help wiki that ships inside the app — the same text you get from the <strong>Help</strong> button, and the same text the playground assistant searches. Corrections go to <a href="${REPO}/blob/main/internal/server/wiki_articles.json"><code>wiki_articles.json</code></a>.</p>
  </article>
</main>`,
  });
}

function renderDocsIndex(articles, categories) {
  const byId = new Map(articles.map((a) => [a.id, a]));
  const known = new Set(categories.flatMap((c) => c.ids));
  const groups = [
    ...categories.map((c) => ({ title: c.title, items: c.ids.map((id) => byId.get(id)).filter(Boolean) })),
    { title: "More", items: articles.filter((a) => !known.has(a.id)) },
  ].filter((g) => g.items.length);

  const sections = groups
    .map(
      (g) => `<h2 id="${slug(g.title)}">${esc(g.title)}</h2>
    <div class="grid">${g.items
      .map(
        (a) => `<a class="card" href="${a.id}.html">
        <h3>${esc(a.title)}</h3>
        <p>${esc(summarize(a.body, 110))}</p>
      </a>`,
      )
      .join("\n")}</div>`,
    )
    .join("\n");

  return page({
    title: "User guide — quartermaster",
    description:
      "How to load models, tune per-model config, use the playground, set up web search, scope API keys and read the VRAM gauges in quartermaster.",
    depth: 1,
    body: `<main class="wrap docs-layout">
  <aside class="docs-nav">${docsNav(articles, categories, null)}</aside>
  <article class="prose">
    <h1>User guide</h1>
    <p>The help wiki that ships with the app, published here so you can read it before installing anything. Inside quartermaster the same articles are one click away under <strong>Help</strong>, and the playground assistant searches them to answer questions about the app itself.</p>
    <p>Looking for how quartermaster works <em>inside</em>? That lives beside the code it describes — see the subsystem table in <a href="${REPO}/blob/main/CLAUDE.md">CLAUDE.md</a>.</p>
    ${sections}
  </article>
</main>`,
  });
}

// ── assets ─────────────────────────────────────────────────────────────────

const FAVICON = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#FF6A2B" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${ICONS.box}</svg>
`;

const FONT_FILES = [
  ["inter/files/inter-latin-400-normal.woff2", "inter-400.woff2"],
  ["inter/files/inter-latin-600-normal.woff2", "inter-600.woff2"],
  ["inter/files/inter-latin-700-normal.woff2", "inter-700.woff2"],
  ["jetbrains-mono/files/jetbrains-mono-latin-400-normal.woff2", "jetbrains-mono-400.woff2"],
  ["jetbrains-mono/files/jetbrains-mono-latin-500-normal.woff2", "jetbrains-mono-500.woff2"],
  ["jetbrains-mono/files/jetbrains-mono-latin-700-normal.woff2", "jetbrains-mono-700.woff2"],
];

// Screenshots the site wants but docs/assets/ doesn't have yet: named on stdout
// rather than shipped as broken images, with the command that produces them.
async function collectShots() {
  const have = new Set(await readdir(IMAGES).catch(() => []));
  const lightOf = (f) => f.replace(/\.png$/, "-light.png");
  const found = GALLERY.filter((s) => have.has(s.file)).map((s) => ({
    ...s,
    light: have.has(lightOf(s.file)) ? lightOf(s.file) : null,
  }));
  const missing = GALLERY.filter((s) => !have.has(s.file));
  if (missing.length) {
    console.warn(`  ! ${missing.length} screenshot(s) not in docs/assets, skipped: ${missing.map((s) => s.file).join(", ")}`);
    console.warn(`    capture with: npm run shots -- --demo   then: npm run site -- --adopt .shots/current`);
  }
  return found;
}

// Copy the dark demo capture of each gallery shot out of a .shots run and into
// docs/assets under the site's own name. .shots/ is gitignored (local visual
// diffing); the site's images have to be committed because the Pages build has
// no running instance to capture from.
const SHOT_SOURCE = {
  "dashboard.png": "dashboard",
  "models.png": "models",
  "model-config.png": "model-config-modal",
  "observe.png": "observe-activity",
  "browse.png": "browse",
  "images.png": "models-image",
};

async function adopt(dir) {
  const from = path.resolve(UI, dir);
  const files = await readdir(from).catch(() => {
    throw new Error(`no such shots directory: ${from}`);
  });
  let n = 0;
  for (const [target, shot] of Object.entries(SHOT_SOURCE)) {
    for (const theme of ["dark", "light"]) {
      // Prefer the demo capture (a model loaded, traffic behind it) over an empty one.
      const match =
        files.find((f) => f.startsWith(`${shot}--${theme}--`) && f.endsWith("--demo.png")) ||
        files.find((f) => f.startsWith(`${shot}--${theme}--`) && f.endsWith(".png"));
      if (!match) continue;
      const name = theme === "dark" ? target : target.replace(/\.png$/, "-light.png");
      await cp(path.join(from, match), path.join(IMAGES, name));
      console.log(`  ✓ ${match} → docs/assets/${name}`);
      n++;
    }
  }
  if (!n) throw new Error(`no matching shots in ${from} (expected e.g. dashboard--dark--1440--demo.png)`);
  console.log(`adopted ${n} screenshot(s); commit docs/assets/ to publish them`);
}

// ── main ───────────────────────────────────────────────────────────────────

async function main() {
  if (ADOPT) await adopt(ADOPT);

  const [articles, categories] = await Promise.all([
    readFile(ARTICLES, "utf8").then(JSON.parse),
    readFile(CATEGORIES, "utf8").then(JSON.parse),
  ]);

  await rm(OUT, { recursive: true, force: true });
  await mkdir(path.join(OUT, "assets", "fonts"), { recursive: true });
  await mkdir(path.join(OUT, "assets", "img"), { recursive: true });
  await mkdir(path.join(OUT, "docs"), { recursive: true });

  await cp(path.join(HERE, "styles.css"), path.join(OUT, "assets", "styles.css"));
  await writeFile(path.join(OUT, "assets", "favicon.svg"), FAVICON);
  for (const [src, name] of FONT_FILES) {
    await cp(path.join(FONTS, src), path.join(OUT, "assets", "fonts", name));
  }
  await cp(IMAGES, path.join(OUT, "assets", "img"), { recursive: true }).catch(() => {});

  // Jekyll is the Pages default and would swallow anything it does not like;
  // this is already HTML.
  await writeFile(path.join(OUT, ".nojekyll"), "");

  const release = latestRelease();
  console.log(release ? `release: ${release.tag} (${release.url})` : "release: none found — CTA falls back to /releases/latest");

  const shots = await collectShots();
  const hero = shots.find((s) => s.file === "dashboard.png") ?? shots[0];

  const landing = page({
    title: "quartermaster — run any local model, on demand",
    description: HERO.lede,
    ogImage: hero?.file,
    extraScripts: [COPY_SCRIPT, shots.length ? GALLERY_SCRIPT : "", REVEAL_SCRIPT].join("\n"),
    body: [
      renderHero(release, HERO.title),
      hero
        ? `<div class="wrap" data-reveal>${shotFrame(hero.file, "The quartermaster dashboard", "localhost:1250/ui/", hero.light)}</div>`
        : "",
      renderFeatures(),
      renderGallery(shots.filter((s) => s !== hero)),
      renderStory(),
      renderInstall(release),
      renderDocsTeaser(articles, categories),
    ].join("\n"),
  });
  await writeFile(path.join(OUT, "index.html"), landing);
  await writeFile(path.join(OUT, "docs", "index.html"), renderDocsIndex(articles, categories));
  for (const a of articles) {
    await writeFile(path.join(OUT, "docs", `${a.id}.html`), await renderArticlePage(a, articles, categories));
  }

  console.log(`${articles.length + 2} pages → ${path.relative(ROOT, OUT)}/`);

  if (argv.includes("--serve")) {
    const { createServer } = await import("node:http");
    const { createReadStream } = await import("node:fs");
    const types = { ".html": "text/html", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2" };
    createServer((req, res) => {
      let p = path.join(OUT, decodeURIComponent(req.url.split("?")[0]));
      if (p.endsWith(path.sep) || !path.extname(p)) p = path.join(p, "index.html");
      res.setHeader("content-type", types[path.extname(p)] ?? "application/octet-stream");
      createReadStream(p).on("error", () => res.writeHead(404).end("not found")).pipe(res);
    }).listen(4321, () => console.log("serving on http://localhost:4321"));
  }
}

main().catch((e) => {
  console.error(e.message);
  process.exit(1);
});
