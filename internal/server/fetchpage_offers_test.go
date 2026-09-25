package server

import (
	"strings"
	"testing"
	"time"
)

// Shape taken from a real pricerunner.dk results + offers page: the listing is
// React-Query state inside <script type="application/json">, prices are
// {amount, currency} objects, and an offer names its shop only by merchantId
// into a sibling `merchants` map. The filter facets have the same name+price
// shape and must not be reported as products.
const pageWithState = `<html><head><title>wd red plus 8tb - PriceRunner</title></head><body>
<div id="root"></div>
<script id="initial_payload" type="application/json">{"state":{"queries":[
 {"data":{"pages":[{"products":[
   {"id":"3340431055","name":"Western Digital Red Plus WD80EFPX 8TB","url":"/pl/36-3340431055/Harddiske/WD80EFPX",
    "lowestPrice":{"amount":"3046.00","currency":"DKK"},"category":{"id":"cl36","name":"Harddiske"}},
   {"id":"3200067626","name":"Western Digital Red Plus NAS WD80EFBX 256MB 8TB","url":"/pl/36-3200067626/Harddiske/WD80EFBX",
    "lowestPrice":{"amount":"2698.96","currency":"DKK"}}]}]}},
 {"data":{
   "filters":[{"filterOptions":[{"id":"OUT_OF_STOCK","name":"OUT_OF_STOCK","lowestPrice":{"amount":"2698.96","currency":"DKK"}}]}],
   "merchants":{"21631":{"id":"21631","name":"Skiftselv.dk"}},
   "offers":[{"name":"WD Red Plus NAS 3,5\" 5400 rpm","url":"https://clk.example/go/1f03","stockStatus":"IN_STOCK",
     "price":{"amount":"2698.96","currency":"DKK"},"shippingCost":{"amount":"0.00","currency":"DKK"},"merchantId":"21631"}]}}
]}}</script>
<script type="application/json">not json at all</script>
</body></html>`

func TestExtractHTML_EmbeddedStateOffers(t *testing.T) {
	x := extractHTML([]byte(pageWithState), mustURL(t, "https://www.pricerunner.dk/results?q=wd"))
	if x.Text != "" {
		t.Errorf("state JSON leaked into page text: %q", x.Text)
	}
	got := harvestOffers(x.State, mustURL(t, "https://www.pricerunner.dk/results?q=wd"))

	want := []string{
		"- Western Digital Red Plus WD80EFPX 8TB | 3046.00 DKK | https://www.pricerunner.dk/pl/36-3340431055/Harddiske/WD80EFPX",
		"- Western Digital Red Plus NAS WD80EFBX 256MB 8TB | 2698.96 DKK | https://www.pricerunner.dk/pl/36-3200067626/Harddiske/WD80EFBX",
		"- WD Red Plus NAS 3,5\" 5400 rpm | 2698.96 DKK | shop: Skiftselv.dk | shipping: 0.00 DKK | stock: IN_STOCK | https://clk.example/go/1f03",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing line %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "OUT_OF_STOCK |") {
		t.Errorf("filter facet reported as a product:\n%s", got)
	}
	if strings.Contains(got, "Harddiske |") {
		t.Errorf("price-less category reported as a product:\n%s", got)
	}
	// Listing order is the page's own order (arrays are walked in sequence).
	if strings.Index(got, "WD80EFPX") > strings.Index(got, "WD80EFBX 256MB") {
		t.Errorf("products out of page order:\n%s", got)
	}
	// Deterministic: the same page always yields the same bytes (cache + KV prefix).
	for range 5 {
		if again := harvestOffers(x.State, mustURL(t, "https://www.pricerunner.dk/results?q=wd")); again != got {
			t.Fatalf("harvest not deterministic:\n%s\n---\n%s", got, again)
		}
	}
}

func TestHarvestOffers_PriceShapesAndCaps(t *testing.T) {
	blob := `{"items":[
	  {"title":"Bare number","price":1299,"currency":"EUR","availability":"https://schema.org/InStock"},
	  {"productName":"Numeric string","salePrice":"499,-","inStock":false,"url":"javascript:void(0)"},
	  {"name":"Not a price","price":"call us"},
	  {"name":"Null price","price":null}]}`
	got := harvestOffers([]string{blob}, mustURL(t, "https://shop.example/"))
	for _, w := range []string{
		"- Bare number | 1299 EUR | stock: InStock",
		"- Numeric string | 499,- | stock: out of stock",
	} {
		if !strings.Contains(got, w+"\n") && !strings.HasSuffix(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "javascript:") || strings.Contains(got, "Not a price") || strings.Contains(got, "Null price") {
		t.Errorf("junk harvested:\n%s", got)
	}

	var many strings.Builder
	many.WriteString(`[`)
	for i := range 200 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"name":"Product number `)
		many.WriteString(strings.Repeat("x", i%7))
		many.WriteString(string(rune('a' + i%26)))
		many.WriteString(`","price":{"amount":"` + strings.Repeat("9", 1+i%5) + `","currency":"DKK"}}`)
	}
	many.WriteString(`]`)
	capped := harvestOffers([]string{many.String()}, nil)
	if n := strings.Count(capped, "\n") + 1; n > pageMaxOffers {
		t.Errorf("%d lines, cap is %d", n, pageMaxOffers)
	}
	if len(capped) > pageMaxOfferChars {
		t.Errorf("%d bytes, cap is %d", len(capped), pageMaxOfferChars)
	}
}

// Proshop wraps each listing tile's price in its add-to-basket <form>. Skipping
// forms as chrome is what made the page read as empty.
func TestExtractHTML_KeepsFormContent(t *testing.T) {
	page := `<html><body><h1>Søgeresultater</h1>
<form action="/Basket/AddItem" method="post"><div class="site-currency-lg">2.699,-</div><button>Læg i kurv</button></form>
</body></html>`
	x := extractHTML([]byte(page), mustURL(t, "https://www.proshop.dk/"))
	if !strings.Contains(x.Text, "2.699,-") {
		t.Errorf("price inside <form> dropped: %q", x.Text)
	}
	if strings.Contains(x.Text, "Læg i kurv") {
		t.Errorf("button chrome leaked: %q", x.Text)
	}
}

func TestFormatPage_Offers(t *testing.T) {
	doc := &pageDoc{URL: "https://www.pricerunner.dk/results?q=wd", Title: "wd", FetchedAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		Offers: "- WD Red | 2698.96 DKK"}
	out := formatPage(doc, 1)
	if !strings.Contains(out, "embedded data") || !strings.Contains(out, "- WD Red | 2698.96 DKK") {
		t.Errorf("offers section missing:\n%s", out)
	}
}
