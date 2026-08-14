package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Discovery half of the YouTube tools. `youtube_transcript` (youtube.go) can
// only read a video the user already named — so a model could analyse a link,
// but not find one, not see what a channel has posted, and not read what people
// said about it. These two tools close that:
//
//	youtube_search   — free-text search OR a channel's video list
//	youtube_comments — the top comments on one video
//
// Same shape as youtube.go: exec-per-request yt-dlp, everything handed to argv
// rebuilt from a validated match, no shell. Nothing here downloads media —
// search/channel listing is `--flat-playlist` (one metadata page, no per-video
// requests), and comments are capped hard (see ytCommentMax).

const (
	// ytBrowseTimeout caps a search / channel listing: one flat metadata page.
	ytBrowseTimeout = 45 * time.Second
	// ytCommentTimeout is longer because comment extraction is the one expensive
	// call here — yt-dlp walks continuation tokens, one request per page.
	ytCommentTimeout = 90 * time.Second

	// ytSearchMax caps results per search/listing. Beyond ~10 the model is
	// picking from noise and paying tokens for it.
	ytSearchMax     = 10
	ytSearchDefault = 8

	// ytCommentMax is deliberately small. Comment extraction costs one HTTP
	// round trip per continuation page, and YouTube rate-limits the same IP the
	// transcript pull uses — a 200-comment dump risks 429ing the tool that
	// actually matters. Ten top comments is the sentiment signal; the rest is
	// repetition.
	ytCommentMax     = 10
	ytCommentDefault = 10
	// ytCommentChars trims one comment. A long copypasta is not worth a page of
	// context.
	ytCommentChars = 400

	// Per-turn caps, mirroring maxYouTube/maxFetches.
	maxYtBrowse = 4
	// maxYtComments: "what do people say about these videos" is a normal ask
	// about a whole search result page, and 2 cut it off mid-task. Cost per call
	// is bounded by ytCommentMax/ytCommentChars (~4KB of context), so context is
	// not the constraint; the shared per-IP rate limit with the transcript tool
	// is, which is what keeps this well under maxYouTube.
	maxYtComments = 6

	ytBrowseCacheTTL = 15 * time.Minute
	ytBrowseCacheMax = 32
)

// ytHandle matches a bare channel handle ("@LinusTechTips"). Handles are
// 3-30 chars of [A-Za-z0-9._-] per YouTube's own rule; the length bound is
// loosened slightly rather than risking a false reject.
var ytHandle = regexp.MustCompile(`^@[A-Za-z0-9._-]{1,40}$`)

// ytChannelID matches the canonical /channel/ id form.
var ytChannelID = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

// ytLegacyName matches the /c/ and /user/ legacy path segments.
var ytLegacyName = regexp.MustCompile(`^[A-Za-z0-9._%-]{1,60}$`)

// ytPlaylistID matches a playlist id.
var ytPlaylistID = regexp.MustCompile(`^[A-Za-z0-9_-]{2,50}$`)

// ytTabs are the channel tabs we will list. Anything else is rejected rather
// than pasted into a URL.
var ytTabs = map[string]bool{"videos": true, "shorts": true, "streams": true}

type ytVideo struct {
	ID       string
	Title    string
	Channel  string
	Duration int    // seconds, 0 if unknown/live
	Views    int64  // -1 if unknown
	Date     string // YYYY-MM-DD, empty if unknown
	Live     string // "is_live", "was_live", "upcoming", or ""
	Desc     string
}

type ytComment struct {
	Author  string
	Text    string
	Likes   int64
	Pinned  bool
	ByOwner bool
}

// Cached as marshalled JSON so one map serves both result shapes (video lists
// and comment blocks). Same cheap whole-map reset as pageCache — entries are
// short-lived and the cap is a bound, not an LRU policy.
type ytBrowseEntry struct {
	json string
	at   time.Time
}

var (
	ytBrowseMu    sync.Mutex
	ytBrowseCache = map[string]ytBrowseEntry{}
)

func ytBrowseGet(key string) (string, bool) {
	ytBrowseMu.Lock()
	defer ytBrowseMu.Unlock()
	e, ok := ytBrowseCache[key]
	if !ok || time.Since(e.at) > ytBrowseCacheTTL {
		return "", false
	}
	return e.json, true
}

