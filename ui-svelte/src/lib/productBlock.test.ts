import { describe, it, expect } from "vitest";
import { isListingUrl, repairProductUrls, type Product } from "./productBlock";

function product(over: Partial<Product>): Product {
  return {
    name: "",
    price: "",
    shop: "",
    image: "",
    url: "",
    specs: [],
    why: "",
    badge: "",
    cite: null,
    ...over,
  };
}

describe("isListingUrl", () => {
  it("flags search and category pages", () => {
    expect(isListingUrl("https://www.proshop.dk/search?q=roborock")).toBe(true);
    expect(isListingUrl("https://shop.example.com/catalogsearch/result/?q=s8")).toBe(true);
    expect(isListingUrl("https://www.elgiganten.dk/category/hvidevarer")).toBe(true);
    expect(isListingUrl("https://example.com/produkt?query=roborock+s8")).toBe(true);
  });

  it("leaves real product pages alone", () => {
    expect(isListingUrl("https://www.proshop.dk/Robotstovsuger/Roborock-S8-MaxV-Ultra/3241234")).toBe(false);
    expect(isListingUrl("https://example.com/p/roborock-s8?variant=black")).toBe(false);
  });

  it("returns false for junk that is not a URL", () => {
    expect(isListingUrl("")).toBe(false);
    expect(isListingUrl("proshop roborock")).toBe(false);
  });
});

describe("repairProductUrls", () => {
  const pages = [
    { title: "Roborock S8 MaxV Ultra - Proshop.dk", url: "https://www.proshop.dk/p/3241234" },
    { title: "Dreame X40 Ultra | Elgiganten", url: "https://www.elgiganten.dk/product/x40" },
  ];

  it("swaps a search URL for the fetched product page", () => {
    const out = repairProductUrls(
      [product({ name: "Roborock S8 MaxV Ultra", url: "https://www.proshop.dk/search?q=roborock" })],
      pages,
    );
    expect(out[0].url).toBe("https://www.proshop.dk/p/3241234");
  });

  it("fills in a missing URL", () => {
    const out = repairProductUrls([product({ name: "Dreame X40 Ultra" })], pages);
    expect(out[0].url).toBe("https://www.elgiganten.dk/product/x40");
  });

  it("keeps a URL that is already a product page", () => {
    const keep = "https://www.proshop.dk/other/999";
    const out = repairProductUrls([product({ name: "Roborock S8 MaxV Ultra", url: keep })], pages);
    expect(out[0].url).toBe(keep);
  });

  it("leaves the URL alone when no page title matches every word", () => {
    const search = "https://www.proshop.dk/search?q=roborock+q7";
    const out = repairProductUrls([product({ name: "Roborock Q7 Max", url: search })], pages);
    expect(out[0].url).toBe(search);
  });

  it("refuses an ambiguous match rather than guessing", () => {
    const two = [
      { title: "Roborock S8 - shop A", url: "https://a.example/1" },
      { title: "Roborock S8 - shop B", url: "https://b.example/2" },
    ];
    const search = "https://a.example/search?q=roborock";
    const out = repairProductUrls([product({ name: "Roborock S8", url: search })], two);
    expect(out[0].url).toBe(search);
  });

  it("does not match on a one-token name", () => {
    const search = "https://a.example/search?q=roborock";
    const out = repairProductUrls([product({ name: "Roborock", url: search })], pages);
    expect(out[0].url).toBe(search);
  });

  it("is a no-op when the turn fetched no pages", () => {
    const out = repairProductUrls([product({ name: "Roborock S8 MaxV Ultra" })], []);
    expect(out[0].url).toBe("");
  });
});
