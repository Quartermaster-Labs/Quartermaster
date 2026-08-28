// Renders the in-app help wiki into the repo's docs/ tree.
//
//   npm run docs            # write docs/
//   npm run docs -- --check # exit 1 if docs/ is stale (CI / pre-release)
//
// The corpus is ONE file, internal/server/wiki_articles.json: the Go server
// embeds it for the `wiki_search` tool, the Help modal renders it, and this
// script publishes it. Nobody hand-writes a page under docs/: an edit there is
// lost on the next run, which is why every generated file says so at the top.
//
// Why publish at all, when the same text is a click away inside the app: the
// audience is people looking at the repo who have not installed anything yet,
// and search engines. That is also the whole scope: anything about internals
// belongs in the subsystem docs next to the code it describes, not here.
import { readdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const ARTICLES = path.join(ROOT, "internal", "server", "wiki_articles.json");
const CATEGORIES = path.join(ROOT, "ui-svelte", "src", "lib", "wiki-categories.json");
const OUT = path.join(ROOT, "docs");

// Marker AND ownership claim: --check compares against it, and the writer only
// deletes stale files that carry it, so a hand-added page (or docs/assets) is
// never collateral.
const STAMP = "<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->";

const readJSON = async (p) => JSON.parse(await readFile(p, "utf8"));

function renderArticle(a) {
  return `${STAMP}\n\n# ${a.title}\n\n${a.body.trim()}\n`;
}

function renderIndex(articles, categories) {
  const byId = new Map(articles.map((a) => [a.id, a]));
  const known = new Set(categories.flatMap((c) => c.ids));
  const groups = [
    ...categories.map((c) => ({ title: c.title, items: c.ids.map((id) => byId.get(id)).filter(Boolean) })),
    { title: "More", items: articles.filter((a) => !known.has(a.id)) },
  ].filter((g) => g.items.length);

  const body = groups
    .map((g) => `## ${g.title}\n\n${g.items.map((a) => `- [${a.title}](${a.id}.md)`).join("\n")}`)
    .join("\n\n");

  return `${STAMP}

# Quartermaster user guide

The same help wiki the app ships with, reachable in-app from **Help** in the sidebar,
and searchable by the playground assistant itself via its \`wiki_search\` tool.

Looking for how quartermaster works *inside*? That lives beside the code it
describes: see the subsystem table in [\`CLAUDE.md\`](../CLAUDE.md).

${body}
`;
}

async function main() {
  const check = process.argv.includes("--check");
  const [articles, categories] = await Promise.all([readJSON(ARTICLES), readJSON(CATEGORIES)]);

  const missing = categories.flatMap((c) => c.ids).filter((id) => !articles.some((a) => a.id === id));
  if (missing.length) throw new Error(`wiki-categories.json lists unknown article id(s): ${missing.join(", ")}`);

  const files = new Map([["README.md", renderIndex(articles, categories)]]);
  for (const a of articles) files.set(`${a.id}.md`, renderArticle(a));

  // Anything we generated before and would not generate now: a renamed or
  // deleted article must not leave a page behind, still indexed and still
  // wrong. Ours-only, by the stamp.
  const existing = (await readdir(OUT).catch(() => [])).filter((f) => f.endsWith(".md"));
  const stale = [];
  for (const f of existing) {
    if (files.has(f)) continue;
    if ((await readFile(path.join(OUT, f), "utf8")).startsWith(STAMP)) stale.push(f);
  }

  if (check) {
    const drifted = [];
    for (const [name, want] of files) {
      const got = await readFile(path.join(OUT, name), "utf8").catch(() => null);
      if (got !== want) drifted.push(got === null ? `${name} (missing)` : name);
    }
    const bad = [...drifted, ...stale.map((f) => `${f} (stale)`)];
    if (bad.length) {
      console.error(`docs/ is out of date with the wiki corpus:\n  ${bad.join("\n  ")}\n\nRun \`npm run docs\` from ui-svelte/.`);
      process.exit(1);
    }
    console.log(`docs/ is up to date (${files.size} files).`);
    return;
  }

  for (const f of stale) await rm(path.join(OUT, f));
  for (const [name, content] of files) await writeFile(path.join(OUT, name), content);
  console.log(`${files.size} files → docs/${stale.length ? `  (removed ${stale.length} stale)` : ""}`);
}

main().catch((e) => {
  console.error(e.message);
  process.exit(1);
});