func ytBrowsePut(key, val string) {
	ytBrowseMu.Lock()
	defer ytBrowseMu.Unlock()
	if len(ytBrowseCache) >= ytBrowseCacheMax {
		ytBrowseCache = map[string]ytBrowseEntry{}
	}
	ytBrowseCache[key] = ytBrowseEntry{json: val, at: time.Now()}
}

// --- channel / playlist URL construction ------------------------------------

// ytChannelURL turns whatever the model passed — a handle, a channel URL, a
// playlist link — into exactly one URL we are willing to hand yt-dlp. Every
// component is rebuilt from a regex match, so no model-supplied text ever
// reaches argv verbatim.
func ytChannelURL(s, tab string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("no channel given")
	}
	if tab == "" {
		tab = "videos"
	}
	tab = strings.ToLower(strings.TrimSpace(tab))
	if !ytTabs[tab] {
		return "", fmt.Errorf("unknown channel tab %q (use videos, shorts or streams)", tab)
	}

	// Bare forms first: "@handle", a raw channel id, or a plain name the model
	// typed without a URL.
	if ytHandle.MatchString(s) {
		return "https://www.youtube.com/" + s + "/" + tab, nil
	}
	if ytChannelID.MatchString(s) {
		return "https://www.youtube.com/channel/" + s + "/" + tab, nil
	}

	work := s
	if !strings.Contains(work, "://") {
		work = "https://" + work
	}
	u, err := url.Parse(work)
	if err != nil {
		return "", fmt.Errorf("not a YouTube channel or playlist")
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	switch host {
	case "youtube.com", "m.youtube.com", "music.youtube.com":
	default:
		return "", fmt.Errorf("not a YouTube URL")
	}
	// A playlist link is listable as-is (and is what a "watch the series" ask
	// looks like), so accept it alongside channels.
	if list := u.Query().Get("list"); list != "" && ytPlaylistID.MatchString(list) {
		return "https://www.youtube.com/playlist?list=" + list, nil
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", fmt.Errorf("not a YouTube channel or playlist")
	}
	switch {
	case ytHandle.MatchString(parts[0]):
		return "https://www.youtube.com/" + parts[0] + "/" + tab, nil
	case parts[0] == "channel" && len(parts) > 1 && ytChannelID.MatchString(parts[1]):
		return "https://www.youtube.com/channel/" + parts[1] + "/" + tab, nil
	case (parts[0] == "c" || parts[0] == "user") && len(parts) > 1 && ytLegacyName.MatchString(parts[1]):
		return "https://www.youtube.com/" + parts[0] + "/" + parts[1] + "/" + tab, nil
	case parts[0] == "playlist":
		return "", fmt.Errorf("playlist link is missing its list= id")
	}
	return "", fmt.Errorf("not a YouTube channel or playlist")
}

// --- fetching ---------------------------------------------------------------

// ytSearch runs a free-text YouTube search. The query goes into a single
// "ytsearchN:<query>" argv element — yt-dlp's own search scheme, so there is no
// URL to build and nothing to escape.
func ytSearch(ctx context.Context, query string, limit int) ([]ytVideo, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("no search query given")
	}
	limit = clampInt(limit, 1, ytSearchMax, ytSearchDefault)
	key := fmt.Sprintf("s\x00%d\x00%s", limit, strings.ToLower(query))
	return ytFlatList(ctx, key, fmt.Sprintf("ytsearch%d:%s", limit, query), limit)
}

// ytChannelVideos lists a channel tab (or a playlist), newest first.
func ytChannelVideos(ctx context.Context, channel, tab string, limit int) ([]ytVideo, error) {
	target, err := ytChannelURL(channel, tab)
	if err != nil {
		return nil, err
	}
	limit = clampInt(limit, 1, ytSearchMax, ytSearchDefault)
	return ytFlatList(ctx, fmt.Sprintf("c\x00%d\x00%s", limit, target), target, limit)
}

