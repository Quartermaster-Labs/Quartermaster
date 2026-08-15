import { describe, it, expect } from "vitest";
import {
  SIZE_STOPS,
  DOWNLOAD_STOPS,
  AGE_STOPS,
  nearestStop,
  capValue,
  capStop,
  fmtParamsStop,
  fmtDownloadsStop,
  fmtAgeStop,
} from "./logScale";

describe("stop tables", () => {
  it("are ascending, so the thumb always moves right for 'more'", () => {
    for (const stops of [SIZE_STOPS, DOWNLOAD_STOPS, AGE_STOPS]) {
      for (let i = 1; i < stops.length; i++) expect(stops[i]).toBeGreaterThan(stops[i - 1]);
    }
  });

  it("grow geometrically rather than linearly", () => {
    // The point of the tables: every step is roughly a constant factor, so the
    // slider spends as much travel on 1B→8B as on 70B→400B. Checked as a bound
    // rather than an exact ratio — the stops are curated round numbers.
    const finite = SIZE_STOPS.filter(Number.isFinite);
    for (let i = 1; i < finite.length; i++) {
      const ratio = finite[i] / finite[i - 1];
      expect(ratio).toBeGreaterThan(1.15);
      expect(ratio).toBeLessThan(2.1);
    }
  });

  it("carries the default size cap as its own stop, so it lands exactly", () => {
    expect(SIZE_STOPS).toContain(120);
  });
});

describe("nearestStop", () => {
  it("finds an exact stop", () => {
    expect(SIZE_STOPS[nearestStop(SIZE_STOPS, 32)]).toBe(32);
    expect(AGE_STOPS[nearestStop(AGE_STOPS, 365)]).toBe(365);
  });

  it("snaps a value that is not in the table", () => {
    // A stored value can predate a change to the table; it must still place.
    expect(SIZE_STOPS[nearestStop(SIZE_STOPS, 13)]).toBe(12);
    expect(DOWNLOAD_STOPS[nearestStop(DOWNLOAD_STOPS, 40_000)]).toBe(30_000);
  });

  it("never picks the infinite stop for a finite value", () => {
    expect(SIZE_STOPS[nearestStop(SIZE_STOPS, 10_000)]).toBe(400);
  });

  it("places the infinite stop itself", () => {
    expect(nearestStop(SIZE_STOPS, Infinity)).toBe(SIZE_STOPS.length - 1);
  });
});

describe("capValue / capStop", () => {
  it("round-trip, with 0 as the API's 'no limit'", () => {
    expect(capValue(Infinity)).toBe(0);
    expect(capValue(120)).toBe(120);
    expect(capStop(0)).toBe(Infinity);
    expect(capStop(120)).toBe(120);
  });
});

describe("labels", () => {
  it("size", () => {
    expect(fmtParamsStop(0.5)).toBe("0.5B");
    expect(fmtParamsStop(70)).toBe("70B");
    expect(fmtParamsStop(Infinity)).toBe("Any size");
  });

  it("downloads", () => {
    expect(fmtDownloadsStop(0)).toBe("Any");
    expect(fmtDownloadsStop(300)).toBe("300+");
    expect(fmtDownloadsStop(10_000)).toBe("10k+");
    expect(fmtDownloadsStop(1_000_000)).toBe("1M+");
  });

  it("age", () => {
    expect(fmtAgeStop(1)).toBe("1 day");
    expect(fmtAgeStop(14)).toBe("14 days");
    expect(fmtAgeStop(180)).toBe("6 months");
    expect(fmtAgeStop(365)).toBe("1 year");
    expect(fmtAgeStop(730)).toBe("2 years");
    expect(fmtAgeStop(Infinity)).toBe("Any age");
  });
});
