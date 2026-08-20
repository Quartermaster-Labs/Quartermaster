import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

// Pre-compressed .gz/.br siblings are made by scripts/compress-assets.mjs
// after the build (wired in package.json), NOT by vite-plugin-compression2 —
// the plugin's two parallel compression instances raced and shipped gzip
// bytes under a .br name, which the Go server then sent as Content-Encoding: br.

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  base: "/ui/",
  build: {
    outDir: "../internal/server/ui_dist",
    assetsDir: "assets",
  },
  server: {
    // wiki.ts imports internal/server/wiki_articles.json (the single wiki
    // corpus, which must live in the Go package for //go:embed).
    fs: { allow: [".."] },
    // yes very insecure but who's running this thing
    // on the public internet for dev?! haha.
    host: "0.0.0.0",
    allowedHosts: true,
    proxy: Object.fromEntries(
      ["/api", "/logs", "/upstream", "/unload", "/v1", "/sdapi"].map((path) => [
        path,
        process.env.QUARTERMASTER_URL ?? "http://localhost:8080",
      ]),
    ),
  },
});
