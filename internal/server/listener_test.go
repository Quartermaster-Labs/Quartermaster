package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/config"
)

// listenerTestServer wires a stub-routed Server with a listener model map.
func listenerTestServer(t *testing.T, listenerModels map[string]map[string]bool) *Server {
	t.Helper()
	s := newTestServer(
		newStubRouter([]string{"assistant1", "game1"}, "local response"),
		newStubRouter(nil, ""),
	)
	s.cfg = config.Config{
		Models: map[string]config.ModelConfig{
			"assistant1": {Name: "Assistant"},
			"game1":      {Name: "Game"},
		},
		Peers: config.PeerDictionaryConfig{
			"peer1": {Models: []string{"remote-model"}},
		},
	}
	s.listenerModels = listenerModels
	return s
}

func listModelIDs(t *testing.T, s *Server, addr string) map[string]bool {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeListener(addr, w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var resp struct {
		Data []modelRecord `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range resp.Data {
		ids[m.ID] = true
	}
	return ids
}

func TestServer_ListenerCatalogFilter(t *testing.T) {
	s := listenerTestServer(t, map[string]map[string]bool{
		":1250": {"assistant1": true},
	})

	ids := listModelIDs(t, s, ":1250")
	if !ids["assistant1"] {
		t.Errorf("assistant1 should be listed on :1250: %v", ids)
	}
	if ids["game1"] {
		t.Errorf("game1 not exposed on :1250 but was listed: %v", ids)
	}
	if ids["remote-model"] {
		t.Errorf("peer models must be hidden on restricted listeners: %v", ids)
	}
}

func TestServer_ListenerCatalogUnrestricted(t *testing.T) {
	s := listenerTestServer(t, map[string]map[string]bool{
		":1250": {"assistant1": true},
	})

	// An address absent from listenerModels is unrestricted: every local model
	// and peer model is listed.
	ids := listModelIDs(t, s, ":9999")
	if !ids["assistant1"] || !ids["game1"] || !ids["remote-model"] {
		t.Errorf("unrestricted listener should list everything: %v", ids)
	}
}

func TestServer_ListenerGatesRequest(t *testing.T) {
	s := listenerTestServer(t, map[string]map[string]bool{
		":1250": {"assistant1": true},
	})

	// assistant1 is exposed on :1250 -> routes to the local stub.
	w := httptest.NewRecorder()
	s.ServeListener(":1250", w, chatRequest("assistant1"))
	if w.Code != http.StatusOK || w.Body.String() != "local response" {
		t.Errorf("assistant1 on :1250 status=%d body=%q", w.Code, w.Body.String())
	}

	// game1 is not exposed on :1250 -> 404 even though the model exists.
	w = httptest.NewRecorder()
	s.ServeListener(":1250", w, chatRequest("game1"))
	if w.Code != http.StatusNotFound {
		t.Errorf("game1 on :1250 status=%d want 404 body=%q", w.Code, w.Body.String())
	}
}

func TestServer_ListenerUnrestrictedRoutesAll(t *testing.T) {
	s := listenerTestServer(t, map[string]map[string]bool{
		":1250": {"assistant1": true},
	})

	// An unscoped address routes any known model.
	w := httptest.NewRecorder()
	s.ServeListener(":9999", w, chatRequest("game1"))
	if w.Code != http.StatusOK || w.Body.String() != "local response" {
		t.Errorf("game1 on unrestricted listener status=%d body=%q", w.Code, w.Body.String())
	}
}
