package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Per-user assistant memory: short standing facts about the user that are worth
// carrying between conversations ("prefers metric", "runs an RX 7900 XTX").
//
// Unlike chats/prefs this is NOT a client-owned opaque blob. Both the browser
// (Settings → Memory) and the model (memory_save / memory_delete, dispatched in
// turns_memory.go) write it, and a whole-array PUT from a tab that loaded before
// the model wrote would silently revert that write. So the server owns the list
// and every mutation is a per-entry upsert/delete under p.mu.
//
// Recall is by INJECTION, not by a tool: memoryBlock is rendered into the chat
// system prompt every turn (ui-svelte/src/lib/memoryTools.ts does the client-side
// half). A read tool would sit in the KV-stable prefix of every conversation and
// still only fire when the model thought to call it; injected facts are always in
// front of it. The cost is the other side of that trade — writing a memory
// changes the system prompt, so it invalidates the KV prefix of every chat.
//
// That cost is paid by the STORE, not by asking the model to hold back: a model
// told to save sparingly saves the wrong things, and telling it to check the block
// before saving only turns a duplicate into a duplicate it argued itself into.
// Instead every save is deduplicated here (memoryDuplicateOf) — a restatement of
// something already known writes nothing at all, and a near-restatement folds into
// the entry that already exists. The injected block is rendered append-only
// (createdAt order, see memoryBlock) so an ordinary save only changes bytes at its
// tail instead of reshuffling every line above it.
//
// The one duplicate the store cannot settle by itself is the same fact in
// different words, which is too far from the original to fold without risking the
// loss of a real second fact. That case is escalated rather than guessed at:
// nearestMemory finds the closest existing entry and turns_memory.go names it in
// the tool result, so the model merges two texts it can see instead of auditing a
// block it mostly skims.
type memoryEntry struct {
	ID   string   `json:"id"`
	Text string   `json:"text"`           // the fact itself, one per entry
	Tags []string `json:"tags,omitempty"` // free-form, for the user's own filtering

	// Source is who wrote it — "assistant" (a memory_save call) or "user" (typed
	// or edited in Settings → Memory). Surfaced so the user can tell what the
	// model decided to remember on its own from what they told it to keep.
	Source    string `json:"source"`
	CreatedAt int64  `json:"createdAt"` // unix seconds
	UpdatedAt int64  `json:"updatedAt"`
}

const (
	// maxMemories caps the list. Old entries are not auto-evicted: silently
	// dropping something the user asked to be remembered is worse than refusing
	// the write, so a save past the cap is an error telling the model to delete
	// something first.
	maxMemories = 200
	// maxMemoryLen caps one entry. A memory is a fact, not a document — anything
	// longer belongs in the system prompt, which is editable and doesn't pretend
	// to be a discrete thing the model can delete.
	maxMemoryLen = 800
)

func (p *Playground) memoriesPath(user string) string {
	return filepath.Join(p.userDir(user), "memories.json")
}

// loadMemoriesLocked reads the user's list. Caller holds p.mu. A missing or
// corrupt file reads as empty rather than failing the request — a chat turn must
// not die because of memory storage.
func (p *Playground) loadMemoriesLocked(user string) []memoryEntry {
	b, err := os.ReadFile(p.memoriesPath(user))
	if err != nil {
		return nil
	}
	var out []memoryEntry
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// saveMemoriesLocked writes the list back. Caller holds p.mu.
func (p *Playground) saveMemoriesLocked(user string, mems []memoryEntry) error {
	path := p.memoriesPath(user)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(mems, "", "  ")
	return os.WriteFile(path, b, 0o644)
}

// listMemories returns the user's memories, newest-updated first.
func (p *Playground) listMemories(user string) []memoryEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	mems := p.loadMemoriesLocked(user)
	sort.SliceStable(mems, func(i, j int) bool { return mems[i].UpdatedAt > mems[j].UpdatedAt })
	return mems
}

