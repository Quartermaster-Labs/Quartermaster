// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

// isNative is read once at module load, so the bridge has to exist BEFORE the
// import. Every test re-imports through a reset registry for the same reason.
async function loadNative(withBridge: boolean) {
  vi.resetModules();
  if (withBridge) {
    (window as any).qmDrag = () => Promise.resolve();
    (window as any).qmOpenExternal = vi.fn(() => Promise.resolve());
  } else {
    delete (window as any).qmDrag;
    delete (window as any).qmOpenExternal;
  }
  return await import("./native");
}

/** Clicks a fresh anchor and reports the URL handed to the browser, if any. */
function clickLink(
  href: string,
  target?: string,
  download?: boolean,
  inApp?: boolean,
): string | undefined {
  const a = document.createElement("a");
  a.href = href;
  if (target) a.target = target;
  if (download) a.setAttribute("download", "");
  if (inApp) a.setAttribute("data-qm-inapp", "");
  document.body.appendChild(a);
  a.dispatchEvent(
    new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }),
  );
  a.remove();
  const calls = ((window as any).qmOpenExternal as any).mock?.calls ?? [];
  return calls.length ? calls[calls.length - 1][0] : undefined;
}

describe("parseRgb", () => {
  it("reads what getComputedStyle actually returns", async () => {
    const { parseRgb } = await loadNative(true);
    expect(parseRgb("rgb(11, 14, 20)")).toEqual([11, 14, 20]);
    // Modern browsers render the space-separated form, and alpha is dropped:
    // the window frame is opaque, so there is nothing to blend it with.
    expect(parseRgb("rgba(11 14 20 / 0.5)")).toEqual([11, 14, 20]);
    expect(parseRgb("rgb(10.6 14.2 20)")).toEqual([11, 14, 20]);
  });

  it("returns null rather than a guess for anything else", async () => {
    const { parseRgb } = await loadNative(true);
    // Leaving the frame alone beats dyeing it black: a wrong colour is a
    // visible band, an unset one is the two pixels we started with.
    expect(parseRgb("transparent")).toBeNull();
    expect(parseRgb("#0b0e14")).toBeNull();
    expect(parseRgb("")).toBeNull();
  });
});

describe("installExternalLinkHandler", () => {
  let teardown = () => {};

  beforeEach(() => {
    // The app is served under /ui/, and the same-page rule keys off pathname.
    window.history.replaceState({}, "", "/ui/");
  });
  afterEach(() => teardown());

  it("sends another origin to the browser", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    expect(clickLink("https://huggingface.co/models")).toBe(
      "https://huggingface.co/models",
    );
  });

  it("keeps the hash router inside the window", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    expect(clickLink("#/models")).toBeUndefined();
  });

  it("keeps a same-origin navigation inside the window", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    expect(clickLink("/ui/other")).toBeUndefined();
  });

  it("sends a same-origin new-window link to the browser", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    expect(clickLink("/logs", "_blank")).toBe(location.origin + "/logs");
  });

  it("leaves a download link to the webview", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    // Another origin AND a new window -- both of which would normally send it to
    // the browser. The download attribute has to outrank both.
    expect(
      clickLink("https://example.com/report.csv", "_blank", true),
    ).toBeUndefined();
  });

  it("leaves a data-qm-inapp link to the app", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    // The sidebar's Playground entry: another origin (its own port) AND a new
    // window, so it would otherwise go straight to the system browser. The
    // handler is capture-phase, so the component's own preventDefault runs too
    // late to stop it -- the attribute is the only thing that can.
    expect(
      clickLink("https://example.com:1250/ui/", "_blank", false, true),
    ).toBeUndefined();
  });

  it("drops a non-http scheme rather than shelling out", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    expect(clickLink("javascript:alert(1)", "_blank")).toBeUndefined();
  });

  it("routes window.open through the bridge", async () => {
    const native = await loadNative(true);
    teardown = native.installExternalLinkHandler();
    window.open("https://example.com/x", "_blank", "noopener");
    expect((window as any).qmOpenExternal).toHaveBeenCalledWith(
      "https://example.com/x",
    );
  });

  it("does nothing at all in a browser", async () => {
    const native = await loadNative(false);
    expect(native.isNative).toBe(false);
    const realOpen = window.open;
    teardown = native.installExternalLinkHandler();
    expect(window.open).toBe(realOpen);
  });
});
