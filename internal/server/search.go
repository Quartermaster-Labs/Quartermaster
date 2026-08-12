package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Web search is a provider CHAIN, not one endpoint.
//
// SearXNG is the right default — local, keyless, no quota — but its public
// engines are HTML scrapers: they answer burst traffic with a CAPTCHA, SearXNG
// then suspends the engine on exponential backoff, and the remaining engines
// are what a query waits on. An agent tool loop out-runs that threshold
// trivially, so the failure the user actually sees is a timeout, not an error
// page. A single provider means that timeout is the end of the tool call.
//
// So: an ordered list of providers, tried in order, each with its own budget.
// A provider that errors, times out, or returns nothing hands off to the next
// one. SearXNG stays first (free), keyed APIs sit behind it as backup quota
// that is only spent when the free path failed.
//
// Providers are configured per playground user and arrive on the turn payload
// (like the SearXNG URL always has) — they are never persisted into a chat.

const (
	// searchHopTimeout bounds ONE provider attempt. Deliberately well under the
	// client timeout: the chain's job is to fail over, and a 15s stall on hop 1
	// is most of the user's patience spent before the working provider is even
	// tried.
	searchHopTimeout = 8 * time.Second
	// searxngHopTimeout is longer — a SearXNG query fans out to N engines and a
	// healthy instance genuinely takes a few seconds.
	searxngHopTimeout = 12 * time.Second

	searchCacheTTL  = 10 * time.Minute
	searchCacheRows = 256
)

// searchProviderCfg is one row of the user's provider chain.
type searchProviderCfg struct {
	ID      string `json:"id"`                // searxng | brave | tavily | duckduckgo | google
	Enabled bool   `json:"enabled"`           // unchecked rows stay in the list, keeping their key
	BaseURL string `json:"baseUrl,omitempty"` // searxng only
	Key     string `json:"key,omitempty"`     // brave / tavily / google
	CX      string `json:"cx,omitempty"`      // google programmable-search engine id
}

// searchProviderIDs is the whitelist. A row with an unknown id is skipped
// rather than guessed at — the id selects which upstream a user's API key is
// sent to, and a typo must not leak it somewhere else.
var searchProviderIDs = map[string]bool{
	"searxng": true, "brave": true, "tavily": true, "duckduckgo": true, "google": true,
}

// ready reports whether this row has everything its provider needs. An enabled
// row missing its key is skipped silently: the chain's whole point is that a
// half-configured provider costs a hop, not the search.
func (c searchProviderCfg) ready() bool {
	if !c.Enabled || !searchProviderIDs[c.ID] {
		return false
	}
	switch c.ID {
	case "searxng":
		return strings.TrimSpace(c.BaseURL) != ""
	case "brave", "tavily":
		return strings.TrimSpace(c.Key) != ""
	case "google":
		return strings.TrimSpace(c.Key) != "" && strings.TrimSpace(c.CX) != ""
	}
	return true // duckduckgo: keyless
}

func (c searchProviderCfg) hopTimeout() time.Duration {
	if c.ID == "searxng" {
		return searxngHopTimeout
	}
	return searchHopTimeout
}

// searchChain runs the configured providers in order and returns the first
// non-empty result set. Errors are accumulated so a total failure tells the
// model (and the user) which providers were tried and why each one failed —
// "Search failed: connection refused" with three providers configured is not a
// debuggable message.
func searchChain(ctx context.Context, providers []searchProviderCfg, query string, limit int) ([]searchResult, string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, "", fmt.Errorf("missing query")
	}
	if limit < 1 || limit > webMaxResults {
		limit = webDefaultResults
	}
	tried := 0
	var probs []string
	for _, p := range providers {
		if !p.ready() {
			continue
		}
		tried++
		if res, ok := searchCacheGet(p, q, limit); ok {
			return res, p.ID, nil
		}
		hopCtx, cancel := context.WithTimeout(ctx, p.hopTimeout())
		res, err := runSearchProvider(hopCtx, p, q, limit)
		cancel()
		switch {
		case ctx.Err() != nil:
			// The caller gave up (turn stopped) — not a provider failure.
			return nil, "", ctx.Err()
		case err != nil:
			probs = append(probs, p.ID+": "+err.Error())
		case len(res) == 0:
			probs = append(probs, p.ID+": no results")
		default:
			searchCachePut(p, q, limit, res)
			return res, p.ID, nil
		}
	}
	if tried == 0 {
		return nil, "", fmt.Errorf("no web search provider configured")
	}
	return nil, "", fmt.Errorf("all search providers failed (%s)", strings.Join(probs, "; "))
}

