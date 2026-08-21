import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { resolve } from "node:path";
import { rename } from "node:fs/promises";
import { existsSync } from "node:fs";

// Second bundle, for the first-run wizard only (cmd/quartermaster-setup).
//
// It is separate from vite.config.ts rather than another route in the dashboard
// because the two ship in different binaries: the wizard's HTML has to render
// before quartermaster exists on disk, and pulling chart.js, mermaid, katex and
// pdfjs into a setup program that draws three steps would be absurd.

// The Go side serves index.html and injects the run token into it. Vite names
// its output after the input HTML, so rename the one emitted asset rather than
// keeping a second index.html at the repo root purely to satisfy the bundler.
function renameHtml(): Plugin {
  return {
    name: "qm-setup-rename-html",
    // writeBundle, not generateBundle: Vite emits the HTML asset from its own
    // post-plugin, after any plugin hook we can order ourselves against, so the
    // file only reliably exists once the bundle has been written to disk.
    async writeBundle(options) {
      const dir = options.dir ?? "";
      const from = resolve(dir, "setup.html");
      if (!existsSync(from)) return;
      await rename(from, resolve(dir, "index.html"));
    },
  };
}

export default defineConfig({
  plugins: [svelte(), tailwindcss(), renameHtml()],
  // Served from the root of the wizard's own loopback server, not under /ui/.
  base: "/",
  // No public/ copy: the dashboard's icons, manifest and fonts are dead weight
  // in a window that has no tab, no bookmark and no installable manifest.
  publicDir: false,
  build: {
    outDir: "../internal/setup/ui_dist",
    assetsDir: "assets",
    // NOT emptied: ui_dist holds a committed .gitkeep that keeps the //go:embed
    // in internal/setup compiling on a tree where the UI was never built.
    emptyOutDir: false,
    rollupOptions: {
      input: resolve(import.meta.dirname, "setup.html"),
    },
  },
});
