// @vitest-environment jsdom
//
// The one spec in this project that needs a DOM: the sanitizer parses HTML with
// DOMParser, on purpose (a regex-based HTML sanitizer is how sanitizers get
// bypassed), so testing it without a DOM would mean testing something else.
import { describe, it, expect } from "vitest";
import { sanitizeHTML, stripFrontmatter, absolutize, renderModelCard } from "./hubMarkdown";

const REPO = "unsloth/Qwen3-8B-GGUF";

describe("stripFrontmatter", () => {
  it("drops the YAML block a model card opens with", () => {
    const md = "---\nlicense: apache-2.0\ntags:\n  - gguf\n---\n# Qwen3\n";
    expect(stripFrontmatter(md)).toBe("# Qwen3\n");
  });

  it("leaves a card that has none, and a horizontal rule mid-document", () => {
    expect(stripFrontmatter("# Qwen3\n\n---\n\nnotes")).toBe("# Qwen3\n\n---\n\nnotes");
  });
});

describe("absolutize", () => {
  it("resolves a repo-relative path against the repo's files", () => {
    expect(absolutize("assets/arch.png", REPO)).toBe(`https://huggingface.co/${REPO}/resolve/main/assets/arch.png`);
    expect(absolutize("./arch.png", REPO)).toBe(`https://huggingface.co/${REPO}/resolve/main/arch.png`);
  });

  it("leaves absolute URLs and anchors alone", () => {
    expect(absolutize("https://example.com/a.png", REPO)).toBe("https://example.com/a.png");
    expect(absolutize("#usage", REPO)).toBe("#usage");
    expect(absolutize("//cdn.example.com/a.png", REPO)).toBe("//cdn.example.com/a.png");
  });
});

describe("sanitizeHTML", () => {
  it("drops script tags", () => {
    const out = sanitizeHTML(`<p>hi</p><script>alert(1)</script>`, REPO);
    expect(out).toContain("hi");
    expect(out).not.toContain("alert");
    expect(out.toLowerCase()).not.toContain("<script");
  });

  it("strips event handlers", () => {
    const out = sanitizeHTML(`<img src="https://e.com/a.png" onerror="alert(1)">`, REPO);
    expect(out).not.toContain("onerror");
    expect(out).not.toContain("alert");
  });

  it("removes a javascript: href but keeps the text", () => {
    const out = sanitizeHTML(`<a href="javascript:alert(1)">click</a>`, REPO);
    expect(out).not.toContain("javascript:");
    expect(out).toContain("click");
  });

  it("routes images through the proxy and resolves relative ones", () => {
    const out = sanitizeHTML(`<img src="assets/arch.png">`, REPO);
    expect(out).toContain("/api/imgproxy?url=");
    expect(out).toContain(encodeURIComponent(`https://huggingface.co/${REPO}/resolve/main/assets/arch.png`));
  });

  it("drops a non-http image rather than inlining it", () => {
    expect(sanitizeHTML(`<img src="data:image/png;base64,AAAA">`, REPO)).not.toContain("data:image");
  });

  it("opens links in a new tab", () => {
    const out = sanitizeHTML(`<a href="https://example.com">x</a>`, REPO);
    expect(out).toContain('target="_blank"');
    expect(out).toContain("noopener");
  });

  it("keeps highlighter classes and drops arbitrary ones", () => {
    const out = sanitizeHTML(`<code class="hljs language-py evil">x</code>`, REPO);
    expect(out).toContain("hljs");
    expect(out).not.toContain("evil");
  });

  it("keeps a numeric width on an image — that is how a card sizes its badges", () => {
    const html = sanitizeHTML(`<img src="https://x.test/badge.svg" width="120" height="20">`, REPO);
    expect(html).toContain(`width="120"`);
    expect(html).toContain(`height="20"`);
  });

  it("drops a non-numeric size, and any size outside an image", () => {
    expect(sanitizeHTML(`<img src="https://x.test/a.png" width="100%">`, REPO)).not.toContain("width");
    expect(sanitizeHTML(`<img src="https://x.test/a.png" width="99999">`, REPO)).not.toContain("width");
    expect(sanitizeHTML(`<td width="200">x</td>`, REPO)).not.toContain("width");
  });

  it("drops an iframe outright", () => {
    expect(sanitizeHTML(`<iframe src="https://evil.com"></iframe>`, REPO).toLowerCase()).not.toContain("<iframe");
  });
});

describe("renderModelCard", () => {
  it("renders prose without the frontmatter", () => {
    const html = renderModelCard("---\nlicense: mit\n---\n# Title\n\nSome **text**.", REPO);
    expect(html).toContain("Title");
    expect(html).toContain("<strong>text</strong>");
    expect(html).not.toContain("license");
  });

  it("returns empty for an empty card, so the caller can skip the section", () => {
    expect(renderModelCard("---\nlicense: mit\n---\n", REPO)).toBe("");
    expect(renderModelCard("", REPO)).toBe("");
  });
});
