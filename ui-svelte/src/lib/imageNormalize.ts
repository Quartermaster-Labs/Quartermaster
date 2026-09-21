// Attachment intake normalization.
//
// The browser and the backends do NOT agree on what an image is. `accept="image/*"`
// and a drag-and-drop both hand us whatever the OS calls an image, and Chromium
// decodes WebP, AVIF and HEIC without blinking, so the thumbnail renders and
// everything looks fine. llama.cpp decodes an attached image with **stb_image**,
// whose format list is PNG / JPEG / BMP / GIF / PSD / PIC / PNM / TGA and stops
// there. A WebP reference image therefore reaches a vision model as bytes it
// cannot parse and comes back as
//
//	400 {"error":{"message":"Failed to load image or audio file"}}
//
// in a couple of milliseconds, naming neither the file nor the format. Since
// WebP is what a browser's "Save image as" hands you for a large share of the
// web, this is reachable by doing the obvious thing.
//
// So: re-encode anything outside the safe list through a canvas at intake. The
// browser has already decoded it to show the thumbnail, so the cost is one draw,
// and normalizing HERE rather than in the enhancer means every consumer of an
// attachment (llama.cpp, sd-server img2img, the mask editor) gets bytes it can
// read.

// PNG and JPEG pass through untouched: they are universally decodable, and a
// needless re-encode would throw away JPEG quality and inflate the data URL that
// the whole payload is carried in. Every other type, INCLUDING the ones stb can
// read (BMP, GIF, TGA), is converted: they are rare enough that uniform output
// is worth more than skipping a draw, and a GIF flattened to its first frame is
// what a still-image consumer wanted anyway.
const PASSTHROUGH = new Set(["image/png", "image/jpeg"]);

// PNG, not JPEG: an attachment may carry alpha (a cut-out subject, a logo), and
// flattening it onto an invented background would change what the model is
// shown. Costs size on a photo; correctness wins over bytes on a local socket.
const TARGET = "image/png";

export function needsTranscode(type: string): boolean {
  return !PASSTHROUGH.has(type.trim().toLowerCase());
}

// toDataUrl reads a file as-is, which is the fast path and the fallback.
function toDataUrl(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as string);
    reader.onerror = () => reject(reader.error ?? new Error("could not read file"));
    reader.readAsDataURL(file);
  });
}

// normalizeImageFile returns a data URL every image consumer can decode.
//
// Never rejects: a transcode that fails falls back to the original bytes. The
// browser refusing to decode its own attachment is a case where passing the file
// through unchanged at least lets the backend produce the real error, which is
// strictly better than dropping the user's image on the floor here.
export async function normalizeImageFile(file: File): Promise<string> {
  if (!needsTranscode(file.type)) return toDataUrl(file);
  try {
    const url = await transcode(file);
    if (url) return url;
  } catch {
    /* fall through to the original bytes */
  }
  return toDataUrl(file);
}

async function transcode(file: Blob): Promise<string> {
  const bmp = await loadBitmap(file);
  const canvas = document.createElement("canvas");
  canvas.width = bmp.width;
  canvas.height = bmp.height;
  const ctx = canvas.getContext("2d");
  if (!ctx) return "";
  ctx.drawImage(bmp as CanvasImageSource, 0, 0);
  if ("close" in bmp && typeof bmp.close === "function") bmp.close();
  return canvas.toDataURL(TARGET);
}

// createImageBitmap is the direct route and is what decodes off the main thread,
// but it is absent in older WebView2 builds and jsdom, so an <img> element is
// kept as the fallback: it goes through the same codec set.
async function loadBitmap(file: Blob): Promise<ImageBitmap | HTMLImageElement> {
  if (typeof createImageBitmap === "function") {
    return createImageBitmap(file);
  }
  const url = URL.createObjectURL(file);
  try {
    return await new Promise<HTMLImageElement>((resolve, reject) => {
      const img = new Image();
      img.onload = () => resolve(img);
      img.onerror = () => reject(new Error("decode failed"));
      img.src = url;
    });
  } finally {
    URL.revokeObjectURL(url);
  }
}
