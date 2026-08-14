// Shopping report cards.
//
// Stage 3 of the shopping assistant used to be a Markdown table, which cannot
// carry a picture and reads as a wall of cells. Instead the model ends its turn
// with a fenced ```products block of JSON; this parses it out and
// `ProductReport.svelte` renders one card per option. Same shape as the ```ask
// block (askBlock.ts): a malformed block must fall through to an ordinary code
// fence rather than swallow the answer, and an unterminated one is hidden while
// it streams instead of showing half-written JSON.
export interface Product {
  name: string;
  price: string;
  shop: string;
  // Absolute http(s) URL of a product image, copied from a fetched page. Empty
  // when the model had none — the card then shows a monogram, never a broken box.
  image: string;
  url: string;
  specs: string[];
  why: string;
  // Short label ("Best value", "Cheapest"). Optional and free-text: the useful
  // distinctions differ per category and a fixed enum would just be ignored.
  badge: string;
  // Source number matching the citation chips, when the model attached one.
  cite: number | null;
}

export interface ProductBlock {
  products: Product[];
  // The model's own one-line verdict, shown above the grid.
  pick: string;
  // The message text with the fence removed — what actually gets rendered.
  cleaned: string;
}

export interface ProductSplit {
  cleaned: string;
  report: ProductBlock | null;
  // The fence is still being written (streaming); the caller shows a placeholder.
  pending: boolean;
}

const FENCE = /```products[ \t]*\r?\n([\s\S]*?)```[ \t]*(?:\r?\n|$)/i;
const OPEN_FENCE = /```products[ \t]*(\r?\n|$)/i;

// Cards per report. A shortlist is the point - twenty cards is a search results
// page, which is what the user came here to avoid.
const MAX_PRODUCTS = 8;

// Bracketed source markers belong on the card's own cite chip, not inside a
// product name, a spec or - worst - a URL, where `…/widget[2]` is a dead link.
// Models append them to every field out of habit, so they are stripped on the
// way in rather than argued about in the prompt.
const CITE_MARK = /\s*\[\d+\](?=\s|$|[.,;:)])|\[\d+\]/g;

function str(v: unknown): string {
  if (typeof v === "number") return String(v);
  if (typeof v !== "string") return "";
  return v.replace(CITE_MARK, " ").replace(/\s{2,}/g, " ").trim();
}

function specList(v: unknown): string[] {
  if (typeof v === "string") return v.split(/\s*[;\n]\s*/).filter(Boolean).slice(0, 6);
  if (!Array.isArray(v)) return [];
  return v.map(str).filter(Boolean).slice(0, 6);
}

// Only absolute http(s) survives. A relative path or a `data:`/`javascript:` URL
// is either broken or hostile, and both render as no image at all.
function webUrl(v: unknown): string {
  const s = str(v);
  return /^https?:\/\/\S+$/i.test(s) ? s : "";
}

function citeNum(v: unknown): number | null {
  const n = typeof v === "number" ? v : typeof v === "string" ? parseInt(v.replace(/\D+/g, ""), 10) : NaN;
  return Number.isFinite(n) && n > 0 ? n : null;
}

/**
 * Pull the ```products block out of an assistant message. Returns null when
 * there isn't one, or when it holds nothing usable.
 */
export function parseProductBlock(content: string): ProductBlock | null {
  const m = FENCE.exec(content);
  if (!m) return null;

  let raw: unknown;
  try {
    raw = JSON.parse(m[1]);
  } catch {
    return null;
  }
  const root = raw as { products?: unknown; options?: unknown; pick?: unknown; verdict?: unknown };
  const list = Array.isArray(raw) ? raw : Array.isArray(root?.products) ? root.products : root?.options;
  if (!Array.isArray(list)) return null;

  const products: Product[] = [];
  for (const p of list) {
    if (!p || typeof p !== "object") continue;
    const o = p as Record<string, unknown>;
    const name = str(o.name) || str(o.title) || str(o.product);
    // A card with no name is not a product, it's noise from a half-followed
    // schema — dropped rather than rendered as an empty tile.
    if (!name) continue;
    products.push({
      name,
      price: str(o.price) || str(o.cost),
      shop: str(o.shop) || str(o.seller) || str(o.store),
      image: webUrl(o.image ?? o.img ?? o.thumbnail),
      url: webUrl(o.url ?? o.link),
      specs: specList(o.specs ?? o.features),
      why: str(o.why) || str(o.reason) || str(o.summary),
      badge: str(o.badge) || str(o.tag),
      cite: citeNum(o.cite ?? o.source),
    });
    if (products.length >= MAX_PRODUCTS) break;
  }
  if (products.length === 0) return null;

  return {
    products,
    pick: Array.isArray(raw) ? "" : str(root?.pick) || str(root?.verdict),
    cleaned: (content.slice(0, m.index) + content.slice(m.index + m[0].length)).trim(),
  };
}

