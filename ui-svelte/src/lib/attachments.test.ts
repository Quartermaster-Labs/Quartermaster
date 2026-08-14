import { describe, it, expect } from "vitest";
import {
  classifyAttachment,
  acceptAttr,
  estimateTokens,
  docBudgetTokens,
  oversizeError,
  buildFileBlock,
  splitFileBlocks,
  docxXmlToText,
  decodeXmlEntities,
  readZipEntry,
  pickTranscribeModel,
  extOf,
} from "./attachments";
import type { Model } from "./types";

function f(name: string, type = ""): File {
  return new File(["x"], name, { type });
}

describe("classifyAttachment", () => {
  it("routes by extension", () => {
    expect(classifyAttachment(f("a.png"))).toBe("image");
    expect(classifyAttachment(f("a.PDF"))).toBe("pdf");
    expect(classifyAttachment(f("notes.docx"))).toBe("docx");
    expect(classifyAttachment(f("rec.m4a"))).toBe("audio");
    expect(classifyAttachment(f("main.go"))).toBe("text");
  });

  it("ignores an empty MIME type on known code extensions (Windows)", () => {
    expect(classifyAttachment(f("app.svelte", ""))).toBe("text");
    expect(classifyAttachment(f("conf.yaml", ""))).toBe("text");
  });

  it("falls back to the MIME type for unknown extensions", () => {
    expect(classifyAttachment(f("blob.bin", "text/plain"))).toBe("text");
    expect(classifyAttachment(f("scan.xyz", "application/pdf"))).toBe("pdf");
    expect(classifyAttachment(f("clip.xyz", "video/webm"))).toBe("audio");
  });

  it("accepts extensionless files and rejects unknown binaries", () => {
    expect(classifyAttachment(f("Dockerfile"))).toBe("text");
    expect(classifyAttachment(f("thing.exe", "application/octet-stream"))).toBeNull();
  });

  it("only offers image extensions when the model can see", () => {
    expect(acceptAttr(true)).toContain(".png");
    expect(acceptAttr(false)).not.toContain(".png");
    expect(acceptAttr(false)).toContain(".pdf");
  });

  it("extOf handles dotless and multi-dot names", () => {
    expect(extOf("Makefile")).toBe("");
    expect(extOf("a.tar.gz")).toBe("gz");
  });
});

describe("size budget", () => {
  it("estimates ~4 chars per token", () => {
    expect(estimateTokens("")).toBe(0);
    expect(estimateTokens("a".repeat(400))).toBe(100);
  });

  it("budgets a fraction of the window, and nothing when the window is unknown", () => {
    expect(docBudgetTokens(32768)).toBe(13107);
    expect(docBudgetTokens(0)).toBe(0);
  });

  it("rejects a file bigger than the budget, naming both numbers", () => {
    const msg = oversizeError("book.pdf", 150000, 32768);
    expect(msg).toContain("book.pdf");
    expect(msg).toContain("150k");
    expect(msg).toContain("33k");
  });

  it("rejects a file that only overflows together with earlier ones", () => {
    expect(oversizeError("b.md", 5000, 32768, 0)).toBeNull();
    expect(oversizeError("b.md", 5000, 32768, 10000)).toContain("already attached");
  });

  it("enforces no limit when the context size is unknown", () => {
    expect(oversizeError("book.pdf", 999999, 0)).toBeNull();
  });
});

describe("file blocks", () => {
  it("round-trips a document through the message text", () => {
    const msg = `${buildFileBlock("spec.md", "hello\nworld", "2 pages")}\n\nsummarize this`;
    const { files, rest } = splitFileBlocks(msg);
    expect(files).toEqual([{ name: "spec.md", note: "2 pages", text: "hello\nworld" }]);
    expect(rest).toBe("summarize this");
  });

  it("round-trips several documents", () => {
    const msg = [buildFileBlock("a.txt", "A"), buildFileBlock("b.txt", "B"), "compare"].join("\n\n");
    const { files, rest } = splitFileBlocks(msg);
    expect(files.map((x) => x.name)).toEqual(["a.txt", "b.txt"]);
    expect(files.map((x) => x.text)).toEqual(["A", "B"]);
    expect(rest).toBe("compare");
  });

  it("escapes a name with quotes and a body containing the closing tag", () => {
    const block = buildFileBlock('we"ird.txt', "before\n</file>\nafter");
    const { files, rest } = splitFileBlocks(block);
    expect(rest).toBe("");
    expect(files[0].name).toBe('we"ird.txt');
    expect(files[0].text).toBe("before\n</file>\nafter");
  });

  it("leaves an unterminated block as prose instead of eating the tail", () => {
    const { files, rest } = splitFileBlocks('<file name="a.txt">\nhalf written');
    expect(files).toEqual([]);
    expect(rest).toContain("half written");
  });

  it("passes an ordinary message through untouched", () => {
    const { files, rest } = splitFileBlocks("what is <file> in html?");
    expect(files).toEqual([]);
    expect(rest).toBe("what is <file> in html?");
  });
});

