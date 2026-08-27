// Screenshot harness for the quartermaster UI.
//
// Drives a headless Chromium against a RUNNING quartermaster instance and writes
// one PNG per (shot x theme x width) into `.shots/<label>/`. Purpose is visual
// review and before/after diffing of design changes — it reads the app's own
// pages only, and never issues an inference request (no model swaps).
//
//   npm run shots                        # capture into .shots/current
//   npm run shots -- --label before      # capture into .shots/before
//   npm run shots -- --url http://localhost:8080
//   npm run shots -- --native            # with the app window's own chrome
//   npm run shots -- --demo              # with a model loaded and traffic behind it
//   npm run shots -- --playground        # the playground app too (canned threads)
//
// The app is hash-routed under /ui/, uses SSE (so `networkidle` never fires —
// we settle on a selector plus a fixed delay), and reads its theme from the
// `theme-mode` localStorage key, which we seed per run rather than clicking the
// sidebar toggle.
//
// ## Two apps, one bundle
//
// A shot names the `app` it belongs to. "dashboard" is the instance's main port;
// "playground" is the second port, which is a different origin, behind a login,
// and whose every screen is per-user server-backed content. Those shots are
// opt-in (--playground) and run in their own browser context against canned
// fixtures — see scripts/shot-playground.mjs, which also explains why that
// context refuses every write.
import { chromium } from "playwright";
import { buildDemo, installDemo, pickModel, recordCatalog } from "./shot-demo.mjs";
import { buildPlayground, installPlayground, pgStorage, CHAT_ID, SHOP_ID, IMAGE_ID, SPEECH_ID } from "./shot-playground.mjs";
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

function parseArgs(argv) {
  const out = {
    url: process.env.QUARTERMASTER_URL || "http://localhost:1250",
    label: "current",
    out: path.join(ROOT, ".shots"),
    themes: ["dark", "light"],
    widths: [1440],
    height: 900,
    settle: 1200,
    only: null,
    native: false,
    demo: false,
    demoModel: undefined,
    tps: undefined,
    pps: undefined,
    playground: false,
    playgroundUrl: process.env.QUARTERMASTER_PLAYGROUND_URL || "",
    playgroundImage: "",
    playgroundPrompt: "",
  };
  for (let i = 0; i < argv.length; i++) {
    const [flag, inline] = argv[i].split(/=(.*)/s);
    const val = () => (inline !== undefined ? inline : argv[++i]);
    switch (flag) {
      case "--url": out.url = val().replace(/\/+$/, ""); break;
      case "--label": out.label = val(); break;
      case "--out": out.out = path.resolve(val()); break;
      case "--themes": out.themes = val().split(","); break;
      case "--widths": out.widths = val().split(",").map(Number); break;
      case "--height": out.height = Number(val()); break;
      case "--settle": out.settle = Number(val()); break;
      case "--only": out.only = val().split(","); break;
      case "--native": out.native = true; break;
      case "--demo": out.demo = true; break;
      case "--demo-model": out.demo = true; out.demoModel = val(); break;
      case "--demo-tps": out.demo = true; out.tps = Number(val()); break;
      case "--demo-pps": out.demo = true; out.pps = Number(val()); break;
      case "--playground": out.playground = true; break;
      case "--playground-url": out.playground = true; out.playgroundUrl = val().replace(/\/+$/, ""); break;
      // The one fixture that cannot be written: a generated picture. Any local
      // image works — it is inlined as a data URL into the canned image thread.
      case "--playground-image": out.playground = true; out.playgroundImage = path.resolve(val()); break;
      case "--playground-prompt": out.playgroundPrompt = val(); break;
      default:
        if (flag.startsWith("--")) throw new Error(`unknown flag: ${flag}`);
    }
  }
  return out;
}

