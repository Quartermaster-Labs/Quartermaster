import type { ChatMessage, ContentPart, ToolCall, ToolDef } from "./types";
import { inferenceHeaders } from "./inferenceAuth";

export type Endpoint = "v1/chat/completions" | "v1/messages" | "v1/responses";

export interface StreamChunk {
  content: string;
  reasoning_content?: string;
  done: boolean;
  // Accumulated tool calls, only present on the terminating chunk when the
  // model requested tools. Only the chat/completions endpoint emits these.
  tool_calls?: ToolCall[];
}

export interface ChatOptions {
  temperature?: number;
  endpoint?: Endpoint;
  max_tokens?: number;
  tools?: ToolDef[];
  // undefined = leave to model default; true/false = force on/off via the
  // llama.cpp chat_template_kwargs.enable_thinking switch (Qwen3 etc).
  reasoning?: boolean;
}

function parseDataUrl(url: string): { media_type: string; data: string } {
  const match = /^data:([^;]+);base64,(.*)$/i.exec(url);
  if (!match) {
    throw new Error("Image is not a base64 data URL");
  }
  return { media_type: match[1], data: match[2] };
}

function splitSystemMessages(messages: ChatMessage[]): { system: string; rest: ChatMessage[] } {
  const systemParts: string[] = [];
  const rest: ChatMessage[] = [];
  for (const msg of messages) {
    if (msg.role === "system") {
      if (typeof msg.content === "string") {
        systemParts.push(msg.content);
      } else {
        for (const part of msg.content) {
          if (part.type === "text") systemParts.push(part.text);
        }
      }
    } else {
      rest.push(msg);
    }
  }
  return { system: systemParts.join("\n\n"), rest };
}

function buildChatCompletionsBody(model: string, messages: ChatMessage[], options?: ChatOptions): object {
  return {
    model,
    // Resend assistant reasoning_content so preserve_thinking templates (Qwen3.6+)
    // can keep prior-turn <think> in history; harmless to models that ignore it.
    // tool_calls (assistant) and tool_call_id (tool role) are passed through so
    // the model sees its own tool requests + the results on the next round.
    messages: messages.map((m) => ({
      role: m.role,
      content: m.content,
      ...(m.role === "assistant" && m.reasoning_content
        ? { reasoning_content: m.reasoning_content }
        : {}),
      ...(m.role === "assistant" && m.tool_calls ? { tool_calls: m.tool_calls } : {}),
      ...(m.role === "tool" && m.tool_call_id ? { tool_call_id: m.tool_call_id } : {}),
    })),
    stream: true,
    temperature: options?.temperature,
    ...(options?.max_tokens ? { max_tokens: options.max_tokens } : {}),
    ...(options?.tools && options.tools.length > 0 ? { tools: options.tools } : {}),
    ...(options?.reasoning !== undefined
      ? { chat_template_kwargs: { enable_thinking: options.reasoning } }
      : {}),
  };
}

function buildMessagesBody(model: string, messages: ChatMessage[], options?: ChatOptions): object {
  const { system, rest } = splitSystemMessages(messages);
  const mapped = rest.map((m) => {
    if (typeof m.content === "string") {
      return { role: m.role, content: m.content };
    }
    const blocks: object[] = [];
    for (const part of m.content as ContentPart[]) {
      if (part.type === "text") {
        blocks.push({ type: "text", text: part.text });
      } else if (m.role !== "assistant") {
        const { media_type, data } = parseDataUrl(part.image_url.url);
        blocks.push({ type: "image", source: { type: "base64", media_type, data } });
      }
    }
    return { role: m.role, content: blocks };
  });

  const body: Record<string, unknown> = {
    model,
    messages: mapped,
    stream: true,
    max_tokens: options?.max_tokens ?? 8192,
  };
  if (system) body.system = system;
  if (options?.temperature !== undefined) body.temperature = options.temperature;
  return body;
}

