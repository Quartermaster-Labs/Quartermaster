// Luminance-only tone match: anchor an image's brightness + contrast to a
// reference WITHOUT touching hue/chroma. Used by the image tab to cancel the
// exposure drift that accumulates when a follow-up edit re-encodes the previous
// output — each VAE round-trip nudges brightness/contrast and over several edits
// it blows out.
//
// Why luminance-only: an earlier version matched full RGB mean+std, which forced
// the reference's whole PALETTE onto the edit — a green-heavy origin tinted every
// edit green. Blowout is a LUMINANCE phenomenon, so we match only luma (a
// per-pixel offset applied equally to R/G/B keeps channel differences — i.e. hue
// and saturation — intact) and leave color alone.

export interface LumaStats {
  mean: number;
  std: number;
}

const luma = (r: number, g: number, b: number) => 0.299 * r + 0.587 * g + 0.114 * b;

// Mean + std of luminance over the buffer. std floored so a flat image can't blow
// up the contrast divisor.
export function lumaStats(data: Uint8ClampedArray | number[]): LumaStats {
  let sum = 0;
  let sq = 0;
  const n = data.length / 4;
  for (let i = 0; i < data.length; i += 4) {
    const l = luma(data[i], data[i + 1], data[i + 2]);
    sum += l;
    sq += l * l;
  }
  const mean = sum / n;
  const std = Math.sqrt(Math.max(sq / n - mean * mean, 1e-6));
  return { mean, std };
}

// In-place: scale each pixel's luma contrast toward the reference and shift its
// brightness, as ONE offset added to all three channels — so hue/saturation are
// preserved. off = (L - srcMean)*(refStd/srcStd - 1) + (refMean - srcMean).
// matchContrast=false pins scale to 1 → brightness-only (mean) match, no contrast
// stretch: gentler and can't blow out when src std << ref std.
export function applyLumaMatch(
  data: Uint8ClampedArray | number[],
  src: LumaStats,
  ref: LumaStats,
  matchContrast = true,
): void {
  const scale = matchContrast ? ref.std / src.std : 1;
  for (let i = 0; i < data.length; i += 4) {
    const l = luma(data[i], data[i + 1], data[i + 2]);
    const off = (l - src.mean) * (scale - 1) + (ref.mean - src.mean);
    for (let c = 0; c < 3; c++) {
      const v = data[i + c] + off;
      data[i + c] = v < 0 ? 0 : v > 255 ? 255 : v;
    }
  }
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = reject;
    img.src = url;
  });
}

function toImageData(img: HTMLImageElement): ImageData {
  const c = document.createElement("canvas");
  c.width = img.naturalWidth;
  c.height = img.naturalHeight;
  const ctx = c.getContext("2d");
  if (!ctx) throw new Error("no 2d context");
  ctx.drawImage(img, 0, 0);
  return ctx.getImageData(0, 0, c.width, c.height);
}

// Match srcUrl's exposure (brightness/contrast) to refUrl, keeping its colors.
// Both data URLs; returns a PNG data URL.
export async function matchColorToRef(srcUrl: string, refUrl: string, matchContrast = true): Promise<string> {
  const [srcImg, refImg] = await Promise.all([loadImage(srcUrl), loadImage(refUrl)]);
  const srcData = toImageData(srcImg);
  const refStats = lumaStats(toImageData(refImg).data);
  const srcStats = lumaStats(srcData.data);
  applyLumaMatch(srcData.data, srcStats, refStats, matchContrast);

  const c = document.createElement("canvas");
  c.width = srcData.width;
  c.height = srcData.height;
  const ctx = c.getContext("2d");
  if (!ctx) throw new Error("no 2d context");
  ctx.putImageData(srcData, 0, 0);
  return c.toDataURL("image/png");
}
