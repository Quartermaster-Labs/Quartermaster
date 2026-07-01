package server

import "testing"

func TestPromptCanon_Canonicalize(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantHit bool
		wantIn  string // substring expected in the rewritten body
	}{
		{
			name:    "openai system timestamp stripped",
			body:    `{"messages":[{"role":"system","content":"session_start at 2026-06-29 12:35:44 UTC"},{"role":"user","content":"hi"}]}`,
			wantHit: true,
			wantIn:  "session_start at 2026-06-29 UTC",
		},
		{
			name:    "developer role also normalized",
			body:    `{"messages":[{"role":"developer","content":"now 2026-06-29T12:35:44Z"}]}`,
			wantHit: true,
			wantIn:  "now 2026-06-29",
		},
		{
			name:    "anthropic top-level system",
			body:    `{"system":"started 2026-06-29 08:00:00","messages":[{"role":"user","content":"hi"}]}`,
			wantHit: true,
			wantIn:  "started 2026-06-29",
		},
		{
			name:    "bare date untouched",
			body:    `{"messages":[{"role":"system","content":"date 2026-06-29"}]}`,
			wantHit: false,
		},
		{
			name:    "no system message",
			body:    `{"messages":[{"role":"user","content":"2026-06-29 12:00:00"}]}`,
			wantHit: false,
		},
		{
			name:    "non-chat body",
			body:    `{"input":"2026-06-29 12:00:00"}`,
			wantHit: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, rule, removed := canonicalizeBody([]byte(tc.body))
			hit := rule != "" && removed > 0
			if hit != tc.wantHit {
				t.Fatalf("hit=%v (rule=%q removed=%d), want %v", hit, rule, removed, tc.wantHit)
			}
			if tc.wantHit {
				if !contains(string(out), tc.wantIn) {
					t.Fatalf("rewritten body %q missing %q", out, tc.wantIn)
				}
				// Idempotent: a second pass removes nothing.
				if _, _, r2 := canonicalizeBody(out); r2 != 0 {
					t.Fatalf("second pass removed %d bytes, want 0 (not idempotent)", r2)
				}
			} else if string(out) != tc.body {
				t.Fatalf("no-op case rewrote body: %q", out)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