describe("docx", () => {
  it("decodes xml entities including numeric ones", () => {
    expect(decodeXmlEntities("a &amp;lt; b")).toBe("a &lt; b");
    expect(decodeXmlEntities("&#65;&#x42;&quot;&apos;")).toBe(`AB"'`);
  });

  it("turns paragraphs into lines and joins runs", () => {
    const xml =
      "<w:body><w:p><w:r><w:t>Hello </w:t></w:r><w:r><w:t xml:space=\"preserve\">world</w:t></w:r></w:p>" +
      "<w:p><w:r><w:t>Second</w:t><w:tab/><w:t>col</w:t></w:r></w:p></w:body>";
    expect(docxXmlToText(xml)).toBe("Hello world\n\nSecond\tcol");
  });

  it("drops empty paragraphs and self-closing runs", () => {
    expect(docxXmlToText("<w:p></w:p><w:p><w:t/></w:p><w:p><w:t>x</w:t></w:p>")).toBe("x");
  });
});

// Hand-built zip so the central-directory walk is exercised without a fixture
// binary. The local header carries a LONGER extra field than the central one —
// deriving the data offset from the central extra length is the classic way to
// read garbage out of a real docx.
function storedZip(name: string, body: string): ArrayBuffer {
  const enc = new TextEncoder();
  const nameB = enc.encode(name);
  const dataB = enc.encode(body);
  const localExtra = new Uint8Array(4);
  const local = new Uint8Array(30 + nameB.length + localExtra.length + dataB.length);
  const lv = new DataView(local.buffer);
  lv.setUint32(0, 0x04034b50, true);
  lv.setUint32(18, dataB.length, true); // compressed size
  lv.setUint32(22, dataB.length, true); // uncompressed size
  lv.setUint16(26, nameB.length, true);
  lv.setUint16(28, localExtra.length, true);
  local.set(nameB, 30);
  local.set(localExtra, 30 + nameB.length);
  local.set(dataB, 30 + nameB.length + localExtra.length);

  const central = new Uint8Array(46 + nameB.length);
  const cv = new DataView(central.buffer);
  cv.setUint32(0, 0x02014b50, true);
  cv.setUint32(20, dataB.length, true);
  cv.setUint32(24, dataB.length, true);
  cv.setUint16(28, nameB.length, true);
  cv.setUint32(42, 0, true); // local header offset
  central.set(nameB, 46);

  const eocd = new Uint8Array(22);
  const ev = new DataView(eocd.buffer);
  ev.setUint32(0, 0x06054b50, true);
  ev.setUint16(8, 1, true);
  ev.setUint16(10, 1, true);
  ev.setUint32(12, central.length, true);
  ev.setUint32(16, local.length, true);

  const out = new Uint8Array(local.length + central.length + eocd.length);
  out.set(local, 0);
  out.set(central, local.length);
  out.set(eocd, local.length + central.length);
  return out.buffer;
}

describe("readZipEntry", () => {
  it("reads a stored entry using the LOCAL header's extra length", async () => {
    const zip = storedZip("word/document.xml", "<w:p><w:t>hi</w:t></w:p>");
    const entry = await readZipEntry(zip, "word/document.xml");
    expect(new TextDecoder().decode(entry!)).toBe("<w:p><w:t>hi</w:t></w:p>");
  });

  it("returns null for a missing entry and throws on a non-zip", async () => {
    const zip = storedZip("word/document.xml", "x");
    expect(await readZipEntry(zip, "word/other.xml")).toBeNull();
    const junk = new Uint8Array(64).buffer;
    await expect(readZipEntry(junk, "a")).rejects.toThrow();
  });
});

describe("pickTranscribeModel", () => {
  const m = (id: string, state: string, extra: Partial<Model> = {}): Model =>
    ({
      id,
      name: id,
      description: "",
      unlisted: false,
      peerID: "",
      state: state as Model["state"],
      capabilities: { audio_transcriptions: true },
      ...extra,
    }) as Model;

  it("prefers a resident model over an idle one", () => {
    const models = [m("whisper-large", "stopped"), m("whisper-small", "ready")];
    expect(pickTranscribeModel(models)?.id).toBe("whisper-small");
  });

  it("honours an explicit pick, and ignores peers and non-ASR models", () => {
    const models = [
      m("whisper-small", "ready"),
      m("whisper-large", "stopped"),
      m("remote", "ready", { peerID: "peer1" }),
      m("qwen", "ready", { capabilities: {} }),
    ];
    expect(pickTranscribeModel(models, "whisper-large")?.id).toBe("whisper-large");
    expect(pickTranscribeModel(models, "nope")?.id).toBe("whisper-small");
    expect(pickTranscribeModel([m("remote", "ready", { peerID: "p" })])).toBeUndefined();
  });
});