// ytFlatList is the shared --flat-playlist call behind search and channel
// listing: ONE metadata page, one JSON line per entry, no per-video requests.
func ytFlatList(ctx context.Context, cacheKey, target string, limit int) ([]ytVideo, error) {
	if hit, ok := ytBrowseGet(cacheKey); ok {
		var vids []ytVideo
		if json.Unmarshal([]byte(hit), &vids) == nil {
			return vids, nil
		}
	}
	bin, err := ytDlpPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ytBrowseTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin,
		"--flat-playlist", "--dump-json",
		"--playlist-end", fmt.Sprint(limit),
		"--no-warnings", "--no-progress", "--ignore-config",
		"--", target,
	)
	hideConsole(cmd)
	out, runErr := cmd.Output()
	vids := parseFlatJSON(string(out), limit)
	if len(vids) == 0 {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("yt-dlp timed out after %s", ytBrowseTimeout)
		}
		if runErr != nil {
			return nil, fmt.Errorf("yt-dlp failed: %s", ytDlpErr(runErr))
		}
		return nil, fmt.Errorf("no videos found")
	}
	if b, err := json.Marshal(vids); err == nil {
		ytBrowsePut(cacheKey, string(b))
	}
	return vids, nil
}

// parseFlatJSON reads yt-dlp's one-JSON-object-per-line output. Fields present
// in a flat entry vary by extractor and version, so every one is optional and a
// line that doesn't parse is skipped rather than failing the call.
func parseFlatJSON(s string, limit int) []ytVideo {
	var out []ytVideo
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		// Which of these a flat entry actually carries varies by extractor: a
		// search result has channel/uploader but no date at all, while a channel
		// tab has neither channel nor uploader (the channel is on the playlist_*
		// fields) and only a release_year. Everything is optional and every
		// fallback below is one of those observed shapes, not defensiveness.
		var e struct {
			ID          string   `json:"id"`
			Title       string   `json:"title"`
			Channel     string   `json:"channel"`
			Uploader    string   `json:"uploader"`
			PlChannel   string   `json:"playlist_channel"`
			PlUploader  string   `json:"playlist_uploader"`
			Duration    *float64 `json:"duration"`
			ViewCount   *int64   `json:"view_count"`
			UploadDate  string   `json:"upload_date"`
			Timestamp   *int64   `json:"timestamp"`
			Release     *int64   `json:"release_timestamp"`
			ReleaseYear *int     `json:"release_year"`
			LiveStatus  string   `json:"live_status"`
			Desc        string   `json:"description"`
			Type        string   `json:"_type"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		// A channel's root listing can yield nested playlist entries; only real
		// videos have an 11-char id.
		if !ytVideoID.MatchString(e.ID) {
			continue
		}
		v := ytVideo{ID: e.ID, Title: e.Title, Views: -1}
		for _, c := range []string{e.Channel, e.Uploader, e.PlChannel, e.PlUploader} {
			if c != "" {
				v.Channel = c
				break
			}
		}
		if e.Duration != nil {
			v.Duration = int(*e.Duration)
		}
		if e.ViewCount != nil {
			v.Views = *e.ViewCount
		}
		if e.LiveStatus != "" && e.LiveStatus != "not_live" {
			v.Live = e.LiveStatus
		}
		v.Date = ytDate(e.UploadDate, e.Timestamp, e.Release, e.ReleaseYear)
		v.Desc = strings.TrimSpace(e.Desc)
		out = append(out, v)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ytDate normalises whichever date field the entry happened to carry into
// YYYY-MM-DD (or a bare year, when that is all there is). A flat search entry
// usually carries NONE of them — that is not an error, and the renderer says
// "date unknown" rather than showing a date that isn't there. Getting real
// dates would mean a full metadata extraction per video, i.e. one HTTP request
// each, which is exactly the cost --flat-playlist exists to avoid.
func ytDate(uploadDate string, ts, release *int64, year *int) string {
	if len(uploadDate) == 8 {
		return uploadDate[0:4] + "-" + uploadDate[4:6] + "-" + uploadDate[6:8]
	}
	for _, p := range []*int64{ts, release} {
		if p != nil && *p > 0 {
			return time.Unix(*p, 0).UTC().Format("2006-01-02")
		}
	}
	if year != nil && *year > 1900 {
		return fmt.Sprint(*year)
	}
	return ""
}

// fetchYouTubeComments pulls the top comments on one video.
//
// Replies are switched off outright (the 3rd/4th max_comments slots are 0):
// a thread's replies are mostly argument between two strangers, they multiply
// the continuation requests that make this the slow tool, and none of it is
// what "what do people think of this video" is asking for.
func fetchYouTubeComments(ctx context.Context, videoID string, limit int) ([]ytComment, ytTranscript, error) {
	if !ytVideoID.MatchString(videoID) {
		return nil, ytTranscript{}, fmt.Errorf("not a valid YouTube video id")
	}
	limit = clampInt(limit, 1, ytCommentMax, ytCommentDefault)
	key := fmt.Sprintf("k\x00%d\x00%s", limit, videoID)
	if hit, ok := ytBrowseGet(key); ok {
		var cached struct {
			C []ytComment
			M ytTranscript
		}
		if json.Unmarshal([]byte(hit), &cached) == nil {
			return cached.C, cached.M, nil
		}
	}
	bin, err := ytDlpPath()
	if err != nil {
		return nil, ytTranscript{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, ytCommentTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin,
		"--skip-download", "--write-comments", "--dump-single-json",
		// max_comments = total, parents, replies, replies-per-thread.
		"--extractor-args", fmt.Sprintf("youtube:comment_sort=top;max_comments=%d,%d,0,0", limit, limit),
		"--no-warnings", "--no-progress", "--no-playlist", "--ignore-config",
		"--", "https://www.youtube.com/watch?v="+videoID,
	)
	hideConsole(cmd)
	out, runErr := cmd.Output()

	var info struct {
		Title      string  `json:"title"`
		Uploader   string  `json:"uploader"`
		Duration   float64 `json:"duration"`
		LikeCount  *int64  `json:"like_count"`
		ViewCount  *int64  `json:"view_count"`
		CommentCnt *int64  `json:"comment_count"`
		Comments   []struct {
			Author     string `json:"author"`
			Text       string `json:"text"`
			LikeCount  *int64 `json:"like_count"`
			IsPinned   bool   `json:"is_pinned"`
			AuthorIsUp bool   `json:"author_is_uploader"`
			Parent     string `json:"parent"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		if ctx.Err() != nil {
			return nil, ytTranscript{}, fmt.Errorf("yt-dlp timed out after %s (comment extraction is slow on heavily-commented videos)", ytCommentTimeout)
		}
		if runErr != nil {
			return nil, ytTranscript{}, fmt.Errorf("yt-dlp failed: %s", ytDlpErr(runErr))
		}
		return nil, ytTranscript{}, fmt.Errorf("could not read comments for this video")
	}
	meta := ytTranscript{ID: videoID, Title: info.Title, Uploader: info.Uploader, Duration: int(info.Duration)}

	var cs []ytComment
	for _, c := range info.Comments {
		// Defence in depth: max_comments should already exclude replies, but a
		// yt-dlp version that ignores the slot must not leak a reply tree in.
		if c.Parent != "" && c.Parent != "root" {
			continue
		}
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if len(text) > ytCommentChars {
			text = strings.ToValidUTF8(text[:ytCommentChars], "") + "…"
		}
		cc := ytComment{Author: c.Author, Text: text, Likes: -1, Pinned: c.IsPinned, ByOwner: c.AuthorIsUp}
		if c.LikeCount != nil {
			cc.Likes = *c.LikeCount
		}
		cs = append(cs, cc)
		if len(cs) >= limit {
			break
		}
	}
	if len(cs) == 0 {
		return nil, meta, fmt.Errorf("this video has no comments, or they are disabled")
	}
	if b, err := json.Marshal(struct {
		C []ytComment
		M ytTranscript
	}{cs, meta}); err == nil {
		ytBrowsePut(key, string(b))
	}
	return cs, meta, nil
}

