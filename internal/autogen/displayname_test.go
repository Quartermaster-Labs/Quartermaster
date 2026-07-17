package autogen

import "testing"

// resolvePublicName swaps a renamed base id's prefix onto every served id
// (base + variants), leaves unrenamed ids alone, and picks the longest matching
// base so a renamed "foo" can't shadow a separately-renamed "foo-mtp".
func TestResolvePublicName(t *testing.T) {
	dn := map[string]string{
		"qwen3.6-27b":     "qwen27b",
		"qwen3.6-27b-mtp": "fast27b", // longer base wins for its own subtree
		"blank":           "",        // empty name => not a rename
	}
	cases := []struct{ id, want string }{
		{"qwen3.6-27b", "qwen27b"},               // solo base
		{"qwen3.6-27b-vision", "qwen27b-vision"}, // variant inherits + keeps suffix
		{"qwen3.6-27b-mtp", "fast27b"},           // exact longer-base match wins
		{"qwen3.6-27b-mtp-q4", "fast27b-q4"},     // longest-prefix, not the short base
		{"other-model", ""},                      // unrenamed => ""
		{"blank", ""},                            // empty name => not renamed
		{"blank-x", ""},
	}
	for _, c := range cases {
		if got := resolvePublicName(dn, c.id); got != c.want {
			t.Errorf("resolvePublicName(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}