function buildResponsesBody(model: string, messages: ChatMessage[], options?: ChatOptions): object {
  const { system, rest } = splitSystemMessages(messages);
  const input = rest.map((m) => {
    const isAssistant = m.role === "assistant";
    if (typeof m.content === "string") {
      const partType = isAssistant ? "output_text" : "input_text";
      return { role: m.role, content: [{ type: partType, text: m.content }] };
    }
    const content = m.content.map((part: ContentPart) => {
      if (part.type === "text") {
        return { type: isAssistant ? "output_text" : "input_text", text: part.text };
      }
      return { type: "input_image", image_url: part.image_url.url };
    });
    return { role: m.role, content };
  });

  const body: Record<string, unknown> = {
    model,
    input,
    stream: true,
  };
  if (system) body.instructions = system;
  if (options?.temperature !== undefined) body.temperature = options.temperature;
  if (options?.max_tokens) body.max_output_tokens = options.max_tokens;
  return body;
}

function buildRequest(
  endpoint: Endpoint,
  model: string,
  messages: ChatMessage[],
  options?: ChatOptions
): { url: string; body: object } {
  const url = "/" + endpoint;
  switch (endpoint) {
    case "v1/messages":
      return { url, body: buildMessagesBody(model, messages, options) };
    case "v1/responses":
      return { url, body: buildResponsesBody(model, messages, options) };
    case "v1/chat/completions":
    default:
      return { url, body: buildChatCompletionsBody(model, messages, options) };
  }
}

function parseChatCompletionsLine(line: string): StreamChunk | null {
  const trimmed = line.trim();
  if (!trimmed || !trimmed.startsWith("data: ")) {
    return null;
  }

  const data = trimmed.slice(6);
  if (data === "[DONE]") {
    return { content: "", done: true };
  }

  try {
    const parsed = JSON.parse(data);
    const delta = parsed.choices?.[0]?.delta;
    const content = delta?.content || "";
    const reasoning_content = delta?.reasoning_content || delta?.reasoning || "";

    if (content || reasoning_content) {
      return { content, reasoning_content, done: false };
    }
    return null;
  } catch {
    return null;
  }
}

// Tool-call deltas arrive fragmented: each chunk carries a partial slice keyed
// by `index`, with name + arguments streamed piecemeal. Accumulate by index.
type ToolAcc = Record<number, { id: string; name: string; args: string }>;

function accumulateToolCalls(acc: ToolAcc, line: string): void {
  const trimmed = line.trim();
  if (!trimmed.startsWith("data: ")) return;
  const data = trimmed.slice(6);
  if (data === "[DONE]") return;
  try {
    const calls = JSON.parse(data).choices?.[0]?.delta?.tool_calls;
    if (!Array.isArray(calls)) return;
    for (const tc of calls) {
      const idx = tc.index ?? 0;
      acc[idx] ??= { id: "", name: "", args: "" };
      if (tc.id) acc[idx].id = tc.id;
      if (tc.function?.name) acc[idx].name += tc.function.name;
      if (tc.function?.arguments) acc[idx].args += tc.function.arguments;
    }
  } catch {
    // ignore malformed line
  }
}

function buildToolCalls(acc: ToolAcc): ToolCall[] {
  return Object.keys(acc)
    .map(Number)
    .sort((a, b) => a - b)
    .map((i) => ({
      id: acc[i].id || `call_${i}`,
      type: "function" as const,
      function: { name: acc[i].name, arguments: acc[i].args },
    }));
}

async function* parseChatCompletionsStream(
  reader: ReadableStreamDefaultReader<Uint8Array>
): AsyncGenerator<StreamChunk> {
  const decoder = new TextDecoder();
  let buffer = "";
  const toolAcc: ToolAcc = {};

  const finish = (): StreamChunk => {
    const tcs = buildToolCalls(toolAcc);
    return { content: "", done: true, ...(tcs.length > 0 ? { tool_calls: tcs } : {}) };
  };

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split("\n");
    buffer = lines.pop() || "";

    for (const line of lines) {
      accumulateToolCalls(toolAcc, line);
      const result = parseChatCompletionsLine(line);
      if (result?.done) {
        yield finish();
        return;
      }
      if (result) {
        yield result;
      }
    }
  }

  accumulateToolCalls(toolAcc, buffer);
  const result = parseChatCompletionsLine(buffer);
  if (result && !result.done) {
    yield result;
  }
  yield finish();
}