/**
 * Split an assistant message into prose + product report, including the
 * mid-stream case: an unterminated ```products fence is cut from the prose and
 * reported as `pending`, so a growing wall of JSON is never shown.
 */
export function splitProducts(content: string): ProductSplit {
  const done = parseProductBlock(content);
  if (done) return { cleaned: done.cleaned, report: done, pending: false };

  // A closed-but-broken fence stays in the text (renders as a code block); only
  // an unterminated one is hidden, since it is still being written.
  if (FENCE.test(content)) return { cleaned: content, report: null, pending: false };
  const open = OPEN_FENCE.exec(content);
  if (open) return { cleaned: content.slice(0, open.index).trimEnd(), report: null, pending: true };
  return { cleaned: content, report: null, pending: false };
}

/**
 * Rewrite a remote image URL through the server-side proxy. Direct hotlinking
 * fails often enough to be the wrong default: shop CDNs reject a foreign
 * Referer, and the proxy also keeps the browser from talking to every shop the
 * assistant visited. See internal/server/imgproxy.go.
 */
export function proxiedImage(url: string): string {
  return `/api/imgproxy?url=${encodeURIComponent(url)}`;
}

// --- product link repair ------------------------------------------------------
//
// The card's whole promise is "here is the thing, go buy it", so a `url` that
// lands on a shop's search page is a broken card: the user has to redo the search
// the assistant already did. Models produce these by copying the URL from a
// web_search hit (which is often a listing) instead of from the fetch_page result
// header of the product page they actually read.
//
// The prompt says to use the fetched page's URL; this is the backstop, matching a
// listing-shaped URL against the pages the turn genuinely opened.

const LISTING_HINTS = ["/search", "/catalogsearch", "/s?", "/sok", "/suche", "/recherche", "/find/", "/browse", "/category/", "/c/"];
const LISTING_PARAMS = ["q", "query", "search", "keyword", "keywords", "k", "s", "term"];

/** True when a URL looks like a search or category listing rather than one product. */
export function isListingUrl(raw: string): boolean {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return false;
  }
  const path = u.pathname.toLowerCase();
  if (LISTING_HINTS.some((h) => path.includes(h.replace("?", "")) && h !== "/s?")) return true;
  for (const p of LISTING_PARAMS) if (u.searchParams.has(p)) return true;
  return false;
}

/** Significant words of a product name — the ones worth matching a title on. */
function nameTokens(name: string): string[] {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, " ")
    .split(" ")
    .filter((w) => w.length > 1 && !["the", "and", "for", "with", "pro", "new"].includes(w));
}

/**
 * Replace listing/missing product URLs with the page the turn actually fetched
 * for that product, matched on title. Deliberately strict — EVERY significant
 * word of the product name must appear in the page title, and an ambiguous match
 * (two pages qualify) is left alone. Linking a card to the wrong product is worse
 * than linking it to a search page.
 */
export function repairProductUrls(products: Product[], pages: { title: string; url: string }[]): Product[] {
  if (!pages.length) return products;
  return products.map((p) => {
    if (p.url && !isListingUrl(p.url)) return p;
    const tokens = nameTokens(p.name);
    if (tokens.length < 2) return p; // too generic to match on safely
    const hits = pages.filter((pg) => {
      const t = pg.title.toLowerCase();
      return tokens.every((w) => t.includes(w));
    });
    return hits.length === 1 ? { ...p, url: hits[0].url } : p;
  });
}
