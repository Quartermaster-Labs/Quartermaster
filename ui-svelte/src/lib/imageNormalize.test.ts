import { describe, it, expect } from "vitest";
import { needsTranscode } from "./imageNormalize";

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
