package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/tidwall/gjson"
)

func anchorOf(t *testing.T, body string) (key string, ok bool) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	k, _, o := sessionAnchor(r)
	return k, o
}

func TestNormalizeTimestamps(t *testing.T) {
	cases := map[string]string{
		"session_start at 2026-06-29 12:35:44.": "session_start at 2026-06-29.",
		"2026-06-27T18:29:21Z":                  "2026-06-27",
		"2026-06-29 12:35+02:00 done":           "2026-06-29 done",
		"Current date: 2026-06-29":              "Current date: 2026-06-29", // bare date untouched
		"no timestamp here":                     "no timestamp here",
	}
	for in, want := range cases {
		if got := normalizeTimestamps(in); got != want {
			t.Errorf("normalizeTimestamps(%q) = %q, want %q", in, got, want)
		}
	}
}

// An agent that stamps the wall-clock time into its system prompt (pi's memory
// snapshot) must map to one stable preamble across runs, and the forwarded body
// must carry the date-only system so the cached KV prefix-matches upstream.
func TestSessionAnchor_NormalizeTimestamps(t *testing.T) {
	mk := func(ts string) (key, preamble, fwd string) {
		body := `{"messages":[{"role":"system","content":"You are pi. session_start at ` + ts + ` done"},{"role":"user","content":"hi"}]}`
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		k, p, _ := sessionAnchor(r)
		b, _ := io.ReadAll(r.Body) // body sessionAnchor re-attached for downstream
		// Content-Length must track the rewritten (shorter) body, or the reverse
		// proxy advertises a stale length and the upstream stalls (502).
		if r.ContentLength != int64(len(b)) {
			t.Errorf("ContentLength %d != forwarded body len %d", r.ContentLength, len(b))
		}
		return k, p, string(b)
	}
	k1, p1, f1 := mk("2026-06-29 12:35:44")
	k2, p2, _ := mk("2026-06-29 18:02:09")
	if k1 != k2 {
		t.Errorf("same-day different-time system must share a key: %s vs %s", k1, k2)
	}
	if p1 != p2 {
		t.Error("preamble must be timestamp-stable across runs")
	}
	if strings.Contains(f1, "12:35:44") {
		t.Errorf("forwarded body still carries the time-of-day:\n%s", f1)
	}
	if !strings.Contains(f1, "session_start at 2026-06-29 done") {
		t.Errorf("forwarded body lost the date:\n%s", f1)
	}
}

