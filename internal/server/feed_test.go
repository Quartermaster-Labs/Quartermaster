package server

import (
	"strings"
	"testing"
)

const rssSample = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Example Blog</title>
  <item>
    <title>Second post</title>
    <link>/posts/2</link>
    <pubDate>Tue, 04 Aug 2026 09:00:00 +0000</pubDate>
    <description>&lt;p&gt;Some &lt;b&gt;markup&lt;/b&gt; in the summary.&lt;/p&gt;</description>
  </item>
  <item>
    <title>First post</title>
    <link>https://example.com/posts/1</link>
    <pubDate>Mon, 03 Aug 2026 09:00:00 +0000</pubDate>
  </item>
</channel></rss>`

const atomSample = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Example</title>
  <entry>
    <title>Atom entry</title>
    <link rel="alternate" href="https://example.org/a"/>
    <published>2026-08-05T10:00:00Z</published>
    <summary>A short summary.</summary>
  </entry>
</feed>`

func TestParseFeedBody_RSS(t *testing.T) {
	var doc feedDoc
	if err := parseFeedBody([]byte(rssSample), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Example Blog" || len(doc.Items) != 2 {
		t.Fatalf("got title %q, %d items", doc.Title, len(doc.Items))
	}
	if doc.Items[0].Title != "Second post" {
		t.Errorf("item order/title: %q", doc.Items[0].Title)
	}
	// Markup must be stripped and entities decoded, or an item's HTML swamps
	// the context window.
	if got := doc.Items[0].Summary; got != "Some markup in the summary." {
		t.Errorf("summary = %q", got)
	}
}

func TestParseFeedBody_Atom(t *testing.T) {
	var doc feedDoc
	if err := parseFeedBody([]byte(atomSample), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 1 || doc.Items[0].Link != "https://example.org/a" {
		t.Fatalf("got %+v", doc.Items)
	}
	if doc.Items[0].Date == "" {
		t.Error("published date dropped")
	}
}

func TestParseFeedBody_NotAFeed(t *testing.T) {
	var doc feedDoc
	if err := parseFeedBody([]byte("<html><body>hello</body></html>"), &doc); err == nil && len(doc.Items) > 0 {
		t.Error("an HTML page parsed as a feed")
	}
}

func TestCleanFeedText(t *testing.T) {
	long := strings.Repeat("word ", 200)
	got := cleanFeedText(long, 50)
	if len(got) > 60 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncation: %q (%d)", got, len(got))
	}
	if got := cleanFeedText("a &amp; b   c", 100); got != "a & b c" {
		t.Errorf("got %q", got)
	}
}

func TestPrettyFeedDate(t *testing.T) {
	if got := prettyFeedDate("Tue, 04 Aug 2026 09:00:00 +0000"); got != "4 Aug 2026" {
		t.Errorf("got %q", got)
	}
	// Unparseable dates pass through rather than being guessed at.
	if got := prettyFeedDate("last tuesday"); got != "last tuesday" {
		t.Errorf("got %q", got)
	}
}

func TestParseFeedArgs(t *testing.T) {
	u, n := parseFeedArgs(`{"url":"https://x/feed","limit":5}`)
	if u != "https://x/feed" || n != 5 {
		t.Fatalf("got %q %d", u, n)
	}
	if _, n := parseFeedArgs(`{"feed":"https://x","limit":999}`); n != feedMaxItems {
		t.Errorf("limit not capped: %d", n)
	}
}