// One entry per screen worth reviewing. `at` is the hash route; `prepare` runs
// after the route settles and is where a shot opens a modal or switches a tab.
// `wait` is a selector that must exist before we shoot, so we never capture a
// half-hydrated frame.
//
// Optional:
//   app       "dashboard" (default) or "playground" — which port it shoots
//   clip      {selector, pad} to photograph ONE element instead of the viewport
//   storage   localStorage seeded before the app's first script runs
//   focus     playground: which canned thread the load opens on
// Which repo the browse shot opens. Author-qualified on purpose: a bare model
// name ranks strangers' abliterated forks above the canonical repo, and the
// model card sits in frame under the file table. A 27B with a SHORT ladder is
// deliberate too -- its four rungs straddle a 24 GB card, so the estimate
// column shows its whole verdict range instead of fourteen rows of "fits on
// GPU", which is what a 12B's twenty-quant ladder photographs as.
const [BROWSE_AUTHOR, BROWSE_NAME] = "lmstudio-community/gemma-3-27b-it-GGUF".split("/");
// Space-separated, not slash-qualified: the hub's full-text search does not
// treat a repo id as an exact lookup.
const BROWSE_QUERY = `${BROWSE_AUTHOR} ${BROWSE_NAME}`;

const SHOTS = [
  { name: "dashboard", at: "#/", wait: "main" },
  { name: "dashboard-rail-open", at: "#/", wait: "main", prepare: async (p) => { await p.hover("aside"); await p.waitForTimeout(400); } },
  { name: "models", at: "#/models", wait: "table" },
  { name: "models-image", at: "#/models/image", wait: "table" },
  // Browse is a two-pane route, and the right pane is the half worth showing --
  // the quant table with the fit verdict against this box's VRAM. Left alone it
  // photographs as "Pick a repo to see its files and model card.", so the shot
  // searches for a repo and opens it. A deliberate query also keeps the picture
  // off whatever the hub happens to be trending that morning.
  {
    name: "browse",
    at: "#/browse",
    wait: "main",
    prepare: async (p) => {
      const box = p.getByPlaceholder("Search Hugging Face").first();
      await box.fill(BROWSE_QUERY);
      await box.press("Enter");
      await p.waitForTimeout(2500);
      // Browse opens filtered to the last 14 days, and the repo we want is a
      // year old -- that empty state offers its own "search the whole hub"
      // escape hatch, so take it rather than reaching into the filter panel.
      const whole = p.getByRole("button", { name: /whole hub/i }).first();
      if (await whole.count()) {
        await whole.click();
        await p.waitForTimeout(2500);
      }
      // The hub is live, so neither the result set nor its ordering is
      // guaranteed. Prefer the row that is actually the repo we asked for;
      // settle for the top row; say so if the search came back empty rather
      // than quietly shooting the placeholder pane.
      // Pinned to the result row's own class set. A looser "button with mono
      // text" matched the header's loaded-model chip, and clicking that
      // navigated to the models page -- a shot of the wrong route that still
      // reported success.
      const rows = p.locator("button.border-card-border-inner");
      const named = rows.filter({ hasText: BROWSE_NAME }).filter({ hasText: BROWSE_AUTHOR }).first();
      const hit = await named.count();
      if (!hit && !(await rows.count())) return `no hub results for "${BROWSE_QUERY}" — right pane left empty`;
      await (hit ? named : rows.first()).click();
      await p.waitForSelector("table.data-table tbody tr", { timeout: 15000 });
      await p.waitForTimeout(600);
      if (!hit) return `hub did not return ${BROWSE_AUTHOR}/${BROWSE_NAME} — shot opened the top result instead`;
    },
  },
  // Observe is one route with four tabs, so each tab is its own shot. We click
  // the tab rather than deep-linking #/performance & co: the tab is also
  // remembered in localStorage, so the hash alone cannot be trusted to win, and
  // a click is what a user does anyway. These are the shots --demo exists for --
  // on an idle instance they photograph as four empty panes.
  ...["Activity", "Logs", "Performance", "Context"].map((tab) => ({
    name: `observe-${tab.toLowerCase()}`,
    at: "#/observe",
    wait: "main",
    prepare: async (p) => {
      await p.getByRole("button", { name: tab, exact: true }).first().click();
      await p.waitForTimeout(900);
    },
  })),
  { name: "api-keys", at: "#/api-keys", wait: "main" },
  {
    name: "settings-modal",
    at: "#/",
    wait: "main",
    prepare: async (p) => {
      await p.getByRole("button", { name: "Settings" }).first().click();
      await p.waitForTimeout(600);
    },
  },
  {
    name: "help-modal",
    at: "#/",
    wait: "main",
    prepare: async (p) => {
      await p.getByRole("button", { name: "Help" }).first().click();
      await p.waitForTimeout(600);
    },
  },
  {
    name: "model-config-modal",
    at: "#/models",
    wait: "table",
    prepare: openModelConfig,
  },
  // The load-plan strip on its own. It is a thin band inside a tall modal, so a
  // full-viewport shot of the same screen buries the one thing this is meant to
  // show. `pad` keeps the modal's own edges in frame — a bar cropped to its
  // bounding box reads as a stray graphic with no idea what it belongs to.
  {
    name: "model-config-vram",
    at: "#/models",
    wait: "table",
    prepare: openModelConfig,
    clip: { selector: '[data-shot="load-plan"]', pad: { x: 0, y: 14 } },
  },

  // --- playground (opt-in: --playground) ----------------------------------
  //
  // No `at`: the playground is not hash-routed, its tab comes out of
  // localStorage; `focus` picks the thread (see shot-playground.mjs). The
  // extra settle is for the markdown/chart/waveform work a chat thread does on
  // mount — chat renders markdown, and the speech tab decodes each clip to draw
  // its waveform.
  {
    name: "pg-chat",
    app: "playground",
    wait: "textarea",
    storage: pgStorage("chat"),
    focus: CHAT_ID,
    settleExtra: 900,
  },
  {
    name: "pg-tools",
    app: "playground",
    wait: "textarea",
    storage: pgStorage("chat"),
    focus: SHOP_ID,
    settleExtra: 1200,
    // The subject of this shot is the tool call, and answer-phase searches are
    // folded into "Sources" — closed, at the very bottom of a long thread, which
    // is exactly where the default scroll-to-latest does NOT put it. Open the
    // fold, then centre it: the queries and what came back are the picture, with
    // the tail of the answer above them for context.
    prepare: async (p) => {
      // has-text, not text-is: the summary is a chevron plus "Sources" plus a
      // separate count span, so its text is "Sources (3)".
      const fold = p.locator('details:has(summary:has-text("Sources"))').first();
      if (!(await fold.count())) return "no Sources fold — the thread rendered no answer-phase search";
      await fold.evaluate((d) => {
        d.open = true;
        d.scrollIntoView({ block: "center" });
      });
      await p.waitForTimeout(500);
    },
  },
  {
    name: "pg-image",
    app: "playground",
    wait: "main, textarea",
    storage: pgStorage("images"),
    focus: IMAGE_ID,
    settleExtra: 900,
    // Guarded rather than skipped from the list, so the run says WHY the shot is
    // missing instead of leaving a gap someone has to trace back to a flag.
    requires: (opts) => (opts.playgroundImage ? null : "skipped: pass --playground-image <file> (a generated picture cannot be synthesized)"),
  },
  {
    name: "pg-speech",
    app: "playground",
    wait: "main, textarea",
    storage: pgStorage("speech"),
    focus: SPEECH_ID,
    settleExtra: 1200,
  },
];

