package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A marked context must not be able to reach the loopback test server, and an
// unmarked one must still reach it: that pair IS the caller-origin rule.
func TestTools_PublicOnlyBlocksPrivateTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(PublicOnly(context.Background()), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := webSearchClient.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a PublicOnly context reached a loopback address")
	}
	if !strings.Contains(err.Error(), "blocked non-public address") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = webSearchClient.Do(req)
	if err != nil {
		t.Fatalf("an unmarked context was refused: %v", err)
	}
	resp.Body.Close()
}

// The marking has to survive the wrapping every caller does to add a timeout,
// since that is what the handlers hand to the provider chain.
func TestTools_PublicOnlySurvivesDerivedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(PublicOnly(context.Background()))
	defer cancel()
	if !isPublicOnly(ctx) {
		t.Fatal("the mark was lost through context.WithCancel")
	}
	if isPublicOnly(context.Background()) {
		t.Fatal("an unmarked context reported as guarded")
	}
}
