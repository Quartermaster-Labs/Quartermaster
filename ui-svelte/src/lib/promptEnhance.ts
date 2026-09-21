import { inferenceHeaders } from "./inferenceAuth";
import type { PromptEnhancerInfo } from "./types";

// Prompt enhancement: hand the user's image prompt to a rewrite model (Qwen's
// PE-I2I / PE-T2I and friends) and get back a more precise one.
//
// The rewrite is deliberately NOT applied inside the image route. It runs here,
// on the client, so the result lands back in the prompt box where it can be
// read and edited before anything renders. A rewrite you cannot see is worse
// than no rewrite: these models fail by confidently inventing details, and on
// the image route that surfaces only as a picture that is subtly not what was
// asked for, with nothing to point at.
//
// The enhancer is a normal catalog model, so this request goes through the one
// router and swaps like any other. On a single-GPU box that means it evicts the
// image model and the image model swaps back in to render: slow, but correct,
// and the alternative (a second scheduler) is the thing the architecture
// forbids outright.

// A vision enhancer is shown the reference images alongside the instruction.
// More than this many is pointless: the edit target is the first one, the rest
// are style/identity refs, and every extra image costs a full mmproj encode.
const MAX_REF_IMAGES = 4;

export class EnhanceError extends Error {}

export interface EnhanceResult {
  prompt: string;
  // What was in the box before, so the caller can offer a one-click revert.
  original: string;
}

// enhancePrompt rewrites `prompt` through `enhancer`. `refImages` are data or
// http URLs; they are sent only when the enhancer is configured for vision, so
// a text-only rewriter is never handed an image its template cannot encode.
export async function enhancePrompt(
  enhancer: PromptEnhancerInfo,
  prompt: string,
  refImages: string[] = [],
  signal?: AbortSignal,
): Promise<EnhanceResult> {
  const original = prompt;
  const text = prompt.trim();
  if (!text) throw new EnhanceError("Nothing to enhance: write a prompt first.");

  const messages: { role: string; content: unknown }[] = [];
  if (enhancer.systemPrompt.trim()) {
    messages.push({ role: "system", content: enhancer.systemPrompt });
  }

  const refs = enhancer.vision ? refImages.filter(Boolean).slice(0, MAX_REF_IMAGES) : [];
  if (refs.length) {
    // Images first, instruction last: these models are trained on an
    // image-then-instruction ordering, and flipping it makes them describe the
    // picture instead of rewriting the request.
    messages.push({
      role: "user",
      content: [
        ...refs.map((url) => ({ type: "image_url", image_url: { url } })),
        { type: "text", text },
      ],
    });
  } else {
    messages.push({ role: "user", content: text });
  }

  let res: Response;
  try {
    res = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: inferenceHeaders({ "Content-Type": "application/json" }),
      body: JSON.stringify({
        model: enhancer.model,
        messages,
        stream: false,
        // A rewriter's job is precision, not variety. Its own sampler defaults
        // are tuned for chat, which is how the same prompt came back as three
        // different scenes on consecutive presses.
        temperature: 0.3,
        max_tokens: 1024,
        // These models emit the rewritten prompt directly. A <think> block just
        // eats the budget and can leave content empty, which reads as a silent
        // failure on a perfectly healthy model.
        chat_template_kwargs: { enable_thinking: false },
      }),
      signal,
    });
  } catch (e) {
    if (signal?.aborted) throw e;
    throw new EnhanceError(`Enhancer unreachable: ${e instanceof Error ? e.message : String(e)}`);
  }
  if (!res.ok) {
    throw new EnhanceError(`Enhance failed: ${res.status} ${(await res.text()).slice(0, 300)}`);
  }

  const json = await res.json();
  const out = json.choices?.[0]?.message?.content;
  const cleaned = typeof out === "string" ? cleanEnhanced(out) : "";
  if (!cleaned) throw new EnhanceError("The enhancer returned nothing usable.");
  return { prompt: cleaned, original };
}

// cleanEnhanced strips the wrappers a rewrite model adds around the prompt: a
// leaked reasoning block, a fenced code block, a "Here is the enhanced prompt:"
// preamble, and the surrounding quotes some of them insist on.
export function cleanEnhanced(raw: string): string {
  let s = raw.replace(/<think>[\s\S]*?<\/think>/gi, "").trim();

  // A fenced block: take its body. The fence is formatting, never content.
  const fence = s.match(/^```[a-z]*\n([\s\S]*?)\n?```$/i);
  if (fence) s = fence[1].trim();

  // A single leading label line ("Enhanced prompt:", "Rewritten:"), only when
  // there is something after it. Matching mid-text would eat a real prompt that
  // happens to start with a colon-terminated clause.
  s = s.replace(/^[^\n:]{0,40}prompt[^\n:]{0,20}:\s*/i, "").trim();

  // Balanced wrapping quotes, but only when they wrap the WHOLE thing and the
  // same quote appears nowhere inside: a prompt containing a quoted sign
  // (a shop sign reading "OPEN") must come through untouched.
  const q = s[0];
  if (s.length > 1 && (q === '"' || q === "'") && s.endsWith(q) && !s.slice(1, -1).includes(q)) {
    s = s.slice(1, -1).trim();
  }
  return s;
}
