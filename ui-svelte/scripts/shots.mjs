// Screenshot harness for the quartermaster UI.
//
// Drives a headless Chromium against a RUNNING quartermaster instance and writes
// one PNG per (shot x theme x width) into `.shots/<label>/`. Purpose is visual
// review and before/after diffing of design changes — it reads the dashboard's
// own pages only, and never issues an inference request (no model swaps).
//
//   npm run shots                        # capture into .shots/current
//   npm run shots -- --label before      # capture into .shots/before
//   npm run shots -- --url http://localhost:8080
//   npm run shots -- --native            # with the app window's own chrome
//
// The app is hash-routed under /ui/, uses SSE (so `networkidle` never fires —
// we settle on a selector plus a fixed delay), and reads its theme from the
// `theme-mode` localStorage key, which we seed per run rather than clicking the
// sidebar toggle.
import { chromium } from "playwright";
import { mkdir, readdir, rm, writeFile } from "node:fs/promises";
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
const SHOTS = [
  { name: "dashboard", at: "#/", wait: "main" },
  { name: "dashboard-rail-open", at: "#/", wait: "main", prepare: async (p) => { await p.hover("aside"); await p.waitForTimeout(400); } },
  { name: "models", at: "#/models", wait: "table" },
  { name: "models-image", at: "#/models/image", wait: "table" },
  { name: "browse", at: "#/browse", wait: "main" },
  { name: "observe", at: "#/observe", wait: "main" },
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
    prepare: async (p) => {
      const cog = p.getByRole("button", { name: "Edit parameters" }).first();
      if (!(await cog.count())) return "skipped: no models in the catalog";
      await cog.click();
      await p.waitForTimeout(1200);
    },
  },
];

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const shots = opts.only ? SHOTS.filter((s) => opts.only.includes(s.name)) : SHOTS;
  if (!shots.length) throw new Error("no shots selected");

  // Fail fast with a useful message rather than 30 screenshots of a browser
  // error page when the instance simply isn't up.
  try {
    const r = await fetch(`${opts.url}/api/mode`);
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
  } catch (e) {
    console.error(`Cannot reach quartermaster at ${opts.url} (${e.message}).`);
    console.error(`Start it, or pass --url http://host:port.`);
    process.exit(1);
  }

  const outDir = path.join(opts.out, opts.label);
  // Clear only this run's own half of the label. Wiping the directory outright
  // would make `--native` and a plain run mutually exclusive within one label,
  // which is the opposite of what the shared naming is for -- and would silently
  // delete the "before" set someone captured five minutes earlier. Stale shots
  // of the same kind still go, so a renamed or deleted SHOTS entry cannot leave
  // a ghost behind.
  const mine = (f) => f.endsWith("--native.png") === opts.native;
  await mkdir(outDir, { recursive: true });
  for (const f of await readdir(outDir)) {
    if (f.endsWith(".png") && mine(f)) await rm(path.join(outDir, f));
  }

  const browser = await chromium.launch();
  const manifest = [];
  const warnings = [];

  for (const theme of opts.themes) {
    for (const width of opts.widths) {
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

      const page = await ctx.newPage();
      page.on("pageerror", (e) => warnings.push(`[${theme}] page error: ${e.message}`));

      for (const shot of shots) {
        // The suffix is part of the name, not a separate directory, so a native
        // and a browser run share one label and diff side by side.
        const file = `${shot.name}--${theme}--${width}${opts.native ? "--native" : ""}.png`;
        try {
          // about:blank first: `goto` to a URL differing only in its hash does
          // NOT reload the document, so without this every shot would inherit
          // the previous one's SPA state — an open modal from the shot before
          // sits over the page and eats the next shot's clicks.
          await page.goto("about:blank");
          await page.goto(`${opts.url}/ui/${shot.at}`, { waitUntil: "domcontentloaded" });
          if (shot.wait) await page.waitForSelector(shot.wait, { timeout: 15000 });
          await page.waitForTimeout(opts.settle);
          const note = shot.prepare ? await shot.prepare(page) : undefined;
          if (note) warnings.push(`[${theme}] ${shot.name}: ${note}`);
          await page.screenshot({ path: path.join(outDir, file) });
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

  await browser.close();
  await writeFile(
    path.join(outDir, opts.native ? "manifest-native.json" : "manifest.json"),
    JSON.stringify({ url: opts.url, label: opts.label, native: opts.native, themes: opts.themes, widths: opts.widths, shots: manifest, warnings }, null, 2),
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