func runSearchProvider(ctx context.Context, p searchProviderCfg, query string, limit int) ([]searchResult, error) {
	switch p.ID {
	case "searxng":
		return searxngSearch(ctx, p.BaseURL, query, limit)
	case "brave":
		return braveSearch(ctx, p.Key, query, limit)
	case "tavily":
		return tavilySearch(ctx, p.Key, query, limit)
	case "duckduckgo":
		return ddgSearch(ctx, query, limit)
	case "google":
		return googleCSESearch(ctx, p.Key, p.CX, query, limit)
	}
	return nil, fmt.Errorf("unknown provider %q", p.ID)
}

// legacySearchChain builds a SearXNG-only chain from the old single-URL field,
// so a turn payload (or a stored client) that predates provider config keeps
// working unchanged.
func legacySearchChain(baseURL string) []searchProviderCfg {
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	return []searchProviderCfg{{ID: "searxng", Enabled: true, BaseURL: baseURL}}
}

// --- result cache ----------------------------------------------------------
//
// Keyed by provider identity as well as query: the same query answered by
// Brave and by SearXNG are different result sets, and a cached SearXNG answer
// must not stand in for the paid provider the user just switched to. The limit
// is part of the key because a model that re-asks wider must not be handed the
// narrower cached answer.

type searchCacheEntry struct {
	res []searchResult
	at  time.Time
}

var searchCache struct {
	mu   sync.Mutex
	rows map[string]searchCacheEntry
}

// cacheIdentity is what distinguishes two configurations of the same provider.
// The API key is NOT part of it — it is a credential, not a scope, and hashing
// it in would only mean a rotated key silently misses the cache.
func (c searchProviderCfg) cacheIdentity() string {
	if c.ID == "searxng" {
		return "searxng\x00" + strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	}
	if c.ID == "google" {
		return "google\x00" + strings.TrimSpace(c.CX)
	}
	return c.ID
}

func searchCacheKey(c searchProviderCfg, query string, limit int) string {
	return fmt.Sprintf("%s\x00%d\x00%s", c.cacheIdentity(), limit, query)
}

func searchCacheGet(c searchProviderCfg, query string, limit int) ([]searchResult, bool) {
	searchCache.mu.Lock()
	defer searchCache.mu.Unlock()
	e, ok := searchCache.rows[searchCacheKey(c, query, limit)]
	if !ok || time.Since(e.at) > searchCacheTTL {
		return nil, false
	}
	return e.res, true
}

func searchCachePut(c searchProviderCfg, query string, limit int, res []searchResult) {
	searchCache.mu.Lock()
	defer searchCache.mu.Unlock()
	if searchCache.rows == nil {
		searchCache.rows = make(map[string]searchCacheEntry)
	}
	if len(searchCache.rows) >= searchCacheRows {
		for k, e := range searchCache.rows {
			if time.Since(e.at) > searchCacheTTL {
				delete(searchCache.rows, k)
			}
		}
		if len(searchCache.rows) >= searchCacheRows {
			for k := range searchCache.rows {
				delete(searchCache.rows, k)
				break
			}
		}
	}
	searchCache.rows[searchCacheKey(c, query, limit)] = searchCacheEntry{res: res, at: time.Now()}
}

// --- shared helpers --------------------------------------------------------

func clipSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > snippetMax {
		return s[:snippetMax]
	}
	return s
}

// searchDo runs the request and returns the body, turning a non-2xx into an
// error carrying a snippet of the upstream's own message — a bare "brave 422"
// hides "subscription token invalid", which is the one thing the user needs.
func searchDo(req *http.Request) ([]byte, error) {
	resp, err := webSearchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snip, _ := readLimited(resp.Body, 300)
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.Join(strings.Fields(snip), " "))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// --- Brave ----------------------------------------------------------------

// braveSearch queries the Brave Search API. A real JSON contract rather than a
// scraper, so it is the failover that actually holds up under an agent loop.
func braveSearch(ctx context.Context, key, query string, limit int) ([]searchResult, error) {
	u, _ := url.Parse("https://api.search.brave.com/res/v1/web/search")
	qs := url.Values{}
	qs.Set("q", query)
	qs.Set("count", fmt.Sprint(limit))
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	// No explicit Accept-Encoding: setting it by hand turns OFF net/http's
	// transparent gzip decompression and hands us a compressed body.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", strings.TrimSpace(key))

	body, err := searchDo(req)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]searchResult, 0, limit)
	for _, r := range parsed.Web.Results {
		if len(out) >= limit {
			break
		}
		// Brave marks query terms with <strong> in the description.
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Content: cleanFeedText(r.Description, snippetMax)})
	}
	return out, nil
}

