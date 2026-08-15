// Stop tables + labels for the model browser's filter sliders.
//
// Every knob in that menu spans orders of magnitude — half a billion parameters
// to "any", ten downloads to a million, a day to forever. On a linear track the
// entire useful part of each range lives in the first few pixels: dragging from
// 0 to 400B, the difference between an 8B and a 14B model is a hundredth of the
// travel, and those are the two the user is actually choosing between.
//
// So the sliders step through a GEOMETRIC table instead: each stop is roughly a
// constant factor above the last, which makes one step a constant *proportion*
// rather than a constant amount — fine control where values are small and dense,
// coarse where they are large and interchangeable. Curated tables rather than a
// generated `exp()` curve, for two reasons: every landing point is a number a
// person would have typed (8B, 32B, 10k, 6 months) instead of 8.63B, and the
// sizes models are actually published at are not evenly spaced anyway.
//
// Every table ascends, so the thumb always moves right for "more". On the two
// that are CAPS that means the permissive end is the right one, and `Infinity`
// is the "no limit" stop sitting last; it is converted to/from the 0 the filter
// state and the API use (`capValue`/`capStop`) at the component boundary. The
// downloads table is a FLOOR instead, so its permissive end is 0 on the left and
// it needs no infinite stop.

export const SIZE_STOPS = [0.5, 1, 1.5, 2, 3, 4, 6, 8, 12, 16, 22, 32, 48, 70, 120, 235, 400, Infinity];

export const DOWNLOAD_STOPS = [0, 10, 30, 100, 300, 1_000, 3_000, 10_000, 30_000, 100_000, 300_000, 1_000_000];

export const AGE_STOPS = [1, 3, 7, 14, 30, 60, 90, 180, 365, 730, Infinity];

/**
 * nearestStop turns a stored value back into a slider position.
 *
 * A stored value need not be in the table — it can predate a change to it, or
 * come from a URL — so this never fails: the closest stop wins, and a value
 * past the top of a table lands on `Infinity` only if the table has it (every
 * finite stop is closer to any finite value than infinity is).
 */
export function nearestStop(stops: number[], v: number): number {
  let best = 0;
  let bestDist = Infinity;
  for (let i = 0; i < stops.length; i++) {
    // Distance by subtraction, except between two infinities, where it is NaN
    // and would lose every comparison — leaving "no limit" parked on the FIRST
    // stop, i.e. the most restrictive one.
    const d = stops[i] === v ? 0 : Math.abs(stops[i] - v);
    if (d < bestDist) {
      best = i;
      bestDist = d;
    }
  }
  return best;
}

/** Slider stop → the filter/API value, where 0 means "no limit". */
export function capValue(stop: number): number {
  return Number.isFinite(stop) ? stop : 0;
}

/** The filter/API value → the slider stop, i.e. the inverse of `capValue`. */
export function capStop(v: number): number {
  return v > 0 ? v : Infinity;
}

/** "8B" / "0.5B" / "Any size". */
export function fmtParamsStop(stop: number): string {
  if (!Number.isFinite(stop)) return "Any size";
  return `${stop}B`;
}

/** "Any" / "300+" / "10k+" / "1M+". */
export function fmtDownloadsStop(stop: number): string {
  if (!stop) return "Any";
  if (stop >= 1_000_000) return `${stop / 1_000_000}M+`;
  if (stop >= 1_000) return `${stop / 1_000}k+`;
  return `${stop}+`;
}

/**
 * "7 days" / "6 months" / "2 years" / "Any age".
 *
 * Months and years are rendered from the stop table's own round figures (30 /
 * 365 days), so they are labels for those stops rather than a calendar
 * calculation — nothing downstream measures a month.
 */
export function fmtAgeStop(stop: number): string {
  if (!Number.isFinite(stop)) return "Any age";
  if (stop === 1) return "1 day";
  if (stop < 30) return `${stop} days`;
  if (stop < 365) return `${Math.round(stop / 30)} months`;
  const years = Math.round(stop / 365);
  return years === 1 ? "1 year" : `${years} years`;
}
