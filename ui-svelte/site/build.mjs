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

const { REPO, UPSTREAM, HERO, PILLS, SECTIONS, STORY, ICONS, SHOWCASE, MORE, INSTALL } =
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
    <a class="brand" href="${up}index.html">${MARK} Quartermaster</a>
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
    <span>MIT licensed. Originally forked from <a href="${UPSTREAM}">llama-swap</a>.</span>
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

// The hero image is the one picture on the page that is already on screen when
// you arrive, so it loads eagerly and at high priority. Left lazy it decodes on
// the first scroll instead, and a screenshot is a big bitmap to decode and
// upload in the middle of a gesture: that alone is a visible stutter.
const eagerImg = (html) =>
  html.replace(/ loading="lazy" decoding="async"/g, ' loading="eager" fetchpriority="high" decoding="async"');

// `light` is the sibling capture (dashboard-light.webp next to dashboard.webp)
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

// Clicking the picture advances, which is what people try first; the tabs jump
// to a specific shot; arrow keys work once a tab has focus. Nothing here runs
// before the markup is already legible.
const GALLERY_SCRIPT = `<script>
(function () {
  [].forEach.call(document.querySelectorAll("[data-gal]"), function (gal) {
    var tabs = [].slice.call(gal.querySelectorAll(".gal-tab"));
    var panels = [].slice.call(gal.querySelectorAll(".gal-panel"));
    if (tabs.length < 2) return;
    var at = 0;
    function show(next, focus) {
      at = (next + tabs.length) % tabs.length;
      tabs.forEach(function (t, i) {
        var on = i === at;
        t.classList.toggle("is-current", on);
        t.setAttribute("aria-selected", String(on));
        t.tabIndex = on ? 0 : -1;
        panels[i].classList.toggle("is-current", on);
        panels[i].hidden = !on;
      });
      if (focus) tabs[at].focus();
    }
    tabs.forEach(function (t, i) {
      t.addEventListener("click", function () { show(i); });
      t.addEventListener("keydown", function (e) {
        if (e.key === "ArrowRight") { e.preventDefault(); show(at + 1, true); }
        else if (e.key === "ArrowLeft") { e.preventDefault(); show(at - 1, true); }
      });
    });
    panels.forEach(function (p) {
      var shot = p.querySelector(".shot");
      if (!shot) return;
      shot.addEventListener("click", function () { show(at + 1); });
      shot.classList.add("is-clickable");
    });
  });
})();
</script>`;

// Two or more shots become a gallery: one large frame, a labelled tab per shot
// under it, and the frame itself advances to the next on click.
//
// Every panel is in the markup and the first is current, so with JS off this is
// a screenshot with a caption rather than an empty box. The tabs are real
// buttons in a tablist, so this is reachable from the keyboard and not just from
// a pointer.
function renderShotGallery(id, shots) {
  const panels = shots
    .map(
      (s, i) => `<div class="gal-panel${i === 0 ? " is-current" : ""}" id="${id}-p${i}" role="tabpanel"
        aria-labelledby="${id}-t${i}"${i === 0 ? "" : " hidden"}>
        ${shotFrame(s.file, `${s.label}: ${s.caption}`, s.label, s.light)}
        <p class="gal-caption"><b>${esc(s.label)}</b>${esc(s.caption)}</p>
      </div>`,
    )
    .join("\n");

  const tabs = shots
    .map(
      (s, i) => `<button class="gal-tab${i === 0 ? " is-current" : ""}" type="button" role="tab"
        id="${id}-t${i}" aria-controls="${id}-p${i}" aria-selected="${i === 0}" tabindex="${i === 0 ? 0 : -1}">
        <span class="gal-n">${i + 1}</span>${esc(s.label)}</button>`,
    )
    .join("\n");

  return `<div class="gal" data-gal>
    <div class="gal-stage">${panels}</div>
    <div class="gal-tabs" role="tablist" aria-label="Screenshots">${tabs}</div>
  </div>`;
}

