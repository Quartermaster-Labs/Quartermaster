package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

var webSearchClient = &http.Client{Timeout: 15 * time.Second}

// handleAPIWebSearch runs a web search on the browser's behalf. Two shapes:
//
//   - POST {providers:[…], q, limit} — the provider chain (search.go). Used by
//     the playground's per-provider Test button. POST, not GET, because the
//     body carries API keys and a query string lands in the access log.
//   - GET ?url=<searxng base>&q=… — the original SearXNG-only proxy, kept for
//     older clients. Returns SearXNG's raw JSON.
//
// Either way it exists because SearXNG ships no CORS headers, so the browser
// cannot reach it directly.
//
// ponytail: the GET form is an open forwarder — it fetches whatever ?url=
// points at (SSRF). Fine for a local single-user tool; restrict to a configured
// allowlist if ever exposed.
func (s *Server) handleAPIWebSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleWebSearchChain(w, r)
		return
	}

	base := strings.TrimSpace(r.URL.Query().Get("url"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if base == "" || q == "" {
		http.Error(w, "missing url or q", http.StatusBadRequest)
		return
	}

	body, err := searxngJSON(r.Context(), base, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (s *Server) handleWebSearchChain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Providers []searchProviderCfg `json:"providers"`
		Query     string              `json:"q"`
		Limit     int                 `json:"limit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	results, provider, err := searchChain(r.Context(), req.Providers, req.Query, req.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Same field names as SearXNG's own JSON so the browser has one result shape
	// whichever provider answered.
	rows := make([]map[string]string, 0, len(results))
	for _, x := range results {
		rows = append(rows, map[string]string{"title": x.Title, "url": x.URL, "content": x.Content})
	}
	writeJSON(w, map[string]any{"provider": provider, "results": rows})
}
