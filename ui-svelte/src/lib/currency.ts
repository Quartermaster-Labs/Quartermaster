import type { ToolDef } from "./types";

// Contract for the `convert_currency` tool. The rate lookup is server-side
// (internal/server/currency.go): Frankfurter (ECB) with open.er-api.com as the
// fallback for currencies ECB does not publish.
//
// Advertised with shopping mode, where the failure it prevents is concrete: the
// user buys in one currency, the best option is priced in another, and a model
// converting from memory quotes a training-cutoff rate as if it were today's.
export const CONVERT_CURRENCY_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "convert_currency",
    description:
      "Convert an amount between currencies at today's reference rate. Use it whenever a price you found is not in the currency the user buys in — never convert from memory, and always keep the price the page itself states.",
    parameters: {
      type: "object",
      properties: {
        amount: {
          type: "number",
          description: "Amount to convert (defaults to 1)",
        },
        from: {
          type: "string",
          description:
            "Currency of the amount, as a 3-letter code (USD, EUR, RON)",
        },
        to: {
          type: "string",
          description: "Currency to convert into, as a 3-letter code",
        },
      },
      required: ["from", "to"],
    },
  },
};

// --- Browser-side rate lookup (ask wizard) ------------------------------------
//
// The wizard needs a rate too, not just the model: a budget question written
// before the currency was known lists brackets in the model's currency, and
// showing "$500" to somebody who spends DKK is a list they cannot answer. The
// rate comes from the SAME server cache the tool uses (GET /api/fx), so this is
// one shared 6h-cached lookup, not a second FX integration.

export interface FxRate {
  from: string;
  to: string;
  rate: number;
  date: string;
  source: string;
}

// Module-level promise cache: several questions (and every repaint) ask for the
// same pair, and an in-flight request must be shared, not repeated.
const fxPending = new Map<string, Promise<FxRate | null>>();

export function fetchFxRate(from: string, to: string): Promise<FxRate | null> {
  const key = `${from}/${to}`;
  const hit = fxPending.get(key);
  if (hit) return hit;
  const p = (async () => {
    try {
      const r = await fetch(
        `/api/fx?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
      );
      if (!r.ok) return null;
      const j = (await r.json()) as FxRate;
      return typeof j?.rate === "number" && j.rate > 0 ? j : null;
    } catch {
      // A failed lookup is not an error worth showing: the caller falls back to
      // a plain text field, which is what it would have shown anyway.
      fxPending.delete(key);
      return null;
    }
  })();
  fxPending.set(key, p);
  return p;
}

const FX_SIGNS = "$€£¥₹₽₺";

/**
 * Round a converted amount to something a person would actually say. A bracket
 * is an approximation, so "3,437.50 DKK" is false precision — and worse, it
 * reads as a figure someone computed on purpose.
 */
export function niceRoundAmount(v: number): number {
  const a = Math.abs(v);
  const step =
    a < 100
      ? 5
      : a < 1_000
        ? 10
        : a < 10_000
          ? 50
          : a < 100_000
            ? 100
            : a < 1_000_000
              ? 1_000
              : 10_000;
  return Math.round(v / step) * step;
}

/**
 * Rewrite a money option ("Under $500", "$500–$800", "1k–2k USD") into the target
 * currency. Currency tokens are dropped from inside the label and the code is
 * appended once at the end, so a range reads "3,450–5,500 DKK" and not
 * "3,450 DKK–5,500 DKK". Returns "" when the label holds no amount to convert —
 * "No fixed budget" must pass through untouched, not gain a currency.
 */
export function convertMoneyLabel(
  label: string,
  rate: number,
  from: string,
  to: string,
): string {
  // String.raw, because a plain template literal eats \d and \b.
  //
  // The trailing code is matched as the KNOWN source currency, not as any
  // three-letter word: "$500 GPU budget" must keep its GPU. Both optional groups
  // hold their own leading \s*, so a label with neither ("$500 for the whole
  // setup") does not lose the space before the next word.
  const code = from.replace(/[^A-Za-z]/g, "").toUpperCase();
  const num = new RegExp(
    String.raw`(?:[$€£¥₹₽₺]\s*)?(\d[\d,.]*)(?:\s*([kK])\b)?` + (code ? String.raw`(?:\s*${code}\b)?` : ""),
    "g",
  );
  let found = false;
  let out = label.replace(num, (m, digits: string, k: string | undefined) => {
    const plain = parseFloat(String(digits).replace(/,/g, ""));
    if (!isFinite(plain)) return m;
    const converted = niceRoundAmount(plain * (k ? 1_000 : 1) * rate);
    found = true;
    return converted.toLocaleString("en-US");
  });
  if (!found) return "";
  // Strip any symbol the regex did not consume (a lone "$" before a word).
  out = out
    .replace(new RegExp(`[${FX_SIGNS}]`, "g"), "")
    .replace(/\s{2,}/g, " ")
    .trim();
  return `${out} ${to}`;
}
