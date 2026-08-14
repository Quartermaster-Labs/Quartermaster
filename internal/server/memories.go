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
// changes the system prompt, so it invalidates the KV prefix of every chat. That
// is why the write tools are deliberately not chatty (see their descriptions).
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

// upsertMemory creates or updates one entry. An empty in.ID creates; a non-empty
// one that matches nothing is an error rather than a create, because the model
// getting an id wrong means it is editing a memory it never read.
func (p *Playground) upsertMemory(user string, in memoryEntry) (memoryEntry, error) {
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		return memoryEntry{}, errMemory("a memory needs `text` - the fact to remember, in one or two sentences")
	}
	if len([]rune(in.Text)) > maxMemoryLen {
		return memoryEntry{}, errMemory("memory too long (max %d characters) - keep it to the fact itself", maxMemoryLen)
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
			return out, p.saveMemoriesLocked(user, mems)
		}
		return memoryEntry{}, errMemory("no memory with id %q - list the memories you were given and use one of those ids, or omit id to save a new one", in.ID)
	}

	if len(mems) >= maxMemories {
		return memoryEntry{}, errMemory("memory is full (%d entries). Delete one that is stale or superseded before saving another", maxMemories)
	}
	in.ID = newMemoryID()
	in.CreatedAt = now
	in.UpdatedAt = now
	mems = append(mems, in)
	return in, p.saveMemoriesLocked(user, mems)
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
		out, err := p.upsertMemory(user, in)
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
