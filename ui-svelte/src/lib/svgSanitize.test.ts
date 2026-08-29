// @vitest-environment jsdom
//
// Like hubMarkdown's spec, this one needs a real DOM: the sanitizer parses with
// DOMParser rather than regexing markup, which is the whole point of it.
import { describe, it, expect } from "vitest";
import { sanitizeSvg } from "./svgSanitize";

const wrap = (inner: string) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">${inner}</svg>`;

describe("sanitizeSvg", () => {
  it("keeps ordinary drawing markup and the viewBox", () => {
    const out = sanitizeSvg(wrap(`<circle cx="5" cy="5" r="4" fill="#f00"/>`));
    expect(out).toContain("viewBox");
    expect(out).toContain("<circle");
    expect(out).toContain('fill="#f00"');
  });

  it("drops scripts and event handlers", () => {
    const out = sanitizeSvg(wrap(`<script>alert(1)</script><rect onload="alert(1)" width="2" height="2"/>`)) ?? "";
    expect(out).not.toContain("script");
    expect(out).not.toContain("onload");
    expect(out).toContain("<rect");
  });

  it("drops foreignObject, taking its HTML with it", () => {
    const out = sanitizeSvg(wrap(`<foreignObject><div xmlns="http://www.w3.org/1999/xhtml">hi</div></foreignObject>`)) ?? "";
    expect(out.toLowerCase()).not.toContain("foreignobject");
    expect(out).not.toContain("<div");
  });

  it("keeps a fragment href but drops a remote or javascript: one", () => {
    const out = sanitizeSvg(wrap(`<use href="#a"/><use href="https://evil.test/x.svg#a"/><a href="javascript:alert(1)"><rect/></a>`)) ?? "";
    expect(out).toContain('href="#a"'); // nothing declares that id, so it is left alone
    expect(out).not.toContain("evil.test");
    expect(out).not.toContain("javascript:");
  });

  it("keeps a data: image but not a remote one", () => {
    const keep = sanitizeSvg(wrap(`<image href="data:image/png;base64,AAAA"/>`)) ?? "";
    expect(keep).toContain("data:image/png");
    const drop = sanitizeSvg(wrap(`<image href="https://evil.test/pixel.png"/>`)) ?? "";
    expect(drop).not.toContain("evil.test");
    expect(drop).not.toContain("<image");
  });

  it("drops a SMIL animation that retargets a link", () => {
    const out = sanitizeSvg(wrap(`<a href="#x"><set attributeName="href" to="javascript:alert(1)"/></a>`)) ?? "";
    expect(out).not.toContain("javascript:");
    expect(out).not.toContain("<set");
  });

  it("drops a style block that fetches from outside the document", () => {
    const out = sanitizeSvg(wrap(`<style>@import url(https://evil.test/x.css);</style><rect/>`)) ?? "";
    expect(out).not.toContain("evil.test");
    expect(out).toContain("<rect");
  });

  it("namespaces ids so two answers' gradients cannot collide", () => {
    const out = sanitizeSvg(wrap(`<defs><linearGradient id="g"/></defs><rect fill="url(#g)"/>`), "qm-3") ?? "";
    expect(out).toContain('id="qm-3-g"');
    expect(out).toContain("url(#qm-3-g)");
  });

  it("still renders slightly malformed markup via the HTML parser", () => {
    const out = sanitizeSvg(`<svg viewBox="0 0 10 10"><rect width="5" height="5"></svg>`) ?? "";
    expect(out).toContain("<rect");
  });

  it("returns null for something that is not an svg", () => {
    expect(sanitizeSvg("not markup at all")).toBeNull();
    expect(sanitizeSvg("")).toBeNull();
  });
});