// Each showcase is its own scroll-into block. A single shot sits beside the
// copy and the side alternates down the page; a gallery needs the full width, so
// the copy goes above it instead.
function renderShowcase(entry, index) {
  const { id, shots = [] } = entry;
  const gallery = shots.length > 1;
  const points = entry.points?.length
    ? `<ul class="sc-points">${entry.points.map((p) => `<li>${icon("check", 15)}<span>${esc(p)}</span></li>`).join("")}</ul>`
    : "";

  const copy = `<div class="sc-copy" data-reveal>
      <span class="eyebrow">${icon(entry.icon, 15)} ${esc(entry.eyebrow)}</span>
      <h3>${esc(entry.title)}</h3>
      <p>${esc(entry.body)}</p>
      ${points}
    </div>`;

  // A section whose shots have not been captured yet still says its piece.
  if (!shots.length) return `<section class="sc sc-bare" id="${id}"><div class="wrap narrow">${copy}</div></section>`;

  if (gallery) {
    return `<section class="sc sc-wide" id="${id}">
  <div class="wrap">
    ${copy}
    <div data-reveal>${renderShotGallery(id, shots)}</div>
  </div>
</section>`;
  }

  const media = `<div class="sc-media" data-reveal>
      ${shotFrame(shots[0].file, `${shots[0].label}: ${shots[0].caption}`, shots[0].label, shots[0].light)}
      <p class="gal-caption"><b>${esc(shots[0].label)}</b>${esc(shots[0].caption)}</p>
    </div>`;

  return `<section class="sc sc-split${index % 2 ? " is-flipped" : ""}" id="${id}">
  <div class="wrap">
    <div class="sc-row">${copy}${media}</div>
  </div>
</section>`;
}

function renderFeatures(showcase) {
  const blocks = showcase.map(renderShowcase).join("\n");
  const cards = MORE.map(
    (f, i) => `<article class="card" data-reveal style="--d:${(i % 3) * 60}ms">
      <div class="ico">${icon(f.icon)}</div>
      <h3>${esc(f.title)}</h3>
      <p>${esc(f.body)}</p>
    </article>`,
  ).join("\n");

  return `<section id="features">
  <div class="wrap">
    ${sectionHead("features")}
  </div>
</section>
${blocks}
<section id="more">
  <div class="wrap">
    ${sectionHead("more")}
    <div class="grid">${cards}</div>
  </div>
</section>`;
}

