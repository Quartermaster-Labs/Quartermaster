package server

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The `fetch_feed` tool: read one RSS or Atom feed.
//
// "What's new on X" is otherwise three flaky searches that return last year's
// article, because a search engine ranks by relevance and a feed is ordered by
// time. Sites that publish one are handing over exactly the answer.
//
// The URL comes from the model, so this reuses pageClient() — the SAME SSRF
// guard as fetch_page (dial-time IP check, no proxy). Do not swap in a plain
// http.Client here.

const (
	feedMaxBytes = 2 << 20
	feedMaxItems = 15
	feedSummary  = 320 // chars of each item's description handed to the model
	feedCacheTTL = 15 * time.Minute
	feedCacheMax = 32
	maxFeeds     = 5
)

type feedItem struct {
	Title, Link, Date, Summary string
}

type feedDoc struct {
	Title, URL string
	Items      []feedItem
	FetchedAt  time.Time
}

type feedCacheEntry struct {
	doc *feedDoc
	at  time.Time
}

var (
	feedCacheMu sync.Mutex
	feedCache   = map[string]feedCacheEntry{}
)

func parseFeedArgs(raw string) (string, int) {
	var a struct {
		URL   string  `json:"url"`
		Feed  string  `json:"feed"`
		Limit float64 `json:"limit"`
		Max   float64 `json:"max_items"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", 0
	}
	u := strings.TrimSpace(a.URL)
	if u == "" {
		u = strings.TrimSpace(a.Feed)
	}
	n := int(a.Limit)
	if n <= 0 {
		n = int(a.Max)
	}
	if n <= 0 || n > feedMaxItems {
		n = feedMaxItems
	}
	return u, n
}

// rssFeed / atomFeed cover the two formats worth supporting. RDF (RSS 1.0)
// parses as rssFeed too — its <item> elements sit at the top level rather than
// under <channel>, hence the second item slice.
type rssFeed struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			Date    string `xml:"pubDate"`
			DCDate  string `xml:"date"`
			Desc    string `xml:"description"`
			Content string `xml:"encoded"`
		} `xml:"item"`
	} `xml:"channel"`
	Title string `xml:"title"`
	Items []struct {
		Title   string `xml:"title"`
		Link    string `xml:"link"`
		Date    string `xml:"pubDate"`
		DCDate  string `xml:"date"`
		Desc    string `xml:"description"`
		Content string `xml:"encoded"`
	} `xml:"item"`
}

type atomFeed struct {
	Title   string `xml:"title"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
		Summary   string `xml:"summary"`
		Content   string `xml:"content"`
	} `xml:"entry"`
}

func fetchFeed(ctx context.Context, raw string, limit int) (*feedDoc, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http(s) feed URL", raw)
	}
	key := u.String()
	feedCacheMu.Lock()
	if e, ok := feedCache[key]; ok && time.Since(e.at) < feedCacheTTL {
		feedCacheMu.Unlock()
		return e.doc, nil
	}
	feedCacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", pageUserAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	resp, err := pageClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, u.Host)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, feedMaxBytes))
	if err != nil {
		return nil, err
	}

	doc := &feedDoc{URL: key, FetchedAt: time.Now()}
	if err := parseFeedBody(body, doc); err != nil {
		return nil, err
	}
	if len(doc.Items) == 0 {
		return nil, fmt.Errorf("no entries found - %s may not be a feed (an HTML page is not one; look for a /feed, /rss or /atom.xml URL)", u.Host)
	}
	if len(doc.Items) > limit {
		doc.Items = doc.Items[:limit]
	}
	// Resolve relative item links against the feed's own URL.
	for i := range doc.Items {
		if l, err := u.Parse(doc.Items[i].Link); err == nil {
			doc.Items[i].Link = l.String()
		}
	}

	feedCacheMu.Lock()
	if len(feedCache) >= feedCacheMax {
		feedCache = map[string]feedCacheEntry{}
	}
	feedCache[key] = feedCacheEntry{doc: doc, at: time.Now()}
	feedCacheMu.Unlock()
	return doc, nil
}

// parseFeedBody tries RSS then Atom. Split out from the fetch so it is testable
// without a network.
func parseFeedBody(body []byte, doc *feedDoc) error {
	var rss rssFeed
	if err := xml.Unmarshal(body, &rss); err == nil {
		items := rss.Channel.Items
		if len(items) == 0 {
			items = rss.Items
		}
		if len(items) > 0 {
			doc.Title = feedFirst(rss.Channel.Title, rss.Title)
			for _, it := range items {
				doc.Items = append(doc.Items, feedItem{
					Title:   cleanFeedText(it.Title, 200),
					Link:    strings.TrimSpace(it.Link),
					Date:    feedFirst(strings.TrimSpace(it.Date), strings.TrimSpace(it.DCDate)),
					Summary: cleanFeedText(feedFirst(it.Desc, it.Content), feedSummary),
				})
			}
			return nil
		}
	}
	var atom atomFeed
	if err := xml.Unmarshal(body, &atom); err != nil {
		return fmt.Errorf("could not parse as RSS or Atom: %v", err)
	}
	doc.Title = cleanFeedText(atom.Title, 200)
	for _, e := range atom.Entries {
		link := ""
		for _, l := range e.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		doc.Items = append(doc.Items, feedItem{
			Title:   cleanFeedText(e.Title, 200),
			Link:    strings.TrimSpace(link),
			Date:    feedFirst(strings.TrimSpace(e.Published), strings.TrimSpace(e.Updated)),
			Summary: cleanFeedText(feedFirst(e.Summary, e.Content), feedSummary),
		})
	}
	return nil
}

var feedTag = regexp.MustCompile(`(?s)<[^>]*>`)

// cleanFeedText strips the markup feeds embed in descriptions and collapses the
// whitespace, then truncates. Feed summaries are frequently a whole article's
// HTML — unstripped, five items would swamp the context window.
func cleanFeedText(s string, max int) string {
	s = feedTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		// Cut on a rune boundary, then back to the last space so a word is not
		// sliced in half.
		cut := max
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
		if i := strings.LastIndex(s, " "); i > max/2 {
			s = s[:i]
		}
		s += "…"
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func feedFirst(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// formatFeed renders the result. Dates are normalized where they parse and
// passed through where they don't — a wrong date here is worse than an ugly one.
func formatFeed(doc *feedDoc) string {
	var b strings.Builder
	title := doc.Title
	if title == "" {
		title = doc.URL
	}
	fmt.Fprintf(&b, "Feed: %s (%s)\nRead at %s. Newest entries first, as the feed itself ordered them:\n",
		title, doc.URL, doc.FetchedAt.Format("2006-01-02 15:04"))
	for i, it := range doc.Items {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, it.Title)
		if d := prettyFeedDate(it.Date); d != "" {
			fmt.Fprintf(&b, "   %s\n", d)
		}
		if it.Link != "" {
			fmt.Fprintf(&b, "   %s\n", it.Link)
		}
		if it.Summary != "" {
			fmt.Fprintf(&b, "   %s\n", it.Summary)
		}
	}
	b.WriteString("\nThese are headlines and blurbs, not articles: call fetch_page on an entry's link before summarising or quoting what it actually says.")
	return b.String()
}

var feedDateLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339,
	"Mon, 2 Jan 2006 15:04:05 -0700", "2006-01-02T15:04:05Z07:00", "2006-01-02",
}

func prettyFeedDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, l := range feedDateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("2 Jan 2006")
		}
	}
	return s
}
