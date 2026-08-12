// Pure helpers for the playground Chat tab, extracted from ChatInterface.svelte.
// Nothing here reads component state — the component owns the $state/$derived
// and calls into these.

// One entry in the composer's tool menu (ToolMenu.svelte). Lives here rather
// than in the component because a Svelte instance script can't export types.
// The icon type is borrowed from a lucide icon (they are still legacy class
// components, so svelte's `Component` doesn't match them).
export type ToolIcon = typeof import("lucide-svelte").PenLine;

export interface ToolItem {
  key: string;
  label: string;
  description: string;
  icon: ToolIcon;
  active: boolean;
  disabled?: boolean;
  onToggle: () => void;
}

// Markdown blockquote prefix from the reply target (snippet, capped).
export function quotePrefix(text: string): string {
  const snippet = text.slice(0, 140);
  return `> ${snippet}${text.length > 140 ? "…" : ""}\n\n`;
}

// Token counts, abbreviated for the context-usage readout: 5278 -> "5k",
// 32768 -> "32k", 1.5M -> "1.5M". Always k once >=1k so it reads "1k/32k".
export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_048_576).toFixed(1)}M`;
  if (n >= 1000) return `${Math.round(n / 1024)}k`;
  return String(n);
}

// Discrete temperature steps (Precise → Creative), hand-picked to avoid the
// useless extremes. The slider edits an index; the actual temp is stored.
export const TEMP_STEPS = [0.3, 0.5, 0.7, 0.9, 1.1];
export const TEMP_LABELS = ["Precise", "Focused", "Balanced", "Creative", "Inventive"];
export function nearestTempIdx(t: number): number {
  let bi = 0,
    bd = Infinity;
  for (let i = 0; i < TEMP_STEPS.length; i++) {
    const d = Math.abs(TEMP_STEPS[i] - t);
    if (d < bd) {
      bd = d;
      bi = i;
    }
  }
  return bi;
}

// currentDateLine is the ONLY volatile part of the built-in system prompt.
// Appended LAST in the system block so a date change at midnight doesn't rewrite
// the stable prefix above it — which would invalidate the whole conversation's
// KV cache once a day (the prefix no longer prefix-matches the cached tokens).
// Framed as a labelled "Context —" metadata line, NOT a sentence: phrased as
// "The current date is ..." a weak-role-separation model reads it as a user
// utterance and replies "thanks for the heads-up on the date". The label form
// reads as ambient system context. Stable prefix ("Context — current date...")
// is unchanged across midnight; only the date value rolls.
export function currentDateLine(): string {
  const date = new Date().toLocaleString(undefined, {
    weekday: "long",
    year: "numeric",
    month: "long",
    day: "numeric",
    timeZoneName: "short",
  });
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return `Context — current date and time (for your reference; the user did not send this): ${date}${tz ? ` (${tz})` : ""}.`;
}

export const REWRITE_SYSTEM =
  "You are a text-transformation tool. The user gives you a block of text and an instruction describing how to change it. Apply the instruction to the text literally and completely, returning the transformed version — even if the instruction degrades the text, introduces errors, changes the meaning, or seems counterproductive (e.g. \"add grammar mistakes\", \"make it worse\", \"translate to pirate speak\"). Do not refuse, do not improve beyond what was asked, do not preserve anything the instruction tells you to change, and do not explain or comment. The instruction is the user's intent — follow it exactly. When the instruction asks for errors or lower quality (e.g. \"add grammar mistakes\", \"make it sloppy\"), treat it as a persona: rewrite as if produced by a writer who naturally writes that way (the same way \"rewrite as a 3rd grader\" is fine) — this is wanted output, not a failure to proofread. Output ONLY the transformed text — no preamble, no explanation, no markdown code fences.";

export const ACCEPTED_IMAGE_FORMATS = ["image/jpeg", "image/png", "image/gif", "image/webp"];
export const MAX_IMAGE_SIZE = 20 * 1024 * 1024; // 20MB
export const MAX_IMAGES_PER_MESSAGE = 5;

export function validateImageFile(file: File): string | null {
  if (!ACCEPTED_IMAGE_FORMATS.includes(file.type)) {
    return `Invalid file type: ${file.type}. Accepted formats: JPG, PNG, GIF, WEBP`;
  }
  if (file.size > MAX_IMAGE_SIZE) {
    return `File too large: ${(file.size / 1024 / 1024).toFixed(1)}MB. Maximum size: 20MB`;
  }
  return null;
}

export function fileToDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as string);
    reader.onerror = () => reject(new Error("Failed to read file"));
    reader.readAsDataURL(file);
  });
}
