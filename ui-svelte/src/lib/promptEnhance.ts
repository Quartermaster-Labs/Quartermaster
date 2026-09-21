import { inferenceHeaders } from "./inferenceAuth";
import type { PromptEnhancerInfo } from "./types";
import { resolveImageDataUrl } from "./imageNormalize";

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
  // Aspect ratio the enhancer asked for ("16:9"), when it returned the
  // structured envelope. Advisory: the caller snaps it to a supported aspect.
  ratio?: string;
  // The enhancer asking to follow an INPUT image's ratio instead ("<image1>").
  // Mutually exclusive with `ratio` per Qwen's schema, and the reason the two
  // are separate fields rather than one nullable string.
  ratioFollow?: string;
}

// These models deliberate in plain prose for hundreds of tokens before emitting
// the answer, and the deliberation is NOT wrapped in <think>, so nothing can
// separate it until the answer actually arrives. A budget that cuts them off
// mid-thought yields a scratchpad with no prompt behind it, which reads as a
// broken model rather than a truncated response. 1024 was not enough for a
// single image plus a one-line instruction under Qwen's own PE system prompt.
const MAX_TOKENS = 4096;

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

  // Resolved HERE rather than at the call site because a caller cannot tell the
  // two apart by looking: a synced session holds "/api/media/image/<hash>.png"
  // where a fresh one holds a data URL, both render identically in an <img>,
  // and only the model can tell the difference (by refusing the ref). One choke
  // point is the only version of this that stays fixed.
  let refs: string[] = [];
  if (enhancer.vision) {
    const picked = refImages.filter(Boolean).slice(0, MAX_REF_IMAGES);
    try {
      refs = await Promise.all(picked.map(resolveImageDataUrl));
    } catch (e) {
      throw new EnhanceError(
        `Could not load the reference image to send it: ${e instanceof Error ? e.message : String(e)}`,
      );
    }
  }
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
        max_tokens: MAX_TOKENS,
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
    throw new EnhanceError(enhanceHttpError(res.status, await res.text()));
  }

  const json = await res.json();
  const choice = json.choices?.[0];
  const out = choice?.message?.content;
  // "length" means the model was still talking when the budget ran out. On its
  // own that is survivable, but combined with an unstructured result it means
  // the answer never arrived and what we have is scratchpad.
  const truncated = choice?.finish_reason === "length";
  const parsed = typeof out === "string" ? parseEnhanced(out) : { prompt: "", structured: false };
  if (!parsed.prompt || (truncated && !parsed.structured)) {
    throw new EnhanceError(
      truncated
        ? `The enhancer ran out of output budget (${MAX_TOKENS} tokens) before it finished, so it never produced a prompt. Shorten its system prompt, or send fewer reference images.`
        : "The enhancer returned nothing usable.",
    );
  }
  return { prompt: parsed.prompt, original, ratio: parsed.ratio, ratioFollow: parsed.ratioFollow };
}

// enhanceHttpError turns a backend error body into something a user can act on.
// The raw body is llama.cpp's JSON envelope, and pasting it into the UI made a
// fixable cause read as an opaque 400.
export function enhanceHttpError(status: number, body: string): string {
  if (/load image or audio file/i.test(body)) {
    // Now that refs are inlined before sending, the remaining way to earn this
    // is a format llama.cpp's stb_image cannot decode.
    return "The enhancer could not read the reference image. Save it as PNG or JPEG and re-attach it: the backend decodes with stb_image, which cannot read WebP, AVIF or HEIC.";
  }
  if (status === 404) {
    return "The enhancer model is not in the catalog any more. Regenerate the config, or clear the enhancer on this model.";
  }
  const detail = jsonMessage(body) || body.trim();
  return `Enhance failed: ${status}${detail ? ` ${detail.slice(0, 300)}` : ""}`;
}

// The body is llama.cpp's {"error":{"message":...}}, but a proxy or a panic can
// put anything here, so a parse failure falls back to the raw text.
function jsonMessage(body: string): string {
  try {
    const j = JSON.parse(body);
    const m = j?.error?.message ?? j?.message;
    return typeof m === "string" ? m : "";
  } catch {
    return "";
  }
}

// The keys a structured enhancer puts its answer under. `rewritten_prompt` is
// Qwen's own PE schema; the other two are what hand-written system prompts in
// circulation use for the same field.
const PROMPT_KEYS = ["rewritten_prompt", "enhanced_prompt", "prompt"];

export interface ParsedEnhance {
  prompt: string;
  ratio?: string;
  ratioFollow?: string;
  // The answer came out of a JSON envelope rather than off the raw text. The
  // caller needs this to tell "the model answered" from "the model was cut off
  // and cleanEnhanced salvaged some prose".
  structured: boolean;
}

// parseEnhanced pulls the rewritten prompt out of whatever the enhancer said.
//
// Two contracts are in play and the enhancer picks, not us: the system prompt
// is the USER's, so it may ask for a bare prompt or, as Qwen's published PE
// prompts do, for a JSON object with the prompt plus the aspect ratio to render
// it at. Structured wins when present, because these models narrate their way
// to the answer and the narration is indistinguishable from a prompt to any
// text-shaped cleanup.
export function parseEnhanced(raw: string): ParsedEnhance {
  // Last, not first: the system prompt's own schema example can be echoed back
  // during deliberation, and the real answer is always the final object.
  for (const obj of jsonObjects(raw).reverse()) {
    if (!obj || typeof obj !== "object") continue;
    const rec = obj as Record<string, unknown>;
    const key = PROMPT_KEYS.find((k) => typeof rec[k] === "string" && (rec[k] as string).trim());
    if (!key) continue;
    const ratio = typeof rec.wh_ratio === "string" ? rec.wh_ratio.trim() : "";
    const follow = typeof rec.ratio_follow === "string" ? rec.ratio_follow.trim() : "";
    return {
      prompt: cleanEnhanced(rec[key] as string),
      ratio: ratio || undefined,
      ratioFollow: follow || undefined,
      structured: true,
    };
  }
  return { prompt: cleanEnhanced(raw), structured: false };
}

// jsonObjects returns every top-level balanced {...} in `s` that parses, in the
// order they appear. Hand-rolled rather than a regex because the payload is a
// prompt: it contains braces, quotes and escapes, and a regex either stops at
// the first "}" inside a string or swallows the rest of the document.
function jsonObjects(s: string): unknown[] {
  const out: unknown[] = [];
  for (let i = 0; i < s.length; i++) {
    if (s[i] !== "{") continue;
    let depth = 0;
    let inStr = false;
    let esc = false;
    for (let j = i; j < s.length; j++) {
      const c = s[j];
      if (inStr) {
        if (esc) esc = false;
        else if (c === "\\") esc = true;
        else if (c === '"') inStr = false;
        continue;
      }
      if (c === '"') inStr = true;
      else if (c === "{") depth++;
      else if (c === "}" && --depth === 0) {
        try {
          out.push(JSON.parse(s.slice(i, j + 1)));
        } catch {
          // Prose that happened to balance a brace. Not an error: the next
          // candidate start is where the real object probably begins.
        }
        i = j;
        break;
      }
    }
  }
  return out;
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
