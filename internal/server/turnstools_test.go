package server

import (
	"strings"
	"testing"
)

func TestTurns_answerOnly(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"<think>plan</think>answer", "answer"},
		{"  <think>plan</think>\n\nanswer", "answer"},              // leading ws trimmed
		{"<think>still thinking", ""},                              // unclosed think, no answer yet
		{"<think>a</think>mid<thinking>b</thinking>end", "midend"}, // multiple flavours
	}
	for _, c := range cases {
		if got := answerOnly(c.in); got != c.want {
			t.Errorf("answerOnly(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTurns_harmonyToThink(t *testing.T) {
	// analysis channel → reasoning, final channel → answer.
	in := "<|channel|>analysis<|message|>thinking<|channel|>final<|message|>the answer"
	got := harmonyToThink(in)
	if want := "<think>thinking</think>the answer"; got != want {
		t.Fatalf("harmonyToThink = %q, want %q", got, want)
	}
	// No channel markup → untouched.
	if got := harmonyToThink("plain"); got != "plain" {
		t.Errorf("harmonyToThink(plain) = %q", got)
	}
}

func TestTurns_searchWiki(t *testing.T) {
	// Corpus must embed (Makefile copies wiki_articles.json into the package).
	if len(wikiArticles) == 0 {
		t.Fatal("wikiArticles empty — wiki_articles.json failed to embed")
	}
	res := searchWiki("how do I set up web search")
	if len(res) == 0 {
		t.Fatal("expected wiki matches for web-search query")
	}
	if res[0].ID != "web-search" {
		t.Errorf("top result = %q, want web-search", res[0].ID)
	}
	if len(res) > wikiMaxResults {
		t.Errorf("returned %d results, cap is %d", len(res), wikiMaxResults)
	}
	// No alphanumeric terms → no results.
	if r := searchWiki("!!!"); r != nil {
		t.Errorf("expected nil for term-less query, got %v", r)
	}
}

func TestTurns_formatSearchResults(t *testing.T) {
	// Pin the stamped date: the model has to see the real one, so it is part of
	// the contract, not incidental formatting.
	orig := searchDate
	searchDate = func() string { return "5 August 2026" }
	defer func() { searchDate = orig }()

	if got := formatSearchResults("q", nil, nil); got != `No results found for "q". (Searched 5 August 2026.)` {
		t.Errorf("empty format = %q", got)
	}
	rs := []searchResult{{Title: "T", URL: "http://x", Content: "snip"}}
	got := formatSearchResults("q", rs, []int{3})
	if !strings.Contains(got, "5 August 2026") {
		t.Errorf("format = %q, want the search date stamped", got)
	}
	if !strings.HasSuffix(got, "\n\n[3] T\nhttp://x\nsnip") {
		t.Errorf("format = %q, want the numbered result block last", got)
	}
}
