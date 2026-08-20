package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
	"golang.org/x/net/html"
)

// The `fetch_page` tool: read ONE web page server-side and hand the model its
// text. Exists because search snippets are not enough for anything price- or
// spec-sensitive — a snippet is a stale, truncated fragment, and a model
// summarizing it as "the current price" is confidently wrong. This reads the
// actual page.
//
// Deliberately NOT a browser: no JS execution, no clicking. Shops that render
// their price client-side fail loudly here (the text simply won't hold a price)
// rather than being guessed at. A headless-browser fallback is the planned
// upgrade path (TODO 9b), not a prerequisite.

const (
	pageTimeout  = 25 * time.Second
	pageMaxBytes = 4 << 20 // read cap off the wire
	pageMaxChars = 12000   // text handed to the model (~3k tokens)
	pageMaxLD    = 6000    // JSON-LD block cap (schema.org Product/Offer = the real price)
	pageCacheTTL = 15 * time.Minute
	pageCacheMax = 64
	// Product images handed to the model. Image URLs are long (CDN paths carry
	// signing junk), so this is a hard few — enough for a report card, not
	// enough to spend the window on a gallery.
	pageMaxImages  = 3
	pageImgCandMax = 24
	pageUserAgent  = "Mozilla/5.0 (compatible; quartermaster/1.0; +local assistant)"

	// maxFetches caps pages read per turn. At pageMaxChars each this is already
	// ~24k tokens of tool output, which is most of a 32k window — a shopping
	// comparison wants breadth, but not at the cost of the conversation.
	maxFetches = 8
)

type pageDoc struct {
	URL       string
	Title     string
	Text      string
	Data      string // compacted JSON-LD, when the page carries any
	Images    []string
	FetchedAt time.Time
	Truncated bool
}

// --- SSRF guard -------------------------------------------------------------

// The URL comes from the MODEL, so the fetcher must not become a proxy into the
// machine's own network: quartermaster's admin API is on loopback and NOT
// API-key gated (see admin.go), and a LAN box or a cloud metadata endpoint is
// one guessed hostname away. The check runs in the dialer's Control hook, i.e.
// on the ALREADY-RESOLVED ip of every connection — which also covers redirects
// and DNS-rebinding (a name that resolves public once and private on the second
// lookup still gets checked at the dial that matters).
func guardDial(network, address string, _ syscall.RawConn) error {
	if network != "tcp4" && network != "tcp6" && network != "tcp" {
		return fmt.Errorf("blocked network %q", network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("unresolvable address %q", address)
	}
	if !isPublicIP(ip) {
		return fmt.Errorf("blocked non-public address %s", ip)
	}
	return nil
}

// isPublicIP is shared.IsPublicIP: the yt-dlp executor enforces the same rule
// on the host it is about to hand the downloader (internal/tools/youtube.go),
// and two copies of a security check are two chances to fix only one of them.
var isPublicIP = shared.IsPublicIP

var pageClient = sync.OnceValue(func() *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: guardDial}
	return &http.Client{
		Timeout: pageTimeout,
		// Proxy deliberately nil: an http proxy would dial on our behalf, and the
		// address guard (which only sees the proxy's own ip) could not vet the
		// real destination.
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConnsPerHost:   2,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
})

// --- fetch ------------------------------------------------------------------

type pageCacheEntry struct {
	doc *pageDoc
	at  time.Time
}

var (
	pageCacheMu sync.Mutex
	pageCache   = map[string]pageCacheEntry{}
)

// fetchPage GETs one page and reduces it to text (+ any JSON-LD). Same
// exec-shaped contract as the other tools: every failure is an explicit error
// the model is told about, never a silent empty document it can narrate over.
func fetchPage(ctx context.Context, raw string) (*pageDoc, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("not a URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http(s) URLs can be fetched, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("URL has no host")
	}
	key := u.String()

	pageCacheMu.Lock()
	if e, ok := pageCache[key]; ok && time.Since(e.at) < pageCacheTTL {
		pageCacheMu.Unlock()
		return e.doc, nil
	}
	pageCacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", pageUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.8")
	req.Header.Set("Accept-Language", "en;q=0.9,*;q=0.5")

	resp, err := pageClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 403/429 here usually means the shop bot-blocks plain HTTP clients. Say
		// so plainly — that is a real, actionable outcome, not a transient blip.
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, u.Host)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "text/plain") && !strings.Contains(ct, "xml") {
		return nil, fmt.Errorf("unsupported content type %q (only web pages can be read)", strings.SplitN(ct, ";", 2)[0])
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, pageMaxBytes))
	if err != nil {
		return nil, err
	}

	doc := &pageDoc{URL: resp.Request.URL.String(), FetchedAt: time.Now()}
	if strings.Contains(ct, "text/plain") {
		doc.Text = strings.TrimSpace(string(body))
	} else {
		doc.Title, doc.Text, doc.Data, doc.Images = extractHTML(body, resp.Request.URL)
	}
	if doc.Text == "" && doc.Data == "" {
		return nil, errors.New("page carried no readable text (likely rendered by JavaScript)")
	}
	if len(doc.Text) > pageMaxChars {
		doc.Text = strings.ToValidUTF8(doc.Text[:pageMaxChars], "")
		doc.Truncated = true
	}

	pageCacheMu.Lock()
	if len(pageCache) >= pageCacheMax {
		pageCache = map[string]pageCacheEntry{} // cheap whole-map reset; entries are short-lived
	}
	pageCache[key] = pageCacheEntry{doc: doc, at: time.Now()}
	pageCacheMu.Unlock()
	return doc, nil
}

