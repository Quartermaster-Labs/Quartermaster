import { inferenceHeaders } from "./inferenceAuth";

// A box prompt in image pixel coords (top-left, bottom-right).
export type SamBox = [number, number, number, number]; // x0, y0, x1, y1
// A point prompt: [x, y, label], label 1 = foreground, 0 = background.
export type SamPoint = [number, number, 0 | 1];

interface SamMask {
  instance_id: number;
  score: number;
  iou_score: number;
  box: SamBox;
  png: string; // base64 PNG, grayscale — object = 255 (white), else 0 (black)
}

interface SamResponse {
  width: number;
  height: number;
  masks: SamMask[];
}

// toBase64 resolves any image source to bare base64 (sam3_server wants raw
// base64, no "data:" prefix). Handles data URLs directly; a URL/path (e.g. the
// playground's "/api/media/<file>.png" refs) is fetched and read as base64.
async function toBase64(src: string): Promise<string> {
  if (src.startsWith("data:")) {
    const i = src.indexOf(",");
    return i >= 0 ? src.slice(i + 1) : src;
  }
  const blob = await fetch(src, { headers: inferenceHeaders() }).then((r) => {
    if (!r.ok) throw new Error(`could not load image (${r.status})`);
    return r.blob();
  });
  const dataUrl: string = await new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => resolve(fr.result as string);
    fr.onerror = () => reject(fr.error);
    fr.readAsDataURL(blob);
  });
  return dataUrl.slice(dataUrl.indexOf(",") + 1);
}

// segment runs SAM (sam3_server, POST /v1/segment) over an image with a text,
// box, and/or point prompt and returns a mask as a PNG data URL — white object
// on black, the exact shape the inpaint path already wants. Box/point are
// single-object, so the best (highest-score) mask is returned; a text prompt is
// a concept that can match several instances, so all masks are unioned into one.
// Returns null when the model finds nothing. The image is re-encoded per call
// (the server encodes+segments in one locked round), so keep prompt count sane.
export async function segment(
  model: string,
  imageDataUrl: string,
  prompt: { text?: string; box?: SamBox; points?: SamPoint[]; multimask?: boolean },
  signal?: AbortSignal,
): Promise<string | null> {
  const body: Record<string, unknown> = { model, image: await toBase64(imageDataUrl) };
  const text = prompt.text?.trim();
  if (text) {
    body.text = text; // PCS path; box/points ignored server-side
  } else {
    if (prompt.box) body.box = prompt.box;
    if (prompt.points?.length) body.points = prompt.points;
    if (prompt.multimask) body.multimask = true;
  }

  const res = await fetch("/v1/segment", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) {
    if (res.status === 502 || res.status === 503 || res.status === 504) {
      throw new Error(
        "Segment model unavailable - it crashed, was evicted, or is still loading. Try again in a moment.",
      );
    }
    const t = await res.text().catch(() => "");
    throw new Error(`Segmentation failed (${res.status})${t ? `: ${t}` : ""}`);
  }
  const data = (await res.json()) as SamResponse;
  if (!data.masks?.length) return null;
  if (text && data.masks.length > 1) return unionMasks(data.masks, data.width, data.height);
  const best = data.masks.reduce((a, b) => (b.score > a.score ? b : a));
  return "data:image/png;base64," + best.png;
}

// unionMasks composites every instance mask (white-on-black) onto one canvas with
// "lighter" so the whites accumulate — the merged concept region for inpaint.
async function unionMasks(masks: SamMask[], w: number, h: number): Promise<string> {
  const cv = document.createElement("canvas");
  cv.width = w;
  cv.height = h;
  const ctx = cv.getContext("2d")!;
  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, w, h);
  ctx.globalCompositeOperation = "lighter";
  for (const m of masks) {
    if (!m.png) continue;
    const img = await loadPng("data:image/png;base64," + m.png);
    ctx.drawImage(img, 0, 0, w, h);
  }
  return cv.toDataURL("image/png");
}

function loadPng(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const im = new Image();
    im.onload = () => resolve(im);
    im.onerror = () => reject(new Error("mask decode failed"));
    im.src = src;
  });
}
