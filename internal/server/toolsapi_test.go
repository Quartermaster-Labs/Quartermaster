package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postTool(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.toolHandler(path)(w, r)
	return w
}

// toolHandler looks the handler up the way server.go wires the routes
// (discoveryChain supplies the auth; the handlers themselves are chain-agnostic,
// so tests drive them directly).
func (s *Server) toolHandler(path string) http.HandlerFunc {
	switch path {
	case "/v1/tools/search":
		return s.handleToolSearch
	case "/v1/tools/youtube/transcript":
		return s.handleToolYouTubeTranscript
	case "/v1/tools/youtube/search":
		return s.handleToolYouTubeSearch
	case "/v1/tools/youtube/comments":
		return s.handleToolYouTubeComments
	}
	panic("unknown tool path " + path)
}

func toolErrMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v (%q)", err, w.Body.String())
	}
	return resp.Error.Message
}

func TestToolsAPI_SearchValidation(t *testing.T) {
	s := &Server{}

	w := postTool(t, s, "/v1/tools/search", "not json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json: status=%d want 400", w.Code)
	}

	w = postTool(t, s, "/v1/tools/search", `{}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(toolErrMessage(t, w), `"query"`) {
		t.Errorf("missing query: status=%d msg=%q", w.Code, toolErrMessage(t, w))
	}

	// A configured caller with no usable providers is a 400, not a 500: the
	// request, not the server, is what is wrong.
	w = postTool(t, s, "/v1/tools/search", `{"query":"hello","providers":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no providers: status=%d want 400", w.Code)
	}
	if msg := toolErrMessage(t, w); !strings.Contains(msg, "no web search provider configured") {
		t.Errorf("message = %q, want the ErrNoProviders text", msg)
	}

	// Disabled / keyless rows count as unconfigured too.
	w = postTool(t, s, "/v1/tools/search", `{"query":"hello","providers":[{"id":"brave","enabled":true}]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unusable provider: status=%d want 400", w.Code)
	}
}

func TestToolsAPI_SearchWithSearxng(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			t.Errorf("unexpected upstream request %s", r.URL)
		}
		w.Write([]byte(`{"results":[{"title":"T","url":"https://example.com/a","content":"C"}]}`))
	}))
	defer up.Close()

	s := &Server{}
	w := postTool(t, s, "/v1/tools/search",
		`{"q":"alpha one","count":3,"providers":[{"id":"searxng","enabled":true,"baseUrl":"`+up.URL+`"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Provider string `json:"provider"`
		Results  []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Provider != "searxng" || len(resp.Results) != 1 || resp.Results[0].URL != "https://example.com/a" {
		t.Errorf("response = %+v", resp)
	}
}

func TestToolsAPI_YouTubeValidation(t *testing.T) {
	s := &Server{}

	// transcript: empty body → missing url.
	w := postTool(t, s, "/v1/tools/youtube/transcript", `{}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(toolErrMessage(t, w), `"url"`) {
		t.Errorf("transcript {}: status=%d msg=%q", w.Code, toolErrMessage(t, w))
	}

	// transcript: a private address is a 400, not a 502 — the caller can fix it.
	// (A non-YouTube URL is no longer an error: yt-dlp handles hundreds of sites.)
	w = postTool(t, s, "/v1/tools/youtube/transcript", `{"url":"http://192.168.1.10/video.mp4"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("private url: status=%d msg=%q", w.Code, toolErrMessage(t, w))
	}

	// search: neither query nor channel.
	w = postTool(t, s, "/v1/tools/youtube/search", `{}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(toolErrMessage(t, w), `"query"`) {
		t.Errorf("search {}: status=%d msg=%q", w.Code, toolErrMessage(t, w))
	}

}