// newMemoryID mints a short opaque id. Short because the model has to type it
// back to update or delete an entry, and a UUID in every injected line is pure
// prefix cost.
func newMemoryID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to the clock: an id collision costs one overwritten memory,
		// while failing the save loses the fact entirely.
		return "m" + time.Now().Format("20060102150405.000000")
	}
	return hex.EncodeToString(b[:])
}

// memoryOutcome says what upsertMemory actually did. A save is not always a
// create, and the caller has to be able to tell the model the truth — otherwise
// it reports "saved that for you" over a write that never happened.
type memoryOutcome int

const (
	memoryCreated   memoryOutcome = iota // a new entry
	memoryUpdated                        // an explicit id replaced that entry
	memoryMerged                         // a near-restatement folded into an existing entry
	memoryDuplicate                      // already remembered verbatim; nothing written
)

// memoryDedupeJaccard is the word overlap above which two memories are treated as
// the same fact. Deliberately high: folding two genuinely different facts into one
// silently loses something the user asked to keep, while missing a near-duplicate
// only costs one extra line. Pairs that differ by a single content word — "prefers
// metric units" / "prefers imperial units" (0.5) — stay well clear of it.
const memoryDedupeJaccard = 0.8

// memoryContainWords is the minimum length for the containment rule, so a one- or
// two-word memory cannot swallow every longer entry that happens to repeat it.
const memoryContainWords = 3

// normalizeMemoryText reduces a memory to what it asserts: lowercased, punctuation
// dropped, whitespace collapsed. Two memories that normalize the same are the same
// fact typed twice.
func normalizeMemoryText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true // suppresses a leading separator too
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// memoryDuplicateOf reports whether `incoming` restates `existing`.
//
// Three rules, safest first: identical once normalized; one wholly contained in
// the other ("Runs an RX 7900 XTX" inside "Runs an RX 7900 XTX with 24GB of VRAM"
// — a refinement, not a second fact); or a word-set overlap at or above
// memoryDedupeJaccard. Word sets rather than edit distance because a memory is a
// sentence whose wording drifts between saves while its content words do not.
func memoryDuplicateOf(existing, incoming string) bool {
	a, b := normalizeMemoryText(existing), normalizeMemoryText(incoming)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	wa, wb := strings.Fields(a), strings.Fields(b)
	short, long, shortN := a, b, len(wa)
	if len(wb) < len(wa) {
		short, long, shortN = b, a, len(wb)
	}
	if shortN >= memoryContainWords && strings.Contains(" "+long+" ", " "+short+" ") {
		return true
	}
	return jaccardWords(wa, wb) >= memoryDedupeJaccard
}

// jaccardWords is |A INTERSECT B| / |A UNION B| over the two word sets.
func jaccardWords(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, w := range a {
		set[w] = true
	}
	inter := 0
	seen := make(map[string]bool, len(b))
	for _, w := range b {
		if seen[w] {
			continue
		}
		seen[w] = true
		if set[w] {
			inter++
		}
	}
	union := len(set) + len(seen) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// --- near-duplicate reporting ---------------------------------------------
//
// memoryDuplicateOf is deliberately strict, so it only catches a restatement in
// roughly the same words. Real duplicates rarely look like that: the same fact
// noticed in two different conversations comes back rephrased ("Runs an RX 7900
// XTX with 24GB of VRAM" / "The user's GPU is an AMD RX 7900 XTX"), which shares
// its content words but not its shape, and lands nowhere near the merge
// threshold. Loosening that threshold is not the fix - a merge is lossy, and a
// wrong one silently destroys a fact the user asked to keep.
//
// So the loose match does not merge, it TELLS. On a create the store finds the
// nearest existing entry and hands it back to the model as part of the tool
// result: here is the id, here is what it says, merge them yourself if it is the
// same fact. That is the one form of duplicate-checking a model is actually good
// at - judging two candidates put in front of it - as opposed to the form it is
// bad at, which is auditing a forty-line block unprompted before every save.
// Being wrong is cheap in this direction: a false neighbour costs a sentence the
// model reads and ignores.

// memoryNearJaccard is the content-word overlap at which two memories are worth
// mentioning to each other. Far below memoryDedupeJaccard because nothing is
// merged on it - the model decides - and a missed neighbour is the failure this
// exists to prevent.
const memoryNearJaccard = 0.3

// memoryNearMinShared stops one incidental word in common from pairing two
// unrelated one-liners, which the ratio alone permits when both are short.
const memoryNearMinShared = 2

// memoryStopWords are words that say nothing about WHICH fact a memory holds.
// "user" is in here on purpose: memories are written in the third person about
// the user, so it appears in most of them and inflates every overlap.
var memoryStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "any": true, "are": true, "as": true, "at": true,
	"be": true, "been": true, "but": true, "by": true, "do": true, "does": true, "for": true,
	"from": true, "had": true, "has": true, "have": true, "he": true, "her": true, "him": true,
	"his": true, "in": true, "is": true, "it": true, "its": true, "not": true, "of": true,
	"on": true, "or": true, "she": true, "so": true, "than": true, "that": true,
	"the": true, "their": true, "them": true, "they": true, "this": true, "to": true,
	"up": true, "use": true, "user": true, "users": true, "was": true, "were": true,
	"when": true, "which": true, "with": true, "would": true,
}