function parseSSEEventBlock(block: string): { event: string; data: string } | null {
  let event = "";
  const dataLines: string[] = [];
  for (const rawLine of block.split("\n")) {
    const line = rawLine.replace(/\r$/, "");
    if (!line || line.startsWith(":")) continue;
    if (line.startsWith("event:")) {
      event = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trim());
    }
  }
  if (dataLines.length === 0 && !event) return null;
  return { event, data: dataLines.join("\n") };
}

async function* parseMessagesStream(
  reader: ReadableStreamDefaultReader<Uint8Array>
): AsyncGenerator<StreamChunk> {
  const decoder = new TextDecoder();
  let buffer = "";

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });
    const blocks = buffer.split("\n\n");
    buffer = blocks.pop() || "";

    for (const block of blocks) {
      const parsed = parseSSEEventBlock(block);
      if (!parsed) continue;
      if (parsed.event === "message_stop") {
        yield { content: "", done: true };
        return;
      }
      if (parsed.event !== "content_block_delta" || !parsed.data) continue;
      try {
        const json = JSON.parse(parsed.data);
        const delta = json.delta;
        if (!delta) continue;
        if (delta.type === "text_delta" && delta.text) {
          yield { content: delta.text, done: false };
        } else if (delta.type === "thinking_delta" && delta.thinking) {
          yield { content: "", reasoning_content: delta.thinking, done: false };
        }
      } catch {
        // ignore malformed event
      }
    }
  }
}

async function* parseResponsesStream(
  reader: ReadableStreamDefaultReader<Uint8Array>
): AsyncGenerator<StreamChunk> {
  const decoder = new TextDecoder();
  let buffer = "";

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });
    const blocks = buffer.split("\n\n");
    buffer = blocks.pop() || "";

    for (const block of blocks) {
      const parsed = parseSSEEventBlock(block);
      if (!parsed) continue;
      if (parsed.event === "response.completed") {
        yield { content: "", done: true };
        return;
      }
      if (!parsed.data) continue;
      try {
        const json = JSON.parse(parsed.data);
        if (parsed.event === "response.output_text.delta" && json.delta) {
          yield { content: json.delta, done: false };
        } else if (parsed.event === "response.reasoning_summary_text.delta" && json.delta) {
          yield { content: "", reasoning_content: json.delta, done: false };
        }
      } catch {
        // ignore malformed event
      }
    }
  }
}

function parseStream(
  endpoint: Endpoint,
  reader: ReadableStreamDefaultReader<Uint8Array>
): AsyncGenerator<StreamChunk> {
  switch (endpoint) {
    case "v1/messages":
      return parseMessagesStream(reader);
    case "v1/responses":
      return parseResponsesStream(reader);
    case "v1/chat/completions":
    default:
      return parseChatCompletionsStream(reader);
  }
}

export async function* streamChatCompletion(
  model: string,
  messages: ChatMessage[],
  signal?: AbortSignal,
  options?: ChatOptions
): AsyncGenerator<StreamChunk> {
  const endpoint = options?.endpoint ?? "v1/chat/completions";
  const { url, body } = buildRequest(endpoint, model, messages, options);

  const response = await fetch(url, {
    method: "POST",
    headers: inferenceHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(body),
    signal,
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`Chat API error: ${response.status} - ${errorText}`);
  }

  const reader = response.body?.getReader();
  if (!reader) {
    throw new Error("Response body is not readable");
  }

  try {
    for await (const chunk of parseStream(endpoint, reader)) {
      yield chunk;
      if (chunk.done) return;
    }
    yield { content: "", done: true };
  } finally {
    reader.releaseLock();
  }
}
