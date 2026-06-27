package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func anchorOf(t *testing.T, body string) (key string, continued, ok bool) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	return sessionAnchor(r)
}

func TestSessionAnchor_StableAcrossTurns(t *testing.T) {
	turn1 := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`
	turn2 := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"more"}]}`

	k1, cont1, ok1 := anchorOf(t, turn1)
	k2, cont2, ok2 := anchorOf(t, turn2)
	if !ok1 || !ok2 {
		t.Fatalf("expected ok for both turns, got %v %v", ok1, ok2)
	}
	if k1 != k2 {
		t.Errorf("key not stable across turns: %s vs %s", k1, k2)
	}
	if cont1 {
		t.Errorf("turn1 (1 user msg) should not be 'continued'")
	}
	if !cont2 {
		t.Errorf("turn2 (2 user msgs) should be 'continued'")
	}
}

// An agentic one-shot has a single user turn but many assistant/tool turns; it
// must NOT count as continued (else throwaway -p runs would persist).
func TestSessionAnchor_AgenticOneShotNotContinued(t *testing.T) {
	body := `{"messages":[
	  {"role":"user","content":"do a task"},
	  {"role":"assistant","content":"step 1"},
	  {"role":"tool","content":"result"},
	  {"role":"assistant","content":"step 2"},
	  {"role":"tool","content":"result"},
	  {"role":"assistant","content":"done"}
	]}`
	_, continued, ok := anchorOf(t, body)
	if !ok {
		t.Fatal("expected ok")
	}
	if continued {
		t.Error("agentic one-shot (1 user turn) must not be 'continued'")
	}
}

func TestSessionAnchor_DistinctConversations(t *testing.T) {
	a, _, _ := anchorOf(t, `{"messages":[{"role":"user","content":"alpha"}]}`)
	b, _, _ := anchorOf(t, `{"messages":[{"role":"user","content":"beta"}]}`)
	if a == b {
		t.Error("different first user messages should yield different keys")
	}
}

func TestSessionAnchor_NoUserSkips(t *testing.T) {
	if _, _, ok := anchorOf(t, `{"messages":[{"role":"system","content":"only system"}]}`); ok {
		t.Error("body with no user message must be skipped (ok=false)")
	}
	if _, _, ok := anchorOf(t, `{"prompt":"not a chat"}`); ok {
		t.Error("non-chat body must be skipped (ok=false)")
	}
}

func TestSlotCache_EnforceCaps(t *testing.T) {
	dir := t.TempDir()
	// Five 10-byte files, mtimes oldest..newest. Budget = 25 bytes => keep newest 2.
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, fileName("m", string(rune('a'+i))))
		if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(i)*time.Minute))
	}
	sc := &slotCache{dir: dir, maxBytes: 25, maxFiles: 100}
	sc.enforceCaps("m", "e") // 'e' is the newest, never evicted

	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 files after byte-cap eviction, got %d", len(entries))
	}
	// The two newest ('d','e') must survive; oldest ('a') must be gone.
	if _, err := os.Stat(filepath.Join(dir, fileName("m", "a"))); err == nil {
		t.Error("oldest file should have been evicted")
	}
	if _, err := os.Stat(filepath.Join(dir, fileName("m", "e"))); err != nil {
		t.Error("newest file should survive")
	}
}

func TestSlotCache_EnforceCapsByCount(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 4; i++ {
		p := filepath.Join(dir, fileName("m", string(rune('a'+i))))
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(i)*time.Minute))
	}
	sc := &slotCache{dir: dir, maxBytes: 1 << 30, maxFiles: 2}
	sc.enforceCaps("m", "d")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 files after count-cap eviction, got %d", len(entries))
	}
}
