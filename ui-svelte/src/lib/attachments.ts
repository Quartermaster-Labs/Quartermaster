// Document attachments for chat: everything the paperclip accepts that is NOT
// an image.
//
// Images ride the OpenAI `image_url` content part and need a vision-capable
// model. Documents don't: they are turned into TEXT in the browser and folded
// into the user's own message, so every model can read them and the whole
// server side (history, replay, compaction, KV prefix) needs no changes at all.
//
// Extraction is therefore deliberately client-side:
//  - PDF via pdf.js (lazy `import()`, same pattern as mermaid — the main bundle
//    is unaffected). No poppler/mutool binary to install, hunt on PATH, or fail
//    on when missing.
//  - DOCX via a minimal zip reader over the native DecompressionStream, since a
//    docx is a zip of XML and one file format does not justify a dependency
//    tree (mammoth → jszip → …).
//  - Audio is the one exception: it is transcribed by the ASR backend through
//    the existing /v1/audio/transcriptions route, then inlined as text like the
//    rest. That swaps the ASR model into VRAM (one GPU, one pool), so the caller
//    warns before doing it.
//
// Neither pdf.js nor pdftotext does OCR: a scanned PDF yields (almost) no text,
// which extractPdfText reports rather than attaching an empty document.

import type { Model } from "./types";

export type DocKind = "text" | "pdf" | "docx" | "audio";

export interface AttachedDoc {
  id: string;
  name: string;
  kind: DocKind;
  /** Extracted plain text. Empty while `status` is "loading". */
  text: string;
  tokens: number;
  /** Short provenance line for the chip ("12 pages", "transcribed"). */
  note?: string;
  status: "loading" | "ready" | "error";
  error?: string;
}

// Extension-driven, with the MIME type only as a fallback: Windows reports an
// empty `File.type` for .md/.ts/.go/.yaml and friends, so trusting the MIME
// would reject exactly the files a developer attaches most.
export const TEXT_EXTS = [
  "txt", "md", "markdown", "csv", "tsv", "json", "jsonl", "yaml", "yml", "toml",
  "xml", "html", "htm", "log", "ini", "cfg", "conf", "env", "sql", "rst", "tex",
  "go", "rs", "py", "js", "mjs", "cjs", "ts", "tsx", "jsx", "svelte", "vue",
  "c", "h", "cc", "cpp", "hpp", "cs", "java", "kt", "swift", "rb", "php", "lua",
  "sh", "bash", "zsh", "ps1", "bat", "make", "mk", "dockerfile", "gradle",
  "diff", "patch",
];
export const AUDIO_EXTS = ["wav", "mp3", "m4a", "mp4", "mpga", "ogg", "oga", "opus", "flac", "webm"];
export const IMAGE_EXTS = ["jpg", "jpeg", "png", "gif", "webp"];

export const MAX_DOCS_PER_MESSAGE = 5;
/** Hard read cap. Beyond this a "text" file is a data dump, not a document. */
export const MAX_DOC_BYTES = 32 * 1024 * 1024;
/**
 * Share of the context window a single message's documents may claim. The rest
 * has to hold the system prompt, the conversation so far, and the reply.
 */
export const DOC_CTX_FRACTION = 0.4;

export function extOf(name: string): string {
  const i = name.lastIndexOf(".");
  return i < 0 ? "" : name.slice(i + 1).toLowerCase();
}

/** null = not something the composer accepts. */
export function classifyAttachment(file: File): "image" | DocKind | null {
  const ext = extOf(file.name);
  if (IMAGE_EXTS.includes(ext)) return "image";
  if (AUDIO_EXTS.includes(ext)) return "audio";
  if (ext === "pdf") return "pdf";
  if (ext === "docx") return "docx";
  if (TEXT_EXTS.includes(ext)) return "text";
  // No extension (or an unlisted one) — fall back to the MIME type. A bare
  // "Dockerfile"/"Makefile" arrives as text/plain or "", so accept an empty type
  // only when the name has no extension at all.
  const mime = file.type;
  if (mime.startsWith("image/")) return "image";
  if (mime.startsWith("audio/") || mime.startsWith("video/")) return "audio";
  if (mime === "application/pdf") return "pdf";
  if (mime.startsWith("text/") || mime === "application/json") return "text";
  if (!ext && !mime) return "text";
  return null;
}

/** The picker's `accept` attribute. Images only appear when the model can see. */
export function acceptAttr(withImages: boolean): string {
  const docs = [...TEXT_EXTS, "pdf", "docx", ...AUDIO_EXTS];
  const all = withImages ? [...IMAGE_EXTS, ...docs] : docs;
  return all.map((e) => `.${e}`).join(",");
}

/**
 * Cheap token estimate. ~4 chars/token is the usual English/code rule of thumb;
 * it only has to be right enough to catch "this is a 200-page PDF".
 */
export function estimateTokens(text: string): number {
  return Math.ceil(text.length / 4);
}

/** 0 = unknown context size, i.e. no budget can be enforced. */
export function docBudgetTokens(ctx: number): number {
  return ctx > 0 ? Math.floor(ctx * DOC_CTX_FRACTION) : 0;
}