// --- extraction -------------------------------------------------------------

// Chrome/nav/legal furniture: dropping these is what turns 12k chars of shop
// page into 12k chars of actual product.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "svg": true, "canvas": true,
	"nav": true, "footer": true, "header": true, "aside": true, "form": true,
	"iframe": true, "template": true, "select": true, "button": true,
}

// Block-level elements get a newline so prices, spec rows and list items don't
// run into one another (a price glued to the next line is unparseable).
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "td": true, "th": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"section": true, "article": true, "dt": true, "dd": true, "hr": true, "table": true,
}

// extractHTML walks the parse tree and returns (title, text, json-ld, images).
// The JSON-LD is kept because schema.org Product/Offer blocks carry the price,
// currency and availability as DATA — far more reliable than reading them out
// of rendered text, and present on most real shops.
func extractHTML(body []byte, base *url.URL) (string, string, string, []string) {
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", "", "", nil
	}
	var title, ld strings.Builder
	var text strings.Builder
	var inTitle bool
	// Two buckets: page-declared hero images (og:image and friends — what a link
	// unfurl would show, i.e. the product) and ordinary <img> as the fallback.
	// Never interleaved: one good og:image beats twenty carousel thumbnails.
	var hero, imgs []string
	// <base href> overrides the response URL for relative resolution. Rare, but
	// when present every relative src on the page is wrong without it.
	docBase := base

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			name := strings.ToLower(n.Data)
			if name == "script" {
				if attrVal(n, "type") == "application/ld+json" && ld.Len() < pageMaxLD {
					if s := strings.TrimSpace(nodeText(n)); s != "" {
						ld.WriteString(s)
						ld.WriteString("\n")
					}
				}
				return
			}
			switch name {
			case "base":
				if h := attrRaw(n, "href"); h != "" && docBase != nil {
					if u, err := docBase.Parse(h); err == nil {
						docBase = u
					}
				}
			case "meta":
				if u := metaImageURL(n); u != "" && len(hero) < pageImgCandMax {
					hero = append(hero, u)
				}
			case "link":
				if attrVal(n, "rel") == "image_src" {
					if u := attrRaw(n, "href"); u != "" && len(hero) < pageImgCandMax {
						hero = append(hero, u)
					}
				}
			case "img":
				if u := imgSrc(n); u != "" && len(imgs) < pageImgCandMax {
					imgs = append(imgs, u)
				}
			}
			if name == "title" {
				inTitle = true
				defer func() { inTitle = false }()
			} else if skipTags[name] {
				return
			}
			if blockTags[name] {
				text.WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			if inTitle {
				title.WriteString(n.Data)
			} else {
				text.WriteString(n.Data)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[strings.ToLower(n.Data)] {
			text.WriteString("\n")
		}
	}
	walk(root)

	data := strings.TrimSpace(ld.String())
	if len(data) > pageMaxLD {
		data = strings.ToValidUTF8(data[:pageMaxLD], "") + "\n…(truncated)"
	}
	return collapseSpace(strings.TrimSpace(title.String())), squeezeLines(text.String()), data, pickImages(docBase, hero, imgs)
}

// attrVal returns an attribute LOWERCASED — for comparing against known keywords
// (`rel="image_src"`, `type="application/ld+json"`). Never use it for a URL: a
// CDN path is case-sensitive and lowercasing it yields a 404.
func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return strings.ToLower(strings.TrimSpace(a.Val))
		}
	}
	return ""
}