// --- Tavily ---------------------------------------------------------------

// tavilySearch queries Tavily, which is built for exactly this call site: its
// `content` field is already extracted page text rather than a SERP snippet,
// so a result is often enough on its own and saves a fetch_page round trip.
func tavilySearch(ctx context.Context, key, query string, limit int) ([]searchResult, error) {
	payload, _ := json.Marshal(map[string]any{
		"query":       query,
		"max_results": limit,
		"api_key":     strings.TrimSpace(key), // older API auth; harmless alongside the header
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))

	body, err := searchDo(req)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]searchResult, 0, limit)
	for _, r := range parsed.Results {
		if len(out) >= limit {
			break
		}
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Content: clipSnippet(r.Content)})
	}
	return out, nil
}

// --- Google Programmable Search -------------------------------------------

// googleCSESearch queries a user's Programmable Search Engine. Best result
// quality of the four, hardest cap (100/day free) — so it belongs late in a
// chain, not first.
func googleCSESearch(ctx context.Context, key, cx, query string, limit int) ([]searchResult, error) {
	if limit > 10 {
		limit = 10 // API ceiling per page; we never paginate
	}
	u, _ := url.Parse("https://www.googleapis.com/customsearch/v1")
	qs := url.Values{}
	qs.Set("key", strings.TrimSpace(key))
	qs.Set("cx", strings.TrimSpace(cx))
	qs.Set("q", query)
	qs.Set("num", fmt.Sprint(limit))
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	body, err := searchDo(req)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Items []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"items"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if parsed.Error.Message != "" {
		return nil, fmt.Errorf("%s", parsed.Error.Message)
	}
	out := make([]searchResult, 0, limit)
	for _, r := range parsed.Items {
		if len(out) >= limit {
			break
		}
		out = append(out, searchResult{Title: r.Title, URL: r.Link, Content: clipSnippet(r.Snippet)})
	}
	return out, nil
}

// --- DuckDuckGo (keyless HTML) --------------------------------------------
//
// Last-resort hop: no key, no quota, but it is a scraper against the same
// defences SearXNG hits, so it is a fallback and never a primary. Parsed with
// regexes rather than a DOM because the page is two repeating classes deep and
// pulling in a parser for it is not worth the dependency.

var (
	ddgAnchorRE  = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRE = regexp.MustCompile(`(?is)class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
)

func ddgSearch(ctx context.Context, query string, limit int) ([]searchResult, error) {
	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", "wt-wt") // no region skew

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The HTML endpoint serves a challenge page to an obviously-automated
	// client; a browser UA is the difference between results and an empty body.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0 Safari/537.36")
	req.Header.Set("Accept", "text/html")

	body, err := searchDo(req)
	if err != nil {
		return nil, err
	}
	page := string(body)
	anchors := ddgAnchorRE.FindAllStringSubmatch(page, limit*2)
	snips := ddgSnippetRE.FindAllStringSubmatch(page, limit*2)

	out := make([]searchResult, 0, limit)
	for i, a := range anchors {
		if len(out) >= limit {
			break
		}
		link := ddgUnwrap(a[1])
		if link == "" {
			continue
		}
		snippet := ""
		if i < len(snips) {
			snippet = cleanFeedText(snips[i][1], snippetMax)
		}
		out = append(out, searchResult{Title: cleanFeedText(a[2], 300), URL: link, Content: snippet})
	}
	if len(out) == 0 && strings.Contains(page, "anomaly") {
		return nil, fmt.Errorf("rate-limited (bot challenge)")
	}
	return out, nil
}

// ddgUnwrap turns DuckDuckGo's //duckduckgo.com/l/?uddg=<encoded> redirect into
// the destination. A result whose real URL cannot be recovered is dropped, not
// passed through — handing the model a tracker redirect means fetch_page reads
// the redirector, not the page.
func ddgUnwrap(href string) string {
	href = strings.TrimSpace(html.UnescapeString(href))
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if target := u.Query().Get("uddg"); target != "" {
		href = target
	}
	if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
		return ""
	}
	return href
}
