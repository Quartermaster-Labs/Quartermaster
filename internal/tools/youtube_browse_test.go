package tools

import (
	"strings"
	"testing"
)

// Everything ytChannelURL accepts is pasted into a URL that reaches yt-dlp's
// argv, so this is the boundary that matters: forms in, and nothing else.
func TestTools_ChannelURL(t *testing.T) {
	ok := []struct{ in, tab, want string }{
		{"@LinusTechTips", "", "https://www.youtube.com/@LinusTechTips/videos"},
		{"@LinusTechTips", "shorts", "https://www.youtube.com/@LinusTechTips/shorts"},
		{"https://www.youtube.com/@Computerphile", "streams", "https://www.youtube.com/@Computerphile/streams"},
		{"youtube.com/@Computerphile/videos", "", "https://www.youtube.com/@Computerphile/videos"},
		{"UCXuqSBlHAE6Xw-yeJA0Tunw", "", "https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw/videos"},
		{"https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw", "", "https://www.youtube.com/channel/UCXuqSBlHAE6Xw-yeJA0Tunw/videos"},
		{"https://www.youtube.com/c/Kurzgesagt", "", "https://www.youtube.com/c/Kurzgesagt/videos"},
		{"https://www.youtube.com/user/Vsauce", "", "https://www.youtube.com/user/Vsauce/videos"},
		// A playlist is listable as-is; the tab is irrelevant to it.
		{"https://www.youtube.com/playlist?list=PLdo5W4Nhv31bZSiqiOL5ta39vSnBxpOPT", "shorts", "https://www.youtube.com/playlist?list=PLdo5W4Nhv31bZSiqiOL5ta39vSnBxpOPT"},
		// A watch URL carrying a list= is still a listable playlist.
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL123456", "", "https://www.youtube.com/playlist?list=PL123456"},
	}
	for _, c := range ok {
		got, err := ytChannelURL(c.in, c.tab)
		if err != nil {
			t.Errorf("ytChannelURL(%q,%q) errored: %v", c.in, c.tab, err)
			continue
		}
		if got != c.want {
			t.Errorf("ytChannelURL(%q,%q) = %q, want %q", c.in, c.tab, got, c.want)
		}
	}

	bad := []struct{ in, tab string }{
		{"", ""},
		{"https://evil.example.com/@x", ""},
		{"https://www.youtube.com/", ""},
		{"@handle with spaces", ""},
		{"@x/../../etc", ""},
		{"https://www.youtube.com/channel/notachannelid", ""},
		// A tab is a path segment: only the known ones may reach it.
		{"@LinusTechTips", "../../evil"},
		{"@LinusTechTips", "about"},
	}
	for _, c := range bad {
		if got, err := ytChannelURL(c.in, c.tab); err == nil {
			t.Errorf("ytChannelURL(%q,%q) accepted, got %q", c.in, c.tab, got)
		}
	}
}

// Flat entries vary by yt-dlp version and extractor: every field is optional,
// non-video entries are dropped, and a garbage line must not kill the batch.
func TestTools_ParseFlatJSON(t *testing.T) {
	raw := strings.Join([]string{
		`{"id":"dQw4w9WgXcQ","title":"A","channel":"Chan","duration":212.0,"view_count":1600000000,"upload_date":"20091025"}`,
		`not json at all`,
		`{"_type":"playlist","id":"PL123","title":"a playlist"}`,
		`{"id":"aaaaaaaaaaa","title":"B","uploader":"Up","timestamp":1700000000,"live_status":"is_live"}`,
		// Channel-tab shape: no channel/uploader/date of its own, only the
		// playlist_* fields and a release_year.
		`{"id":"bbbbbbbbbbb","title":"C","playlist_channel":"PC","release_year":2024}`,
	}, "\n")
	vids := parseFlatJSON(raw, 10)
	if len(vids) != 3 {
		t.Fatalf("got %d videos, want 3: %+v", len(vids), vids)
	}
	if vids[0].Duration != 212 || vids[0].Date != "2009-10-25" || vids[0].Views != 1600000000 {
		t.Errorf("entry 0 = %+v", vids[0])
	}
	// uploader is the fallback for channel; unix timestamps normalise too.
	if vids[1].Channel != "Up" || vids[1].Live != "is_live" || vids[1].Date != "2023-11-14" {
		t.Errorf("entry 1 = %+v", vids[1])
	}
	// Unknown view count must read as unknown (-1), never as zero views; the
	// channel and the coarse year come off the playlist_* fallbacks.
	if vids[2].Views != -1 || vids[2].Channel != "PC" || vids[2].Date != "2024" {
		t.Errorf("entry 2 = %+v", vids[2])
	}
	if got := parseFlatJSON(raw, 2); len(got) != 2 {
		t.Errorf("limit ignored: got %d", len(got))
	}
}

