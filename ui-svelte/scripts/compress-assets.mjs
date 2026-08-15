// Deterministic gzip + brotli pre-compression for the UI build.
//
// Replaces the two `vite-plugin-compression2` instances that used to run
// inside `vite build` (one gzip, one brotli, on the same asset set). They
// raced: on 2026-08-15 four of 136 siblings contained gzip bytes under a
// `.br` name, the Go server (which trusts the extension and sets
// `Content-Encoding: br`) shipped them, and browser module fetches died
// with "Failed to fetch dynamically imported module". One script,
// sequential writes, plus a verify pass that decompresses every sibling
// and compares it to the plain file — a corrupt artifact now fails the
// build instead of reaching the Go embed.
//
// Run automatically by `npm run build` (wired in package.json).

import {
  brotliCompressSync,
  brotliDecompressSync,
  gzipSync,
  gunzipSync,
} from "node:zlib";
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const OUT_DIR = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "internal", "server", "ui_dist");
const THRESHOLD = 1024; // compress only files larger than this (matches old plugin config)

function walk(dir, out = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);
    if (entry.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

// 1) Compress: every plain file over the threshold gets a .gz and a .br sibling.
let compressed = 0;
for (const file of walk(OUT_DIR)) {
  if (file.endsWith(".br") || file.endsWith(".gz")) continue;
  if (statSync(file).size <= THRESHOLD) continue;
  const data = readFileSync(file);
  writeFileSync(file + ".gz", gzipSync(data, { level: 9 }));
  writeFileSync(file + ".br", brotliCompressSync(data)); // quality 11 (node default)
  compressed++;
}

// 2) Verify: every sibling in the tree must decompress to exactly the plain
// file's bytes. This also catches anything written by other tools.
let verified = 0;
let bad = 0;
for (const file of walk(OUT_DIR)) {
  if (!file.endsWith(".br") && !file.endsWith(".gz")) continue;
  const plain = file.slice(0, -3);
  const decode = file.endsWith(".br") ? brotliDecompressSync : gunzipSync;
  try {
    const out = decode(readFileSync(file));
    if (!out.equals(readFileSync(plain))) throw new Error("decoded bytes differ from plain file");
    verified++;
  } catch (e) {
    bad++;
    console.error(`corrupt ${file.endsWith(".br") ? "brotli" : "gzip"}: ${plain.slice(OUT_DIR.length + 1)}/${file.slice(plain.length)}: ${e.message}`);
  }
}

console.log(`compress-assets: ${compressed} files compressed, ${verified} siblings verified`);
if (bad > 0) {
  console.error(`compress-assets: ${bad} corrupt sibling(s) — build must not ship these. Delete them and rebuild.`);
  process.exit(1);
}
