package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Server-side ports of the playground's tool + reasoning helpers, so the turn
// runner (turns.go) can drive the model→tool→model loop headlessly (survives a
// closed tab). Kept behaviourally identical to the client originals:
//   - wiki: ui-svelte/src/lib/wiki.ts (corpus shared via wiki_articles.json)
//   - web: ui-svelte/src/lib/webSearch.ts
//   - reasoning: ui-svelte/src/lib/reasoning.ts

// --- wiki corpus (single source: ui-svelte/src/lib/wikiArticles.json, copied
// here by the Makefile `ui` target so Go can embed it) ---------------------

//go:embed wiki_articles.json
var wikiJSON []byte

type wikiArticle struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Keywords []string `json:"keywords"`
	Body     string   `json:"body"`
}

var wikiArticles = func() []wikiArticle {
	var a []wikiArticle
	_ = json.Unmarshal(wikiJSON, &a)
	return a
}()

const wikiMaxResults = 3

// searchWiki scores articles by term overlap (title > keywords > body) and
// returns the best few. Port of wiki.ts searchWiki.
func searchWiki(query string) []wikiArticle {
	terms := regexp.MustCompile(`[a-z0-9]+`).FindAllString(strings.ToLower(query), -1)
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		a wikiArticle
		s int
	}
	var out []scored
	for _, a := range wikiArticles {
		title := strings.ToLower(a.Title)
		keys := strings.ToLower(strings.Join(a.Keywords, " "))
		body := strings.ToLower(a.Body)
		score := 0
		for _, t := range terms {
			if strings.Contains(title, t) {
				score += 3
			}
			if strings.Contains(keys, t) {
				score += 2
			}
			if strings.Contains(body, t) {
				score += 1
			}
		}
		if score > 0 {
			out = append(out, scored{a, score})
		}
	}
	// stable sort by score desc (small N, insertion sort keeps ties in corpus order)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].s > out[j-1].s; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > wikiMaxResults {
		out = out[:wikiMaxResults]
	}
	res := make([]wikiArticle, len(out))
	for i, s := range out {
		res[i] = s.a
	}
	return res
}

// formatWikiResults renders the plain-text tool message. Port of wiki.ts.
func formatWikiResults(query string, results []wikiArticle, numbers []int) string {
	if len(results) == 0 {
		var topics []string
		for _, a := range wikiArticles {
			topics = append(topics, "- "+a.Title)
		}
		return fmt.Sprintf("No wiki article matched %q. Available topics:\n%s", query, strings.Join(topics, "\n"))
	}
	var parts []string
	for i, a := range results {
		parts = append(parts, fmt.Sprintf("## [%d] %s\n%s", numbers[i], a.Title, a.Body))
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// --- web search (SearXNG) --------------------------------------------------

type searchResult struct {
	Title   string
	URL     string
	Content string
}

const (
	webMaxResults = 5
	snippetMax    = 400
)

// searxngSearch queries SearXNG's JSON API directly (server-side, no browser
// CORS concern). Port of webSearch.ts searxngSearch, minus the /api/websearch
// proxy hop (we ARE the server).
func searxngSearch(ctx context.Context, baseURL, query string) ([]searchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("SearXNG URL not set")
	}
	target, err := url.Parse(base + "/search")
	if err != nil {
		return nil, err
	}
	qs := target.Query()
	qs.Set("q", query)
	qs.Set("format", "json")
	target.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := webSearchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snip, _ := readLimited(resp.Body, 512)
		return nil, fmt.Errorf("searxng %s: %s", resp.Status, snip)
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]searchResult, 0, webMaxResults)
	for _, r := range parsed.Results {
		if len(out) >= webMaxResults {
			break
		}
		c := r.Content
		if len(c) > snippetMax {
			c = c[:snippetMax]
		}
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Content: c})
	}
	return out, nil
}

// formatSearchResults renders the plain-text tool message. Port of webSearch.ts.
func formatSearchResults(query string, results []searchResult, numbers []int) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q.", query)
	}
	var lines []string
	for i, r := range results {
		lines = append(lines, fmt.Sprintf("[%d] %s\n%s\n%s", numbers[i], r.Title, r.URL, r.Content))
	}
	return fmt.Sprintf("Search results for %q:\n\n%s", query, strings.Join(lines, "\n\n"))
}

// --- reasoning markup (port of reasoning.ts) -------------------------------

var (
	harmonyCtrlRe = regexp.MustCompile(`(?i)<\|(?:start|end|return|constrain)\|?>(?:assistant|user|system)?`)
	harmonyMsgRe  = regexp.MustCompile(`<\|message\|?>`)
	channelRe     = regexp.MustCompile(`(?i)<\|channel\|?>\s*([a-zA-Z]+)\s*(?:<\|message\|?>)?`)

	thinkClosedRe = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)>.*?</(?:think|thinking|reasoning)>`)
	thinkOpenRe   = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)>.*$`)
)

// harmonyToThink rewrites gpt-oss harmony channel markup into <think> blocks.
// Port of reasoning.ts harmonyToThink. No-op when no channel markup present.
func harmonyToThink(text string) string {
	locs := channelRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text
	}
	clean := func(s string) string {
		return harmonyMsgRe.ReplaceAllString(harmonyCtrlRe.ReplaceAllString(s, ""), "")
	}
	var b strings.Builder
	b.WriteString(clean(text[:locs[0][0]]))
	for i, m := range locs {
		start := m[1] // end of this channel marker
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		channel := strings.ToLower(text[m[2]:m[3]])
		body := clean(text[start:end])
		isFinal := channel == "final" || channel == "commentary"
		switch {
		case isFinal:
			b.WriteString(body)
		case i == len(locs)-1:
			b.WriteString("<think>" + body) // last segment, still streaming — leave open
		default:
			b.WriteString("<think>" + body + "</think>")
		}
	}
	return b.String()
}

// answerOnly strips reasoning markup of every flavour, leaving the answer text.
// Port of the inline answerOnly() in ChatInterface.svelte.
func answerOnly(s string) string {
	s = harmonyToThink(s)
	s = thinkClosedRe.ReplaceAllString(s, "")
	s = thinkOpenRe.ReplaceAllString(s, "")
	return strings.TrimLeft(s, " \t\r\n")
}