func TestTools_Count(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1500: "1.5K", 45000: "45K", 2_400_000: "2.4M", 1_600_000_000: "1.6B"}
	for n, want := range cases {
		if got := Count(n); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTools_ClampInt(t *testing.T) {
	if got := clampInt(0, 1, 10, 8); got != 8 {
		t.Errorf("unset = %d, want default 8", got)
	}
	if got := clampInt(50, 1, 10, 8); got != 10 {
		t.Errorf("over = %d, want 10", got)
	}
	if got := clampInt(-3, 1, 10, 8); got != 8 {
		t.Errorf("negative = %d, want default 8", got)
	}
}

// The id reaches argv, so a malformed one must never get that far.
func TestTools_GetCommentsRejectsBadID(t *testing.T) {
	if _, _, err := GetComments(t.Context(), "not-an-id", 5); err == nil {
		t.Error("bad video id accepted")
	}
}

// A comment block that reads like the video's own content is the failure mode
// worth guarding: the header must say what these are.
func TestTools_FormatComments(t *testing.T) {
	out := FormatComments(
		[]Comment{{Author: "someone", Text: "great video", Likes: 4200, Pinned: true}},
		Transcript{ID: "dQw4w9WgXcQ", Title: "T"},
		2,
	)
	for _, want := range []string{"[2] ", "opinions", "watch?v=dQw4w9WgXcQ", "4.2K likes", "pinned"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestTools_FormatVideos(t *testing.T) {
	out := FormatVideos(`"x"`, []Video{
		{ID: "dQw4w9WgXcQ", Title: "A", Channel: "Chan", Duration: 212, Views: 1600000000, Date: "2009-10-25"},
		{ID: "aaaaaaaaaaa", Title: "B", Live: "is_live", Views: -1},
	}, []int{1, 2}, false)
	for _, want := range []string{"[1] A", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "3:32", "1.6B views", "LIVE NOW", "relevance order", "media_transcript"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Unknown view count must not render as "0 views".
	if strings.Contains(out, "0 views") {
		t.Errorf("unknown view count rendered as a number:\n%s", out)
	}
}

// A dateless listing (the normal shape of a search result) must say so, or the
// model calls the first hit "the latest video".
func TestTools_FormatVideosDatelessSaysSo(t *testing.T) {
	undated := FormatVideos("channel @x", []Video{{ID: "dQw4w9WgXcQ", Title: "A", Views: -1}}, []int{1}, true)
	if !strings.Contains(undated, "Upload dates are not available") {
		t.Errorf("dateless listing did not disclaim dates:\n%s", undated)
	}
	if !strings.Contains(undated, "newest first") {
		t.Errorf("channel listing did not state its ordering:\n%s", undated)
	}
	dated := FormatVideos("channel @x", []Video{{ID: "dQw4w9WgXcQ", Title: "A", Views: -1, Date: "2024-01-02"}}, []int{1}, true)
	if strings.Contains(dated, "Upload dates are not available") {
		t.Errorf("dated listing wrongly disclaimed dates:\n%s", dated)
	}
}
