package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// searxngJSON is the ONE choke point every SearXNG query goes through — the
// browser proxy (websearch.go) and the server-run turn loop (turnstools.go).
//
// Public SearXNG engines (duckduckgo/brave/startpage/google) are HTML scrapers
// with no API contract; they defend against burst traffic by serving a CAPTCHA,
// after which SearXNG suspends the engine with exponential backoff. An agent
// tool loop trivially out-runs that threshold, so this gate does two things:
//
//   - serializes queries and spaces them by minQueryGap (one in flight, ever)
//   - caches raw response bodies per (base, query) for cacheTTL, so a loop that
//     re-asks the same thing costs zero upstream requests
const (
	minQueryGap  = 1500 * time.Millisecond
	cacheTTL     = 10 * time.Minute
	cacheMaxRows = 256
)

type searxCacheEntry struct {
	body []byte
	at   time.Time
}

var searxGate struct {
	sendMu sync.Mutex // held across a request: serializes + paces upstream calls
	last   time.Time

	mu    sync.Mutex
	cache map[string]searxCacheEntry
}

// searxngJSON fetches <base>/search?q=..&format=json, rate-limited and cached.
// Returns the raw JSON body so callers can either stream it on or decode it.
func searxngJSON(ctx context.Context, baseURL, query string) ([]byte, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("SearXNG URL not set")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("missing query")
	}
	key := base + "\x00" + q

	if body, ok := searxCacheGet(key); ok {
		return body, nil
	}

	target, err := url.Parse(base + "/search")
	if err != nil {
		return nil, err
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("url must be http(s)")
	}
	qs := target.Query()
	qs.Set("q", q)
	qs.Set("format", "json")
	target.RawQuery = qs.Encode()

	searxGate.sendMu.Lock()
	defer searxGate.sendMu.Unlock()

	// Re-check: a concurrent caller may have just fetched the same query while
	// we waited for the lock.
	if body, ok := searxCacheGet(key); ok {
		return body, nil
	}
	if wait := minQueryGap - time.Since(searxGate.last); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := webSearchClient.Do(req)
	searxGate.last = time.Now()
	if err != nil {
		return nil, fmt.Errorf("searxng unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snip, _ := readLimited(resp.Body, 512)
		return nil, fmt.Errorf("searxng %s: %s", resp.Status, snip)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	searxCachePut(key, body)
	return body, nil
}

func searxCacheGet(key string) ([]byte, bool) {
	searxGate.mu.Lock()
	defer searxGate.mu.Unlock()
	e, ok := searxGate.cache[key]
	if !ok || time.Since(e.at) > cacheTTL {
		return nil, false
	}
	return e.body, true
}

func searxCachePut(key string, body []byte) {
	searxGate.mu.Lock()
	defer searxGate.mu.Unlock()
	if searxGate.cache == nil {
		searxGate.cache = make(map[string]searxCacheEntry)
	}
	if len(searxGate.cache) >= cacheMaxRows {
		for k, e := range searxGate.cache {
			if time.Since(e.at) > cacheTTL {
				delete(searxGate.cache, k)
			}
		}
		// Still full of live rows: drop an arbitrary one rather than grow.
		if len(searxGate.cache) >= cacheMaxRows {
			for k := range searxGate.cache {
				delete(searxGate.cache, k)
				break
			}
		}
	}
	searxGate.cache[key] = searxCacheEntry{body: body, at: time.Now()}
}
