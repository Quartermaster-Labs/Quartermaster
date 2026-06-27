package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/logmon"
	"github.com/tidwall/gjson"
)

func anchorOf(t *testing.T, body string) (key string, continued, ok bool) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	k, _, c, o := sessionAnchor(r)
	return k, c, o
}

// X-Conversation-Id keys the file: stable even when the opening is rewritten
// (compaction), and distinct ids never collide despite an identical opening.
func TestSessionAnchor_ConversationIDHeader(t *testing.T) {
	mk := func(id, body string) (string, bool, bool) {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		if id != "" {
			r.Header.Set("X-Conversation-Id", id)
		}
		k, _, c, o := sessionAnchor(r)
		return k, c, o
	}
	orig := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`
	compacted := `{"messages":[{"role":"user","content":"SUMMARY: ...different opening..."}]}`

	kOrig, _, _ := mk("chat-1", orig)
	kCompacted, _, _ := mk("chat-1", compacted) // same id, rewritten opening
	if kOrig != kCompacted {
		t.Errorf("same conversation id must yield same key across compaction: %s vs %s", kOrig, kCompacted)
	}

	// Two different chats with an IDENTICAL opening must not collide when ids differ.
	kA, _, _ := mk("chat-A", orig)
	kB, _, _ := mk("chat-B", orig)
	if kA == kB {
		t.Error("distinct conversation ids with same opening must not collide")
	}

	// Header beats content: same id, different opening => key tracks the id only.
	if kOrig == kA {
		t.Error("different ids should produce different keys regardless of body")
	}
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

// fakeBackend stands in for a llama-server: /slots reports KV occupancy and
// /slots/0?action=save writes the snapshot file the real server would.
func fakeBackend(t *testing.T, kvTokens int, saveDir string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/slots" && r.Method == http.MethodGet:
			fmt.Fprintf(w, `[{"n_prompt_tokens":%d,"n_ctx":100000,"is_processing":false}]`, kvTokens)
		case r.URL.Path == "/slots/0" && r.URL.Query().Get("action") == "save":
			body, _ := io.ReadAll(r.Body)
			fn := gjson.GetBytes(body, "filename").String()
			os.WriteFile(filepath.Join(saveDir, fn), []byte("kv"), 0o644)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/slots/0" && r.URL.Query().Get("action") == "restore":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/chat/completions":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newEvictTestCache(dir, base string) *slotCache {
	sc := &slotCache{
		enabled:         true,
		dir:             dir,
		minTokens:       30000,
		maxBytes:        1 << 30,
		maxFiles:        20,
		participates:    func(string) bool { return true },
		running:         func() map[string]string { return map[string]string{"m": base} },
		client:          &http.Client{Timeout: 5 * time.Second},
		log:             logmon.NewWriter(io.Discard),
		occupant:        map[string]*occInfo{},
		pending:         map[string]string{},
		pendingSeed:     map[string]bool{},
		pendingPreamble: map[string]string{},
		awaitConfirm:    map[string]string{},
	}
	return sc
}

func TestSlotCache_SaveOnEvict(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 35000, dir) // above 30k threshold
	sc := newEvictTestCache(dir, srv.URL)
	sc.occupant["m"] = &occInfo{key: "abc", continued: false, dirty: true} // 1-user-turn pi run

	sc.saveOnEvict("m")

	if _, err := os.Stat(filepath.Join(dir, fileName("m", "abc"))); err != nil {
		t.Fatalf("expected KV snapshot written on evict, got %v", err)
	}
	if sc.occupant["m"] != nil {
		t.Error("occupant should be dropped after evict-save (slot is gone)")
	}
}

func TestSlotCache_SaveOnEvict_BelowThresholdSkips(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 5000, dir) // below 30k threshold
	sc := newEvictTestCache(dir, srv.URL)
	sc.occupant["m"] = &occInfo{key: "abc", dirty: true}

	sc.saveOnEvict("m")

	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("cheap conversation must not be saved, got %d files", len(entries))
	}
}

// Cold restore: a saved file + a pending key => restoreOnLoad restores it after
// the model reloads and marks it resident. This is the switch-back round trip.
func TestSlotCache_RestoreOnLoad(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 0, dir)
	sc := newEvictTestCache(dir, srv.URL)
	// A prior eviction left a snapshot on disk.
	os.WriteFile(filepath.Join(dir, fileName("m", "abc")), []byte("kv"), 0o644)

	sc.markPendingRestore("m", "abc", "") // request arrived for cold model with saved KV
	if sc.pending["m"] != "abc" {
		t.Fatal("expected pending restore recorded")
	}
	sc.restoreOnLoad("m") // process readied

	if sc.pending["m"] != "" {
		t.Error("pending should be cleared after restore")
	}
	if occ := sc.occupant["m"]; occ == nil || occ.key != "abc" {
		t.Errorf("expected restored conversation marked resident, got %+v", occ)
	}
}

// No saved file => no pending, no restore (cold-prefill as normal).
func TestSlotCache_RestoreOnLoad_NoFileSkips(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 0, dir)
	sc := newEvictTestCache(dir, srv.URL)

	sc.markPendingRestore("m", "abc", "") // nothing saved
	if _, ok := sc.pending["m"]; ok {
		t.Error("no file => must not record pending restore")
	}
	sc.restoreOnLoad("m")
	if sc.occupant["m"] != nil {
		t.Error("no pending => no restore, occupant stays empty")
	}
}

// Tier 1: bestSeed picks the saved session sharing the longest preamble prefix,
// honours the minimum-prefix threshold, and skips oversized files.
func TestSlotCache_BestSeed(t *testing.T) {
	dir := t.TempDir()
	sc := &slotCache{dir: dir, occupant: map[string]*occInfo{}}

	shared := strings.Repeat("S", seedMinPrefixBytes+500) // long common static preamble
	writeSession := func(key, meta string, binSize int) {
		os.WriteFile(filepath.Join(dir, fileName("m", key)), make([]byte, binSize), 0o644)
		os.WriteFile(filepath.Join(dir, metaName("m", key)), []byte(meta), 0o644)
	}
	writeSession("good", shared+"_AAAA", 1024) // long shared prefix, small file
	writeSession("short", "S_only_tiny", 1024) // tiny shared prefix

	incoming := shared + "_CCCC_different_tail"
	key, _, ok := sc.bestSeed("m", incoming)
	if !ok || key != "good" {
		t.Fatalf("bestSeed = (%q, %v), want good,true", key, ok)
	}

	// A preamble that shares less than the threshold yields no seed.
	if _, _, ok := sc.bestSeed("m", "Sx_nothing_in_common"); ok {
		t.Error("below-threshold prefix must not seed")
	}
}

// confirmReuse only counts when a restore is awaiting confirmation, and splits
// on whether the upstream actually reported cached tokens.
func TestSlotCache_ConfirmReuse(t *testing.T) {
	sc := newEvictTestCache(t.TempDir(), "")

	// No restore pending => warm-slot reuse isn't ours; nothing recorded.
	sc.confirmReuse("m", 100, 50)
	if sc.counters.ConfirmedReuses != 0 || sc.counters.ConfirmedMisses != 0 {
		t.Fatalf("unexpected counters without a pending restore: %+v", sc.counters)
	}

	// Restore happened and the next request reused KV.
	sc.awaitConfirm["m"] = "restore-hit"
	sc.confirmReuse("m", 5000, 2680)
	if sc.counters.ConfirmedReuses != 1 || sc.counters.CachedTokensSeen != 2680 {
		t.Fatalf("confirmed reuse not recorded: %+v", sc.counters)
	}
	if _, ok := sc.awaitConfirm["m"]; ok {
		t.Error("awaitConfirm must clear after a confirmation")
	}

	// Restore happened but upstream cached nothing => confirm-miss.
	sc.awaitConfirm["m"] = "restore-seed"
	sc.confirmReuse("m", 5000, 0)
	if sc.counters.ConfirmedMisses != 1 || sc.counters.ConfirmedReuses != 1 {
		t.Fatalf("confirm-miss not recorded: %+v", sc.counters)
	}
}

// Preamble cache: a cold load with no exact file mints a system+tools-only seed,
// a later cold load with the same preamble reuses it (hit, no re-mint), and the
// file is categorized separately from per-conversation snapshots.
func TestSlotCache_PreambleCache(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 0, dir)
	sc := newEvictTestCache(dir, srv.URL)

	preamble := strings.Repeat("S", seedMinPrefixBytes+100) + "\x00tools\x00[]"
	pfile := fileName("m", preambleKey(preambleHash(preamble)))

	sc.markPendingRestore("m", "conv1", preamble) // cold, no exact file
	sc.restoreOnLoad("m")
	if sc.counters.PreambleMints != 1 {
		t.Fatalf("expected 1 preamble mint, got %+v", sc.counters)
	}
	if _, err := os.Stat(filepath.Join(dir, pfile)); err != nil {
		t.Fatalf("preamble cache file not written: %v", err)
	}

	sc.markPendingRestore("m", "conv2", preamble) // different conversation, same preamble
	sc.restoreOnLoad("m")
	if sc.counters.PreambleMints != 1 || sc.counters.PreambleHits != 1 {
		t.Fatalf("expected mint=1 hit=1, got %+v", sc.counters)
	}

	st := sc.stats()
	if len(st.PreambleFiles) != 1 || len(st.Files) != 0 {
		t.Fatalf("expected 1 preamble file and 0 conversation files, got %d/%d", len(st.PreambleFiles), len(st.Files))
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
