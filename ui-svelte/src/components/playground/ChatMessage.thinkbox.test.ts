import { describe, it, expect } from "vitest";
import { render } from "svelte/server";
import ChatMessage from "./ChatMessage.svelte";

// Regression: inline <think> boxes used to follow the "span still streaming"
// flag, so every thought after the first unfolded itself mid-answer while the
// field-based box above stayed collapsed. Nothing may auto-open now — the
// thought is rendered, but inside a closed <details>.
describe("inline think box", () => {
  const content = "**Brief:** answer text\n\nNow let me search.\n\n<think>SECRET_THOUGHT still going";

  it("keeps a streaming mid-answer thought collapsed", () => {
    const { body } = render(ChatMessage as any, {
      props: { role: "assistant", content, isStreaming: true, isReasoning: false, isSearching: false },
    });
    expect(body).not.toContain("<details open");
    const i = body.indexOf("SECRET_THOUGHT");
    expect(i).toBeGreaterThan(-1); // present, just hidden
    const before = body.slice(0, i);
    expect(before.lastIndexOf("<details")).toBeGreaterThan(before.lastIndexOf("</details"));
    // ...and only there: no second copy leaking into the answer text.
    expect(body.split("SECRET_THOUGHT").length - 1).toBe(1);
  });

  it("keeps a finished thought collapsed too", () => {
    const { body } = render(ChatMessage as any, {
      props: { role: "assistant", content: content + "</think>\n\nmore answer", isStreaming: false },
    });
    expect(body).not.toContain("<details open");
  });
});
