import type { ImageGenerationRequest, ImageGenerationResponse } from "./types";
import { inferenceHeaders } from "./inferenceAuth";

export async function generateImage(
  model: string,
  prompt: string,
  size: string,
  signal?: AbortSignal
): Promise<ImageGenerationResponse> {
  const request: ImageGenerationRequest = {
    model,
    prompt,
    n: 1,
    size,
  };

  const response = await fetch("/v1/images/generations", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(request),
    signal,
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`Image API error: ${response.status} - ${errorText}`);
  }

  return response.json();
}

// Upscale a single image via the server's standalone ESRGAN runner
// (realesrgan-ncnn-vulkan, exec-per-request). Takes a data URL (or base64),
// returns a PNG data URL of the upscaled result. `model` is optional (server
// picks the first discovered upscaler when omitted).
export async function upscaleImage(
  image: string,
  scale = 4,
  model?: string,
  signal?: AbortSignal
): Promise<string> {
  const response = await fetch("/v1/images/upscale", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify({ image, scale, ...(model ? { model } : {}) }),
    signal,
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`Upscale error: ${response.status} - ${errorText}`);
  }

  const data = (await response.json()) as { image: string };
  return data.image;
}
