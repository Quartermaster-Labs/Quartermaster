package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Link unfurling for the playground chat: a YouTube URL pasted into any message
// renders as a card (thumbnail + title + channel), Discord-style, whether or not
// the model ever called media_transcript on it.
//
// Deliberately NOT yt-dlp. The transcript path shells out per request and pulls
// caption tracks, which YouTube rate-limits (a 429 was hit during development);
// unfurling fires on every link a user types, so it must be cheap. YouTube's
// oEmbed endpoint is one small unauthenticated GET returning exactly the two
// fields a card needs. It carries no duration -- the card shows title + channel
// only, and the duration we DO have (from a transcript fetch) stays on the
// transcript step's own line.
//
// The thumbnail is NOT proxied: the UI points <img> straight at i.ytimg.com.
// That is an outbound request to Google from the user's browser on every render
// of a chat holding a YouTube link -- a deliberate call, since the alternative
// (download + store in the per-user media dir) costs disk and a second fetch
// path for what is a cosmetic thumbnail. Switch to proxying via
// extractMedia/handlePlaygroundMedia if a locked-down/offline deployment ever
// needs it.

const (
	ytMetaTimeout = 10 * time.Second
	// Cards render on every repaint of a long chat, so misses must be rare.
	// oEmbed answers are effectively immutable (a title rename is not worth a
	// refetch), hence a much longer TTL than the transcript cache's 30 min.
	ytMetaTTL = 24 * time.Hour
	ytMetaMax = 256
)

var ytMetaClient = &http.Client{Timeout: ytMetaTimeout}

// ytMeta is the card payload. Thumb is a hotlink URL, not a local ref.
type ytMeta struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Uploader string `json:"uploader"`
	Thumb    string `json:"thumb"`
	URL      string `json:"url"`
}

var ytMetaCache struct {
	mu sync.Mutex
	m  map[string]ytMetaEntry
}

type ytMetaEntry struct {
	meta ytMeta
	at   time.Time
}

// handleAPIYouTubeMeta — GET /api/youtube/meta?id=<11-char video id>. Same
// same-origin-proxy reasoning as handleAPIWebSearch: oEmbed's CORS headers are
// not something to bet the UI on, and one server-side cache serves every user
// and every repaint instead of each tab hammering Google on its own.
func (s *Server) handleAPIYouTubeMeta(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	// Accept a full URL too, so the client never has to duplicate the parser.
	if !ytVideoID.MatchString(id) {
		id = parseYouTubeID(id)
	}
	if id == "" {
		http.Error(w, "missing or invalid id", http.StatusBadRequest)
		return
	}

	meta, err := fetchYouTubeMeta(r.Context(), id)
	if err != nil {
		// 502, not 404: an unfurl failure is cosmetic and the client just drops
		// the card rather than showing an error in the middle of a chat.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

// fetchYouTubeMeta resolves a video id to its card fields via oEmbed, cached.
func fetchYouTubeMeta(ctx context.Context, id string) (ytMeta, error) {
	if !ytVideoID.MatchString(id) {
		return ytMeta{}, fmt.Errorf("invalid video id")
	}
	if m, ok := ytMetaGet(id); ok {
		return m, nil
	}

	watch := "https://www.youtube.com/watch?v=" + id
	api := "https://www.youtube.com/oembed?format=json&url=" + url.QueryEscape(watch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return ytMeta{}, err
	}
	resp, err := ytMetaClient.Do(req)
	if err != nil {
		return ytMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 401/404 here means private, deleted, or age-restricted.
		return ytMeta{}, fmt.Errorf("youtube oembed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ytMeta{}, err
	}

	var raw struct {
		Title      string `json:"title"`
		AuthorName string `json:"author_name"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ytMeta{}, fmt.Errorf("youtube oembed: bad json: %w", err)
	}

	meta := ytMeta{
		ID:       id,
		Title:    strings.TrimSpace(raw.Title),
		Uploader: strings.TrimSpace(raw.AuthorName),
		// oEmbed's own thumbnail_url is hqdefault (480x360) with 4:3 letterbox
		// bars on modern uploads. mqdefault is the same image at card size
		// without them, and the URL is derivable from the id.
		Thumb: "https://i.ytimg.com/vi/" + id + "/mqdefault.jpg",
		URL:   watch,
	}
	ytMetaPut(id, meta)
	return meta, nil
}

func ytMetaGet(id string) (ytMeta, bool) {
	ytMetaCache.mu.Lock()
	defer ytMetaCache.mu.Unlock()
	e, ok := ytMetaCache.m[id]
	if !ok || time.Since(e.at) > ytMetaTTL {
		return ytMeta{}, false
	}
	return e.meta, true
}

func ytMetaPut(id string, m ytMeta) {
	ytMetaCache.mu.Lock()
	defer ytMetaCache.mu.Unlock()
	if ytMetaCache.m == nil {
		ytMetaCache.m = map[string]ytMetaEntry{}
	}
	// Evict the oldest entry rather than the whole map: these are ~200 bytes
	// each and re-fetching costs a round trip to Google.
	if len(ytMetaCache.m) >= ytMetaMax {
		var oldest string
		var oldestAt time.Time
		for k, v := range ytMetaCache.m {
			if oldest == "" || v.at.Before(oldestAt) {
				oldest, oldestAt = k, v.at
			}
		}
		delete(ytMetaCache.m, oldest)
	}
	ytMetaCache.m[id] = ytMetaEntry{meta: m, at: time.Now()}
}
