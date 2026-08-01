package server

import (
	"strings"
	"testing"
)

func TestParseYouTubeID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":          "dQw4w9WgXcQ",
		"https://youtube.com/watch?v=dQw4w9WgXcQ&t=872s":       "dQw4w9WgXcQ",
		"http://m.youtube.com/watch?v=dQw4w9WgXcQ":             "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                         "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?t=30":                    "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":           "dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ":            "dQw4w9WgXcQ",
		"https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ":   "dQw4w9WgXcQ",
		"www.youtube.com/watch?v=dQw4w9WgXcQ":                  "dQw4w9WgXcQ",
		"dQw4w9WgXcQ":                                          "dQw4w9WgXcQ",
		"  https://www.youtube.com/watch?v=dQw4w9WgXcQ  ":      "dQw4w9WgXcQ",
		"https://vimeo.com/12345":                              "",
		"https://www.youtube.com/watch?v=short":                "",
		"https://example.com/watch?v=dQw4w9WgXcQ":              "",
		"not a url at all":                                     "",
		"":                                                     "",
		"https://www.youtube.com/playlist?list=PLdQw4w9WgXcQ1": "",
	}
	for in, want := range cases {
		if got := parseYouTubeID(in); got != want {
			t.Errorf("parseYouTubeID(%q) = %q, want %q", in, got, want)
		}
	}
}

// Auto-caption VTT is a rolling window: every cue repeats the previous cue's
// last line, and words carry inline <time><c> markup. Both must be gone, and
// cues must fold into ~30s paragraphs with ONE timestamp each -- that is the
// whole point of the tool being affordable in context.
func TestVTTToParagraphs(t *testing.T) {
	raw := "WEBVTT\nKind: captions\nLanguage: en\n\n" +
		"00:00:01.199 --> 00:00:03.629 align:start position:0%\nhello and welcome\n\n" +
		"00:00:03.629 --> 00:00:06.000 align:start position:0%\nhello and welcome\nto the <00:00:04.100><c> show</c>\n\n" +
		"00:00:06.000 --> 00:00:09.000\nto the show\ntoday we talk about &amp; things\n\n" +
		"00:00:41.500 --> 00:00:44.000\na much later point\n\n"

	got := vttToParagraphs(raw)
	paras := strings.Split(got, "\n\n")
	if len(paras) != 2 {
		t.Fatalf("paragraphs = %d, want 2 (30s granularity):\n%s", len(paras), got)
	}
	want0 := "[0:01] hello and welcome to the show today we talk about & things"
	if paras[0] != want0 {
		t.Errorf("paragraph 0 = %q, want %q", paras[0], want0)
	}
	if paras[1] != "[0:41] a much later point" {
		t.Errorf("paragraph 1 = %q, want %q", paras[1], "[0:41] a much later point")
	}
	if strings.Contains(got, "-->") || strings.Contains(got, "<c>") {
		t.Errorf("timing lines / markup survived:\n%s", got)
	}
}

func TestYTClock(t *testing.T) {
	for sec, want := range map[int]string{0: "0:00", 9: "0:09", 61: "1:01", 872: "14:32", 3600: "1:00:00", 3725: "1:02:05"} {
		if got := ytClock(sec); got != want {
			t.Errorf("ytClock(%d) = %q, want %q", sec, got, want)
		}
	}
}

// Truncation must keep whole paragraphs and report the timestamp it cut at, so
// the model can say what it is missing instead of summarising a third of a
// video as if it were the whole thing.
func TestYTTruncateReportsCut(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("[" + ytClock(i*30) + "] " + strings.Repeat("word ", 40) + "\n\n")
	}
	text := strings.TrimSpace(b.String())

	out, cutAt, truncated := ytTruncate(text, 100) // 100 tokens ~ 400 chars
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if len(out) > 400 {
		t.Errorf("kept %d chars, want <= 400", len(out))
	}
	if cutAt == "" || strings.Contains(cutAt, "[") {
		t.Errorf("cutAt = %q, want a bare timestamp", cutAt)
	}
	if strings.HasSuffix(out, "\n") || strings.Count(out, "[")-strings.Count(out, "]") != 0 {
		t.Errorf("truncated mid-paragraph: %q", out)
	}

	full, _, tr := ytTruncate(text, 1_000_000)
	if tr || full != text {
		t.Error("short-enough transcript must pass through untouched")
	}
}

func TestFormatYouTubeTranscriptAnnouncesTruncation(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("[" + ytClock(i*30) + "] " + strings.Repeat("word ", 40) + "\n\n")
	}
	tr := ytTranscript{ID: "dQw4w9WgXcQ", Title: "Long talk", Uploader: "Chan", Duration: 7200, Text: strings.TrimSpace(b.String())}

	out := formatYouTubeTranscript(tr, 3)
	for _, want := range []string{"[3] YouTube transcript", `"Long talk"`, "Chan, 2:00:00", "watch?v=dQw4w9WgXcQ", "INCOMPLETE", "transcript truncated at"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	short := formatYouTubeTranscript(ytTranscript{ID: "dQw4w9WgXcQ", Text: "[0:00] hi"}, 0)
	if strings.Contains(short, "INCOMPLETE") || strings.HasPrefix(short, "[0]") {
		t.Errorf("short transcript mis-annotated:\n%s", short)
	}
}

func TestParseYouTubeArgs(t *testing.T) {
	u, lang := parseYouTubeArgs(`{"url":" https://youtu.be/dQw4w9WgXcQ ","lang":"pt-BR"}`)
	if u != "https://youtu.be/dQw4w9WgXcQ" || lang != "pt-BR" {
		t.Errorf("got %q/%q", u, lang)
	}
	// Models routinely name the argument something else; accept the obvious alias.
	if u, _ := parseYouTubeArgs(`{"video":"dQw4w9WgXcQ"}`); u != "dQw4w9WgXcQ" {
		t.Errorf("video alias = %q", u)
	}
	if u, l := parseYouTubeArgs(`not json`); u != "" || l != "" {
		t.Errorf("bad json = %q/%q, want empty", u, l)
	}
}

// The language code goes into a yt-dlp argument; only well-formed codes may.
func TestFetchYouTubeTranscriptRejectsBadInput(t *testing.T) {
	if _, err := fetchYouTubeTranscript(t.Context(), "not-an-id", "en"); err == nil {
		t.Error("bad video id accepted")
	}
	if _, err := fetchYouTubeTranscript(t.Context(), "dQw4w9WgXcQ", "en; rm -rf /"); err == nil {
		t.Error("injected language code accepted")
	}
}
