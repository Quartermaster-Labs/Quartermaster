package config

import "testing"

func TestSplitCtxRequest(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantBase string
		wantCtx  int
		wantOK   bool
	}{
		{"plain model", "qwen", "qwen", 0, false},
		{"with ctx", "qwen?ctx=32768", "qwen", 32768, true},
		{"spaces tolerated", "qwen?ctx= 4096 ", "qwen", 4096, true},
		{"non-numeric", "qwen?ctx=big", "qwen?ctx=big", 0, false},
		{"empty value", "qwen?ctx=", "qwen?ctx=", 0, false},
		{"below floor", "qwen?ctx=16", "qwen?ctx=16", 0, false},
		{"above ceiling", "qwen?ctx=99999999", "qwen?ctx=99999999", 0, false},
		{"negative", "qwen?ctx=-4096", "qwen?ctx=-4096", 0, false},
		// A model id may legitimately contain '?'; only the ctx marker splits.
		{"other query", "qwen?foo=1", "qwen?foo=1", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, ctx, ok := SplitCtxRequest(tc.in)
			if base != tc.wantBase || ctx != tc.wantCtx || ok != tc.wantOK {
				t.Errorf("SplitCtxRequest(%q) = %q,%d,%v want %q,%d,%v",
					tc.in, base, ctx, ok, tc.wantBase, tc.wantCtx, tc.wantOK)
			}
		})
	}
}

// The ctx suffix must resolve to the REAL model id, so listener scoping,
// API-key scoping and metrics labels all see the base model and cannot be
// bypassed by appending "?ctx=".
func TestRealModelName_CtxSuffixResolvesToBase(t *testing.T) {
	c := &Config{
		Models:  map[string]ModelConfig{"qwen": {}},
		aliases: map[string]string{"q": "qwen"},
	}
	for _, in := range []string{"qwen?ctx=32768", "q?ctx=32768"} {
		got, ok := c.RealModelName(in)
		if !ok || got != "qwen" {
			t.Errorf("RealModelName(%q) = %q,%v want qwen,true", in, got, ok)
		}
	}
	// An unknown base is still unknown, suffix or not.
	if _, ok := c.RealModelName("nope?ctx=32768"); ok {
		t.Error("unknown base model must not resolve")
	}
	// An unusable ctx value falls through to a plain (failing) lookup rather than
	// silently loading the model at its configured size.
	if _, ok := c.RealModelName("qwen?ctx=nope"); ok {
		t.Error("malformed ctx suffix must not resolve to the base model")
	}
}
