package server

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
)

// Verbatim replay of a tool turn.
//
// `replayToolCalls` (turnsreplay.go) RECONSTRUCTS a past turn from the `searches`
// the client stores. What it rebuilds is close to, but not the same bytes as,
// what the turn loop actually forwarded while that turn ran:
//
//   - live sends the round's prose ON the tool_calls message; the rebuild sends
//     an empty one and glues that prose onto the front of the answer instead
//   - live sends the full result plus the cite reminder (`citeHint`); the rebuild
//     sends it truncated at `replayResultMax` with `replayNote` on the end
//   - live uses the model's own tool-call ids; the rebuild invents `hist_i_j`
//
// So the turn that was just served with one set of bytes is re-sent with another
// on the NEXT message, and the prompt prefix changes retroactively, mid
// conversation. That is a partial reprefill on a plain-attention model and a
// TOTAL one on a recurrent/hybrid arch, which cannot rewind to an arbitrary
// earlier position: measured on qwen3.8-27b, 56k tokens and 96 s of prefill with
// `cached_tokens: 0` on a turn whose history was identical but for one tool
// block five messages back.
//
// The fix is to stop reconstructing what we already have. Each turn records the
// exact `apiTail` it forwarded — assistant-with-calls, tool results, any nudge —
// and the next turn splices those bytes back in. The rebuild stays as the
// fallback for anything not in the store (a turn from before a restart, an
// evicted entry, a chat imported from elsewhere), which is exactly today's
// behaviour, so a miss costs a reprefill and never an error.
//
// Deliberately in memory only. Persisting it would put megabytes of tool output
// into chats.json for the client to sync on every read, to save one reprefill
// per conversation per server restart.

// Store bounds. A turn's tail is the round prose plus its tool results — tens of
// KB typically, a few hundred with transcripts in it. 128 turns covers every
// chat a session realistically comes back to; the byte cap is the real limiter
// and is charged against the raw JSON we hold.
const (
	replayStoreMaxEntries = 128
	replayStoreMaxBytes   = 32 << 20
)

// turnRecord is one turn's forwarded tail, as sent.
type turnRecord struct {
	// msgs is the tail verbatim: the assistant messages carrying tool_calls, the
	// tool result messages, and any nudge the loop appended between rounds.
	msgs []json.RawMessage
	// spoken is the prose each recorded assistant message already carries, in
	// order. The client concatenates every round's content into ONE stored
	// message, so replaying that message whole would send the same prose twice —
	// once inside the recorded tool_calls message, once at the front of the
	// answer. trimSpoken takes it back off.
	spoken []string
	size   int
}

// trimSpoken removes the prose already present in the recorded messages from the
// front of the client's stored answer, leaving what the model generated AFTER
// the last tool result — which is what the live prompt had at that position.
func (r *turnRecord) trimSpoken(content string) string {
	out := content
	trimmed := false
	for _, s := range r.spoken {
		if s == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(out, s); ok {
			out = rest
			trimmed = true
		}
	}
	if trimmed {
		out = strings.TrimLeft(out, "\n")
	}
	return out
}

// replayStore is a byte-bounded LRU of turnRecords, keyed by replayRecordKey.
type replayStore struct {
	mu         sync.Mutex
	items      map[string]*list.Element
	lru        *list.List // front = most recently used; Value = *replayEntry
	bytes      int
	maxEntries int
	maxBytes   int
}

type replayEntry struct {
	key string
	rec *turnRecord
}

func newReplayStore() *replayStore {
	return &replayStore{
		items:      map[string]*list.Element{},
		lru:        list.New(),
		maxEntries: replayStoreMaxEntries,
		maxBytes:   replayStoreMaxBytes,
	}
}

// put stores (or replaces) a turn's tail. A record larger than the whole budget
// is dropped rather than evicting everything else for one transcript-heavy turn.
func (s *replayStore) put(key string, rec *turnRecord) {
	if s == nil || key == "" || rec == nil || len(rec.msgs) == 0 || rec.size > s.maxBytes {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		s.bytes -= el.Value.(*replayEntry).rec.size
		el.Value.(*replayEntry).rec = rec
		s.bytes += rec.size
		s.lru.MoveToFront(el)
	} else {
		s.items[key] = s.lru.PushFront(&replayEntry{key: key, rec: rec})
		s.bytes += rec.size
	}
	for s.lru.Len() > s.maxEntries || s.bytes > s.maxBytes {
		back := s.lru.Back()
		if back == nil {
			break
		}
		e := back.Value.(*replayEntry)
		s.lru.Remove(back)
		delete(s.items, e.key)
		s.bytes -= e.rec.size
	}
}

// get returns the recorded tail for a key, refreshing its recency. nil = miss,
// and the caller falls back to reconstruction.
func (s *replayStore) get(key string) *turnRecord {
	if s == nil || key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[key]
	if !ok {
		return nil
	}
	s.lru.MoveToFront(el)
	return el.Value.(*replayEntry).rec
}

// replayRecordKey identifies a turn by the search metadata that turn produced —
// the same data the client hands back on the next message, so both sides compute
// it from identical bytes. NOT the message index: compaction and edits shift
// those, and a shifted index would splice one turn's tool results under another
// turn's answer. The chat id is folded in so two chats that ran the identical
// search cannot share a record whose cite numbering came from the other one.
func replayRecordKey(chatID string, searches []turnSearch) string {
	if len(searches) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(chatID))
	for _, s := range searches {
		h.Write([]byte{0})
		h.Write([]byte(s.Kind))
		h.Write([]byte{0})
		h.Write([]byte(s.Query))
		h.Write([]byte{0})
		h.Write([]byte(s.Results))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// recordTurn stores what this turn forwarded, so the next one can replay it
// byte-for-byte instead of rebuilding an approximation. Called from runLoop's
// defer, i.e. also on error/cancel: a turn that died after two searches still
// gets those two results replayed exactly.
func (tm *turnManager) recordTurn(at *activeTurn, chatID string, tail []json.RawMessage, spoken []string) {
	if tm == nil || tm.replays == nil || len(tail) == 0 {
		return
	}
	at.mu.Lock()
	searches := append([]turnSearch(nil), at.searches...)
	at.mu.Unlock()
	key := replayRecordKey(chatID, searches)
	if key == "" {
		return // no searches persisted → nothing on the next turn will look this up
	}
	rec := &turnRecord{
		msgs:   append([]json.RawMessage(nil), tail...),
		spoken: append([]string(nil), spoken...),
	}
	for _, m := range rec.msgs {
		rec.size += len(m)
	}
	tm.replays.put(key, rec)
}

// replayLookup is the hook replayToolCalls uses to find a recorded turn.
func (tm *turnManager) replayLookup(chatID string) func([]turnSearch) *turnRecord {
	if tm == nil || tm.replays == nil {
		return nil
	}
	return func(searches []turnSearch) *turnRecord {
		return tm.replays.get(replayRecordKey(chatID, searches))
	}
}
