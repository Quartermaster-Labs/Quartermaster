package tools

import (
	"strings"
	"testing"
)

func TestTools_ParseVideoID(t *testing.T) {
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
		if got := ParseVideoID(in); got != want {
			t.Errorf("ParseVideoID(%q) = %q, want %q", in, got, want)
		}
	}
}

// Auto-caption VTT is a rolling window: every cue repeats the previous cue's
// last line, and words carry inline <time><c> markup. Both must be gone, and
// cues must fold into ~30s paragraphs with ONE timestamp each -- that is the
// whole point of the tool being affordable in context.
func TestTools_VTTToParagraphs(t *testing.T) {
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

func TestTools_YTClock(t *testing.T) {
	for sec, want := range map[int]string{0: "0:00", 9: "0:09", 61: "1:01", 872: "14:32", 3600: "1:00:00", 3725: "1:02:05"} {
		if got := ytClock(sec); got != want {
			t.Errorf("ytClock(%d) = %q, want %q", sec, got, want)
		}
	}
}

// Truncation must keep whole paragraphs and report the timestamp it cut at, so
// the model can say what it is missing instead of summarising a third of a
// video as if it were the whole thing.
func TestTools_YTTruncateReportsCut(t *testing.T) {
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

func TestTools_FormatTranscriptAnnouncesTruncation(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("[" + ytClock(i*30) + "] " + strings.Repeat("word ", 40) + "\n\n")
	}
	tr := Transcript{ID: "dQw4w9WgXcQ", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Site: "youtube.com", Title: "Long talk", Uploader: "Chan", Duration: 7200, Text: strings.TrimSpace(b.String())}

	out := FormatTranscript(tr, 3, 0)
	for _, want := range []string{"[3] YouTube transcript", `"Long talk"`, "Chan, 2:00:00", "watch?v=dQw4w9WgXcQ", "INCOMPLETE", "transcript truncated at"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	short := FormatTranscript(Transcript{ID: "dQw4w9WgXcQ", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Text: "[0:00] hi"}, 0, 0)
	if strings.Contains(short, "INCOMPLETE") || strings.HasPrefix(short, "[0]") {
		t.Errorf("short transcript mis-annotated:\n%s", short)
	}
}

// Off YouTube there is no id, so the header must not claim YouTube and must not
// offer a &t= deep link that would 404 on the other site.
func TestTools_FormatTranscriptOffYouTube(t *testing.T) {
	out := FormatTranscript(Transcript{URL: "https://vimeo.com/12345", Site: "vimeo.com", Title: "Talk", Text: "[0:00] hi"}, 0, 0)
	if strings.Contains(out, "YouTube") || strings.Contains(out, "&t=") {
		t.Errorf("non-YouTube transcript headed as YouTube: %q", out)
	}
	if !strings.Contains(out, "https://vimeo.com/12345") {
		t.Errorf("source URL missing: %q", out)
	}
}

// Everything handed to yt-dlp is vetted first: the target by ParseMediaTarget,
// the language code by a strict regex.
func TestTools_ParseMediaTarget(t *testing.T) {
	// A bare id and every YouTube URL shape canonicalise to one watch URL, so
	// the cache keys on it and two spellings of the same video hit once.
	for _, in := range []string{"dQw4w9WgXcQ", "https://youtu.be/dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=x"} {
		got, err := ParseMediaTarget(t.Context(), in)
		if err != nil {
			t.Fatalf("ParseMediaTarget(%q): %v", in, err)
		}
		if got.ID != "dQw4w9WgXcQ" || got.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
			t.Errorf("ParseMediaTarget(%q) = %+v", in, got)
		}
	}
	// Any other site passes through with no id - that is the whole point.
	got, err := ParseMediaTarget(t.Context(), "https://vimeo.com/12345")
	if err != nil || got.ID != "" || got.Site != "vimeo.com" {
		t.Errorf("vimeo: %+v err=%v", got, err)
	}
	// Rejected: not a URL, non-http schemes, credentials, and anything that
	// resolves to an address the SSRF rule keeps yt-dlp away from.
	for _, bad := range []string{"", "not a url", "file:///etc/passwd", "ftp://example.com/a", "http://user:pw@example.com/a", "http://127.0.0.1/a", "http://192.168.1.10/v.mp4", "http://localhost:9000/x"} {
		if _, err := ParseMediaTarget(t.Context(), bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTools_GetTranscriptRejectsBadInput(t *testing.T) {
	if _, err := GetTranscript(t.Context(), "not-an-id", "en"); err == nil {
		t.Error("bad video id accepted")
	}
	if _, err := GetTranscript(t.Context(), "dQw4w9WgXcQ", "en; rm -rf /"); err == nil {
		t.Error("injected language code accepted")
	}
}

// The per-call transcript budget works by shrinking each fetch's ceiling, so a
// long transcript must truncate to what is left — loudly, never silently.
func TestTools_FormatTranscriptBudget(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("[0:30] some spoken words here\n\n")
	}
	tr := Transcript{ID: "dQw4w9WgXcQ", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Title: "Long", Text: b.String()}
	small := FormatTranscript(tr, 0, 500)
	big := FormatTranscript(tr, 0, 0) // 0 = the per-video ceiling
	if len(small) >= len(big) {
		t.Errorf("budget ignored: small=%d big=%d", len(small), len(big))
	}
	if !strings.Contains(small, "INCOMPLETE") {
		t.Errorf("truncation not announced:\n%s", small[:300])
	}
	// An over-large ask is clamped to the per-video ceiling, not honoured.
	if got := FormatTranscript(tr, 0, 10*ytMaxTokens); len(got) != len(big) {
		t.Errorf("ceiling not enforced: %d vs %d", len(got), len(big))
	}
}