// contentWords is the normalized word set of a memory minus the stop words, so
// two phrasings of one fact are compared on what they actually assert.
func contentWords(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(normalizeMemoryText(s)) {
		if memoryStopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// memoryNearness scores two memories on shared content words, returning the
// overlap ratio and the count of words they share.
func memoryNearness(a, b string) (float64, int) {
	wa, wb := contentWords(a), contentWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0, 0
	}
	set := make(map[string]bool, len(wa))
	for _, w := range wa {
		set[w] = true
	}
	shared := 0
	for _, w := range wb {
		if set[w] {
			shared++
		}
	}
	union := len(wa) + len(wb) - shared
	if union == 0 {
		return 0, shared
	}
	return float64(shared) / float64(union), shared
}

// nearestMemory returns the entry most similar to text, excluding excludeID (the
// entry just written), or ok=false when nothing is close enough to be worth
// mentioning. Read-only, and taken after the write, so a failure here can never
// cost the save.
func (p *Playground) nearestMemory(user, text, excludeID string) (memoryEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var best memoryEntry
	bestScore := 0.0
	for _, m := range p.loadMemoriesLocked(user) {
		if m.ID == excludeID {
			continue
		}
		score, shared := memoryNearness(m.Text, text)
		if shared < memoryNearMinShared || score < memoryNearJaccard || score <= bestScore {
			continue
		}
		best, bestScore = m, score
	}
	return best, bestScore > 0
}

// mergeTags unions two tag lists through the usual normalization.
func mergeTags(a, b []string) []string {
	return cleanTags(append(append([]string{}, a...), b...))
}

// upsertMemory creates, updates, merges or no-ops one entry, saying which in the
// returned memoryOutcome. An explicit in.ID replaces that entry; an id that
// matches nothing is an error rather than a create, because the model getting an
// id wrong means it is editing a memory it never read.
//
// An idless save is deduplicated against the whole list first (memoryDuplicateOf),
// so the model may save a fact whenever it sees one without first proving to
// itself that it is new. That is the point: restraint is the store's job, not a
// judgement call handed to a model that is bad at it.
func (p *Playground) upsertMemory(user string, in memoryEntry) (memoryEntry, memoryOutcome, error) {
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		return memoryEntry{}, memoryCreated, errMemory("a memory needs `text` - the fact to remember, in one or two sentences")
	}
	if len([]rune(in.Text)) > maxMemoryLen {
		return memoryEntry{}, memoryCreated, errMemory("memory too long (max %d characters) - keep it to the fact itself", maxMemoryLen)
	}
	if in.Source != "user" {
		in.Source = "assistant"
	}
	in.Tags = cleanTags(in.Tags)

	p.mu.Lock()
	defer p.mu.Unlock()
	mems := p.loadMemoriesLocked(user)
	now := time.Now().Unix()

	if in.ID != "" {
		for i := range mems {
			if mems[i].ID != in.ID {
				continue
			}
			mems[i].Text = in.Text
			mems[i].Tags = in.Tags
			mems[i].Source = in.Source
			mems[i].UpdatedAt = now
			out := mems[i]
			return out, memoryUpdated, p.saveMemoriesLocked(user, mems)
		}
		return memoryEntry{}, memoryUpdated, errMemory("no memory with id %q - list the memories you were given and use one of those ids, or omit id to save a new one", in.ID)
	}

	// Deduplicate before the cap check: re-saying something already remembered has
	// to keep working on a full list — the write is a no-op, so there is nothing to
	// refuse, and a "memory is full" error over a fact already stored is a lie.
	for i := range mems {
		if !memoryDuplicateOf(mems[i].Text, in.Text) {
			continue
		}
		if normalizeMemoryText(mems[i].Text) == normalizeMemoryText(in.Text) {
			// Verbatim restatement: write NOTHING, not even UpdatedAt. The injected
			// block has to come back byte-identical, and a no-op that still touches
			// the entry is a no-op that still costs a reprefill.
			return mems[i], memoryDuplicate, nil
		}
		// Near-restatement: fold it into the entry that already exists. The longer
		// text wins so a merge can never drop detail the other phrasing carried — a
		// genuine shortening is a correction, which the model makes with an explicit
		// id. CreatedAt is preserved, so the entry keeps its place in the
		// append-only injected block instead of jumping to the end. Source is left
		// alone too: an assistant restatement does not make a user's memory the
		// model's own.
		if len(in.Text) > len(mems[i].Text) {
			mems[i].Text = in.Text
		}
		mems[i].Tags = mergeTags(mems[i].Tags, in.Tags)
		mems[i].UpdatedAt = now
		out := mems[i]
		return out, memoryMerged, p.saveMemoriesLocked(user, mems)
	}

	if len(mems) >= maxMemories {
		return memoryEntry{}, memoryCreated, errMemory("memory is full (%d entries). Delete one that is stale or superseded before saving another", maxMemories)
	}
	in.ID = newMemoryID()
	in.CreatedAt = now
	in.UpdatedAt = now
	mems = append(mems, in)
	return in, memoryCreated, p.saveMemoriesLocked(user, mems)
}

