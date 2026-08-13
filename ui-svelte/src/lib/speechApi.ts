import type { SpeechGenerationRequest } from "./types";
import { inferenceHeaders } from "./inferenceAuth";
import { safeVoice } from "./voices";

export async function generateSpeech(
  model: string,
  input: string,
  voice: string,
  signal?: AbortSignal,
  instructions?: string
): Promise<Blob> {
  const request: SpeechGenerationRequest = {
    model,
    input,
    // One choke point for every caller (Speech tab, read-aloud, voice preview):
    // the voice pref is shared across models, and a name one engine knows can
    // ABORT the other's server process. See safeVoice.
    voice: safeVoice(model, voice),
    response_format: "wav",
    // voice_design models design a voice from this style description; the
    // tts-server maps OpenAI `instructions` → the ABI `instruct` field.
    ...(instructions ? { instructions } : {}),
  };

  const response = await fetch("/v1/audio/speech", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(request),
    signal,
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`Speech API error: ${response.status} - ${errorText}`);
  }

  return response.blob();
}
