import { describe, it, expect } from "vitest";
import { formatSpeed, formatDuration, formatRelativeTime, shortReqPath } from "./activityFormat";

describe("formatSpeed", () => {
  it("prints one decimal", () => {
    expect(formatSpeed(58.24)).toBe("58.2");
  });
  it("renders a negative (unmeasured) speed as a dash", () => {
    expect(formatSpeed(-1)).toBe("-");
  });
});

describe("formatDuration", () => {
  it("converts ms to seconds with two decimals", () => {
    expect(formatDuration(9231)).toBe("9.23s");
  });
});

describe("formatRelativeTime", () => {
  const now = Date.UTC(2026, 0, 1, 12, 0, 0);
  const ago = (ms: number) => new Date(now - ms).toISOString();

  it("collapses the last few seconds to 'now'", () => {
    expect(formatRelativeTime(ago(2_000), now)).toBe("now");
  });
  it("counts seconds, then minutes, then hours", () => {
    expect(formatRelativeTime(ago(30_000), now)).toBe("30s ago");
    expect(formatRelativeTime(ago(5 * 60_000), now)).toBe("5m ago");
    expect(formatRelativeTime(ago(3 * 3_600_000), now)).toBe("3h ago");
  });
  it("stops counting past a day", () => {
    expect(formatRelativeTime(ago(50 * 3_600_000), now)).toBe("a while ago");
  });
  // A record stamped slightly in the future (clock skew between the server and
  // the browser) must not print as a negative age.
  it("treats a future stamp as now", () => {
    expect(formatRelativeTime(ago(-10_000), now)).toBe("now");
  });
});

describe("shortReqPath", () => {
  it("drops the version prefix", () => {
    expect(shortReqPath("/v1/chat/completions")).toBe("chat/completions");
    expect(shortReqPath("/upstream/foo")).toBe("upstream/foo");
  });
});