/**
 * Inlines the picture the image shot is built around.
 *
 * The prompt has to come with it: a card captioned with a prompt that did not
 * produce the picture above it is a fabricated screenshot, not a fixture. Pass
 * --playground-prompt, or name the file after the prompt and let the stem stand
 * in for it.
 */
async function loadImageTurn(file, prompt) {
  if (!file) return null;
  const MIME = { ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp" };
  const ext = path.extname(file).toLowerCase();
  const mime = MIME[ext];
  if (!mime) throw new Error(`--playground-image: unsupported type ${ext || file}`);
  const bytes = await readFile(file);
  return {
    prompt: prompt || path.basename(file, ext).replace(/[-_]+/g, " "),
    dataUrl: `data:${mime};base64,${bytes.toString("base64")}`,
  };
}

/**
 * Turn a shot's `clip` spec into a Playwright clip rect, or undefined for the
 * usual whole-viewport shot.
 *
 * A cropped element is not the same picture as a cropped screenshot: the box is
 * measured AFTER `prepare` has run, so it lands on wherever the element ended up
 * (a modal's sticky strip moves with the modal). The padding is deliberate —
 * a rect flush to the element's border shaves the shadow and the rounded corner
 * off and the crop reads as a torn-out rectangle rather than as a component.
 *
 * `pad` is per-axis for a reason: an element that already spans its container
 * edge to edge (a modal's full-width strip) has nothing of its own to the left
 * and right, so horizontal padding only drags in whatever sits BEHIND the modal
 * and the crop gains two ragged slivers of an unrelated page.
 *
 * Coordinates are visual pixels and so is the clip, so there is no zoom
 * correction to do here; the harness never sets --qm-scale.
 */
async function clipOf(page, spec) {
  if (!spec) return undefined;
  const el = page.locator(spec.selector).first();
  await el.waitFor({ state: "visible", timeout: 10000 });
  const box = await el.boundingBox();
  if (!box) throw new Error(`clip: ${spec.selector} has no box`);
  const px = spec.pad?.x ?? (typeof spec.pad === "number" ? spec.pad : 0);
  const py = spec.pad?.y ?? (typeof spec.pad === "number" ? spec.pad : 0);
  const view = page.viewportSize();
  const x = Math.max(0, box.x - px);
  const y = Math.max(0, box.y - py);
  return {
    x,
    y,
    width: Math.min(box.width + px * 2, view.width - x),
    height: Math.min(box.height + py * 2, view.height - y),
  };
}

// Shared by the two model-config shots: open the first model's parameter modal.
async function openModelConfig(p) {
  const cog = p.getByRole("button", { name: "Edit parameters" }).first();
  if (!(await cog.count())) return "skipped: no models in the catalog";
  await cog.click();
  await p.waitForTimeout(1200);
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  // Naming a playground shot with --only opts into it: asking for it by name is
  // already the explicit request the flag exists to be.
  if (opts.only?.some((n) => SHOTS.find((s) => s.name === n)?.app === "playground")) opts.playground = true;
  const shots = (opts.only ? SHOTS.filter((s) => opts.only.includes(s.name)) : SHOTS)
    .filter((s) => s.app !== "playground" || opts.playground);
  if (!shots.length) throw new Error("no shots selected");

  // Fail fast with a useful message rather than 30 screenshots of a browser
  // error page when the instance simply isn't up.
  let mode = null;
  try {
    const r = await fetch(`${opts.url}/api/mode`);
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    mode = await r.json();
  } catch (e) {
    console.error(`Cannot reach quartermaster at ${opts.url} (${e.message}).`);
    console.error(`Start it, or pass --url http://host:port.`);
    process.exit(1);
  }

  // The dashboard knows the playground's port (it links to it), so the flag
  // usually needs no argument.
  if (opts.playground && !opts.playgroundUrl) {
    if (!mode?.playgroundPort) {
      console.error(`This instance has no playground port configured (GET /api/mode).`);
      console.error(`Start it with -playground-port, or pass --playground-url http://host:port.`);
      process.exit(1);
    }
    opts.playgroundUrl = `${new URL(opts.url).protocol}//${new URL(opts.url).hostname}:${mode.playgroundPort}`;
  }

  const outDir = path.join(opts.out, opts.label);
  // What KIND of run this is, carried in every filename it writes. Kinds share
  // one label directory so they diff side by side.
  const suffix = `${opts.native ? "--native" : ""}${opts.demo ? "--demo" : ""}`;
  // Clear only this run's own kind. Wiping the directory outright would make the
  // kinds mutually exclusive within a label, which is the opposite of what the
  // shared naming is for -- and would silently delete the "before" set someone
  // captured five minutes earlier. Stale shots of the SAME kind still go, so a
  // renamed or deleted SHOTS entry cannot leave a ghost behind.
  //
  // ...but a --only run is not a full run, and sweeping its whole kind would
  // delete the shots it is NOT retaking. A one-shot `--only` follow-up did
  // exactly that to a complete ten-shot set captured minutes earlier: same kind,
  // so the sweep took all ten and the run put back one. With --only the sweep is
  // limited to the selected names, which is the same ghost-prevention the full
  // sweep does, scoped to what this run actually replaces.
  //
  // (The manifest is still per-run and so lists only what this run took. Nothing
  // reads it -- site/build.mjs adopts by filename -- it is a record of the run.)
  const NAMED = /^(.*)--(?:dark|light)--\d+(.*)\.png$/;
  const picked = opts.only ? new Set(shots.map((s) => s.name)) : null;
  await mkdir(outDir, { recursive: true });
  for (const f of await readdir(outDir)) {
    const m = NAMED.exec(f);
    if (!m || m[2] !== suffix) continue;
    if (picked && !picked.has(m[1])) continue;
    await rm(path.join(outDir, f));
  }

  const warnings = [];
  // Playground writes already reported, so the same store saving on every load
  // is one line rather than one per shot per theme.
  const refused = new Set();

  // Recorded once, replayed into every context: the catalog is real (read off
  // the instance's own event stream, the only place the UI can learn it), and
  // only the state on top of it is synthesized. Nothing here loads a model or
  // sends an inference request. See scripts/shot-demo.mjs.
  let demo = null;
  let playground = null;
  // The playground fixture needs the catalog too — the model in its composer has
  // to be one this box has — so the recording is shared rather than tied to
  // --demo.
  if (opts.demo || opts.playground) {
    const { models, logs } = await recordCatalog(opts.url);
    if (opts.demo) {
      const perf = await fetch(`${opts.url}/api/performance`).then((r) => r.json()).catch(() => null);
      demo = buildDemo(models, perf, { model: opts.demoModel, tps: opts.tps, pps: opts.pps, logs });
      console.log(`  demo: ${models.length} models, showing ${demo.model.id} loaded`);
      if (opts.demoModel && demo.model.id !== opts.demoModel) {
        warnings.push(`--demo-model ${opts.demoModel} is not in the catalog; fell back to ${demo.model.id}`);
      }
    }
    if (opts.playground) {
      playground = buildPlayground({
        model: demo?.model ?? pickModel(models),
        imageModel: models.find((m) => m.capabilities?.image_generation),
        speechModel: models.find((m) => m.capabilities?.audio_speech),
        imageTurn: await loadImageTurn(opts.playgroundImage, opts.playgroundPrompt),
      });
      console.log(`  playground: ${opts.playgroundUrl} as "${playground.user}" (canned threads, writes refused)`);
    }
  }

  const browser = await chromium.launch();
  const manifest = [];

  // One context per (theme, width, app). The apps are separate origins with
  // separate fixtures, and a context carries both — sharing one would put the
  // playground's write-refusing route in front of the dashboard's requests.
  const byApp = new Map();
  for (const s of shots) {
    const app = s.app ?? "dashboard";
    if (!byApp.has(app)) byApp.set(app, []);
    byApp.get(app).push(s);
  }

  for (const theme of opts.themes) {
    for (const width of opts.widths) {
      for (const [app, appShots] of byApp) {
        const base = app === "playground" ? opts.playgroundUrl : opts.url;
        const ctx = await browser.newContext({
          viewport: { width, height: opts.height },
          deviceScaleFactor: 2,
          colorScheme: theme === "light" ? "light" : "dark",
          reducedMotion: "reduce", // shimmer/pulse animations must not make shots non-deterministic
        });
        // Seed the theme before any app code runs, so we never capture a frame in
        // the wrong theme while a click-toggle propagates.
        await ctx.addInitScript((t) => {
          try {
            localStorage.setItem("theme-mode", JSON.stringify(t));
          } catch {}
        }, theme);

        // --native: shoot the app window's chrome, not the browser's view of the
        // same page. `isNative` is a module-load feature test on the `qm*`
        // bindings internal/nativewin injects (lib/native.ts) -- no build flag and
        // no user-agent sniffing -- so defining them before the bundle's first
        // script is the whole trick. That renders TitleBar, TabStrip and
        // WindowControls, and gets main.ts to set `data-native`, which index.css
        // keys the shortened h-screen off: every page re-lays-out at the app
        // window's real height rather than the browser's.
        //
        // Every binding resolves rather than throwing, because the app's calls are
        // fire-and-forget and a rejection here would only show up as console
        // noise. What this CANNOT reproduce is the part DWM composites and
        // Chromium never sees: the rounded corners, the frame colour
        // qmCaptionColor sets, and the window shadow. Those need a real window
        // capture.
        if (opts.native) {
          await ctx.addInitScript(() => {
            const noop = () => Promise.resolve();
            Object.assign(window, {
              qmDrag: noop,
              qmMinimize: noop,
              qmMaximize: noop,
              qmClose: noop,
              qmCaptionColor: noop,
              qmOpenExternal: noop,
              qmPickFolder: () => Promise.resolve(""),
            });
          });
        }

        // The playground learns the catalog from the same SSE channel the
        // dashboard does, so the demo stream (if any) is worth installing on both:
        // it is what puts a real, ready model in the composer's dropdown.
        if (demo) await installDemo(ctx, demo);
        let pg = null;
        if (app === "playground") {
          // Reported once per route, not once per load: every shot reloads the
          // app and the same stores save again, so the raw stream would bury a
          // genuinely new PUT under thirty copies of a known one.
          pg = await installPlayground(ctx, playground, (w) => {
            if (refused.has(w)) return;
            refused.add(w);
            warnings.push(`refused a write to the playground: ${w} (a store saved during a shot — see shot-playground.mjs)`);
          });
        }

        const page = await ctx.newPage();
        page.on("pageerror", (e) => warnings.push(`[${theme}] page error: ${e.message}`));

        for (const shot of appShots) {
          // The suffix is part of the name, not a separate directory, so the kinds
          // share one label and diff side by side.
          const file = `${shot.name}--${theme}--${width}${suffix}.png`;
          const blocked = shot.requires?.(opts);
          if (blocked) {
            warnings.push(`[${theme}] ${shot.name}: ${blocked}`);
            console.log(`  – ${file}  (${blocked})`);
            continue;
          }
          try {
            // about:blank first: `goto` to a URL differing only in its hash does
            // NOT reload the document, so without this every shot would inherit
            // the previous one's SPA state — an open modal from the shot before
            // sits over the page and eats the next shot's clicks.
            await page.goto("about:blank");
            const url = `${base}/ui/${shot.at ?? ""}`;
            // Served history is rewritten BEFORE the load that reads it: which
            // thread opens is decided by which one is most recent, not by
            // anything on the page.
            pg?.focus(shot.focus);
            await page.goto(url, { waitUntil: "domcontentloaded" });
            // Which tab is open is localStorage, read once at module load — so it
            // has to be written with a document of that origin already loaded,
            // and read on the NEXT load. Hence the deliberate second navigation.
            if (shot.storage) {
              await page.evaluate((kv) => {
                for (const [k, v] of Object.entries(kv)) {
                  try {
                    localStorage.setItem(k, v);
                  } catch {}
                }
              }, shot.storage);
              await page.goto(url, { waitUntil: "domcontentloaded" });
            }
            if (shot.wait) await page.waitForSelector(shot.wait, { timeout: 15000 });
            await page.waitForTimeout(opts.settle + (shot.settleExtra ?? 0));
            const note = shot.prepare ? await shot.prepare(page) : undefined;
            if (note) warnings.push(`[${theme}] ${shot.name}: ${note}`);
            await page.screenshot({ path: path.join(outDir, file), clip: await clipOf(page, shot.clip) });
            manifest.push({ shot: shot.name, theme, width, file, note });
            console.log(`  ✓ ${file}${note ? `  (${note})` : ""}`);
          } catch (e) {
            warnings.push(`[${theme}] ${shot.name}: FAILED — ${e.message}`);
            console.log(`  ✗ ${file}  ${e.message.split("\n")[0]}`);
          }
        }
        await ctx.close();
      }
    }
  }

  await browser.close();
  await writeFile(
    path.join(outDir, `manifest${suffix || "--plain"}.json`),
    JSON.stringify({ url: opts.url, label: opts.label, native: opts.native, demo: demo?.model.id ?? false, themes: opts.themes, widths: opts.widths, shots: manifest, warnings }, null, 2),
  );

  console.log(`\n${manifest.length} shots → ${outDir}`);
  if (warnings.length) {
    console.log(`\n${warnings.length} warning(s):`);
    for (const w of warnings) console.log(`  - ${w}`);
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