// --- rendering --------------------------------------------------------------

// formatYouTubeVideos renders a result list the model can pick from and cite.
// Each line carries the watch URL verbatim so the follow-up youtube_transcript
// call needs no id surgery.
//
// The header states the ORDERING and, when the listing came back without upload
// dates (the normal case for a search — see ytDate), says so outright. A model
// shown an undated list will otherwise describe the first result as "the
// latest", which is a claim nothing here supports.
func formatYouTubeVideos(what string, vids []ytVideo, numbers []int, newestFirst bool) string {
	dated := false
	for _, v := range vids {
		if v.Date != "" {
			dated = true
			break
		}
	}
	order := "in YouTube's own relevance order, NOT by date"
	if newestFirst {
		order = "newest first"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "YouTube results for %s - %s, fetched %s.\n", what, order, searchDate())
	if !dated {
		b.WriteString("Upload dates are not available in this listing, so do not state or imply when any of these was published.\n")
	}
	b.WriteString("\n")
	for i, v := range vids {
		if numbers[i] > 0 {
			fmt.Fprintf(&b, "[%d] ", numbers[i])
		}
		vurl := "https://www.youtube.com/watch?v=" + v.ID
		b.WriteString(orURL(v.Title, vurl))
		b.WriteString("\n" + vurl + "\n")
		var meta []string
		if v.Channel != "" {
			meta = append(meta, v.Channel)
		}
		switch {
		case v.Live == "is_live":
			meta = append(meta, "LIVE NOW")
		case v.Live == "upcoming":
			meta = append(meta, "upcoming")
		case v.Duration > 0:
			meta = append(meta, ytClock(v.Duration))
		}
		if v.Date != "" {
			meta = append(meta, "uploaded "+v.Date)
		}
		if v.Views >= 0 {
			meta = append(meta, ytCount(v.Views)+" views")
		}
		if len(meta) > 0 {
			b.WriteString(strings.Join(meta, " · ") + "\n")
		}
		if v.Desc != "" {
			d := v.Desc
			if len(d) > 200 {
				d = strings.ToValidUTF8(d[:200], "") + "…"
			}
			b.WriteString(strings.Join(strings.Fields(d), " ") + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("These are titles and metadata only - nothing here tells you what was actually said in a video. Call youtube_transcript on one before summarising or quoting it.")
	return b.String()
}

// formatYouTubeComments renders the comment block. The header states plainly
// what comments are (opinion, ranked by likes, not a sample of anything) —
// models otherwise report "viewers say X" as if it were a measured result.
func formatYouTubeComments(cs []ytComment, meta ytTranscript, citation int) string {
	var b strings.Builder
	if citation > 0 {
		fmt.Fprintf(&b, "[%d] ", citation)
	}
	b.WriteString("Top YouTube comments")
	if meta.Title != "" {
		fmt.Fprintf(&b, " on %q", meta.Title)
	}
	b.WriteString("\nhttps://www.youtube.com/watch?v=" + meta.ID + "\n")
	fmt.Fprintf(&b, "The %d most-liked top-level comments, replies excluded. These are individual opinions ranked by likes - not a representative sample, not fact-checked, and not the video's content. Quote them as what a commenter said, never as what is true or as \"the consensus\".\n\n", len(cs))
	for i, c := range cs {
		author := c.Author
		if author == "" {
			author = "(unknown)"
		}
		fmt.Fprintf(&b, "%d. %s", i+1, author)
		var tags []string
		if c.ByOwner {
			tags = append(tags, "channel owner")
		}
		if c.Pinned {
			tags = append(tags, "pinned")
		}
		if c.Likes >= 0 {
			tags = append(tags, ytCount(c.Likes)+" likes")
		}
		if len(tags) > 0 {
			b.WriteString(" (" + strings.Join(tags, ", ") + ")")
		}
		b.WriteString(": " + strings.Join(strings.Fields(c.Text), " ") + "\n")
	}
	return b.String()
}

// --- small shared helpers ---------------------------------------------------

// ytCount humanises a view/like count. Exact numbers past a few thousand are
// noise in a summary and cost tokens.
func ytCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dK", n/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func clampInt(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ytDlpErr renders an exec error, preferring yt-dlp's own stderr line over
// "exit status 1".
func ytDlpErr(err error) string {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return firstLine(string(ee.Stderr))
	}
	return err.Error()
}
