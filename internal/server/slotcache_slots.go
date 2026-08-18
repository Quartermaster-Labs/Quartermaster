package server

// Multi-slot support for the slot KV cache: how many slots a model launches
// with (--parallel N), what each slot currently holds, which slot a given
// conversation gets pinned to, and the id_slot injection that makes that pin
// stick upstream.
//
// Why pin at all: llama-server picks a slot itself (longest common prefix), and
// its choice is invisible to us until after the fact. Our save/restore has to
// name a slot (POST /slots/<id>?action=...), so an unpinned multi-slot server
// would let us restore a conversation into a slot that is streaming tokens for
// someone else. Pinning makes the mapping ours: conversation -> slot is decided
// here, the request carries it, and save/restore always targets the slot that
// actually holds the conversation.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sk is the bookkeeping key for one model's slot. Slot 0 keys by the bare model
// id, so a single-slot model — the default, and every model until someone sets
// parallel > 1 — keys exactly as it did when this cache only knew /slots/0.
// Higher slots suffix "#N".
func sk(model string, idx int) string {
	if idx <= 0 {
		return model
	}
	return model + "#" + strconv.Itoa(idx)
}

// modelOf reverses sk for display/grouping.
func modelOf(key string) string {
	if i := strings.LastIndex(key, "#"); i > 0 {
		return key[:i]
	}
	return key
}

// slotIndexOf reverses sk for display/grouping.
func slotIndexOf(key string) int {
	if i := strings.LastIndex(key, "#"); i > 0 {
		if n, err := strconv.Atoi(key[i+1:]); err == nil {
			return n
		}
	}
	return 0
}

// slotCount reports how many server slots a model launches with (--parallel N,
// clamped to >= 1). It reads the model's CONFIGURED command, not the live
// /slots array, so a cold model can be assigned a slot before its process
// exists — which is exactly when the cold-restore path needs to know.
func (sc *slotCache) slotCount(model string) int {
	if sc == nil || sc.slots == nil {
		return 1
	}
	if n := sc.slots(model); n > 1 {
		return n
	}
	return 1
}

// slotState is one entry of llama-server's /slots array, reduced to what slot
// assignment and the save gate care about.
type slotState struct {
	id     int
	tokens int64 // live KV occupancy
	busy   bool  // mid-generation: never restore over this one
}

// slotStates reads /slots once and returns per-slot occupancy. One GET covers
// every slot, so the assignment path costs the same on an 8-slot model as on a
// 1-slot one. A failed scrape returns nil: callers treat "unknown" as "not
// busy, zero tokens", which is the pre-multi-slot behaviour.
func (sc *slotCache) slotStates(ctx context.Context, base string) []slotState {
	body, err := sc.httpGet(ctx, strings.TrimRight(base, "/")+"/slots")
	if err != nil {
		return nil
	}
	arr := gjson.ParseBytes(body)
	if !arr.IsArray() {
		return nil
	}
	var out []slotState
	for i, s := range arr.Array() {
		id := i
		if v := s.Get("id"); v.Exists() {
			id = int(v.Int())
		}
		st := slotState{id: id, tokens: s.Get("n_prompt_tokens").Int(), busy: s.Get("is_processing").Bool()}
		if st.busy {
			st.tokens += s.Get("next_token.0.n_decoded").Int()
		}
		out = append(out, st)
	}
	return out
}

// tokensAt returns slot idx's live KV occupancy, 0 when unknown.
func tokensAt(states []slotState, idx int) int64 {
	for _, s := range states {
		if s.id == idx {
			return s.tokens
		}
	}
	return 0
}

// busySet maps slot id -> mid-generation.
func busySet(states []slotState) map[int]bool {
	if len(states) == 0 {
		return nil
	}
	m := make(map[int]bool, len(states))
	for _, s := range states {
		m[s.id] = s.busy
	}
	return m
}

// occSnap is a lock-free copy of whoever held a slot before it was reassigned,
// so the caller can save them without holding stateMu across the I/O.
type occSnap struct {
	key      string
	preamble string
	dirty    bool
	occupied bool
}

// acquire pins a conversation to one of a model's slots and claims it,
// returning the slot index, a snapshot of the previous occupant, and whether
// this conversation was already resident there.
//
// Preference order:
//  1. the slot already holding this conversation — sticky, so a continuing chat
//     never migrates and never re-restores what is already live;
//  2. any never-used slot;
//  3. the least-recently-used slot that is NOT mid-generation — the busy filter
//     is what stops a second agent from evicting the slot currently streaming
//     tokens to the first;
//  4. plain LRU, when every slot is busy (llama-server queues the request behind
//     that slot's current task, same as it would have anyway).
//
// The claim is made under stateMu before any I/O, so two concurrent new
// conversations can't select the same slot.
func (sc *slotCache) acquire(model, key, preamble string, n int, busy map[int]bool) (idx int, prev occSnap, same bool) {
	if n < 1 {
		n = 1
	}
	now := time.Now()
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
	if sc.lastUse == nil {
		sc.lastUse = map[string]time.Time{}
	}

	for i := 0; i < n; i++ {
		if o := sc.occupant[sk(model, i)]; o != nil && o.key == key {
			sc.lastUse[sk(model, i)] = now
			return i, occSnap{}, true
		}
	}

	pick := -1
	for i := 0; i < n; i++ {
		if sc.occupant[sk(model, i)] == nil {
			pick = i
			break
		}
	}
	if pick < 0 {
		pick = lruSlot(sc.lastUse, model, n, busy, true)
	}
	if pick < 0 {
		pick = lruSlot(sc.lastUse, model, n, busy, false)
	}

	if o := sc.occupant[sk(model, pick)]; o != nil {
		prev = occSnap{key: o.key, preamble: o.preamble, dirty: o.dirty, occupied: true}
	}
	// Claim immediately: an occupant with the incoming key is what makes a
	// concurrent acquire() for a THIRD conversation skip this slot as recently
	// used. dirty stays false until markResident sees the request actually run.
	sc.occupant[sk(model, pick)] = &occInfo{key: key, preamble: preamble}
	sc.lastUse[sk(model, pick)] = now
	return pick, prev, false
}

// lruSlot returns the least-recently-used slot of a model, optionally skipping
// slots that are mid-generation. -1 when the filter excluded everything.
func lruSlot(lastUse map[string]time.Time, model string, n int, busy map[int]bool, skipBusy bool) int {
	pick, oldest := -1, time.Time{}
	for i := 0; i < n; i++ {
		if skipBusy && busy[i] {
			continue
		}
		t := lastUse[sk(model, i)]
		if pick < 0 || t.Before(oldest) {
			pick, oldest = i, t
		}
	}
	return pick
}

// pinSlot rewrites the forwarded body with "id_slot": idx so llama-server
// serves the request on the slot we just restored into. Called only when a
// model runs more than one slot — a single-slot server has nothing to choose,
// and leaving its body untouched keeps the common path byte-identical.
//
// ponytail: llama-server reads id_slot on the OpenAI/native completion routes.
// A route that re-parses the body into fresh params (Anthropic /v1/messages)
// may drop it, in which case llama picks the slot itself and our pin degrades to
// a hint — visible as confirm-miss in the KV Cache tab, never a wrong answer.
func pinSlot(r *http.Request, idx int) {
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return
	}
	nb, err := sjson.SetBytes(body, "id_slot", idx)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body)) // restore what we consumed
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(nb))
	r.Header.Del("Transfer-Encoding")
	r.Header.Set("Content-Length", strconv.Itoa(len(nb)))
	r.ContentLength = int64(len(nb))
}
