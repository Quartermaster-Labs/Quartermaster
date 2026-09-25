package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Embedded app state: the product list a JS-built shop page draws itself from.
//
// Next.js / Nuxt / React-Query shops (PriceRunner, many chain stores) ship the
// whole result set as a `<script type="application/json">` blob and build the
// visible listing from it in the browser. The HTML text walk sees none of that,
// so the page looked "rendered by JavaScript" when the prices were in the bytes
// all along. This reads those blobs and keeps ONLY what a shopping answer needs:
// one line per object that carries both a name and a price. The rest of the
// blob (menus, translations, feature flags) is hundreds of KB of noise and is
// never shown to the model.

const (
	pageMaxOffers      = 40   // lines handed to the model
	pageMaxOfferChars  = 5000 // and their byte budget (~1.2k tokens)
	pageMaxStateBlob   = 4 << 20
	offerMaxDepth      = 48
	offerMaxNameLength = 160
)

var (
	offerNameKeys     = []string{"name", "title", "productName", "displayName"}
	offerPriceKeys    = []string{"price", "salePrice", "currentPrice", "finalPrice", "lowestPrice", "minPrice", "priceInclVat", "offerPrice"}
	offerCurrencyKeys = []string{"currency", "priceCurrency", "currencyCode"}
	offerURLKeys      = []string{"url", "productUrl", "href", "link"}
	offerStockKeys    = []string{"stockStatus", "availability", "inStock", "stock"}
	// A merchant is either named inline or referenced by id into a sibling
	// table (PriceRunner's offers carry `merchantId`, the names sit in a
	// `merchants` map) — the id form is resolved against every id/name pair in
	// the blob.
	offerShopNameKeys = []string{"merchantName", "shopName", "storeName", "sellerName", "retailerName", "merchant", "seller", "shop", "store"}
	offerShopIDKeys   = []string{"merchantId", "shopId", "storeId", "sellerId", "retailerId"}
)

// enumName is an UPPER_SNAKE identifier. Filter facets ("OUT_OF_STOCK" with a
// lowestPrice) have the name+price shape of a product and are not one.
var enumName = regexp.MustCompile(`^[A-Z0-9_]+$`)

// harvestOffers turns the page's embedded JSON blobs into model-facing lines.
// Deterministic (map keys are walked sorted, arrays in order) so the same page
// always yields the same text — it is cached and it lands in the KV prefix of
// every later turn.
func harvestOffers(blobs []string, base *url.URL) string {
	var roots []any
	for _, b := range blobs {
		if len(b) > pageMaxStateBlob {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(b))
		dec.UseNumber() // "2698.96" must stay "2698.96", not 2698.9600000000001
		var v any
		if dec.Decode(&v) == nil {
			roots = append(roots, v)
		}
	}
	if len(roots) == 0 {
		return ""
	}

	names := map[string]string{} // id -> name, for merchantId lookups
	for _, r := range roots {
		indexNames(r, names, 0)
	}

	var out bytes.Buffer
	seen := map[string]bool{}
	n := 0
	var walk func(v any, depth int) bool
	walk = func(v any, depth int) bool {
		if depth > offerMaxDepth {
			return true
		}
		switch t := v.(type) {
		case map[string]any:
			if line := offerLine(t, names, base); line != "" && !seen[line] {
				if n >= pageMaxOffers || out.Len()+len(line) > pageMaxOfferChars {
					return false
				}
				seen[line] = true
				out.WriteString(line)
				out.WriteByte('\n')
				n++
			}
			for _, k := range sortedKeys(t) {
				if !walk(t[k], depth+1) {
					return false
				}
			}
		case []any:
			for _, e := range t {
				if !walk(e, depth+1) {
					return false
				}
			}
		}
		return true
	}
	for _, r := range roots {
		if !walk(r, 0) {
			break
		}
	}
	return strings.TrimSpace(out.String())
}

