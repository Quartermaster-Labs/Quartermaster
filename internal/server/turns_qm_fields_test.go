package server

import (
	"strings"
	"testing"
)

// The field catalog is reflection-derived, so the guarantee worth testing is
// "every DTO field is reachable" — not a hand-listed sample that would drift
// exactly like the prose list this replaced.
func TestServer_QmFieldCatalogCoversDTO(t *testing.T) {
	specs := qmModelFieldSpecs()
	if len(specs) < 50 {
		t.Fatalf("model field catalog looks truncated: %d fields", len(specs))
	}
	want := map[string]string{"ctx": "int", "chatTemplateFile": "string", "slotCache": "bool|null", "ctxVariants": "int[]", "vramTargetGB": "number", "variants": "object[]"}
	got := map[string]string{}
	for _, s := range specs {
		got[s.Name] = s.Type
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("field %q: type %q, want %q", name, got[name], typ)
		}
	}
	cat := qmFieldCatalog()
	for _, s := range specs {
		if !strings.Contains(cat, "• "+s.Name+" (") {
			t.Errorf("catalog is missing field %q", s.Name)
		}
	}
}

func TestServer_QmValidateChanges(t *testing.T) {
	specs := qmModelFieldSpecs()
	cases := []struct {
		name    string
		changes map[string]any
		wantSub string // "" => must validate clean
	}{
		{"ok", map[string]any{"ctx": float64(32768), "chatTemplateFile": "D:/t.jinja"}, ""},
		{"nullable pointer", map[string]any{"slotCache": nil}, ""},
		{"null on non-pointer", map[string]any{"ctx": nil}, "cannot be null"},
		{"typo suggests field", map[string]any{"chatTemplate": "x"}, "did you mean 'chatTemplateFile'"},
		{"unknown field", map[string]any{"turboMode": true}, "unknown field 'turboMode'"},
		{"wrong type", map[string]any{"ctx": "big"}, "must be an integer"},
		{"fractional int", map[string]any{"ctx": 1.5}, "must be a whole number"},
		{"variants array", map[string]any{"variants": []any{}}, "one variant at a time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateQmChanges(specs, tc.changes)
			if tc.wantSub == "" {
				if msg != "" {
					t.Fatalf("want clean, got %q", msg)
				}
				return
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("got %q, want it to contain %q", msg, tc.wantSub)
			}
		})
	}
}
