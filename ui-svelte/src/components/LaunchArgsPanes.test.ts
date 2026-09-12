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
});