// offerLine renders one name+price object, or "" when m is not one.
func offerLine(m map[string]any, names map[string]string, base *url.URL) string {
	name := firstString(m, offerNameKeys)
	if name == "" || enumName.MatchString(name) {
		return ""
	}
	var price, cur string
	for _, k := range offerPriceKeys {
		if price, cur = priceOf(m[k]); price != "" {
			break
		}
	}
	if price == "" {
		return ""
	}
	if cur == "" {
		cur = firstString(m, offerCurrencyKeys)
	}
	if len(name) > offerMaxNameLength {
		name = strings.ToValidUTF8(name[:offerMaxNameLength], "") + "…"
	}

	var b strings.Builder
	b.WriteString("- ")
	b.WriteString(name)
	b.WriteString(" | ")
	b.WriteString(price)
	if cur != "" {
		b.WriteString(" ")
		b.WriteString(cur)
	}
	if shop := shopOf(m, names); shop != "" && shop != name {
		b.WriteString(" | shop: ")
		b.WriteString(shop)
	}
	if s, c := priceOf(m["shippingCost"]); s != "" {
		fmt.Fprintf(&b, " | shipping: %s", strings.TrimSpace(s+" "+c))
	}
	if st := stockOf(m); st != "" {
		b.WriteString(" | stock: ")
		b.WriteString(st)
	}
	if u := linkOf(m, base); u != "" {
		b.WriteString(" | ")
		b.WriteString(u)
	}
	return b.String()
}

// priceOf reads a price in the three shapes seen in the wild: a bare number, a
// numeric string, or an object {amount|value|price, currency}.
func priceOf(v any) (string, string) {
	switch t := v.(type) {
	case json.Number:
		return t.String(), ""
	case string:
		s := strings.TrimSpace(t)
		if s != "" && strings.ContainsAny(s, "0123456789") && len(s) <= 32 {
			return s, ""
		}
	case map[string]any:
		for _, k := range []string{"amount", "value", "price", "current"} {
			if p, _ := priceOf(t[k]); p != "" {
				return p, firstString(t, offerCurrencyKeys)
			}
		}
	}
	return "", ""
}

func shopOf(m map[string]any, names map[string]string) string {
	for _, k := range offerShopNameKeys {
		switch t := m[k].(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case map[string]any:
			if s := firstString(t, offerNameKeys); s != "" {
				return s
			}
		}
	}
	for _, k := range offerShopIDKeys {
		if id := scalarString(m[k]); id != "" {
			if s := names[id]; s != "" {
				return s
			}
		}
	}
	return ""
}

func stockOf(m map[string]any) string {
	for _, k := range offerStockKeys {
		switch t := m[k].(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" && len(s) <= 48 {
				// schema.org writes "https://schema.org/InStock"; the tail is the value.
				return s[strings.LastIndex(s, "/")+1:]
			}
		case bool:
			if t {
				return "in stock"
			}
			return "out of stock"
		}
	}
	return ""
}

// linkOf resolves the object's own link against the page. Only http(s) is
// kept: a `javascript:` or app-scheme href is not something to hand the model
// as a place to buy.
func linkOf(m map[string]any, base *url.URL) string {
	raw := firstString(m, offerURLKeys)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// indexNames records every {id, name} pair so a `merchantId` elsewhere in the
// blob can be named. First writer wins: a colliding id across entity types is
// possible in principle, and a stable wrong-ish answer beats a flapping one.
func indexNames(v any, names map[string]string, depth int) {
	if depth > offerMaxDepth {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		if id := scalarString(t["id"]); id != "" {
			if nm, _ := t["name"].(string); nm != "" {
				if _, ok := names[id]; !ok {
					names[id] = strings.TrimSpace(nm)
				}
			}
		}
		for _, e := range t {
			indexNames(e, names, depth+1)
		}
	case []any:
		for _, e := range t {
			indexNames(e, names, depth+1)
		}
	}
}

func firstString(m map[string]any, keys []string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			if s = collapseSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	}
	return ""
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
