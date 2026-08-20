package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/tools"
)

// /v1/tools/* — data-plane tool execution for external AI projects.
//
// A consuming project keeps its own model-visible tool schemas and local
// wrappers, but delegates execution to quartermaster instead of re-implementing
// the tool. The endpoints return the same compact, model-consumable output the
// playground turn loop already feeds a model, so a result can be dropped into a
// context as-is. Provider configuration is stateless per call: the request
// body carries everything needed, mirroring how turn payloads do it.
//
// Auth is the same API-key credential as the inference API (discoveryChain),
// not the admin chain — these are data-plane calls, not ops.
//
// Errors are OpenAI-shaped: {"error":{"message":...}}. Statuses: 400 bad args
// or unconfigured (ErrNoProviders), 502 upstream failure, 503 yt-dlp missing.

func toolError(w http.ResponseWriter, status int, msg string) {
	writeJSONStatus(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

func decodeToolBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		toolError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// --- POST /v1/tools/search -------------------------------------------------

type toolSearchReq struct {
	Query     string                 `json:"query"`
	Q         string                 `json:"q"`
	Limit     int                    `json:"limit"`
	Count     int                    `json:"count"`
	Providers []tools.SearchProvider `json:"providers"`
}

// handleToolSearch runs the web-search provider chain. Providers arrive in the
// request body (same shape the playground turn payload carries); an empty or
// all-disabled list is a 400, not a 500 — the caller configured this call.
func (s *Server) handleToolSearch(w http.ResponseWriter, r *http.Request) {
	var req toolSearchReq
	if !decodeToolBody(w, r, &req) {
		return
	}
	query := strings.TrimSpace(firstNonEmpty(req.Query, req.Q))
	if query == "" {
		toolError(w, http.StatusBadRequest, `missing required field: "query"`)
		return
	}
	limit := firstNonZero(req.Limit, req.Count)

	res, provider, err := tools.Search(r.Context(), req.Providers, query, limit)
	if err != nil {
		if errors.Is(err, tools.ErrNoProviders) {
			toolError(w, http.StatusBadRequest, err.Error()+": pass a providers array, e.g. [{\"id\":\"searxng\",\"enabled\":true,\"baseUrl\":\"http://localhost:8080\"}]")
			return
		}
		toolError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"provider": provider, "results": res})
}

// --- YouTube targets -------------------------------------------------------

// toolYouTubeTarget is the shared "point at a video" body: a URL or the bare
// 11-char id, under whichever name the caller used.
type toolYouTubeTarget struct {
	URL   string `json:"url"`
	Video string `json:"video"`
	ID    string `json:"id"`
	Link  string `json:"link"`
	Lang  string `json:"lang"`
	Limit int    `json:"limit"`
	Count int    `json:"count"`
}

// target returns the trimmed pointer-at-a-thing string, or reports the 400. Used
// by the transcript route, which accepts any site yt-dlp handles and leaves the
// vetting to tools.ParseMediaTarget.
func (t toolYouTubeTarget) target(w http.ResponseWriter) (string, bool) {
	s := strings.TrimSpace(firstNonEmpty(t.URL, t.Video, t.ID, t.Link))
	if s == "" {
		toolError(w, http.StatusBadRequest, `missing required field: "url" (the page of a video, talk, stream or episode - or a YouTube video id)`)
		return "", false
	}
	return s, true
}

// resolve extracts the video id or reports the 400 already. YouTube-only paths
// (comments) use this; anything id-shaped is what the extractor needs.
func (t toolYouTubeTarget) resolve(w http.ResponseWriter) (id string, ok bool) {
	target := strings.TrimSpace(firstNonEmpty(t.URL, t.Video, t.ID, t.Link))
	if target == "" {
		toolError(w, http.StatusBadRequest, `missing required field: "url" (a YouTube URL or an 11-character video id)`)
		return "", false
	}
	if id := tools.ParseVideoID(target); id != "" {
		return id, true
	}
	toolError(w, http.StatusBadRequest, `could not extract a YouTube video id from `+quote(target))
	return "", false
}

// --- POST /v1/tools/youtube/transcript -------------------------------------

// handleToolYouTubeTranscript fetches a transcript via yt-dlp and returns the
// structured result (id/url/site/title/uploader/duration + ~30s timestamped
// paragraphs, truncated at the per-recording ceiling with an explicit
// INCOMPLETE marker). Not YouTube-only despite the path: any page yt-dlp can
// pull subtitles from works, and the route keeps its name because external
// callers already point at it.
func (s *Server) handleToolYouTubeTranscript(w http.ResponseWriter, r *http.Request) {
	var req toolYouTubeTarget
	if !decodeToolBody(w, r, &req) {
		return
	}
	target, ok := req.target(w)
	if !ok {
		return
	}
	// Vet here as well as inside GetTranscript so a malformed or private-address
	// target is a 400 the caller can fix, not a 502 that reads as our fault.
	if _, err := tools.ParseMediaTarget(r.Context(), target); err != nil {
		toolError(w, http.StatusBadRequest, err.Error())
		return
	}
	tr, err := tools.GetTranscript(r.Context(), target, strings.TrimSpace(req.Lang))
	if err != nil {
		if errors.Is(err, tools.ErrDlpMissing) {
			toolError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		toolError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, tr)
}

// --- POST /v1/tools/youtube/search -----------------------------------------

type toolYouTubeSearchReq struct {
	Query   string `json:"query"`
	Q       string `json:"q"`
	Channel string `json:"channel"`
	Handle  string `json:"handle"`
	Tab     string `json:"tab"`
	Limit   int    `json:"limit"`
	Count   int    `json:"count"`
}

// handleToolYouTubeSearch runs a free-text YouTube search, or lists a channel
// tab / playlist when channel (or handle) is given. One of the two is required.
// The tools layer clamps limit to its per-call ceiling.
func (s *Server) handleToolYouTubeSearch(w http.ResponseWriter, r *http.Request) {
	var req toolYouTubeSearchReq
	if !decodeToolBody(w, r, &req) {
		return
	}
	query := strings.TrimSpace(firstNonEmpty(req.Query, req.Q))
	channel := strings.TrimSpace(firstNonEmpty(req.Channel, req.Handle))
	if query == "" && channel == "" {
		toolError(w, http.StatusBadRequest, `one of "query" (search) or "channel" (list a channel/playlist) is required`)
		return
	}
	limit := firstNonZero(req.Limit, req.Count)

	var vids []tools.Video
	var err error
	if channel != "" {
		vids, err = tools.ChannelVideos(r.Context(), channel, strings.TrimSpace(req.Tab), limit)
	} else {
		vids, err = tools.SearchVideos(r.Context(), query, limit)
	}
	if err != nil {
		if errors.Is(err, tools.ErrDlpMissing) {
			toolError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		toolError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"videos": vids})
}

// --- POST /v1/tools/youtube/comments ---------------------------------------

// handleToolYouTubeComments fetches top-level comments plus the video's own
// metadata, so a caller can quote who said what next to what the video is.
func (s *Server) handleToolYouTubeComments(w http.ResponseWriter, r *http.Request) {
	var req toolYouTubeTarget
	if !decodeToolBody(w, r, &req) {
		return
	}
	id, ok := req.resolve(w)
	if !ok {
		return
	}
	limit := firstNonZero(req.Limit, req.Count)
	comments, video, err := tools.GetComments(r.Context(), id, limit)
	if err != nil {
		if errors.Is(err, tools.ErrDlpMissing) {
			toolError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		toolError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"video": video, "comments": comments})
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func quote(s string) string { return `"` + s + `"` }
