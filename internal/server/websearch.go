package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var webSearchClient = &http.Client{Timeout: 15 * time.Second}

// handleAPIWebSearch proxies a SearXNG JSON query for the chat playground so the
// browser reaches it same-origin and dodges CORS (SearXNG ships no CORS headers).
// Takes ?url=<searxng base>&q=<query>, forwards to <base>/search?format=json and
// streams the JSON back.
//
// ponytail: open forwarder — it fetches whatever ?url= points at (SSRF). Fine for
// a local single-user tool; restrict to a configured allowlist if ever exposed.
func (s *Server) handleAPIWebSearch(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(strings.TrimSpace(r.URL.Query().Get("url")), "/")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if base == "" || q == "" {
		http.Error(w, "missing url or q", http.StatusBadRequest)
		return
	}

	target, err := url.Parse(base + "/search")
	if err != nil {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		http.Error(w, "url must be http(s)", http.StatusBadRequest)
		return
	}
	qs := target.Query()
	qs.Set("q", q)
	qs.Set("format", "json")
	target.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := webSearchClient.Do(req)
	if err != nil {
		http.Error(w, "searxng unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		http.Error(w, "searxng "+resp.Status+": "+string(body), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}