func attrRaw(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

// --- images -----------------------------------------------------------------

// The shopping report shows a picture per option, and the model can only put one
// there if it was handed the URL — the text extraction above deliberately drops
// every tag, so without this an <img> is invisible to it.

// heroMeta are the meta tags a page uses to declare "this is the picture OF this
// page" — the same ones a chat/social unfurl reads. On a product page that is the
// product shot, which is exactly what the card wants.
var heroMeta = map[string]bool{
	"og:image": true, "og:image:url": true, "og:image:secure_url": true,
	"twitter:image": true, "twitter:image:src": true,
}

func metaImageURL(n *html.Node) string {
	key := attrVal(n, "property")
	if key == "" {
		key = attrVal(n, "name")
	}
	if !heroMeta[key] {
		return ""
	}
	return attrRaw(n, "content")
}

// junkImg matches the furniture every shop page is full of — sprites, payment
// badges, flags, tracking pixels. A card showing a Visa logo is worse than a card
// showing nothing.
var junkImg = regexp.MustCompile(`(?i)sprite|logo|icon|favicon|placeholder|pixel|spinner|loading|badge|flag|payment|banner|avatar|1x1`)

// imgSrc picks the best URL off one <img>, skipping ones that are declared small
// (a 60px thumb is not a product shot) or obviously chrome. Lazy-loaded images
// keep their real URL in data-src/srcset while `src` holds a blank gif, so those
// are preferred over src, not ignored.
func imgSrc(n *html.Node) string {
	if w := attrVal(n, "width"); w != "" && smallDim(w) {
		return ""
	}
	if h := attrVal(n, "height"); h != "" && smallDim(h) {
		return ""
	}
	src := attrRaw(n, "data-src")
	if src == "" {
		src = firstSrcset(attrRaw(n, "srcset"))
	}
	if src == "" {
		src = firstSrcset(attrRaw(n, "data-srcset"))
	}
	if src == "" {
		src = attrRaw(n, "src")
	}
	if src == "" || strings.HasPrefix(strings.ToLower(src), "data:") {
		return ""
	}
	if junkImg.MatchString(src) {
		return ""
	}
	return src
}

// firstSrcset takes the first candidate of a `url 320w, url 640w` list. First,
// not largest: the widths are descriptors, the order is the author's, and a 3000px
// original is a slow proxy fetch for a card thumbnail.
func firstSrcset(s string) string {
	if s == "" {
		return ""
	}
	first := strings.TrimSpace(strings.SplitN(s, ",", 2)[0])
	return strings.TrimSpace(strings.SplitN(first, " ", 2)[0])
}

func smallDim(v string) bool {
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		if n >= 200 {
			return false
		}
	}
	return n > 0 && n < 200
}

// pickImages resolves candidates against the page URL, drops non-http and
// duplicates, and returns at most pageMaxImages — hero images first.
func pickImages(base *url.URL, hero, imgs []string) []string {
	out := make([]string, 0, pageMaxImages)
	seen := map[string]bool{}
	for _, list := range [][]string{hero, imgs} {
		for _, raw := range list {
			if len(out) >= pageMaxImages {
				return out
			}
			u, err := url.Parse(strings.TrimSpace(raw))
			if err != nil {
				continue
			}
			if base != nil {
				u = base.ResolveReference(u)
			}
			if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
				continue
			}
			s := u.String()
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// squeezeLines collapses intra-line whitespace and drops blank lines entirely —
// raw HTML text is mostly indentation, and every nested block element would
// otherwise contribute its own empty line (a spec table would come back double
// spaced, for no gain in readability and a doubled token count).
func squeezeLines(s string) string {
	var b strings.Builder
	b.Grow(len(s) / 2)
	for _, line := range strings.Split(s, "\n") {
		if line = collapseSpace(line); line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// formatPage renders the tool message. The fetch TIME is stated explicitly: a
// price is only true as of when it was read, and the model is expected to say
// so rather than present it as a standing fact.
func formatPage(doc *pageDoc, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## [%d] %s\n%s\nRead at %s.\n", n, orURL(doc.Title, doc.URL), doc.URL, doc.FetchedAt.Format("2006-01-02 15:04"))
	if doc.Data != "" {
		b.WriteString("\nStructured data on the page (schema.org JSON-LD - prices/availability here are the page's own machine-readable values):\n")
		b.WriteString(doc.Data)
		b.WriteString("\n")
	}
	if len(doc.Images) > 0 {
		b.WriteString("\nImages on this page (the first is the page's own main image - copy a URL verbatim into the report's `image` field; do not invent or edit one):\n")
		for _, u := range doc.Images {
			b.WriteString(u)
			b.WriteString("\n")
		}
	}
	if doc.Text != "" {
		b.WriteString("\nPage text:\n")
		b.WriteString(doc.Text)
	}
	if doc.Truncated {
		b.WriteString("\n\n[TRUNCATED - the page was longer than the read limit. Do not claim this is the whole page.]")
	}
	return b.String()
}
