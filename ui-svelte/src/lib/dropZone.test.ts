import { describe, it, expect } from "vitest";
import { collectDropFiles, dragHasFiles } from "./dropZone";

function file(name: string): File {
  return new File(["x"], name, { type: "text/plain" });
}

/** Enough of a DataTransfer for the two pure helpers. */
function dt(opts: {
  types?: string[];
  items?: Array<{ kind: string; file?: File; dir?: string }>;
  files?: File[];
}): DataTransfer {
  const items = opts.items?.map((i) => ({
    kind: i.kind,
    getAsFile: () => i.file ?? null,
    webkitGetAsEntry: () => (i.dir ? { isDirectory: true, name: i.dir } : { isDirectory: false, name: i.file?.name }),
  }));
  return {
    types: opts.types ?? ["Files"],
    items: items as unknown as DataTransferItemList,
    files: (opts.files ?? []) as unknown as FileList,
  } as unknown as DataTransfer;
}

describe("dragHasFiles", () => {
  it("is true for an OS file drag", () => {
    expect(dragHasFiles({ dataTransfer: dt({ types: ["Files"] }) } as DragEvent)).toBe(true);
  });

  it("is false for a dragged text selection", () => {
    expect(dragHasFiles({ dataTransfer: dt({ types: ["text/plain", "text/html"] }) } as DragEvent)).toBe(false);
  });

  it("is false with no dataTransfer at all", () => {
    expect(dragHasFiles({ dataTransfer: null } as unknown as DragEvent)).toBe(false);
  });
});

describe("collectDropFiles", () => {
  it("takes the files out of items", () => {
    const a = file("a.md");
    const b = file("b.txt");
    const { files, skipped } = collectDropFiles(dt({ items: [{ kind: "file", file: a }, { kind: "file", file: b }] }));
    expect(files.map((f) => f.name)).toEqual(["a.md", "b.txt"]);
    expect(skipped).toEqual([]);
  });

  it("names a dropped folder instead of attaching it", () => {
    const a = file("a.md");
    const { files, skipped } = collectDropFiles(
      dt({ items: [{ kind: "file", dir: "notes" }, { kind: "file", file: a }] })
    );
    expect(files.map((f) => f.name)).toEqual(["a.md"]);
    expect(skipped).toEqual(["notes"]);
  });

  it("ignores non-file items (a dragged string rides along with the files)", () => {
    const a = file("a.md");
    const { files } = collectDropFiles(dt({ items: [{ kind: "string" }, { kind: "file", file: a }] }));
    expect(files.map((f) => f.name)).toEqual(["a.md"]);
  });

  it("falls back to dataTransfer.files when items carry no entry metadata", () => {
    const got = collectDropFiles({
      items: undefined,
      files: [file("a.md")] as unknown as FileList,
    } as unknown as DataTransfer);
    expect(got.files.map((f) => f.name)).toEqual(["a.md"]);
    expect(got.skipped).toEqual([]);
  });
});
