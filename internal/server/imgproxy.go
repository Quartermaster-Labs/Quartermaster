package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GET /api/imgproxy?url=<image url> — fetch one remote image and re-serve it.
//
// The shopping report shows a product picture per card, and those URLs come off
// shop CDNs. Hotlinking them straight from an <img> mostly fails: CDNs reject a
// foreign Referer, some 403 unknown origins outright, and an http-only image on
// a page served over https is blocked as mixed content. A browser <img> can send
// no custom header, so the fix has to be server-side.
//
// Uses the SAME client as fetch_page — i.e. the same SSRF guard on the resolved
// IP of every dial, and Proxy: nil. That is load-bearing here for the same
// reason: the URL originates from the model, which got it from a web page.
const (
	imgProxyMaxBytes = 8 << 20
	imgProxyTimeout  = 20 * time.Second
)

func (s *Server) handleAPIImageProxy(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("url"))
	if raw == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "url must be an absolute http(s) URL", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), imgProxyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// No Referer on purpose: sending this box's own origin is what gets the
	// request refused by hotlink protection, and sending the shop's own page URL
	// would be a forgery. Absent reads as a direct hit, which CDNs allow.
	req.Header.Set("User-Agent", pageUserAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/*;q=0.8")

	resp, err := pageClient().Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("HTTP %d from %s", resp.StatusCode, u.Host), http.StatusBadGateway)
		return
	}

	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	// SVG is a document, not a picture: served same-origin it can run script, so
	// it is refused rather than sanitized. Everything else must still declare
	// itself an image — this endpoint must not become a general file proxy.
	if !strings.HasPrefix(ct, "image/") || ct == "image/svg+xml" {
		http.Error(w, "not an image", http.StatusUnsupportedMediaType)
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// Product shots don't change; the chat re-renders this on every scroll and
	// reload, and each miss is a round trip to a foreign CDN.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// Content-Length deliberately not forwarded: the copy below is capped, so an
	// oversized (or lying) upstream length would promise bytes we never send and
	// the browser would report a truncated response instead of a short image.
	io.Copy(w, io.LimitReader(resp.Body, imgProxyMaxBytes))
}
