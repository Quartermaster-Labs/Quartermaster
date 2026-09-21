// @vitest-environment jsdom
// FileReader (via toDataUrl) is a DOM API: node has Blob but not FileReader.
import { describe, it, expect, vi } from "vitest";
import { needsTranscode, resolveImageDataUrl } from "./imageNormalize";

// The gate that decides whether an attachment reaches a vision model as bytes
// stb_image can parse. PNG/JPEG pass through; everything the browser decodes
// but stb does not must be caught HERE, because the backend's only answer is a
// 400 that names neither the file nor the format.
describe("needsTranscode", () => {
  it("passes PNG and JPEG through", () => {
    expect(needsTranscode("image/png")).toBe(false);
    expect(needsTranscode("image/jpeg")).toBe(false);
    // A file input hands back the type verbatim, casing and all.
    expect(needsTranscode("IMAGE/PNG")).toBe(false);
    expect(needsTranscode(" image/jpeg ")).toBe(false);
  });

  it("converts the formats stb_image cannot read", () => {
    expect(needsTranscode("image/webp")).toBe(true);
    expect(needsTranscode("image/avif")).toBe(true);
    expect(needsTranscode("image/heic")).toBe(true);
  });

  it("converts an unknown or missing type rather than trusting it", () => {
    expect(needsTranscode("")).toBe(true);
    expect(needsTranscode("application/octet-stream")).toBe(true);
  });
});

// The bug this file was written for: a synced session holds
// "/api/media/image/<hash>.png" where a fresh one holds a data URL. They render
// identically in an <img>, and llama.cpp answers the ref with
// "Failed to load image or audio file" because it decodes the string rather
// than fetching it.
describe("resolveImageDataUrl", () => {
  it("passes a data URL through without a fetch", async () => {
    const spy = vi.fn();
    vi.stubGlobal("fetch", spy);
    const url = "data:image/png;base64,iVBORw0KGgo=";
    expect(await resolveImageDataUrl(url)).toBe(url);
    expect(spy).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("fetches a media ref and inlines it", async () => {
    vi.stubGlobal("fetch", async () => ({
      ok: true,
      blob: async () => new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
    }));
    const out = await resolveImageDataUrl("/api/media/image/eb0a7e4c945864c7.png");
    expect(out.startsWith("data:image/png;base64,")).toBe(true);
    vi.unstubAllGlobals();
  });

  it("throws with the ref in the message when it cannot be loaded", async () => {
    vi.stubGlobal("fetch", async () => ({ ok: false, status: 404 }));
    await expect(resolveImageDataUrl("/api/media/image/gone.png")).rejects.toThrow(/gone\.png: 404/);
    vi.unstubAllGlobals();
  });
});
