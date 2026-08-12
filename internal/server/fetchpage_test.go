package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFetchPage_BlocksNonPublicAddresses(t *testing.T) {
	// The URL comes from the model, so a loopback target (this test server, but
	// equally quartermaster's own un-keyed admin API) must be refused at dial.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>secret</body></html>"))
	}))
	defer srv.Close()

	if _, err := fetchPage(context.Background(), srv.URL); err == nil {
		t.Fatal("expected loopback fetch to be blocked, got success")
	}
}

func TestIsPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "192.168.1.5", "172.16.0.1", "169.254.169.254",
		"0.0.0.0", "100.64.1.2", "224.0.0.1", "240.0.0.1", "::1", "fd00::1", "fe80::1",
	}
	for _, s := range blocked {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "2606:2800:220:1::1"} {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestFetchPage_RejectsNonHTTPScheme(t *testing.T) {
	for _, u := range []string{"file:///C:/windows/win.ini", "ftp://example.com/x", "notaurl"} {
		if _, err := fetchPage(context.Background(), u); err == nil {
			t.Errorf("%s: expected error", u)
		}
	}
}

func TestExtractHTML(t *testing.T) {
	page := `<html><head><title> Widget  Pro </title>
<script type="application/ld+json">{"@type":"Product","offers":{"price":"299.00","priceCurrency":"EUR"}}</script>
<script>var tracking = "noise";</script><style>.a{color:red}</style></head>
<body><nav>Home Shop Cart</nav><h1>Widget Pro</h1><p>Fast   widget.</p>
<table><tr><td>Weight</td><td>1.2 kg</td></tr></table>
<footer>© shop</footer></body></html>`

	title, text, ld, _ := extractHTML([]byte(page), mustURL(t, "https://shop.example/p/widget"))
	if title != "Widget Pro" {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(ld, `"price":"299.00"`) {
		t.Errorf("json-ld not extracted: %q", ld)
	}
	if strings.Contains(text, "tracking") || strings.Contains(text, "color:red") {
		t.Errorf("script/style leaked into text: %q", text)
	}
	if strings.Contains(text, "Home Shop Cart") || strings.Contains(text, "© shop") {
		t.Errorf("nav/footer chrome leaked into text: %q", text)
	}
	if !strings.Contains(text, "Fast widget.") {
		t.Errorf("body text missing/uncollapsed: %q", text)
	}
	// Table cells must not run together — a price glued to the next cell is
	// unparseable for the model.
	if !strings.Contains(text, "Weight\n1.2 kg") {
		t.Errorf("table cells not line-separated: %q", text)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// The report cards can only show a picture the model was handed, so image
// extraction is a feature of the tool output, not a nicety: og:image first,
// relative paths absolutised, chrome and tiny images dropped.
func TestExtractHTML_Images(t *testing.T) {
	page := `<html><head><title>Widget</title>
<meta property="og:image" content="/img/hero.JPG">
<link rel="image_src" href="https://cdn.example/alt.png"></head>
<body><header><img src="/img/logo.png" width="800"></header>
<img src="/img/sprite-set.png"><img src="/img/thumb.png" width="80">
<img data-src="https://cdn.example/gallery-1.jpg" src="data:image/gif;base64,R0lGOD">
<img srcset="https://cdn.example/g2-320.jpg 320w, https://cdn.example/g2-640.jpg 640w">
</body></html>`

	_, _, _, imgs := extractHTML([]byte(page), mustURL(t, "https://shop.example/p/widget"))
	if len(imgs) == 0 {
		t.Fatal("no images extracted")
	}
	// og:image wins, resolved against the page URL with its case intact.
	if imgs[0] != "https://shop.example/img/hero.JPG" {
		t.Errorf("first image = %q, want the resolved og:image", imgs[0])
	}
	if len(imgs) > pageMaxImages {
		t.Errorf("got %d images, cap is %d", len(imgs), pageMaxImages)
	}
	joined := strings.Join(imgs, " ")
	for _, bad := range []string{"logo", "sprite", "thumb", "data:"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%q leaked into images: %v", bad, imgs)
		}
	}
	// Hero images come first as a block; the lazy/srcset <img> URLs only fill
	// what's left, and only in their real form (never the blank-gif src).
	if !strings.Contains(joined, "https://cdn.example/alt.png") {
		t.Errorf("link rel=image_src missing: %v", imgs)
	}
}

func TestExtractHTML_ImagesBaseTag(t *testing.T) {
	page := `<html><head><base href="https://cdn.example/assets/">
<meta property="og:image" content="hero.png"></head><body><p>x</p></body></html>`
	_, _, _, imgs := extractHTML([]byte(page), mustURL(t, "https://shop.example/p/widget"))
	if len(imgs) != 1 || imgs[0] != "https://cdn.example/assets/hero.png" {
		t.Errorf("base href ignored: %v", imgs)
	}
}

func TestParseFetchArgs(t *testing.T) {
	cases := map[string]string{
		`{"url":" https://example.com/p "}`: "https://example.com/p",
		`{"link":"https://example.com/q"}`:  "https://example.com/q",
		`{}`:                                "",
		`not json`:                          "",
	}
	for args, want := range cases {
		if got := parseFetchArgs(args); got != want {
			t.Errorf("parseFetchArgs(%s) = %q, want %q", args, got, want)
		}
	}
}
