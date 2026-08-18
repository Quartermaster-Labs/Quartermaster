import { describe, expect, it } from "vitest";
import { isDisposable, type ChatSession } from "./chatHistory";
import type { ChatMessage } from "../lib/types";

function session(messages: ChatMessage[], extra: Partial<ChatSession> = {}): ChatSession {
  return { id: "a", title: "t", messages, updatedAt: 0, ...extra };
}
const exchange = (reply: Partial<ChatMessage>): ChatSession =>
  session([
    { role: "user", content: "hi" },
    { role: "assistant", content: "", ...reply },
  ]);

describe("isDisposable — junk", () => {
  it("a blank chat", () => {
    expect(isDisposable(session([]))).toBe(true);
  });

  it("a chat whose turn never started", () => {
    expect(isDisposable(session([{ role: "user", content: "hi" }]))).toBe(true);
  });

  it("a chat whose only reply is an error", () => {
    expect(isDisposable(exchange({ content: "\n\n**Error:** model failed to load" }))).toBe(true);
  });

  it("a chat of nothing but empty/errored turns", () => {
    const s = session([
      { role: "user", content: "one" },
      { role: "assistant", content: "\n\n**Error:** no backend" },
      { role: "user", content: "two" },
      { role: "assistant", content: "   " },
    ]);
    expect(isDisposable(s)).toBe(true);
  });
});

describe("isDisposable — keeps real chats", () => {
  it("a normal exchange, including multipart content", () => {
    expect(
      isDisposable(
        session([
          { role: "user", content: [{ type: "text", text: "hi" }] },
          { role: "assistant", content: "hello" },
        ]),
      ),
    ).toBe(false);
  });

  it("a reply that streamed prose before erroring", () => {
    expect(isDisposable(exchange({ content: "Half an ans\n\n**Error:** connection reset" }))).toBe(
      false,
    );
  });

  it("a reply that talks about an error without being one", () => {
    expect(
      isDisposable(exchange({ content: "Your build fails here:\n\n**Error:** missing semicolon" })),
    ).toBe(false);
  });

  it("a reply that only produced reasoning, searches or a tool call", () => {
    expect(isDisposable(exchange({ reasoning_content: "thinking…" }))).toBe(false);
    expect(isDisposable(exchange({ searches: [{ query: "q", results: "r" }] }))).toBe(false);
    expect(
      isDisposable(
        exchange({
          tool_calls: [{ id: "1", type: "function", function: { name: "f", arguments: "{}" } }],
        }),
      ),
    ).toBe(false);
  });

  it("a good chat whose LAST turn failed", () => {
    const s = session([
      { role: "user", content: "one" },
      { role: "assistant", content: "a real answer" },
      { role: "user", content: "two" },
      { role: "assistant", content: "\n\n**Error:** context overflow" },
    ]);
    expect(isDisposable(s)).toBe(false);
  });

  it("an empty chat the user gave standing instructions", () => {
    expect(isDisposable(session([], { instructions: "always answer in French" }))).toBe(false);
  });

  it("anything of an unrecognized shape (keep-by-default)", () => {
    expect(isDisposable({ id: "a", title: "t", updatedAt: 0 } as unknown as ChatSession)).toBe(false);
    expect(isDisposable(undefined as unknown as ChatSession)).toBe(false);
    expect(
      isDisposable(session([{ role: "assistant" } as unknown as ChatMessage])),
    ).toBe(true); // no content of any kind is still junk, not a crash
  });
});
