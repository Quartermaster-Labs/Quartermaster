package server

import (
	"encoding/json"
	"testing"
)

// mergeAndDiff decides what the approval card shows AND what gets PUT — a no-op
// leaking through would apply an empty change or mislead the user. JSON numbers
// decode to float64, so a "same value" must compare equal across that.
func TestMergeAndDiff(t *testing.T) {
	cur := map[string]any{}
	json.Unmarshal([]byte(`{"ctx":8192,"vramTargetGB":22.0,"kvK":"q8_0"}`), &cur)
	chg := map[string]any{}
	json.Unmarshal([]byte(`{"ctx":32768,"vramTargetGB":22.0,"kvK":"q4_0"}`), &chg)

	body, diff := mergeAndDiff(cur, chg)

	// vramTargetGB unchanged (22.0==22.0) → not in the diff; ctx + kvK are.
	if len(diff) != 2 {
		t.Fatalf("want 2 changed rows, got %d: %+v", len(diff), diff)
	}
	seen := map[string]bool{}
	for _, d := range diff {
		seen[d.Key] = true
	}
	if !seen["ctx"] || !seen["kvK"] || seen["vramTargetGB"] {
		t.Fatalf("wrong diff keys: %+v", diff)
	}
	// Merged body carries every field (changed + preserved) for the full-replace PUT.
	if !jsonEqual(body["ctx"], float64(32768)) || !jsonEqual(body["vramTargetGB"], 22.0) {
		t.Fatalf("merged body wrong: %+v", body)
	}
}

// The playground whitelist gates what a chat model may write into a user's
// prefs — a bad type or out-of-range value must be rejected before it reaches
// disk, and unlisted keys must not resolve at all.
func TestPlaygroundPrefFields(t *testing.T) {
	// Valid values pass and coerce as expected.
	if v, err := playgroundPrefFields["webSearch"].validate(true); err != nil || v != true {
		t.Fatalf("webSearch=true: got %v, %v", v, err)
	}
	if _, err := playgroundPrefFields["temperature"].validate(1.5); err != nil {
		t.Fatalf("temperature=1.5 should pass: %v", err)
	}
	if _, err := playgroundPrefFields["maxTokens"].validate(4096.0); err != nil {
		t.Fatalf("maxTokens=4096 should pass: %v", err)
	}

	// Range + type violations are rejected.
	if _, err := playgroundPrefFields["temperature"].validate(5.0); err == nil {
		t.Fatal("temperature=5 out of range, want error")
	}
	if _, err := playgroundPrefFields["maxTokens"].validate(0.0); err == nil {
		t.Fatal("maxTokens=0 below min, want error")
	}
	if _, err := playgroundPrefFields["webSearch"].validate("yes"); err == nil {
		t.Fatal("webSearch=string, want type error")
	}

	// Unlisted settings don't resolve (off-limits: presets, theme, model, …).
	if _, ok := playgroundPrefFields["systemPresets"]; ok {
		t.Fatal("systemPresets must not be writable")
	}
}

func TestMergeAndDiff_NoOp(t *testing.T) {
	cur := map[string]any{}
	json.Unmarshal([]byte(`{"ttlSec":3600}`), &cur)
	chg := map[string]any{}
	json.Unmarshal([]byte(`{"ttlSec":3600}`), &chg)
	if _, diff := mergeAndDiff(cur, chg); len(diff) != 0 {
		t.Fatalf("identical value should produce no diff, got %+v", diff)
	}
}
