import type { SdApiTxt2ImgRequest, SdApiImg2ImgRequest, SdApiResponse, SdApiLora } from "./types";
import { inferenceHeaders } from "./inferenceAuth";

async function postSd(path: string, request: unknown, signal?: AbortSignal): Promise<SdApiResponse> {
  const response = await fetch(path, {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(request),
    signal,
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`SDAPI error: ${response.status} - ${errorText}`);
  }

  return response.json();
}

export function generateSdImage(request: SdApiTxt2ImgRequest, signal?: AbortSignal): Promise<SdApiResponse> {
  return postSd("/sdapi/v1/txt2img", request, signal);
}

export function generateSdImg2Img(request: SdApiImg2ImgRequest, signal?: AbortSignal): Promise<SdApiResponse> {
  return postSd("/sdapi/v1/img2img", request, signal);
}

export async function fetchSdLoras(
  model: string,
  signal?: AbortSignal
): Promise<SdApiLora[]> {
  const response = await fetch(
    `/sdapi/v1/loras?model=${encodeURIComponent(model)}`,
    { headers: inferenceHeaders(), signal }
  );

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`SDAPI loras error: ${response.status} - ${errorText}`);
  }

  return response.json();
}