// X-Conversation-Id keys the file: stable even when the opening is rewritten
// (compaction), and distinct ids never collide despite an identical opening.
func TestSessionAnchor_ConversationIDHeader(t *testing.T) {
	mk := func(id, body string) (string, bool) {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		if id != "" {
			r.Header.Set("X-Conversation-Id", id)
		}
		k, _, o := sessionAnchor(r)
		return k, o
	}
	orig := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`
	compacted := `{"messages":[{"role":"user","content":"SUMMARY: ...different opening..."}]}`

	kOrig, _ := mk("chat-1", orig)
	kCompacted, _ := mk("chat-1", compacted) // same id, rewritten opening
	if kOrig != kCompacted {
		t.Errorf("same conversation id must yield same key across compaction: %s vs %s", kOrig, kCompacted)
	}

	// Two different chats with an IDENTICAL opening must not collide when ids differ.
	kA, _ := mk("chat-A", orig)
	kB, _ := mk("chat-B", orig)
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

	k1, ok1 := anchorOf(t, turn1)
	k2, ok2 := anchorOf(t, turn2)
	if !ok1 || !ok2 {
		t.Fatalf("expected ok for both turns, got %v %v", ok1, ok2)
	}
	if k1 != k2 {
		t.Errorf("key not stable across turns: %s vs %s", k1, k2)
	}
}

func TestSessionAnchor_DistinctConversations(t *testing.T) {
	a, _ := anchorOf(t, `{"messages":[{"role":"user","content":"alpha"}]}`)
	b, _ := anchorOf(t, `{"messages":[{"role":"user","content":"beta"}]}`)
	if a == b {
		t.Error("different first user messages should yield different keys")
	}
}

func TestSessionAnchor_NoUserSkips(t *testing.T) {
	if _, ok := anchorOf(t, `{"messages":[{"role":"system","content":"only system"}]}`); ok {
		t.Error("body with no user message must be skipped (ok=false)")
	}
	if _, ok := anchorOf(t, `{"prompt":"not a chat"}`); ok {
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
		awaitConfirm:    map[string][]string{},
	}
	return sc
}

func TestSlotCache_SaveOnEvict(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 35000, dir) // above 30k threshold
	sc := newEvictTestCache(dir, srv.URL)
	sc.occupant["m"] = &occInfo{key: "abc", dirty: true} // 1-user-turn pi run

	sc.saveOnEvict("m")

	if _, err := os.Stat(filepath.Join(dir, fileName("m", "abc"))); err != nil {
		t.Fatalf("expected KV snapshot written on evict, got %v", err)
	}
	if sc.occupant["m"] != nil {
		t.Error("occupant should be dropped after evict-save (slot is gone)")
	}
}

// A multi-GB save for model A must not stall a request for model B. Each model
// is its own llama-server with its own slot 0, so only same-model work needs
// serializing — the old single global sc.mu made every model queue behind the
// slowest save.
func TestSlotCache_SaveDoesNotBlockOtherModels(t *testing.T) {
	dir := t.TempDir()
	saving := make(chan struct{})  // closed once A's save is in flight
	release := make(chan struct{}) // closes to let A's save finish

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slots/0" && r.URL.Query().Get("action") == "save" {
			close(saving)
			<-release // stand in for a multi-GB write
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == "/slots" {
			fmt.Fprint(w, `[{"n_prompt_tokens":35000,"n_ctx":100000,"is_processing":false}]`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sc := newEvictTestCache(dir, srv.URL)
	sc.running = func() map[string]string { return map[string]string{"a": srv.URL, "b": srv.URL} }
	sc.occupant["a"] = &occInfo{key: "old", dirty: true}

	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		sc.onSwitch(context.Background(), "a", srv.URL, "new", "", 0)
	}()
	<-saving // A now holds its model lock, parked mid-save

	// B has no occupant and no preamble, so this is pure bookkeeping: it must
	// return immediately rather than wait behind A.
	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		sc.onSwitch(context.Background(), "b", srv.URL, "k", "", 0)
	}()
	select {
	case <-bDone:
	case <-time.After(5 * time.Second):
		t.Fatal("model b blocked behind model a's save — per-model locking regressed")
	}
	if occ := sc.occupant["b"]; occ == nil || occ.key != "k" {
		t.Errorf("expected b resident with key k, got %+v", occ)
	}

	close(release)
	<-aDone
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

	sc.markPendingRestore("m", "abc", "", 0) // request arrived for cold model with saved KV
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

	sc.markPendingRestore("m", "abc", "", 0) // nothing saved
	if _, ok := sc.pending["m"]; ok {
		t.Error("no file => must not record pending restore")
	}
	sc.restoreOnLoad("m")
	// The slot is CLAIMED for the incoming conversation (that is what stops a
	// second conversation from being assigned the same slot), but nothing was
	// restored into it and nothing has run there yet.
	if occ := sc.occupant["m"]; occ != nil && occ.dirty {
		t.Error("no pending => nothing restored or run, slot must not be dirty")
	}
	for _, ev := range sc.stats().Events {
		if strings.HasPrefix(ev.Op, "restore") {
			t.Errorf("no pending => no restore, got event %q", ev.Op)
		}
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

// A tail-free preamble cache is preferred over a longer-prefix conversation seed:
// restoring the conversation's diverging tail over-restores KV (wasted on plain
// models, "non-consecutive token position" + reprocess on hybrid). Among equal
// kinds, the smaller .bin (less excess tail) wins.
func TestSlotCache_BestSeed_MinimizesOverRestore(t *testing.T) {
	shared := strings.Repeat("S", seedMinPrefixBytes+500)
	write := func(dir, key, meta string, binSize int) {
		os.WriteFile(filepath.Join(dir, fileName("m", key)), make([]byte, binSize), 0o644)
		os.WriteFile(filepath.Join(dir, metaName("m", key)), []byte(meta), 0o644)
	}

	t.Run("prefers tail-free preamble over longer-prefix conversation", func(t *testing.T) {
		dir := t.TempDir()
		sc := &slotCache{dir: dir, occupant: map[string]*occInfo{}}
		write(dir, "convo", shared+"_AAAA", 4096)   // 1 byte longer shared prefix, big tail
		write(dir, preambleKey("zz"), shared, 2048) // tail-free, 1 byte shorter prefix
		key, _, ok := sc.bestSeed("m", shared+"_CCCC_tail")
		if !ok || key != preambleKey("zz") {
			t.Fatalf("bestSeed = (%q,%v), want tail-free preamble seed", key, ok)
		}
	})

	t.Run("among equals prefers the smaller .bin", func(t *testing.T) {
		dir := t.TempDir()
		sc := &slotCache{dir: dir, occupant: map[string]*occInfo{}}
		write(dir, "big", shared+"_X", 8192)
		write(dir, "small", shared+"_X", 1024) // same shared prefix, less excess tail
		key, _, ok := sc.bestSeed("m", shared+"_X_then_diverge")
		if !ok || key != "small" {
			t.Fatalf("bestSeed = (%q,%v), want small", key, ok)
		}
	})
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
	sc.pushAwait("m", "restore-hit")
	sc.confirmReuse("m", 5000, 2680)
	if sc.counters.ConfirmedReuses != 1 || sc.counters.CachedTokensSeen != 2680 {
		t.Fatalf("confirmed reuse not recorded: %+v", sc.counters)
	}
	if _, ok := sc.awaitConfirm["m"]; ok {
		t.Error("awaitConfirm must clear after a confirmation")
	}

	// Restore happened but upstream cached nothing => confirm-miss.
	sc.pushAwait("m", "restore-seed")
	sc.confirmReuse("m", 5000, 0)
	if sc.counters.ConfirmedMisses != 1 || sc.counters.ConfirmedReuses != 1 {
		t.Fatalf("confirm-miss not recorded: %+v", sc.counters)
	}

	// Two agents on one model interleave: both ops queue, both confirm (FIFO),
	// neither overwrites the other.
	sc.pushAwait("m", "preamble")
	sc.pushAwait("m", "restore-hit")
	sc.confirmReuse("m", 5000, 1000)
	sc.confirmReuse("m", 5000, 2000)
	if sc.counters.ConfirmedReuses != 3 || sc.counters.CachedTokensSeen != 2680+1000+2000 {
		t.Fatalf("FIFO confirms not both counted: %+v", sc.counters)
	}
}

// prunePreambleFiles is LRU-by-use: a touched (recently restored) preamble survives
// pruning even if it was minted earliest, while a stale untouched one is evicted.
func TestSlotCache_PrunePreambleLRU(t *testing.T) {
	dir := t.TempDir()
	sc := newEvictTestCache(dir, "")

	// One more preamble than the cap, oldest mtime first.
	base := time.Now().Add(-time.Hour)
	var keys []string
	for i := 0; i < maxPreambleGenerations+1; i++ {
		key := preambleKey(fmt.Sprintf("env%02d", i))
		keys = append(keys, key)
		bin := filepath.Join(dir, fileName("m", key))
		meta := filepath.Join(dir, metaName("m", key))
		if err := os.WriteFile(bin, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(meta, []byte("p"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Minute) // env00 oldest
		os.Chtimes(bin, mt, mt)
	}

	// env00 is the oldest by mint, but it's actively used → touch it to now.
	oldest := filepath.Join(dir, fileName("m", keys[0]))
	now := time.Now()
	os.Chtimes(oldest, now, now)

	sc.prunePreambleFiles("m")

	// env00 (touched) must survive; env01 (now the oldest untouched) must be gone.
	if _, err := os.Stat(oldest); err != nil {
		t.Errorf("touched preamble env00 was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, fileName("m", keys[1]))); !os.IsNotExist(err) {
		t.Errorf("stale preamble env01 should have been pruned, err=%v", err)
	}
}

// Recurrent archs skip the PARTIAL-prefix paths (preamble mint/seed) because those
// need a rewind a rolling state cannot do — but the EXACT restore still runs, and is
// the whole point: measured on Qwen3.8-27B, a cross-process exact restore reused
// 19,757 of 19,782 tokens (prefill 34.4s -> 0.35s). See seedSkip.
func TestSlotCache_RecurrentSkipsSeedsButRestoresExact(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 0, dir)
	sc := newEvictTestCache(dir, srv.URL)
	sc.recurrent = func(string) bool { return true }

	// Case A: cold, no exact file — must not mint or seed.
	preamble := strings.Repeat("S", seedMinPrefixBytes+100) + "\x00tools\x00[]"
	sc.markPendingRestore("m", "conv1", preamble, 0)
	sc.restoreOnLoad("m")
	if sc.counters.PreambleMints != 0 || sc.counters.RestoreSeeds != 0 {
		t.Fatalf("recurrent model must not mint or seed, got %+v", sc.counters)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("seed skip must write no files, got %d entries", len(entries))
	}

	// Case B: an EXACT saved file exists and is pending — it MUST be restored.
	exact := "conv2"
	if err := os.WriteFile(filepath.Join(dir, fileName("m", exact)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc.markPendingRestore("m", exact, preamble, 0)
	sc.restoreOnLoad("m")
	if sc.counters.RestoreHits != 1 {
		t.Fatalf("recurrent model must restore an exact file, got %+v", sc.counters)
	}
	if sc.counters.PreambleMints != 0 || sc.counters.RestoreSeeds != 0 {
		t.Fatalf("exact restore must not also seed, got %+v", sc.counters)
	}
}

// A conversation that went BACKWARDS (shorter body than the snapshot) must not be
// restored on a recurrent arch: the saved state runs past the incoming prompt, so
// serving it needs a rewind and reuses nothing while still paying the read.
// Plain attention trims fine and must still restore.
func TestSlotCache_StaleRestoreSkipsShorterOnRecurrent(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 0, dir)

	mk := func(recurrent bool) *slotCache {
		sc := newEvictTestCache(dir, srv.URL)
		sc.recurrent = func(string) bool { return recurrent }
		return sc
	}
	// A snapshot produced by a 5000-byte request.
	if err := os.WriteFile(filepath.Join(dir, fileName("m", "c")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk(true).writeSavedLen("m", "c", 5000)

	cases := []struct {
		name      string
		recurrent bool
		body      int
		want      bool
	}{
		{"recurrent shorter", true, 4990, true},
		{"recurrent equal", true, 5000, false},
		{"recurrent longer", true, 5200, false},
		{"plain attn shorter", false, 100, false},
		{"unknown body size", true, 0, false},
	}
	for _, c := range cases {
		if got := mk(c.recurrent).staleRestore("m", "c", c.body); got != c.want {
			t.Errorf("%s: staleRestore = %v, want %v", c.name, got, c.want)
		}
	}

	// No .len sidecar (a file saved by an older build) must still restore.
	if err := os.WriteFile(filepath.Join(dir, fileName("m", "old")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if mk(true).staleRestore("m", "old", 1) {
		t.Error("a snapshot with no .len must not be skipped")
	}

	// End to end through the cold path: shorter body => no pending restore.
	sc := mk(true)
	sc.markPendingRestore("m", "c", "", 4990)
	sc.restoreOnLoad("m")
	if sc.counters.RestoreHits != 0 {
		t.Fatalf("shorter body must not restore, got %+v", sc.counters)
	}
	sc2 := mk(true)
	sc2.markPendingRestore("m", "c", "", 6000)
	sc2.restoreOnLoad("m")
	if sc2.counters.RestoreHits != 1 {
		t.Fatalf("longer body must restore, got %+v", sc2.counters)
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

	sc.markPendingRestore("m", "conv1", preamble, 0) // cold, no exact file
	sc.restoreOnLoad("m")
	if sc.counters.PreambleMints != 1 {
		t.Fatalf("expected 1 preamble mint, got %+v", sc.counters)
	}
	if _, err := os.Stat(filepath.Join(dir, pfile)); err != nil {
		t.Fatalf("preamble cache file not written: %v", err)
	}

	sc.markPendingRestore("m", "conv2", preamble, 0) // different conversation, same preamble
	sc.restoreOnLoad("m")
	if sc.counters.PreambleMints != 1 || sc.counters.PreambleHits != 1 {
		t.Fatalf("expected mint=1 hit=1, got %+v", sc.counters)
	}

	st := sc.stats()
	if len(st.PreambleFiles) != 1 || len(st.Files) != 0 {
		t.Fatalf("expected 1 preamble file and 0 conversation files, got %d/%d", len(st.PreambleFiles), len(st.Files))
	}
}

// Warm path: a brand-new conversation on an already-loaded model mints the
// agent's preamble cache on first sight. A SECOND new conversation with the same
// preamble must NOT re-restore from disk — the slot already holds the preamble
// live, so we skip (preamble-warm) and let the upstream reuse the prefix natively.
// Restoring the disk copy would clobber valid live state (and yields 0 reuse on
// hybrid/linear-attn models).
func TestSlotCache_PreambleCache_WarmSwitch(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 0, dir)
	sc := newEvictTestCache(dir, srv.URL)

	preamble := strings.Repeat("S", seedMinPrefixBytes+100) + "\x00tools\x00[]"

	sc.onSwitch(context.Background(), "m", srv.URL, "conv1", preamble, 0) // warm, new convo
	if sc.counters.PreambleMints != 1 {
		t.Fatalf("warm switch should mint preamble, got %+v", sc.counters)
	}
	sc.onSwitch(context.Background(), "m", srv.URL, "conv2", preamble, 0) // same preamble, new convo
	if sc.counters.PreambleWarm != 1 || sc.counters.PreambleHits != 0 {
		t.Fatalf("same-preamble warm switch should skip restore (warm=1 hit=0), got %+v", sc.counters)
	}
}

// supersedesPreamble treats a small middle diff (a date) as the same agent, but
// not a genuinely different system prompt that merely shares identical tools.
func TestSlotCache_SupersedesPreamble(t *testing.T) {
	body := strings.Repeat("S", 4000)
	tools := "\x00tools\x00" + strings.Repeat("T", 4000)
	dateA := body + "Today is 2026-06-28." + body + tools
	dateB := body + "Today is 2026-06-29." + body + tools // same agent, date bumped
	if !supersedesPreamble(dateB, dateA) {
		t.Error("a date-only change must be detected as the same agent")
	}

	other := strings.Repeat("X", 8000) + tools // different system, identical tools
	if supersedesPreamble(other, dateA) {
		t.Error("a different system prompt sharing tools must NOT be treated as a refresh")
	}
}

// On mint of a refreshed (date-bumped) preamble, the prior generation is deleted.
func TestSlotCache_PreambleCache_DropsStale(t *testing.T) {
	dir := t.TempDir()
	srv := fakeBackend(t, 0, dir)
	sc := newEvictTestCache(dir, srv.URL)

	body := strings.Repeat("S", seedMinPrefixBytes)
	day1 := body + "date=2026-06-28" + "\x00tools\x00[]"
	day2 := body + "date=2026-06-29" + "\x00tools\x00[]"
	old := fileName("m", preambleKey(preambleHash(day1)))

	sc.onSwitch(context.Background(), "m", srv.URL, "c1", day1, 0) // mint day1
	if _, err := os.Stat(filepath.Join(dir, old)); err != nil {
		t.Fatalf("day1 preamble not written: %v", err)
	}
	sc.onSwitch(context.Background(), "m", srv.URL, "c2", day2, 0) // mint day2 -> drops day1
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Errorf("stale day1 preamble should be deleted, stat err=%v", err)
	}
}

// Anthropic /v1/messages: the system prompt lives in a top-level "system" field,
// not a system-role message — sessionAnchor must still surface it in the preamble.
func TestSessionAnchor_AnthropicSystem(t *testing.T) {
	body := `{"system":"you are helpful","tools":[{"name":"x"}],"messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	_, preamble, ok := sessionAnchor(r)
	if !ok {
		t.Fatal("expected anchor ok")
	}
	if !strings.Contains(preamble, "you are helpful") || !strings.Contains(preamble, `"name":"x"`) {
		t.Errorf("anthropic system+tools not captured in preamble: %q", preamble)
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

// fakeMultiBackend stands in for a llama-server launched with --parallel n:
// /slots reports n slots (busy[i] marks one mid-generation) and
// /slots/<i>?action=save writes the snapshot file, recording which slot it came
// from so a test can assert the pin actually landed.
func fakeMultiBackend(t *testing.T, n int, kvTokens int64, busy map[int]bool, saveDir string, saved map[string]int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slots" && r.Method == http.MethodGet {
			parts := make([]string, 0, n)
			for i := 0; i < n; i++ {
				parts = append(parts, fmt.Sprintf(`{"id":%d,"n_prompt_tokens":%d,"n_ctx":100000,"is_processing":%v}`, i, kvTokens, busy[i]))
			}
			fmt.Fprintf(w, "[%s]", strings.Join(parts, ","))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/slots/") {
			idx, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/slots/"))
			if err != nil || idx < 0 || idx >= n {
				w.WriteHeader(http.StatusBadRequest) // llama-server rejects an out-of-range id_slot
				return
			}
			body, _ := io.ReadAll(r.Body)
			fn := gjson.GetBytes(body, "filename").String()
			if r.URL.Query().Get("action") == "save" {
				os.WriteFile(filepath.Join(saveDir, fn), []byte("kv"), 0o644)
			}
			mu.Lock()
			saved[r.URL.Query().Get("action")+":"+fn] = idx
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Two conversations against a 2-slot model get one slot each, and each stays put
// when it comes back — the whole point of running more than one slot.
func TestSlotCache_MultiSlot_PinsConversationsToOwnSlots(t *testing.T) {
	dir := t.TempDir()
	saved := map[string]int{}
	srv := fakeMultiBackend(t, 2, 0, nil, dir, saved)
	sc := newEvictTestCache(dir, srv.URL)
	sc.slots = func(string) int { return 2 }

	a := sc.onSwitch(context.Background(), "m", srv.URL, "convA", "", 0)
	sc.markResident("m", a, "convA", "", 0)
	b := sc.onSwitch(context.Background(), "m", srv.URL, "convB", "", 0)
	sc.markResident("m", b, "convB", "", 0)
	if a == b {
		t.Fatalf("two conversations must land on different slots, both got %d", a)
	}
	if again := sc.onSwitch(context.Background(), "m", srv.URL, "convA", "", 0); again != a {
		t.Errorf("conversation A moved slots: %d -> %d", a, again)
	}
}

// With every slot occupied, a third conversation evicts the least-recently-used
// slot that is NOT generating — stealing the busy one would restore over a live
// stream.
func TestSlotCache_MultiSlot_SkipsBusySlot(t *testing.T) {
	dir := t.TempDir()
	saved := map[string]int{}
	// Slot 0 is the LRU one, but it is mid-generation, so slot 1 must be taken.
	srv := fakeMultiBackend(t, 2, 0, map[int]bool{0: true}, dir, saved)
	sc := newEvictTestCache(dir, srv.URL)
	sc.slots = func(string) int { return 2 }

	sc.markResident("m", 0, "convA", "", 0)
	sc.markResident("m", 1, "convB", "", 0)

	got := sc.onSwitch(context.Background(), "m", srv.URL, "convC", "", 0)
	if got != 1 {
		t.Fatalf("expected the idle slot 1, got %d", got)
	}
}

// A model swap must not throw away N-1 conversations: every occupied slot is
// snapshotted before the process dies.
func TestSlotCache_MultiSlot_SaveOnEvictAllSlots(t *testing.T) {
	dir := t.TempDir()
	saved := map[string]int{}
	srv := fakeMultiBackend(t, 2, 35000, nil, dir, saved) // above the 30k worth-it gate
	sc := newEvictTestCache(dir, srv.URL)
	sc.slots = func(string) int { return 2 }

	sc.markResident("m", 0, "convA", "", 0)
	sc.markResident("m", 1, "convB", "", 0)
	sc.saveOnEvict("m")

	for _, k := range []string{"convA", "convB"} {
		if _, err := os.Stat(filepath.Join(dir, fileName("m", k))); err != nil {
			t.Errorf("expected %s snapshotted on evict: %v", k, err)
		}
	}
	if saved["save:"+fileName("m", "convB")] != 1 {
		t.Errorf("convB must be saved from slot 1, got slot %d", saved["save:"+fileName("m", "convB")])
	}
	if sc.occupant[sk("m", 1)] != nil {
		t.Error("occupants dropped after evict-save (the slots are gone)")
	}
}

// Restores name the slot the conversation was pinned to, not slot 0.
func TestSlotCache_MultiSlot_RestoreTargetsPinnedSlot(t *testing.T) {
	dir := t.TempDir()
	saved := map[string]int{}
	srv := fakeMultiBackend(t, 2, 0, nil, dir, saved)
	sc := newEvictTestCache(dir, srv.URL)
	sc.slots = func(string) int { return 2 }
	os.WriteFile(filepath.Join(dir, fileName("m", "convB")), []byte("kv"), 0o644)

	sc.markResident("m", 0, "convA", "", 0) // slot 0 taken by a live conversation
	idx := sc.markPendingRestore("m", "convB", "", 0)
	if idx == 0 {
		t.Fatal("convB must not be pinned onto the slot convA holds")
	}
	sc.restoreOnLoad("m")
	if got, ok := saved["restore:"+fileName("m", "convB")]; !ok || got != idx {
		t.Errorf("restore targeted slot %d (present=%v), want %d", got, ok, idx)
	}
}

// pinSlot must rewrite the body AND its length, or the reverse proxy advertises
// a stale Content-Length and the upstream stalls.
func TestPinSlot(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	pinSlot(r, 3)
	got, _ := io.ReadAll(r.Body)
	if v := gjson.GetBytes(got, "id_slot").Int(); v != 3 {
		t.Errorf("id_slot = %d, want 3", v)
	}
	if r.ContentLength != int64(len(got)) || r.Header.Get("Content-Length") != strconv.Itoa(len(got)) {
		t.Errorf("Content-Length %d/%q != body %d", r.ContentLength, r.Header.Get("Content-Length"), len(got))
	}
}
