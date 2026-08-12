import { describe, it, expect } from "vitest";
import { convertMoneyLabel, niceRoundAmount } from "./currency";

// USD→DKK, roughly the real rate. Exact figures below follow from it.
const RATE = 6.9;

describe("convertMoneyLabel", () => {
  it("converts a single amount and moves the code to the end", () => {
    expect(convertMoneyLabel("Under $500", RATE, "USD", "DKK")).toBe("Under 3,450 DKK");
  });

  it("converts a range once, not per bound", () => {
    expect(convertMoneyLabel("$500–$800", RATE, "USD", "DKK")).toBe("3,450–5,500 DKK");
  });

  it("expands a k suffix", () => {
    expect(convertMoneyLabel("1k–2k USD", RATE, "USD", "DKK")).toBe("6,900–13,800 DKK");
  });

  // An option with no amount is not a money option; giving it a currency would
  // turn "No fixed budget" into "No fixed budget DKK".
  it("returns empty for a label with no amount", () => {
    expect(convertMoneyLabel("No fixed budget", RATE, "USD", "DKK")).toBe("");
    expect(convertMoneyLabel("As cheap as possible", RATE, "USD", "DKK")).toBe("");
  });

  // The three-letter suffix must not eat the following word.
  it("keeps trailing words", () => {
    expect(convertMoneyLabel("$500 for the whole setup", RATE, "USD", "DKK")).toBe("3,450 for the whole setup DKK");
  });
});

describe("niceRoundAmount", () => {
  it("rounds to a figure a person would say", () => {
    expect(niceRoundAmount(3437.5)).toBe(3450);
    expect(niceRoundAmount(87.3)).toBe(85);
    expect(niceRoundAmount(13_812)).toBe(13_800);
  });
});
