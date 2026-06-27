package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/logmon"
	"github.com/mostlygeek/llama-swap/internal/shared"
	"github.com/tidwall/gjson"
)

// slotCache persists a llama-server slot's KV-cache to disk so an expensive,
// long-lived conversation survives being evicted from the single live slot by a
// throwaway request — and is restored (instead of reprefilled) when it returns.
//
// It only acts on the WARM path: a request for an already-running model. When a
// different conversation arrives it saves the outgoing one (if it's worth it)
// and restores the incoming one before forwarding. Cold restore (returning after
// a model swap) is deliberately out of scope — see the package notes.
//
// "Worth saving" = both gates must hold: the conversation is "continued" — the
// HUMAN has sent >=2 user messages (an agentic one-shot `-p` run has a single
// user turn but many assistant/tool turns, so counting assistant turns would
// wrongly persist it; counting user turns does not) AND its live KV is at least
// minSaveTokens (cheap conversations aren't worth the disk write). Files are
// keyed by a stable conversation anchor so the same chat overwrites its own file
// across turns, and bounded by an LRU budget.
type slotCache struct {
	enabled   bool
	dir       string
	minTokens int64
	maxBytes  int64
	maxFiles  int

	running func() map[string]string // model id -> resolved upstream base URL
	// participates reports whether a model opted into slot persistence (its
	// generated cmd carries --slot-save-path). Without it the per-model checkbox
	// is off and llama-server can't save/restore, so the cache stays out of its way.
	participates func(model string) bool
	client       *http.Client
	log          *logmon.Monitor

	// ponytail: one global lock guards the occupant map AND serializes the
	// save/restore HTTP calls. A multi-GB save blocks other models' requests for
	// its duration — fine for single-user local inference. Upgrade path: per-model
	// locks keyed by model id if concurrent multi-model traffic ever matters.
	mu       sync.Mutex
	occupant map[string]*occInfo // model id -> who currently holds its slot
}

// occInfo is the conversation currently resident in a model's slot.
type occInfo struct {
	key       string // stable conversation anchor hash
	continued bool   // human sent >=2 user messages (a real ongoing conversation)
	dirty     bool   // has run (generated) since its last save
}

// newSlotCache builds the cache from config, applying defaults for unset knobs.
// Returns a disabled cache when the feature is off so callers can stay branchless.
func newSlotCache(cfg config.SlotCacheConfig, running func() map[string]string, participates func(string) bool, log *logmon.Monitor) *slotCache {
	sc := &slotCache{
		enabled:      cfg.Enable,
		dir:          cfg.Path,
		running:      running,
		participates: participates,
		log:          log,
		client:       &http.Client{Timeout: 60 * time.Second}, // a large save/restore can be slow
		occupant:     map[string]*occInfo{},
	}
	if !sc.enabled {
		return sc
	}
	if sc.dir == "" {
		sc.dir = config.DefaultSlotCachePath()
	}
	sc.minTokens = int64(cfg.MinSaveTokens)
	if sc.minTokens <= 0 {
		sc.minTokens = 30000
	}
	maxGB := cfg.MaxDiskGB
	if maxGB <= 0 {
		maxGB = 10
	}
	sc.maxBytes = int64(maxGB * (1 << 30))
	sc.maxFiles = cfg.MaxSessions
	if sc.maxFiles <= 0 {
		sc.maxFiles = 20
	}
	if err := os.MkdirAll(sc.dir, 0o755); err != nil {
		sc.log.Warnf("slotcache: cannot create %s: %v — disabling", sc.dir, err)
		sc.enabled = false
	}
	return sc
}

// middleware wires the slot cache into the model-dispatch chain. It is a no-op
// unless enabled and the request is a chat-style body for a running model.
func (sc *slotCache) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sc == nil || !sc.enabled {
			next.ServeHTTP(w, r)
			return
		}
		key, continued, ok := sessionAnchor(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		data, _ := shared.ReadContext(r.Context())
		model := data.ModelID
		if model == "" || sc.participates == nil || !sc.participates(model) {
			next.ServeHTTP(w, r) // model opted out (no --slot-save-path) — stay out of its way
			return
		}
		if base, running := sc.running()[model]; running {
			sc.onSwitch(r.Context(), model, base, key, continued)
		}
		next.ServeHTTP(w, r)
		// The request generated (or at least ran), so the resident KV changed.
		sc.markResident(model, key, continued)
	})
}