function fmtTok(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

/**
 * Reject-with-numbers: the user is told the size of the thing and the size of
 * the window, not just "too large". `used` is what earlier attachments on the
 * same message already claimed.
 */
export function oversizeError(name: string, tokens: number, ctx: number, used = 0): string | null {
  const budget = docBudgetTokens(ctx);
  if (budget <= 0) return null; // context size unknown — don't invent a limit
  if (tokens > budget) {
    return `"${name}" is about ${fmtTok(tokens)} tokens; this model's context is ${fmtTok(ctx)}, so an attachment can use at most ${fmtTok(budget)}. Pick a higher-context variant of the model, or attach a smaller file.`;
  }
  if (used + tokens > budget) {
    return `"${name}" (${fmtTok(tokens)} tokens) doesn't fit: the files already attached use ${fmtTok(used)} of the ${fmtTok(budget)}-token attachment budget for this model.`;
  }
  return null;
}

// --- message wire format ----------------------------------------------------
//
// A document is inlined into the user's message as a <file> block. The chat
// transcript IS the storage: nothing extra is persisted, replays and
// compaction see the same text the model saw, and an old chat opened after this
// feature changes still renders, because ChatMessage parses the block back out
// of the plain string it already has.

const FILE_OPEN = /<file name="([^"]*)"(?: note="([^"]*)")?>\n/;

function escapeAttr(s: string): string {
  return s.replace(/[<>"&]/g, (c) => ({ "<": "&lt;", ">": "&gt;", '"': "&quot;", "&": "&amp;" })[c]!);
}

function unescapeAttr(s: string): string {
  return s
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&");
}

export function buildFileBlock(name: string, text: string, note?: string): string {
  // A document that itself contains the closing tag would truncate the block and
  // let the rest of the file read as the user's own words.
  const body = text.replace(/<\/file>/g, "<\\/file>");
  const attrs = `name="${escapeAttr(name)}"${note ? ` note="${escapeAttr(note)}"` : ""}`;
  return `<file ${attrs}>\n${body}\n</file>`;
}

export interface ParsedFile {
  name: string;
  note: string;
  text: string;
}

/**
 * Pull the <file> blocks back out of a user message so the bubble can render
 * them as chips instead of dumping 40k characters into the thread.
 */
export function splitFileBlocks(content: string): { files: ParsedFile[]; rest: string } {
  const files: ParsedFile[] = [];
  let rest = "";
  let s = content;
  for (;;) {
    const open = FILE_OPEN.exec(s);
    if (!open) break;
    const bodyStart = open.index + open[0].length;
    const end = s.indexOf("\n</file>", bodyStart);
    if (end < 0) break; // unterminated — leave it as prose rather than eat the tail
    rest += s.slice(0, open.index);
    files.push({
      name: unescapeAttr(open[1]),
      note: unescapeAttr(open[2] ?? ""),
      text: s.slice(bodyStart, end).replace(/<\\\/file>/g, "</file>"),
    });
    s = s.slice(end + "\n</file>".length);
  }
  rest += s;
  return { files, rest: rest.trim() };
}

// --- extraction -------------------------------------------------------------

export async function readTextFile(file: File): Promise<string> {
  const text = await file.text();
  // A binary file that slipped through classification (unknown extension, empty
  // MIME) decodes to replacement characters. Attaching that wastes the whole
  // budget on garbage, so say what happened instead.
  const sample = text.slice(0, 4096);
  const bad = (sample.match(/�| /g) ?? []).length;
  if (sample.length > 0 && bad / sample.length > 0.05) {
    throw new Error(`"${file.name}" doesn't look like text.`);
  }
  return text;
}

export async function extractPdfText(file: File): Promise<{ text: string; pages: number }> {
  const pdfjs = await import("pdfjs-dist");
  // Bundled locally, never a CDN: the playground has to work offline.
  const worker = await import("pdfjs-dist/build/pdf.worker.min.mjs?url");
  pdfjs.GlobalWorkerOptions.workerSrc = worker.default;

  // Keep the loading task: it owns the worker, and destroying the task is what
  // actually tears it down (the document proxy has no destroy of its own).
  const task = pdfjs.getDocument({ data: new Uint8Array(await file.arrayBuffer()) });
  const doc = await task.promise;
  try {
    const parts: string[] = [];
    for (let p = 1; p <= doc.numPages; p++) {
      const page = await doc.getPage(p);
      const content = await page.getTextContent();
      let line = "";
      const lines: string[] = [];
      for (const item of content.items) {
        if (!("str" in item)) continue;
        line += item.str;
        if (item.hasEOL) {
          lines.push(line);
          line = "";
        }
      }
      if (line) lines.push(line);
      parts.push(lines.join("\n").trim());
      page.cleanup();
    }
    const text = parts.join("\n\n").trim();
    if (text.length < 16) {
      throw new Error(
        `"${file.name}" has no extractable text; it is probably a scan. Text extraction is not OCR.`
      );
    }
    return { text, pages: doc.numPages };
  } finally {
    await task.destroy();
  }
}

// --- docx: a zip of XML -----------------------------------------------------

async function inflateRaw(bytes: Uint8Array): Promise<Uint8Array> {
  if (typeof DecompressionStream === "undefined") {
    throw new Error("This browser can't unpack .docx files (no DecompressionStream).");
  }
  const stream = new Blob([bytes as BlobPart]).stream().pipeThrough(new DecompressionStream("deflate-raw"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

/**
 * Read ONE entry out of a zip via its central directory. Enough for a docx
 * (deflate or stored, no zip64); anything else throws rather than guessing.
 */
export async function readZipEntry(buf: ArrayBuffer, path: string): Promise<Uint8Array | null> {
  const view = new DataView(buf);
  const bytes = new Uint8Array(buf);
  // End of central directory: fixed 22 bytes plus a comment of up to 64k.
  let eocd = -1;
  for (let i = buf.byteLength - 22; i >= Math.max(0, buf.byteLength - 22 - 65535); i--) {
    if (view.getUint32(i, true) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) throw new Error("Not a zip file (no end-of-central-directory record).");

  const count = view.getUint16(eocd + 10, true);
  let p = view.getUint32(eocd + 16, true);
  const dec = new TextDecoder();
  for (let i = 0; i < count; i++) {
    if (view.getUint32(p, true) !== 0x02014b50) throw new Error("Corrupt zip central directory.");
    const method = view.getUint16(p + 10, true);
    const compSize = view.getUint32(p + 20, true);
    const nameLen = view.getUint16(p + 28, true);
    const extraLen = view.getUint16(p + 30, true);
    const commentLen = view.getUint16(p + 32, true);
    const localOff = view.getUint32(p + 42, true);
    const name = dec.decode(bytes.subarray(p + 46, p + 46 + nameLen));
    if (name === path) {
      // The local header repeats the name and carries its OWN extra field, whose
      // length differs from the central one — computing the data offset from the
      // central extra length is the classic way to read garbage here.
      if (view.getUint32(localOff, true) !== 0x04034b50) throw new Error("Corrupt zip entry header.");
      const lNameLen = view.getUint16(localOff + 26, true);
      const lExtraLen = view.getUint16(localOff + 28, true);
      const start = localOff + 30 + lNameLen + lExtraLen;
      const data = bytes.subarray(start, start + compSize);
      if (method === 0) return data;
      if (method === 8) return await inflateRaw(data);
      throw new Error(`Unsupported zip compression method ${method}.`);
    }
    p += 46 + nameLen + extraLen + commentLen;
  }
  return null;
}

export function decodeXmlEntities(s: string): string {
  return s
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, d: string) => String.fromCodePoint(Number(d)))
    .replace(/&#x([0-9a-fA-F]+);/g, (_, h: string) => String.fromCodePoint(parseInt(h, 16)))
    .replace(/&amp;/g, "&");
}

/**
 * WordprocessingML → plain text. One paragraph (`w:p`) per line; runs (`w:t`)
 * concatenated. Deliberately shallow: tables come out as their cell text, which
 * is what a model needs, and chasing full fidelity here is how this turns into a
 * dependency.
 */
export function docxXmlToText(xml: string): string {
  const paras = xml.split(/<\/w:p>/);
  const out: string[] = [];
  for (const para of paras) {
    let line = "";
    const runs = para.matchAll(/<w:(t|tab|br)(?:\s[^>]*)?(\/?)>/g);
    for (const m of runs) {
      const tag = m[1];
      if (tag === "tab") {
        line += "\t";
        continue;
      }
      if (tag === "br") {
        line += "\n";
        continue;
      }
      if (m[2] === "/") continue; // <w:t/> — empty run
      const from = m.index + m[0].length;
      const close = para.indexOf("</w:t>", from);
      if (close < 0) continue;
      line += decodeXmlEntities(para.slice(from, close));
    }
    line = line.trim();
    if (line) out.push(line);
  }
  return out.join("\n\n");
}

export async function extractDocxText(file: File): Promise<string> {
  const entry = await readZipEntry(await file.arrayBuffer(), "word/document.xml");
  if (!entry) throw new Error(`"${file.name}" is not a Word document (no word/document.xml).`);
  const text = docxXmlToText(new TextDecoder().decode(entry)).trim();
  if (!text) throw new Error(`"${file.name}" has no extractable text.`);
  return text;
}

// --- audio ------------------------------------------------------------------

/**
 * Pick the ASR model to transcribe an audio attachment with. Prefers one that
 * is already resident, because loading one evicts the chat model.
 */
export function pickTranscribeModel(models: Model[], preferred?: string): Model | undefined {
  const asr = models.filter((m) => !m.peerID && m.capabilities?.audio_transcriptions);
  if (preferred) {
    const want = asr.find((m) => m.id === preferred);
    if (want) return want;
  }
  return asr.find((m) => m.state === "ready") ?? asr.find((m) => !m.unlisted) ?? asr[0];
}