// deleteMemory removes one entry by id, reporting whether it existed.
func (p *Playground) deleteMemory(user, id string) (memoryEntry, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	mems := p.loadMemoriesLocked(user)
	for i := range mems {
		if mems[i].ID != id {
			continue
		}
		gone := mems[i]
		mems = append(mems[:i], mems[i+1:]...)
		return gone, true, p.saveMemoriesLocked(user, mems)
	}
	return memoryEntry{}, false, nil
}

// cleanTags trims, lowercases and dedups tags, dropping empties. Tags come from
// the model as often as from the user, so "Hardware" and "hardware " must not
// become two filters over the same set.
func cleanTags(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] || len(out) >= 8 {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// memErr is a user-facing memory error: its text is written for the MODEL to
// read as a tool result (say what to do next, not just what went wrong) and is
// also fine as an HTTP 400 body.
type memErr struct{ msg string }

func (e memErr) Error() string { return e.msg }

func errMemory(format string, args ...any) error {
	return memErr{msg: fmt.Sprintf(format, args...)}
}

// --- HTTP -----------------------------------------------------------------

// GET /api/memories — the logged-in user's memories, newest-updated first.
// POST /api/memories — upsert one entry (body: {id?,text,tags?}), returns it.
// DELETE /api/memories/{id} — remove one entry.
//
// Registered on apiChain like the rest of the playground surface; every handler
// resolves the user from the signed cookie, so one user can never touch another's
// list.
func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	user := p.userFromRequest(r)
	if user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, p.listMemories(user))

	case http.MethodPost:
		var in memoryEntry
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// A memory typed or edited in the UI is the user's own, whatever the client
		// sent — only the tool path may claim "assistant".
		in.Source = "user"
		out, _, err := p.upsertMemory(user, in)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)

	case http.MethodDelete:
		id := r.PathValue("id")
		_, ok, err := p.deleteMemory(user, id)
		if err != nil {
			http.Error(w, "could not save memories", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
