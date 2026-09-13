import { describe, it, expect } from "vitest";
import { render } from "svelte/server";
import LaunchArgsPanes from "./LaunchArgsPanes.svelte";

// The composed pane is the source of truth for what runs, and users copy the
// command out of it. Svelte strips a whitespace-only <span>, so a separator
// written that way renders (and copies) nothing and glues every token to the
// next one; the explicit {' '} is load-bearing.
describe("LaunchArgsPanes", () => {
  it("keeps the tokens space-separated in the composed command", () => {
    const { body } = render(LaunchArgsPanes, {
      props: {
        customArgs: "--cache-ram 2048",
        customArgsOff: false,
        layers: {
          generated: "-m a.gguf -c 8192",
          effective: "-m a.gguf --cache-ram 2048",
          custom: "--cache-ram 2048",
          ownedKnobs: ["cacheRam"],
          tokens: [
            { text: "-m", source: "generated" },
            { text: "a.gguf", source: "generated" },
            { text: "--cache-ram", source: "custom" },
            { text: "2048", source: "custom" },
          ],
        },
      },
    });
    const text = body.replace(/<!--.*?-->/g, "").replace(/<[^>]*>/g, "");
    expect(text).toContain("-m a.gguf --cache-ram 2048");
  });

  it("shows an unknown flag with its nearest match", () => {
    const { body } = render(LaunchArgsPanes, {
      props: {
        customArgs: "--cms 512",
        customArgsOff: false,
        layers: {
          generated: "-c -cms 256",
          effective: "-c -cms 256 --cms 512",
          custom: "--cms 512",
          ownedKnobs: [],
          tokens: [
            { text: "-c", source: "generated" },
            { text: "-cms", source: "generated" },
            { text: "256", source: "generated" },
            { text: "--cms", source: "custom" },
            { text: "512", source: "custom" },
          ],
          issues: [
            {
              token: "--cms",
              kind: "unknown",
              message: "llama-server does not accept this flag, so it will refuse to start",
              suggestions: ["-cms", "--checkpoint-min-step"],
            },
          ],
        },
      },
    });
    const text = body.replace(/<!--.*?-->/g, "").replace(/<[^>]*>/g, "");
    expect(text).toContain("did you mean -cms or --checkpoint-min-step");
    expect(text).toContain("1 issue");
  });
});
