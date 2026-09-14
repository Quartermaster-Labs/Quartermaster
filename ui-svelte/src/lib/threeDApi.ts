import { inferenceHeaders } from "./inferenceAuth";

// TRELLIS.2 image-to-mesh, served by trellis2-server on POST /v1/3d/generations.
//
// This route is shaped unlike anything else the playground calls, in two ways
// that decide the whole design of the 3D tab:
//
//   1. THE IMAGE IS THE BODY. There is no JSON document naming a model, so the
//      model id travels as ?model=<id> and every knob travels as a query
//      parameter (internal/server/server.go, modelPostRawRoutes). Content-Type
//      is the image's own type, which is how the backend decides how to decode
//      it.
//   2. IT IS SYNCHRONOUS. Unlike video (a job id in milliseconds, polled for
//      minutes) this request STAYS OPEN for the whole generation, roughly 90s at
//      the 512 profile plus the first-request weight load. There is no job API,
//      no queue position and no progress field, so the UI has an elapsed counter
//      and the backend's own log lines and nothing else.
//
// The backend is also SINGLE-THREADED: while a mesh is generating that process
// serves no other request, which is why the tab allows one generation at a time.

/** Per-request knobs. All of them are query parameters; all are optional. */
export interface ThreeDGenRequest {
  model: string;
  /** Sampling steps, both stages. Backend default 12. */
  steps?: number;
  /** Texture atlas resolution. Backend default 1024. */
  textureSize?: number;
  /** Coordinate resolution the model works at: 512 or 1024. */
  pipeline?: number;
  /** Geometry only, no texture bake. Much faster. */
  shapeOnly?: boolean;
  /** -1 (or undefined) leaves the backend to pick a random one. */
  seed?: number;
}

export interface ThreeDResult {
  /** The mesh as a data: URL, ready for the viewer and for the session store. */
  src: string;
  /** Size of the GLB in bytes, for the turn's footer. */
  bytes: number;
}

// friendlyThreeDError mirrors videoApi's: a 5xx from the gateway means the
// backend is gone rather than that the request was wrong, and 503 is the one
// status this backend gives a specific meaning to (weights still loading).
async function friendlyThreeDError(response: Response): Promise<string> {
  const text = await response.text().catch(() => "");
  let detail = text;
  try {
    const parsed = JSON.parse(text);
    if (typeof parsed?.error === "string") detail = parsed.error;
    else if (typeof parsed?.error?.message === "string") detail = parsed.error.message;
  } catch {
    // not JSON - keep the raw text
  }
  // "not ready" is the backend's own wording for an incomplete model directory,
  // and it names the missing file. Worth surfacing verbatim: it is a setup
  // problem the user can fix, not a transient failure to retry.
  if (response.status === 503 && detail.includes("not ready")) {
    return `Model not ready: ${detail.replace(/^.*not ready:\s*/, "")}`;
  }
  if (response.status === 502 || response.status === 503 || response.status === 504) {
    return "3D model unavailable - it crashed, was evicted, or is still loading. Try again in a moment.";
  }
  return `Mesh generation failed (${response.status})${detail ? `: ${detail}` : ""}`;
}

/**
 * Split a data: URL into the bytes and mime the route wants.
 *
 * The whole request body is the image, so unlike the /sdapi routes there is no
 * base64 field to fill: the bytes go on the wire raw and the mime becomes the
 * Content-Type header.
 */
async function imageBody(url: string): Promise<{ blob: Blob; mime: string }> {
  // Works for both shapes a turn can hold: a fresh pick is a data: URL, and one
  // reloaded from disk is a same-origin /api/media/ path (extractMedia rewrote
  // it on the first PUT). fetch() handles both, so no special-casing.
  const res = await fetch(url);
  if (!res.ok) throw new Error("Could not read that image.");
  const blob = await res.blob();
  return { blob, mime: blob.type || "image/png" };
}

/**
 * Generate one mesh. Resolves with the GLB as a data: URL.
 *
 * Aborting the signal drops the RESPONSE, not the render: the backend has no
 * cancel route, so a stopped generation keeps running to completion and only
 * then frees the process. The caller should say so rather than implying the GPU
 * was freed.
 */
export async function generateMesh(
  imageUrl: string,
  req: ThreeDGenRequest,
  signal?: AbortSignal,
): Promise<ThreeDResult> {
  const { blob, mime } = await imageBody(imageUrl);

  const q = new URLSearchParams({ model: req.model });
  if (req.steps != null) q.set("steps", String(req.steps));
  if (req.textureSize != null) q.set("texture_size", String(req.textureSize));
  if (req.pipeline != null) q.set("pipeline", String(req.pipeline));
  if (req.shapeOnly) q.set("shape_only", "1");
  // -1 is the UI's "random", and the backend's random is what you get by NOT
  // sending the parameter at all. Sending -1 would be a literal seed.
  if (req.seed != null && req.seed >= 0) q.set("seed", String(req.seed));

  const response = await fetch(`/v1/3d/generations?${q}`, {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": mime }),
    body: blob,
    signal,
  });
  if (!response.ok) throw new Error(await friendlyThreeDError(response));

  const glb = await response.blob();
  if (!glb.size) throw new Error("The backend returned an empty mesh.");
  return { src: await blobToDataUrl(glb), bytes: glb.size };
}

// A GLB is tens of megabytes, so this is the one expensive step in the whole
// flow. Done anyway because a data: URL is what the session store and the
// server's extractMedia both understand: the alternative is a blob: URL, which
// is scoped to this document and would be a dead link the moment the thread is
// reloaded. The mime is forced rather than trusted: the response carries
// model/gltf-binary, but a proxy that rewrites it to octet-stream would leave
// media/other/<hash>.bin on disk instead of a .glb.
function blobToDataUrl(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => {
      const url = fr.result as string;
      resolve("data:model/gltf-binary;base64," + url.slice(url.indexOf(",") + 1));
    };
    fr.onerror = () => reject(new Error("Could not read the returned mesh."));
    fr.readAsDataURL(blob);
  });
}

/** Human size for a mesh, for the turn footer. */
export function fmtBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${Math.round(n / (1 << 10))} KB`;
  return `${n} B`;
}