// The one section written in the first person. It sits between the screenshots
// and the docs so the page has somewhere to stop being a spec sheet.
function renderStory() {
  // Upstream gets a link on its first mention in the prose rather than a button
  // underneath: the sentence already says what llama-swap is to us, so a
  // separate CTA would only be a second, weaker way of saying it.
  let linked = false;
  const paras = STORY.map((p) => {
    let html = esc(p);
    if (!linked && html.includes("llama-swap")) {
      html = html.replace("llama-swap", `<a href="${UPSTREAM}">llama-swap</a>`);
      linked = true;
    }
    return `<p>${html}</p>`;
  }).join("\n      ");

  return `<section id="story">
  <div class="wrap narrow">
    ${sectionHead("story")}
    <div class="story" data-reveal>${paras}</div>
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
    title: `${a.title} · Quartermaster`,
    description: summarize(a.body),
    depth: 1,
    body: `<main class="wrap docs-layout">
  <aside class="docs-nav">${docsNav(articles, categories, a.id)}</aside>
  <article class="prose">
    <h1>${esc(a.title)}</h1>
    ${body}
    <p class="docs-foot">This page is generated from the help wiki that ships inside the app: the same text you get from the <strong>Help</strong> button, and the same text the playground assistant searches. Corrections go to <a href="${REPO}/blob/main/internal/server/wiki_articles.json"><code>wiki_articles.json</code></a>.</p>
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
    title: "User guide · Quartermaster",
    description:
      "How to load models, tune per-model config, use the playground, set up web search, scope API keys and read the VRAM gauges in Quartermaster.",
    depth: 1,
    body: `<main class="wrap docs-layout">
  <aside class="docs-nav">${docsNav(articles, categories, null)}</aside>
  <article class="prose">
    <h1>User guide</h1>
    <p>The help wiki that ships with the app, published here so you can read it before installing anything. Inside Quartermaster the same articles are one click away under <strong>Help</strong>, and the playground assistant searches them to answer questions about the app itself.</p>
    <p>Looking for how Quartermaster works <em>inside</em>? That lives beside the code it describes: see the subsystem table in <a href="${REPO}/blob/main/CLAUDE.md">CLAUDE.md</a>.</p>
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

// Resolve every showcase's shots against what is actually in docs/assets/, and
// return the showcase with the missing ones dropped. Screenshots the site wants
// but doesn't have are named on stdout rather than shipped as broken images: a
// section with nothing left renders as copy alone and the page still holds
// together, so the site is publishable before the capture run happens.
async function collectShowcase() {
  const have = new Set(await readdir(IMAGES).catch(() => []));
  const lightOf = (f) => f.replace(/\.webp$/, "-light.webp");
  const missing = [];

  const resolved = SHOWCASE.map((entry) => ({
    ...entry,
    shots: entry.shots.flatMap((s) => {
      if (!have.has(s.file)) {
        missing.push(s.file);
        return [];
      }
      return [{ ...s, light: have.has(lightOf(s.file)) ? lightOf(s.file) : null }];
    }),
  }));

  if (missing.length) {
    console.warn(`  ! ${missing.length} screenshot(s) not in docs/assets, skipped: ${missing.join(", ")}`);
    // The playground is a second app on its own port and is opt-in, so a run
    // without the flag comes back having silently skipped exactly the shots
    // this line was supposed to tell you how to get.
    const flags = missing.some((f) => f.startsWith("pg-")) ? "--demo --playground" : "--demo";
    console.warn(`    capture with: npm run shots -- ${flags}   then: npm run site -- --adopt .shots/current`);
    if (missing.includes("pg-image.webp")) {
      console.warn(`    pg-image also needs: --playground-image <file> --playground-prompt "<the prompt that made it>"`);
    }
    const orphan = missing.filter((f) => !Object.keys(SHOT_SOURCE).includes(f));
    if (orphan.length) {
      console.warn(`    no capture recipe for: ${orphan.join(", ")} — see site/content.mjs SHOWCASE`);
    }
  }
  return resolved;
}

// Copy the dark demo capture of each gallery shot out of a .shots run and into
// docs/assets under the site's own name. .shots/ is gitignored (local visual
// diffing); the site's images have to be committed because the Pages build has
// no running instance to capture from.
// Left-hand side is the name the site uses, right-hand side is the `npm run
// shots` entry that produces it. A site shot with no entry here has no capture
// recipe yet and must be dropped in by hand; collectShowcase() says which.
const SHOT_SOURCE = {
  "dashboard.webp": "dashboard",
  "models.webp": "models",
  "model-config.webp": "model-config-modal",
  "vram-gauge.webp": "model-config-vram",
  "browse.webp": "browse",
  "images.webp": "models-image",
  // The playground half. These come from `npm run shots -- --playground`, which
  // photographs canned threads against a write-refusing route rather than the
  // operator's own conversations; pg-image additionally needs a picture handed
  // to it (--playground-image), because a generated one cannot be synthesized.
  "pg-chat.webp": "pg-chat",
  "pg-tools.webp": "pg-tools",
  "pg-image.webp": "pg-image",
  "pg-speech.webp": "pg-speech",
};

// The shots harness captures at device scale, which on this machine means
// 2880px wide. The page never shows one wider than ~1120 CSS px, so shipping
// the raw capture costs a ~20MB decoded bitmap and a downscale on the GPU for a
// picture nobody can see the detail in. Half that width is still retina-sharp
// at the size it renders.
const MAX_SHOT_WIDTH = 1600;
const WEBP_QUALITY = 0.9;

// Re-encoding needs a raster decoder, and the only one this repo already
// depends on is the browser Playwright drives (a devDependency, and the same
// one that produced the captures). Deliberately confined to --adopt: the Pages
// runner only ever copies files that were committed already resized, so CI
// never installs a browser for this.
async function optimizeInto(files, from) {
  const { chromium } = await import("playwright");
  const browser = await chromium.launch();
  const page = await browser.newPage();
  try {
    let n = 0;
    for (const [src, target] of files) {
      // Passed as a data URL rather than a file:// path: an about:blank page
      // cannot read file:// (EncodingError), and a data URL keeps the canvas
      // untainted so toDataURL still works.
      const dataUrl = `data:image/png;base64,${(await readFile(path.join(from, src))).toString("base64")}`;
      const out = await page.evaluate(
        async ([url, maxW, q]) => {
          const img = new Image();
          img.src = url;
          await img.decode();
          const scale = Math.min(1, maxW / img.naturalWidth);
          const c = document.createElement("canvas");
          c.width = Math.round(img.naturalWidth * scale);
          c.height = Math.round(img.naturalHeight * scale);
          const ctx = c.getContext("2d");
          ctx.imageSmoothingQuality = "high";
          ctx.drawImage(img, 0, 0, c.width, c.height);
          return { data: c.toDataURL("image/webp", q).split(",")[1], w: c.width, h: c.height };
        },
        [dataUrl, MAX_SHOT_WIDTH, WEBP_QUALITY],
      );
      const buf = Buffer.from(out.data, "base64");
      await writeFile(path.join(IMAGES, target), buf);
      console.log(`  ✓ ${src} → docs/assets/${target} (${out.w}×${out.h}, ${(buf.length / 1024) | 0} KB)`);
      n++;
    }
    return n;
  } finally {
    await browser.close();
  }
}

async function adopt(dir) {
  const from = path.resolve(UI, dir);
  const files = await readdir(from).catch(() => {
    throw new Error(`no such shots directory: ${from}`);
  });
  const jobs = [];
  for (const [target, shot] of Object.entries(SHOT_SOURCE)) {
    for (const theme of ["dark", "light"]) {
      // Prefer the demo capture (a model loaded, traffic behind it) over an empty one.
      const match =
        files.find((f) => f.startsWith(`${shot}--${theme}--`) && f.endsWith("--demo.png")) ||
        files.find((f) => f.startsWith(`${shot}--${theme}--`) && f.endsWith(".png"));
      if (!match) continue;
      jobs.push([match, theme === "dark" ? target : target.replace(/\.webp$/, "-light.webp")]);
    }
  }
  if (!jobs.length) throw new Error(`no matching shots in ${from} (expected e.g. dashboard--dark--1440--demo.png)`);
  const n = await optimizeInto(jobs, from);
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
  console.log(release ? `release: ${release.tag} (${release.url})` : "release: none found, CTA falls back to /releases/latest");

  const showcase = await collectShowcase();
  const haveImages = new Set(await readdir(IMAGES).catch(() => []));
  const heroFile = "dashboard.webp";
  const hero = haveImages.has(heroFile)
    ? { file: heroFile, light: haveImages.has("dashboard-light.webp") ? "dashboard-light.webp" : null }
    : null;
  const galleries = showcase.some((s) => s.shots.length > 1);

  const landing = page({
    title: "Quartermaster · run any local model, on demand",
    description: HERO.lede,
    ogImage: hero?.file,
    extraScripts: [COPY_SCRIPT, galleries ? GALLERY_SCRIPT : "", REVEAL_SCRIPT].join("\n"),
    body: [
      renderHero(release, HERO.title),
      hero
        // Deliberately not revealed: it is a full-width PNG right at the fold,
        // and fading a texture that size in mid-scroll is the one thing on this
        // page heavy enough to drop frames. It is already on screen anyway.
        ? `<div class="wrap">${eagerImg(shotFrame(hero.file, "The Quartermaster dashboard", "localhost:1250/ui/", hero.light))}</div>`
        : "",
      renderFeatures(showcase),
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
    const types = { ".html": "text/html", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".webp": "image/webp", ".woff2": "font/woff2" };
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
