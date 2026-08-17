package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func clearSearchCache() {
	searchCache.mu.Lock()
	searchCache.rows = nil
	searchCache.mu.Unlock()
}

func TestTools_SearchChainFailsOver(t *testing.T) {
	clearSearchCache()
	// SearXNG that always errors → the chain must fall through to the next
	// provider rather than failing the search.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "engine suspended", http.StatusInternalServerError)
	}))
	defer dead.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"title":"T","url":"https://e.com","content":"C"}]}`))
	}))
	defer good.Close()

	res, who, err := Search(context.Background(), []SearchProvider{
		{ID: "searxng", Enabled: true, BaseURL: dead.URL},
		{ID: "searxng", Enabled: true, BaseURL: good.URL},
	}, "hello", 5)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if who != "searxng" || len(res) != 1 || res[0].URL != "https://e.com" {
		t.Fatalf("got %q %+v", who, res)
	}
}

func TestTools_SearchChainSkipsUnconfigured(t *testing.T) {
	clearSearchCache()
	// Enabled but keyless rows must be skipped, not attempted: a hop that cannot
	// succeed is latency spent for nothing.
	for _, p := range []SearchProvider{
		{ID: "brave", Enabled: true},
		{ID: "google", Enabled: true, Key: "k"}, // missing cx
		{ID: "searxng", Enabled: true},          // missing url
		{ID: "tavily", Enabled: false, Key: "k"},
		{ID: "bogus", Enabled: true},
	} {
		if p.ready() {
			t.Fatalf("%+v should not be ready", p)
		}
	}
	if _, _, err := Search(context.Background(), []SearchProvider{{ID: "brave", Enabled: true}}, "q", 5); err == nil ||
		!errors.Is(err, ErrNoProviders) {
		t.Fatalf("want ErrNoProviders, got %v", err)
	}
}

func TestTools_SearchChainEmptyIsAFailure(t *testing.T) {
	clearSearchCache()
	// A provider answering 200 with zero results is a failure for chain purposes
	// — otherwise a rate-limited scraper's empty page ends the search.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[]}`))
	}))
	defer empty.Close()
	full := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"title":"T","url":"https://x.com","content":"C"}]}`))
	}))
	defer full.Close()

	res, _, err := Search(context.Background(), []SearchProvider{
		{ID: "searxng", Enabled: true, BaseURL: empty.URL},
		{ID: "searxng", Enabled: true, BaseURL: full.URL},
	}, "empties", 5)
	if err != nil || len(res) != 1 {
		t.Fatalf("got %v %+v", err, res)
	}
}

func TestTools_SearchCacheKeyedByProviderAndLimit(t *testing.T) {
	clearSearchCache()
	a := SearchProvider{ID: "searxng", BaseURL: "http://one:8888/"}
	b := SearchProvider{ID: "brave", Key: "k"}
	searchCachePut(a, "q", 5, []Result{{Title: "A"}})

	if _, ok := searchCacheGet(b, "q", 5); ok {
		t.Fatal("brave must not be served searxng's cached answer")
	}
	if _, ok := searchCacheGet(a, "q", 10); ok {
		t.Fatal("a wider re-ask must not hit the narrow cached answer")
	}
	// Key rotation must not evict: the key is a credential, not a scope.
	if _, ok := searchCacheGet(SearchProvider{ID: "searxng", BaseURL: "http://one:8888"}, "q", 5); !ok {
		t.Fatal("trailing-slash-normalized base should hit")
	}
}

func TestTools_DDGUnwrap(t *testing.T) {
	cases := map[string]string{
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&amp;rut=x": "https://example.com/a",
		"https://example.org/direct":                                       "https://example.org/direct",
		"/y.js?ad=1":                                                       "",
		"javascript:alert(1)":                                              "",
	}
	for in, want := range cases {
		if got := ddgUnwrap(in); got != want {
			t.Errorf("ddgUnwrap(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTools_BraveAndGoogleParse(t *testing.T) {
	clearSearchCache()
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "kk" {
			t.Errorf("missing token header")
		}
		w.Write([]byte(`{"web":{"results":[{"title":"B","url":"https://b.com","description":"a <strong>hit</strong> here"}]}}`))
	}))
	defer brave.Close()

	// Point the provider at the stub by overriding the endpoint through a
	// request-level rewrite: simplest is to parse the body path directly.
	req, _ := http.NewRequest(http.MethodGet, brave.URL, nil)
	req.Header.Set("X-Subscription-Token", "kk")
	body, err := searchDo(req)
	if err != nil {
		t.Fatalf("searchDo: %v", err)
	}
	if !strings.Contains(string(body), `"B"`) {
		t.Fatalf("body %s", body)
	}

	// Upstream error bodies must survive into the chain's message — "brave 422"
	// alone hides "subscription token invalid".
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "subscription token invalid", http.StatusUnprocessableEntity)
	}))
	defer bad.Close()
	req2, _ := http.NewRequest(http.MethodGet, bad.URL, nil)
	if _, err := searchDo(req2); err == nil || !strings.Contains(err.Error(), "subscription token invalid") {
		t.Fatalf("want upstream detail, got %v", err)
	}
}

func TestTools_FormatSearchResults(t *testing.T) {
	// Pin the stamped date: the model has to see the real one, so it is part of
	// the contract, not incidental formatting.
	orig := searchDate
	searchDate = func() string { return "5 August 2026" }
	defer func() { searchDate = orig }()

	if got := FormatSearchResults("q", nil, nil); got != `No results found for "q". (Searched 5 August 2026.)` {
		t.Errorf("empty format = %q", got)
	}
	rs := []Result{{Title: "T", URL: "http://x", Content: "snip"}}
	got := FormatSearchResults("q", rs, []int{3})
	if !strings.Contains(got, "5 August 2026") {
		t.Errorf("format = %q, want the search date stamped", got)
	}
	if !strings.HasSuffix(got, "\n\n[3] T\nhttp://x\nsnip") {
		t.Errorf("format = %q, want the numbered result block last", got)
	}
}