// onSwitch handles the moment a (possibly) different conversation arrives for a
// warm model: save the outgoing one if worth it, restore the incoming one if we
// have it on disk, then mark the incoming as resident.
func (sc *slotCache) onSwitch(ctx context.Context, model, base, key string, continued bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	occ := sc.occupant[model]
	if occ != nil && occ.key == key {
		return // same conversation continuing — nothing to swap
	}

	if occ != nil && occ.dirty && occ.continued {
		// Read the live slot occupancy; only persist a conversation big enough to
		// be expensive to reprefill.
		if toks := sc.slotTokens(ctx, base); toks >= sc.minTokens {
			if err := sc.save(ctx, base, model, occ.key); err != nil {
				sc.log.Warnf("slotcache: save %s/%s: %v", model, occ.key, err)
			} else {
				occ.dirty = false
				sc.enforceCaps(model, occ.key)
			}
		}
	}

	// Restore the incoming conversation's KV so the forwarded request reuses it
	// instead of reprefilling from scratch.
	if sc.fileExists(model, key) {
		if err := sc.restore(ctx, base, model, key); err != nil {
			sc.log.Warnf("slotcache: restore %s/%s: %v", model, key, err)
		}
	}
	sc.occupant[model] = &occInfo{key: key, continued: continued}
}

// markResident records that `key` now holds the model's slot and has run.
func (sc *slotCache) markResident(model, key string, continued bool) {
	if model == "" {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	occ := sc.occupant[model]
	if occ == nil || occ.key != key {
		occ = &occInfo{key: key, continued: continued}
		sc.occupant[model] = occ
	}
	occ.continued = continued
	occ.dirty = true
}

// slotTokens returns the live KV occupancy of the model's (single) slot.
func (sc *slotCache) slotTokens(ctx context.Context, base string) int64 {
	body, err := sc.httpGet(ctx, strings.TrimRight(base, "/")+"/slots")
	if err != nil {
		return 0
	}
	var bm BackendMetrics
	parseSlotsInto(&bm, body)
	return bm.KVCacheTokens
}

func (sc *slotCache) save(ctx context.Context, base, model, key string) error {
	return sc.slotAction(ctx, base, "save", fileName(model, key))
}

func (sc *slotCache) restore(ctx context.Context, base, model, key string) error {
	return sc.slotAction(ctx, base, "restore", fileName(model, key))
}

// slotAction calls llama-server's POST /slots/0?action=save|restore. The
// filename is relative to the server's --slot-save-path (== sc.dir).
func (sc *slotCache) slotAction(ctx context.Context, base, action, filename string) error {
	u := strings.TrimRight(base, "/") + "/slots/0?action=" + action
	payload := fmt.Sprintf(`{"filename":%q}`, filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s -> %s", action, resp.Status)
	}
	return nil
}

func (sc *slotCache) httpGet(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := sc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (sc *slotCache) fileExists(model, key string) bool {
	_, err := os.Stat(filepath.Join(sc.dir, fileName(model, key)))
	return err == nil
}

// enforceCaps deletes the oldest files (by mtime) until the cache is within both
// the byte budget and the file-count cap. The just-saved file (newest) is never
// the eviction target. keepKey/keepModel name it so it is also skipped explicitly.
func (sc *slotCache) enforceCaps(keepModel, keepKey string) {
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return
	}
	type f struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []f
	var total int64
	keep := fileName(keepModel, keepKey)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, f{filepath.Join(sc.dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, fl := range files {
		if total <= sc.maxBytes && len(files) <= sc.maxFiles {
			break
		}
		if filepath.Base(fl.path) == keep {
			continue
		}
		if err := os.Remove(fl.path); err == nil {
			total -= fl.size
			files = files[:len(files)-1] // count drops by one (value irrelevant; we only read len)
		}
	}
}

// fileName is the on-disk name for a (model, conversation) KV snapshot. Keyed by
// the stable conversation anchor so the same chat overwrites its own file.
func fileName(model, key string) string {
	return sanitize(model) + "__" + key + ".bin"
}

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string {
	return unsafeFile.ReplaceAllString(s, "_")
}

// sessionAnchor derives a stable per-conversation key and the "continued" flag
// from a chat-style request body, restoring the body for downstream handlers.
//
// Key = sha256(first system message + first user message): invariant across a
// conversation's turns (history grows, the opening doesn't), so a chat always
// maps to one file. continued = the human sent >=2 user messages — an agentic
// one-shot run has a single user turn but many assistant/tool turns, so we count
// USER turns, not assistant turns, to tell an ongoing chat from a throwaway.
// ok=false when the body has no user message (not a chat we can key).
func sessionAnchor(r *http.Request) (key string, continued bool, ok bool) {
	if r.Body == nil {
		return "", false, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return "", false, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body)) // restore for the next handler

	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return "", false, false
	}
	var sys, firstUser string
	var userTurns int
	for _, m := range msgs.Array() {
		switch m.Get("role").String() {
		case "system", "developer":
			if sys == "" {
				sys = m.Get("content").Raw
			}
		case "user":
			if userTurns == 0 {
				firstUser = m.Get("content").Raw
			}
			userTurns++
		}
	}
	if userTurns == 0 {
		return "", false, false
	}
	sum := sha256.Sum256([]byte(sys + "\x00" + firstUser))
	return hex.EncodeToString(sum[:16]), userTurns >= 2, true
}
