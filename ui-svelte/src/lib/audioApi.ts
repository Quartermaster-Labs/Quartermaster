import type { AudioTranscriptionResponse } from "./types";
import { inferenceHeaders } from "./inferenceAuth";

export async function transcribeAudio(
  model: string,
  file: Blob,
  signal?: AbortSignal,
  // Live-mic segments are bare Blobs; the backend keys off the extension, so a
  // filename has to be supplied when there isn't one.
  filename = "audio.wav"
): Promise<AudioTranscriptionResponse> {
  const formData = new FormData();
  formData.append("file", file, file instanceof File ? file.name : filename);
  formData.append("model", model);

  const response = await fetch("/v1/audio/transcriptions", {
    method: "POST",
    headers: inferenceHeaders(),
    body: formData,
    signal,
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`Audio API error: ${response.status} - ${errorText}`);
  }

  return response.json();
}
