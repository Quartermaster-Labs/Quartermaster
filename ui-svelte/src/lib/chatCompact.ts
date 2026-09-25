import type { ChatMessage } from "./types";
import { inferenceHeaders } from "./inferenceAuth";

// Auto-compact: when a conversation nears the model's context window, the oldest
// turns are summarized by the model itself into a compact brief. The full history
// is still kept on disk and shown in the UI — compaction only moves a boundary so
// that future turns send `summary + messages after the boundary` instead of the
// whole transcript, letting the chat continue past the raw context limit without
// overflowing. The most recent KEEP_RECENT messages always stay verbatim.

// Fraction of the model's context (live KV usage ratio) at which we compact.
export const COMPACT_AT = 0.8;
// How many of the most recent messages to keep out of the summary (sent verbatim).
export const KEEP_RECENT = 6;

const SUMMARY_PROMPT =
  "Summarize the conversation above into a concise brief that preserves every " +
  "fact, decision, name, number, code snippet, and open question needed to " +
  "continue seamlessly. Use compact bullet points. Output only the summary.";

// cleanTitle extracts a usable title from a raw model reply: drop any reasoning
// block (closed or unclosed), take the last non-empty line, strip wrapping
// quotes, cap at 48 chars. Pure so it can be unit-tested.
// stripThink removes a reasoning block from a non-streaming reply, closed or
// unclosed. \ackends that split reasoning into `reasoning_content` never put it
// in `content`; templates that ignore enable_thinking do, so both callers here
// have to cope with either shape.
export function stripThink(text: string): string {
  return text.replace(/<think>[\s\S]*?(<\/think>|$)/gi, "").trim();
}

export function cleanTitle(text: string): string {
  const clean = stripThink(text);
  const line = clean.split("\n").map((l) => l.trim()).filter(Boolean).pop() ?? "";
  return line.replace(/^["']|["']$/g, "").slice(0, 48);
}

// generateTitle names the conversation from its opening exchange, using the CHAT
// model. Returns "" on any failure so callers can fall back to the first-message
// heuristic.
//
// The vendored 80M CPU title model (POST /api/chats/title, internal/server/
// titlegen.go) was tried here first and gave poor chat titles: at that size it
// tail-copies the opening request instead of naming the topic. It still titles
// reasoning boxes, where it only ever summarizes prose handed to it. Naming a
// chat costs the chat model one short round trip and it is warm by definition —
// the title is generated right after the first answer streamed from it.
export async function generateTitle(
  model: string,
  messages: ChatMessage[],
  signal?: AbortSignal,
): Promise<string> {
  const parts = messages
    .filter((m) => m.role === "user" || m.role === "assistant")
    .slice(0, 2)
    .map((m) => ({ role: m.role, content: m.content }));
  if (parts.length === 0) return "";

  parts.push({
    role: "user",
    content:
      "Give a short title (max 6 words) for this conversation. Output only the " +
      "title, no quotes, no punctuation at the end.",
  });

  try {
    const res = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: inferenceHeaders({ "Content-Type": "application/json" }),
      // enable_thinking:false stops a reasoning model from burning the budget on
      // a <think> block (which left the title empty); cleanTitle still strips any
      // think tags a model emits regardless.
      body: JSON.stringify({
        model,
        messages: parts,
        stream: false,
        temperature: 0.3,
        max_tokens: 64,
        chat_template_kwargs: { enable_thinking: false },
      }),
      signal,
    });
    if (!res.ok) return "";
    const json = await res.json();
    const text = json.choices?.[0]?.message?.content;
    if (typeof text !== "string") return "";
    return cleanTitle(text);
  } catch {
    return "";
  }
}

// summarizeConversation folds priorSummary + the given (older) messages into a
// single refreshed summary via a non-streaming completion on the same model.
export async function summarizeConversation(
  model: string,
  messages: ChatMessage[],
  priorSummary: string,
  signal?: AbortSignal,
): Promise<string> {
  const parts: { role: string; content: unknown }[] = [];
  if (priorSummary) {
    parts.push({ role: "system", content: `Summary of earlier conversation:\n${priorSummary}` });
  }
  for (const m of messages) {
    // Skip tool plumbing and reasoning — only the visible exchange matters here.
    if (m.role === "tool") continue;
    parts.push({ role: m.role, content: m.content });
  }
  parts.push({ role: "user", content: SUMMARY_PROMPT });

  const res = await fetch("/v1/chat/completions", {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    // enable_thinking:false for the same reason generateTitle sets it, and it
    // bites harder here: with reasoning on, the model can spend the entire
    // budget inside <think> and return an EMPTY content, which surfaced as a
    // flat "Compaction failed" on a perfectly healthy model -- and only
    // sometimes, since it depends on how long it happened to think.
    body: JSON.stringify({
      model,
      messages: parts,
      stream: false,
      temperature: 0.3,
      max_tokens: 1536,
      chat_template_kwargs: { enable_thinking: false },
    }),
    signal,
  });
  return readSummary(res);
}

// readSummary pulls the summary out of a non-streaming completion, or throws
// saying which kind of empty it got: a template that ignored enable_thinking
// and thought anyway reads very differently from a backend that returned
// nothing at all, and the toast is the only place the user sees it.
async function readSummary(res: Response): Promise<string> {
  if (!res.ok) {
    throw new Error(`the model returned ${res.status}`);
  }
  const json = await res.json();
  const choice = json.choices?.[0];
  const text = typeof choice?.message?.content === "string" ? stripThink(choice.message.content) : "";
  if (!text) {
    throw new Error(
      choice?.message?.tool_calls?.length
        ? "the model called a tool instead of summarizing"
        : choice?.message?.reasoning_content
          ? "the model produced only reasoning, no summary"
          : `the model returned no summary (finish: ${choice?.finish_reason ?? "unknown"})`,
    );
  }
  return text;
}

// compactInPlacePrompt is the instruction appended to the live conversation.
// The model sees the whole history (the kept tail included, since that is what
// its KV holds), so it is told where to stop: the tail stays verbatim and a
// summary that repeats it just wastes context. The earlier summary lives in the
// system prompt, so it has to be folded in by name.
export function compactInPlacePrompt(hasPriorSummary: boolean, keepFrom: string): string {
  const snippet = keepFrom.replace(/\s+/g, " ").trim().slice(0, 120);
  return (
    "Context is running out, so the older part of this conversation is about to be replaced by a summary. " +
    "Write that summary now: a concise brief that preserves every fact, decision, name, number, code snippet, " +
    "and open question needed to continue seamlessly. Use compact bullet points. " +
    (hasPriorSummary ? "Fold in everything from the \"Summary of earlier conversation\" in the system prompt. " : "") +
    (snippet
      ? `Cover only the messages BEFORE the user message that begins "${snippet}"; that message and everything after it are kept word for word, so leave them out. `
      : "") +
    "Do not call any tools and do not answer anything above. Output only the summary."
  );
}

// summarizeInPlace asks the model for the summary as one more user turn on the
// conversation it already holds (POST /api/chats/compact). `turn` is the body a
// turn would send, messages already ending with compactInPlacePrompt. The server
// assembles the history exactly as a turn does and keys the request to the same
// conversation, so it reuses the KV exactly as far as the next turn would, and
// the chat's KV is neither evicted nor saved (internal/server/turnscompact.go).
export async function summarizeInPlace(turn: Record<string, unknown>, signal?: AbortSignal): Promise<string> {
  const res = await fetch("/api/chats/compact", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...turn, temperature: 0.3, max_tokens: 1536 }),
    signal,
  });
  return readSummary(res);
}
