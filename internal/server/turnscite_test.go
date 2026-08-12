package server

import "testing"

func TestTurns_stripPhantomCites(t *testing.T) {
	one := []turnCitation{{N: 1, Title: "A", URL: "http://a"}}
	cases := []struct {
		name  string
		in    string
		cites []turnCitation
		want  string
	}{
		{"no sources at all", "It compresses [1].", nil, "It compresses."},
		{"known source kept", "It shipped in March [1].", one, "It shipped in March [1]."},
		{"unknown number dropped", "Real [1] and made-up [4].", one, "Real [1] and made-up."},
		{"several markers", "Rests on both [1][2].", one, "Rests on both [1]."},
		{"nothing to do", "Plain prose.", nil, "Plain prose."},
		{"array index in inline code", "Use `arr[0]` here.", nil, "Use `arr[0]` here."},
		{"array index in fence", "```go\nx := a[0]\n```", nil, "```go\nx := a[0]\n```"},
		{"prose around a fence", "Nope [2].\n```\na[1]\n```\nAlso [3].", nil, "Nope.\n```\na[1]\n```\nAlso."},
		{"unclosed fence left alone", "text\n```\na[1]", nil, "text\n```\na[1]"},
	}
	for _, c := range cases {
		if got := stripPhantomCites(c.in, c.cites); got != c.want {
			t.Errorf("%s: stripPhantomCites(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// A citation registered by a later tool round must survive: the strip runs once
// at end of turn, against the full registry.
func TestTurns_stripPhantomCitesKeepsLateSources(t *testing.T) {
	cites := []turnCitation{{N: 1}, {N: 2}, {N: 3}}
	in := "First [1], then [2], finally [3], but not [9]."
	want := "First [1], then [2], finally [3], but not."
	if got := stripPhantomCites(in, cites); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
