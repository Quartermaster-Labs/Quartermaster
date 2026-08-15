import { describe, it, expect } from "vitest";
import { EFFORT_OFF, EFFORT_ON, effortOptions, resolveEffort, requestEffort } from "./effort";

// The ladder as Qwen 3.8 declares it: unsorted, and that spelling exactly.
const QWEN = ["xhigh", "medium", "low"];

describe("effortOptions", () => {
  it("offers a plain on/off when the model declares no ladder", () => {
    expect(effortOptions(undefined).map((o) => o.value)).toEqual([EFFORT_OFF, EFFORT_ON]);
    expect(effortOptions([]).map((o) => o.value)).toEqual([EFFORT_OFF, EFFORT_ON]);
  });

  it("puts None first and sorts the ladder cheapest-first", () => {
    expect(effortOptions(QWEN).map((o) => o.value)).toEqual([EFFORT_OFF, "low", "medium", "xhigh"]);
  });

  it("keeps the advertised order for levels it does not know", () => {
    expect(effortOptions(["thorough", "balanced"]).map((o) => o.value)).toEqual([
      EFFORT_OFF,
      "thorough",
      "balanced",
    ]);
  });

  it("labels xhigh readably and capitalizes unknown levels", () => {
    expect(effortOptions(QWEN).find((o) => o.value === "xhigh")?.label).toBe("Extra high");
    expect(effortOptions(["balanced"]).find((o) => o.value === "balanced")?.label).toBe("Balanced");
  });
});

describe("resolveEffort", () => {
  it("keeps a pick the model accepts", () => {
    expect(resolveEffort("low", QWEN)).toBe("low");
    expect(resolveEffort("XHIGH", QWEN)).toBe("xhigh"); // the template's spelling wins
  });

  it("defaults to medium when the pick is not on the ladder", () => {
    expect(resolveEffort("on", QWEN)).toBe("medium");
    expect(resolveEffort("high", QWEN)).toBe("medium");
  });

  it("falls back to the middle rung when there is no medium", () => {
    expect(resolveEffort("on", ["low", "high"])).toBe("low");
    expect(resolveEffort("on", ["minimal", "low", "high"])).toBe("low");
  });

  it("never overrides an explicit none, and stays on/off without a ladder", () => {
    expect(resolveEffort(EFFORT_OFF, QWEN)).toBe(EFFORT_OFF);
    expect(resolveEffort(EFFORT_OFF, [])).toBe(EFFORT_OFF);
    expect(resolveEffort("medium", [])).toBe(EFFORT_ON);
  });
});

describe("requestEffort", () => {
  it("sends a level, and nothing for on/off", () => {
    expect(requestEffort("medium")).toBe("medium");
    expect(requestEffort(EFFORT_ON)).toBe("");
    expect(requestEffort(EFFORT_OFF)).toBe("");
  });
});
