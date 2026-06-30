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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/config"
	"github.com/radu0120/llama-quartermaster/internal/logmon"
	"github.com/radu0120/llama-quartermaster/internal/shared"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// slotCache persists a llama-server slot's KV-cache to disk so an expensive,
// long-lived conversation survives being evicted from the single live slot by a
// throwaway request — and is restored (instead of reprefilled) when it returns.
//
// It covers both paths. WARM: a request for an already-running model saves the
// outgoing conversation (if worth it) and restores the incoming one before
// forwarding (onSwitch). COLD: a request for an evicted model is restored after
// the router reloads it — saveOnEvict snapshots the KV before the process is
// killed (via the router pre-stop hook), and restoreOnLoad reads it back once the
// process readies again (via the post-start hook), so a conversation survives a
// full model swap, not just a same-model conversation switch.
//
// "Worth saving" = its live KV is at least minSaveTokens (cheap conversations
// aren't worth the disk write). Cost is the sole gate — a single-turn chat with
// a long answer or big context is still expensive to reprefill, so there is no
// turn-count gate (an earlier "continued >=2 user turns" gate silently dropped
// such conversations from tracking before they could be saved). Files are keyed
// by a stable conversation anchor so the same chat overwrites its own file
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
	// recurrent reports whether a model's gguf is a hybrid/recurrent arch
	// (GatedDeltaNet/SSM, FullAttnInterval>0). Such models can only restore their
	// recurrent state at its exact saved length, so we run the exact whole-slot
	// restore-hit but skip the partial-prefix paths (preamble cache + Tier-1 seed)
	// for them — see restoreOnLoad. nil => treat all models as plain attention.
	recurrent func(model string) bool
	client    *http.Client
	log       *logmon.Monitor

	// ponytail: one global lock guards the occupant map AND serializes the
	// save/restore HTTP calls. A multi-GB save blocks other models' requests for
	// its duration — fine for single-user local inference. Upgrade path: per-model
	// locks keyed by model id if concurrent multi-model traffic ever matters.
	mu       sync.Mutex
	occupant map[string]*occInfo // model id -> who currently holds its slot
	// pending maps a model id -> conversation key to restore the next time that
	// model reaches Ready. Set when a request arrives for a model that is NOT
	// running (cold) but has a saved KV file; consumed by restoreOnLoad once the
	// router has loaded the process. This is what makes restore-after-swap work.
	pending map[string]string
	// pendingSeed marks whether the pending restore is a Tier-1 SEED (a similar
	// session's prefix) rather than an exact session match — for stats labelling.
	pendingSeed map[string]bool
	// pendingPreamble maps a cold model -> the incoming request's preamble
	// (system+tools), so restoreOnLoad can seed from / mint this agent's preamble
	// cache once the process is up. Set when a cold request has no exact saved file.
	pendingPreamble map[string]string
	// awaitConfirm[model] = FIFO of restore/mint ops ("restore-hit"/"restore-seed"/
	// "preamble") each awaiting confirmation from a subsequent request for that model
	// via cached_tokens. A queue (not a single slot) because one model can serve many
	// agents at once (Qwen Code main + memory subagent share a model) — interleaved
	// requests would otherwise overwrite each other's pending op and lose confirmations.
	// ponytail: FIFO, so counts are right; under out-of-order completion the per-event
	// op *label* can still mismatch the request that confirmed it — upgrade to per-
	// request correlation only if labels (not totals) start mattering.
	awaitConfirm map[string][]string

	// stats: counters + a bounded ring of recent events, surfaced at /api/kvcache
	// for the Observe → KV Cache tab. Guarded by its own lock so record() can be
	// called from inside sc.mu-held sections without reentrancy.
	statsMu  sync.Mutex
	counters kvCounters
	events   []kvEvent // newest last, capped at kvEventRing
}

// Tier-1 seed + monitoring tunables. ponytail: constants, not config knobs —
// promote to SlotCacheConfig if these ever need per-deployment tuning.
const (
	seedMinPrefixBytes = 2048      // min shared preamble bytes to seed a cold slot from a similar session
	seedMaxFileBytes   = 512 << 20 // skip seeding from a file larger than this (read cost > prefix gain)
	metaMaxBytes       = 16 << 10  // preamble snapshot stored per file for prefix matching
	kvEventRing        = 200       // recent-event ring size

	// Preamble cache (a category distinct from per-conversation files): one
	// preamble-only KV per (model, system+tools), seeding every cold/warm load
	// that shares it. preambleKeyPrefix tags the file key; keep a few generations per
	// model so a changed preamble (e.g. a daily date bump) overwrites cleanly.
	preambleKeyPrefix = "preamble_"
	// maxPreambleGenerations bounds preamble caches kept per model. Must hold ALL
	// environments that share a model, not just one: a single Qwen Code session already
	// mints 3 (main agent + memory subagents), so pi/Cline/etc on the same model need
	// headroom or switching harnesses evicts the other's preamble. Pruned LRU-by-use
	// (preamble-hit touches the file), so hot environments survive regardless of age.
	maxPreambleGenerations = 8
)

// kvCounters tallies lifetime cache activity for the monitoring tab.
type kvCounters struct {
	Saves        int64 `json:"saves"`
	RestoreHits  int64 `json:"restoreHits"`  // exact session match restored
	RestoreSeeds int64 `json:"restoreSeeds"` // Tier-1 similar-prefix seed restored
	Misses       int64 `json:"misses"`       // cold, nothing to restore or seed
	Errors       int64 `json:"errors"`
	// Confirmed reuse: the request after a restore reported cached_tokens > 0 from
	// the upstream — proof the restored KV was actually reused, not just loaded.
	ConfirmedReuses  int64 `json:"confirmedReuses"`
	ConfirmedMisses  int64 `json:"confirmedMisses"`  // restore happened but upstream cached 0
	CachedTokensSeen int64 `json:"cachedTokensSeen"` // sum of confirmed cached_tokens
	// Preamble cache (system+tools seed) activity.
	PreambleMints int64 `json:"preambleMints"` // freshly synthesized preamble KV files
	PreambleHits  int64 `json:"preambleHits"`  // cold/warm load seeded from an existing preamble file
	PreambleWarm  int64 `json:"preambleWarm"`  // skipped restore — slot already held the preamble live
}

// kvEvent is one recent cache action shown in the live event log.
type kvEvent struct {
	Time   time.Time `json:"time"`
	Model  string    `json:"model"`
	Op     string    `json:"op"` // save | restore-hit | restore-seed | seed-pending | miss | error
	Key    string    `json:"key"`
	Detail string    `json:"detail,omitempty"`
	Bytes  int64     `json:"bytes,omitempty"`
	Tokens int64     `json:"tokens,omitempty"`
}

// occInfo is the conversation currently resident in a model's slot.
type occInfo struct {
	key      string // stable conversation anchor hash
	dirty    bool   // has run (generated) since its last save
	preamble string // system + tools prefix, persisted as a .meta sidecar for seed matching
}

// newSlotCache builds the cache from config, applying defaults for unset knobs.
// Returns a disabled cache when the feature is off so callers can stay branchless.
func newSlotCache(cfg config.SlotCacheConfig, running func() map[string]string, participates func(string) bool, recurrent func(string) bool, log *logmon.Monitor) *slotCache {
	sc := &slotCache{
		enabled:         cfg.Enable,
		dir:             cfg.Path,
		running:         running,
		participates:    participates,
		recurrent:       recurrent,
		log:             log,
		client:          &http.Client{Timeout: 60 * time.Second}, // a large save/restore can be slow
		occupant:        map[string]*occInfo{},
		pending:         map[string]string{},
		pendingSeed:     map[string]bool{},
		pendingPreamble: map[string]string{},
		awaitConfirm:    map[string][]string{},
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
		key, preamble, ok := sessionAnchor(r)
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
			sc.onSwitch(r.Context(), model, base, key, preamble)
		} else {
			// Cold: model not loaded. The forwarded request will trigger a router
			// load; arrange for its KV to be restored (exact match) or seeded from a
			// similar session's prefix (Tier 1) once it readies.
			sc.markPendingRestore(model, key, preamble)
		}
		next.ServeHTTP(w, r)
		// The request generated (or at least ran), so the resident KV changed.
		sc.markResident(model, key, preamble)
	})
}

// onSwitch handles the moment a (possibly) different conversation arrives for a
// warm model: save the outgoing one if worth it, restore the incoming one if we
// have it on disk, then mark the incoming as resident.
func (sc *slotCache) onSwitch(ctx context.Context, model, base, key, preamble string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	occ := sc.occupant[model]
	if occ != nil && occ.key == key {
		return // same conversation continuing — nothing to swap
	}

	if occ != nil && occ.dirty {
		// Read the live slot occupancy; only persist a conversation big enough to
		// be expensive to reprefill. No turn-count gate: a single-turn chat with a
		// long answer (or big context) is still expensive — and if we skip it here,
		// the outgoing conversation is dropped from occupant tracking unsaved, so a
		// later unload's saveOnEvict can't recover it either (it only sees whatever
		// anchor took the slot next). The toks>=minTokens check is the worth-it gate.
		if toks := sc.slotTokens(ctx, base); toks >= sc.minTokens {
			if err := sc.save(ctx, base, model, occ.key, occ.preamble); err != nil {
				sc.log.Warnf("slotcache: save %s/%s: %v", model, occ.key, err)
				sc.record(kvEvent{Model: model, Op: "error", Key: short(occ.key), Detail: "save"})
			} else {
				occ.dirty = false
				sc.enforceCaps(model, occ.key)
				sc.record(kvEvent{Model: model, Op: "save", Key: short(occ.key), Tokens: toks})
			}
		}
	}

	// Restore the incoming conversation's KV so the forwarded request reuses it
	// instead of reprefilling from scratch. No exact file: seed this agent's
	// system+tools preamble cache (minting it on first sight) so a brand-new
	// conversation on a warm model still reuses the shared prefix — the same Tier-1
	// seed the cold path does, not just cold loads.
	if sc.fileExists(model, key) {
		if err := sc.restore(ctx, base, model, key); err != nil {
			sc.log.Warnf("slotcache: restore %s/%s: %v", model, key, err)
			sc.record(kvEvent{Model: model, Op: "error", Key: short(key), Detail: "restore"})
		} else {
			sc.record(kvEvent{Model: model, Op: "restore-hit", Key: short(key)})
			sc.pushAwait(model, "restore-hit") // expect a forwarded request to confirm reuse
		}
	} else if occ != nil && occ.preamble == preamble && preamble != "" {
		// Slot already holds this exact preamble live (a different conversation from
		// the same agent). Restoring the disk copy would clobber valid live state with
		// a worse one — and on hybrid/linear-attn models (Qwen3.6) a disk-restored
		// preamble doesn't re-extend for a new continuation (confirm-miss, 0 reused).
		// Leave the warm slot; the request reuses the shared prefix natively.
		sc.record(kvEvent{Model: model, Op: "preamble-warm"})
	} else if sc.ensurePreambleSeed(ctx, base, model, preamble) {
		sc.pushAwait(model, "preamble")
	}
	sc.occupant[model] = &occInfo{key: key, preamble: preamble}
}

// markPendingRestore records that, when `model` next reaches Ready, its slot
// should be restored before serving. Two tiers: an EXACT saved file for this
// conversation, or — Tier 1 — a SEED from the most similar prior session's
// prefix (system + tools) so a brand-new chat, a post-compaction lineage, or a
// fresh agent run still reuses the shared static preamble instead of cold
// reprefilling it. Called from the middleware when a request hits a cold model.
func (sc *slotCache) markPendingRestore(model, key, preamble string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.fileExists(model, key) {
		sc.pending[model] = key
		sc.pendingSeed[model] = false
		delete(sc.pendingPreamble, model)
		return
	}
	// No exact file: defer the seed decision to restoreOnLoad, where the process is
	// up so we can mint/restore this agent's preamble cache (Tier 1). Stash the
	// preamble for it to use.
	delete(sc.pending, model)
	sc.pendingPreamble[model] = preamble
}

// restoreOnLoad restores a model's slot KV right after its process becomes Ready
// and before the triggering request is served, completing the cross-swap round
// trip: saveOnEvict wrote the file when the model was evicted; this reads it back
// so the returning conversation reuses its KV instead of reprefilling. The router
// calls this from the process post-start hook.
func (sc *slotCache) restoreOnLoad(model string) {
	if sc == nil || !sc.enabled || model == "" {
		return
	}
	sc.mu.Lock()
	key := sc.pending[model]
	preamble := sc.pendingPreamble[model]
	delete(sc.pending, model)
	delete(sc.pendingSeed, model)
	delete(sc.pendingPreamble, model)
	sc.mu.Unlock()
	if key == "" && preamble == "" {
		return
	}
	if sc.participates == nil || !sc.participates(model) {
		return
	}
	base, running := sc.running()[model]
	if !running {
		return // process not actually up (aborted start, etc.)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Exact saved conversation: restore it verbatim.
	if key != "" {
		if err := sc.restore(ctx, base, model, key); err != nil {
			sc.log.Warnf("slotcache: load-restore %s/%s: %v", model, key, err)
			sc.record(kvEvent{Model: model, Op: "error", Key: short(key), Detail: "load-restore"})
			return
		}
		sc.record(kvEvent{Model: model, Op: "restore-hit", Key: short(key)})
		sc.mu.Lock()
		sc.occupant[model] = &occInfo{key: key} // resident, not yet dirty (nothing ran)
		sc.pushAwait(model, "restore-hit")
		sc.mu.Unlock()
		return
	}

	// Hybrid/recurrent models (GatedDeltaNet/SSM) can only restore their recurrent
	// state at the exact saved length. The partial-prefix paths below (preamble
	// cache + Tier-1 seed) land that state at the wrong position, so llama-server
	// logs "non-consecutive token position" and reprocesses the whole prompt in a
	// loop (0 reuse, slow). The exact restore-hit above already returned for this
	// model; skip the partial paths entirely for recurrent archs.
	if sc.recurrent != nil && sc.recurrent(model) {
		sc.record(kvEvent{Model: model, Op: "miss", Detail: "recurrent-skip-seed"})
		return
	}

	// No exact file: seed this agent's shared system+tools preamble, minting the
	// preamble cache on first sight. The real conversation's key is set by
	// markResident once the triggering request runs, so don't claim occupancy here.
	if sc.ensurePreambleSeed(ctx, base, model, preamble) {
		sc.mu.Lock()
		sc.pushAwait(model, "preamble")
		sc.mu.Unlock()
		return
	}
	// Fallback: a similar prior session's prefix (handles preambles too short or
	// system-less to mint a clean preamble cache from).
	if seedKey, _, ok := sc.bestSeed(model, preamble); ok {
		if err := sc.restore(ctx, base, model, seedKey); err != nil {
			sc.log.Warnf("slotcache: load-seed %s/%s: %v", model, seedKey, err)
			sc.record(kvEvent{Model: model, Op: "error", Key: short(seedKey), Detail: "load-seed"})
			return
		}
		sc.record(kvEvent{Model: model, Op: "restore-seed", Key: short(seedKey)})
		sc.mu.Lock()
		sc.pushAwait(model, "restore-seed")
		sc.mu.Unlock()
		return
	}
	sc.record(kvEvent{Model: model, Op: "miss"})
}

// ensurePreambleSeed makes this agent's preamble (system+tools) KV resident in the
// model's slot and persisted as a reusable "preamble cache" — a category distinct
// from per-conversation files: one preamble-only KV per (model, system+tools),
// minted once and reused by every cold/warm load that shares it. A changed preamble
// (e.g. a daily date bump) hashes differently, so it mints a fresh file and the
// old generation is pruned (one rewrite/day in the common case).
//
// Returns true when the slot now holds the preamble (restored or freshly minted),
// so the triggering request reuses it instead of cold-prefilling the shared
// prefix. Best-effort: any failure returns false and the caller falls back.
//
// Minting needs a synthetic system+tools-only prefill (llama-server can only save
// the whole live slot), so this must run only when the slot is safe to clobber —
// here, right after a cold load, before the triggering request is served.
func (sc *slotCache) ensurePreambleSeed(ctx context.Context, base, model, preamble string) bool {
	sysRaw, _ := splitPreamble(preamble)
	// ponytail: mint only when there's a real system prompt and the preamble is big
	// enough to be worth a synth prefill + disk file. A tools-only preamble needs a
	// user turn to render and isn't worth the chat-template edge cases.
	// Upgrade path: synthesize a dummy user turn if a tools-only agent needs seeding.
	//
	// ponytail: synthPrefill mints via /v1/chat/completions (OpenAI template). A harness
	// served through a different upstream template (Anthropic-native /v1/messages) may
	// tokenize the preamble differently, so the restored KV won't prefix-match — visible
	// as a confirm-miss in the KV Cache tab, no correctness harm. Upgrade path: mint via
	// the same endpoint the request used if that mismatch shows up in practice.
	if sysRaw == "" || len(preamble) < seedMinPrefixBytes {
		return false
	}
	hash := preambleHash(preamble)
	pkey := preambleKey(hash)
	if sc.fileExists(model, pkey) {
		if err := sc.restore(ctx, base, model, pkey); err != nil {
			sc.record(kvEvent{Model: model, Op: "error", Key: short(hash), Detail: "preamble-restore"})
			return false
		}
		sc.record(kvEvent{Model: model, Op: "preamble-hit", Key: short(hash)})
		// Touch mtime so prunePreambleFiles is LRU-by-use, not LRU-by-mint: a preamble
		// minted once but restored often (pi's stable prompt) must not look "oldest" and
		// get evicted when another environment mints on the same model.
		now := time.Now()
		_ = os.Chtimes(filepath.Join(sc.dir, fileName(model, pkey)), now, now)
		return true
	}
	// Mint: a synthetic system+tools-only prefill leaves the preamble KV in the
	// slot, which we then save as this agent's preamble cache.
	if err := sc.synthPrefill(ctx, base, model, preamble); err != nil {
		sc.record(kvEvent{Model: model, Op: "error", Key: short(hash), Detail: "preamble-mint"})
		return false
	}
	if err := sc.save(ctx, base, model, pkey, preamble); err != nil {
		sc.record(kvEvent{Model: model, Op: "error", Key: short(hash), Detail: "preamble-save"})
		return false
	}
	sc.dropStalePreambles(model, pkey, preamble) // delete this agent's prior date-bumped generations
	sc.prunePreambleFiles(model)
	var bytes int64
	if fi, err := os.Stat(filepath.Join(sc.dir, fileName(model, pkey))); err == nil {
		bytes = fi.Size()
	}
	sc.record(kvEvent{Model: model, Op: "preamble-mint", Key: short(hash), Bytes: bytes})
	return true
}

// synthPrefill issues a system+tools-only chat request (max_tokens 1) so the
// upstream renders the preamble through its chat template and leaves the matching
// KV in slot 0, ready to be saved as a preamble cache.
func (sc *slotCache) synthPrefill(ctx context.Context, base, model, preamble string) error {
	sysRaw, toolsRaw := splitPreamble(preamble)
	var b strings.Builder
	b.WriteString(`{"messages":[{"role":"system","content":`)
	b.WriteString(sysRaw) // raw JSON value (string or content-array) — inserted verbatim
	b.WriteString(`}],`)
	if toolsRaw != "" && toolsRaw != "null" {
		b.WriteString(`"tools":`)
		b.WriteString(toolsRaw)
		b.WriteString(`,`)
	}
	b.WriteString(`"max_tokens":1,"stream":false,"cache_prompt":true,"model":`)
	b.WriteString(strconv.Quote(model))
	b.WriteByte('}')

	u := strings.TrimRight(base, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(b.String()))
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
		return fmt.Errorf("synth-prefill -> %s", resp.Status)
	}
	return nil
}

// splitPreamble splits the anchor preamble (sys + "\x00tools\x00" + toolsJSON)
// back into its system content and tools JSON halves.
func splitPreamble(p string) (sysRaw, toolsRaw string) {
	const sep = "\x00tools\x00"
	if i := strings.Index(p, sep); i >= 0 {
		return p[:i], p[i+len(sep):]
	}
	return p, ""
}

// isoTimeOfDay matches an ISO date immediately followed by a time-of-day, e.g.
// "2026-06-29 12:35:44", "2026-06-29T12:35:44.123Z", "2026-06-29 12:35+02:00".
// Group 1 is the date; the time (and any fraction/offset) is dropped.
var isoTimeOfDay = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})[ T]\d{2}:\d{2}(?::\d{2})?(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)

// normalizeTimestamps reduces any ISO datetime in s to date granularity. A bare
// date (no time-of-day) is left untouched. See sessionAnchor for why.
func normalizeTimestamps(s string) string {
	return isoTimeOfDay.ReplaceAllString(s, "$1")
}

func preambleHash(preamble string) string {
	sum := sha256.Sum256([]byte("preamble\x00" + preamble))
	return hex.EncodeToString(sum[:16])
}

func preambleKey(hash string) string { return preambleKeyPrefix + hash }

func isPreambleKey(key string) bool { return strings.HasPrefix(key, preambleKeyPrefix) }

// prunePreambleFiles keeps only the most-recently-used maxPreambleGenerations
// preamble caches per model, deleting the rest (and their .meta). "Used" = mtime,
// which preamble-hit touches — so this is LRU by access, not by mint time, and a
// hot but old preamble (pi's stable prompt) survives while a stale one is evicted.
func (sc *slotCache) prunePreambleFiles(model string) {
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return
	}
	prefix := sanitize(model) + "__" + preambleKeyPrefix
	type f struct {
		path  string
		mtime time.Time
	}
	var files []f
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".bin") || !strings.HasPrefix(name, prefix) {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, f{filepath.Join(sc.dir, name), info.ModTime()})
		}
	}
	if len(files) <= maxPreambleGenerations {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) }) // newest first
	for _, fl := range files[maxPreambleGenerations:] {
		_ = os.Remove(fl.path)
		_ = os.Remove(strings.TrimSuffix(fl.path, ".bin") + ".meta")
	}
}

// saveOnEvict persists a model's live slot KV just before its process is stopped
// for eviction/unload. This is the path that handles a model SWAP — the common
// case onSwitch misses: onSwitch only fires when a new request arrives for the
// SAME warm model, but evicting model A to load model B kills A's process (slot
// gone) without any A request to trigger a save. The router calls this first.
//
// Gated on cost only (slotTokens >= minTokens): an expensive-to-reprefill
// conversation is worth saving regardless of whether it's an interactive chat or
// an agentic run with a single user turn. (onSwitch uses the same cost-only gate.)
func (sc *slotCache) saveOnEvict(model string) {
	if sc == nil || !sc.enabled || model == "" {
		return
	}
	if sc.participates == nil || !sc.participates(model) {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()

	occ := sc.occupant[model]
	if occ == nil || !occ.dirty {
		return // nothing ran since the last save (or nothing resident)
	}
	base, running := sc.running()[model]
	if !running {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if toks := sc.slotTokens(ctx, base); toks >= sc.minTokens {
		if err := sc.save(ctx, base, model, occ.key, occ.preamble); err != nil {
			sc.log.Warnf("slotcache: evict-save %s/%s: %v", model, occ.key, err)
			sc.record(kvEvent{Model: model, Op: "error", Key: short(occ.key), Detail: "evict-save"})
		} else {
			sc.enforceCaps(model, occ.key)
			sc.record(kvEvent{Model: model, Op: "save", Key: short(occ.key), Detail: "evict", Tokens: toks})
		}
	}
	// The process is about to die; its slot will be gone. Drop the occupant so a
	// later load + request restores from disk rather than treating it as resident.
	delete(sc.occupant, model)
}

// markResident records that `key` now holds the model's slot and has run.
func (sc *slotCache) markResident(model, key, preamble string) {
	if model == "" {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	occ := sc.occupant[model]
	if occ == nil || occ.key != key {
		occ = &occInfo{key: key}
		sc.occupant[model] = occ
	}
	occ.preamble = preamble
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

func (sc *slotCache) save(ctx context.Context, base, model, key, preamble string) error {
	if err := sc.slotAction(ctx, base, "save", fileName(model, key)); err != nil {
		return err
	}
	// Persist the preamble snapshot alongside the KV so Tier-1 seeding can prefix-
	// match future cold requests against it. Best-effort: a missing .meta just makes
	// this file ineligible as a seed, never breaks restore.
	if preamble != "" {
		// Preamble caches store the FULL preamble so supersedesPreamble can match the
		// real suffix (a truncated tail breaks the daily-refresh detector). Per-
		// conversation seeds only need a prefix, so they stay capped.
		if len(preamble) > metaMaxBytes && !isPreambleKey(key) {
			preamble = preamble[:metaMaxBytes]
		}
		_ = os.WriteFile(filepath.Join(sc.dir, metaName(model, key)), []byte(preamble), 0o644)
	}
	return nil
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
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("%s -> %s: %s", action, resp.Status, string(rb))
	}
	io.Copy(io.Discard, resp.Body)
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
		if strings.Contains(e.Name(), "__"+preambleKeyPrefix) {
			continue // preamble caches are sticky shared seeds; pruned separately
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
			// Drop the preamble sidecar too (best-effort): foo.bin -> foo.meta.
			_ = os.Remove(strings.TrimSuffix(fl.path, ".bin") + ".meta")
		}
	}
}

// fileName is the on-disk name for a (model, conversation) KV snapshot. Keyed by
// the stable conversation anchor so the same chat overwrites its own file.
func fileName(model, key string) string {
	return sanitize(model) + "__" + key + ".bin"
}

// metaName is the preamble sidecar for a (model, conversation) KV snapshot.
func metaName(model, key string) string {
	return sanitize(model) + "__" + key + ".meta"
}

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string {
	return unsafeFile.ReplaceAllString(s, "_")
}

// short trims a 32-hex anchor to a readable prefix for the monitoring UI.
func short(key string) string {
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

// commonPrefixLen returns the number of leading bytes a and b share.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// commonSuffixLen returns the number of trailing bytes a and b share.
func commonSuffixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// preambleDynDeltaMax bounds the "small dynamic bits" (a date, a session id) that
// may differ between two generations of the SAME agent's preamble. A larger middle
// diff means a genuinely different agent, not a daily refresh of the same one.
const preambleDynDeltaMax = 512

// supersedesPreamble reports whether new and old are the same preamble apart from
// one short dynamic span — a shared prefix and suffix bracketing a tiny middle
// diff on both sides. True => old is a stale generation of new's agent, safe to
// drop. Requires the diff to be small on BOTH sides so a different agent that
// merely shares identical tools (long common suffix) isn't mistaken for a refresh.
func supersedesPreamble(new, old string) bool {
	lcp := commonPrefixLen(new, old)
	lcs := commonSuffixLen(new[lcp:], old[lcp:]) // beyond the shared prefix, no overlap
	return len(new)-lcp-lcs <= preambleDynDeltaMax && len(old)-lcp-lcs <= preambleDynDeltaMax
}

// dropStalePreambles deletes prior preamble caches for the model that are the same
// agent's preamble as `preamble` apart from a small dynamic span (supersedesPreamble)
// — e.g. yesterday's date-stamped generation once today's is minted. keepKey is the
// just-minted file, never a target. Backstop: prunePreambleFiles still caps the rest.
func (sc *slotCache) dropStalePreambles(model, keepKey, preamble string) {
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return
	}
	prefix := sanitize(model) + "__"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".meta") || !strings.HasPrefix(name, prefix+preambleKeyPrefix) {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".meta")
		if key == keepKey {
			continue
		}
		stored, err := os.ReadFile(filepath.Join(sc.dir, name))
		if err != nil {
			continue
		}
		if supersedesPreamble(preamble, string(stored)) {
			_ = os.Remove(filepath.Join(sc.dir, fileName(model, key)))
			_ = os.Remove(filepath.Join(sc.dir, name))
		}
	}
}

// bestSeed finds the saved session for `model` to seed a cold slot from. Among
// candidates whose stored preamble shares >= seedMinPrefixBytes with the incoming
// `preamble`, it minimizes *over-restore* — KV restored beyond the shared prefix.
// Restoring a long sibling CONVERSATION whose tail diverges from the new prompt
// is wasted I/O on plain-attention models and actively harmful on hybrid/recurrent
// ones: the restored state sits at the sibling's full length, the new prompt
// matches only the shared preamble, and the layers that can't be rewound emit
// "non-consecutive token position" + full reprocess (0 reuse). A preamble-cache
// file has no conversation tail, so restoring it never exceeds the prefix — prefer
// it. We don't store seed token-length, so the proxy is: tail-free first, then
// longest shared prefix, then smallest .bin (least excess tail).
//
// Returns its key and the .bin size; this is the Tier-1 lookup that lets a cold,
// never-seen conversation reuse a similar prior session's static system+tools
// preamble via llama-server's own prefix matching after restore.
func (sc *slotCache) bestSeed(model, preamble string) (key string, bytes int64, ok bool) {
	if preamble == "" {
		return "", 0, false
	}
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return "", 0, false
	}
	prefix := sanitize(model) + "__"
	bestLen := 0
	var bestPreamble bool
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".meta") || !strings.HasPrefix(name, prefix) {
			continue
		}
		k := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".meta")
		fi, err := os.Stat(filepath.Join(sc.dir, fileName(model, k)))
		if err != nil || fi.Size() > seedMaxFileBytes {
			continue // no KV file or too big to be worth reading for a prefix
		}
		stored, err := os.ReadFile(filepath.Join(sc.dir, name))
		if err != nil {
			continue
		}
		n := commonPrefixLen(preamble, string(stored))
		if n < seedMinPrefixBytes {
			continue
		}
		isPre := isPreambleKey(k)
		// Preference order: tail-free (preamble cache) beats a tailed conversation;
		// then longer shared prefix; then smaller .bin (least excess KV to restore).
		better := false
		switch {
		case key == "":
			better = true
		case isPre != bestPreamble:
			better = isPre
		case n != bestLen:
			better = n > bestLen
		default:
			better = fi.Size() < bytes
		}
		if better {
			bestLen, bestPreamble, key, bytes = n, isPre, k, fi.Size()
		}
	}
	if key != "" {
		return key, bytes, true
	}
	return "", 0, false
}

// record appends an event to the bounded ring and bumps the matching counter.
// Safe to call from inside sc.mu-held sections (uses a separate lock).
func (sc *slotCache) record(ev kvEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	sc.statsMu.Lock()
	switch ev.Op {
	case "save":
		sc.counters.Saves++
	case "restore-hit":
		sc.counters.RestoreHits++
	case "restore-seed":
		sc.counters.RestoreSeeds++
	case "miss":
		sc.counters.Misses++
	case "error":
		sc.counters.Errors++
	case "confirm":
		sc.counters.ConfirmedReuses++
		sc.counters.CachedTokensSeen += ev.Tokens
	case "confirm-miss":
		sc.counters.ConfirmedMisses++
	case "preamble-mint":
		sc.counters.PreambleMints++
	case "preamble-hit":
		sc.counters.PreambleHits++
	case "preamble-warm":
		sc.counters.PreambleWarm++
	}
	sc.events = append(sc.events, ev)
	if len(sc.events) > kvEventRing {
		sc.events = sc.events[len(sc.events)-kvEventRing:]
	}
	sc.statsMu.Unlock()
}

// awaitConfirmMax bounds the per-model pending-op FIFO so a model that mints/
// restores but never receives a confirming request can't grow unbounded.
const awaitConfirmMax = 16

// pushAwait queues an op for the next request on model to confirm. Caller holds sc.mu.
func (sc *slotCache) pushAwait(model, op string) {
	q := append(sc.awaitConfirm[model], op)
	if len(q) > awaitConfirmMax {
		q = q[len(q)-awaitConfirmMax:]
	}
	sc.awaitConfirm[model] = q
}

// confirmReuse is called by the metrics monitor after each successful request
// with the upstream-reported prompt and cached token counts. When the request
// immediately follows a restore for that model, it confirms whether the restored
// KV was actually reused (cached_tokens > 0) — the only honest proof the cache
// delivered a benefit rather than just loading a file.
func (sc *slotCache) confirmReuse(model string, prompt, cached int) {
	if sc == nil || !sc.enabled || model == "" {
		return
	}
	sc.mu.Lock()
	q := sc.awaitConfirm[model]
	if len(q) == 0 {
		sc.mu.Unlock()
		return // not a post-restore request; warm-slot reuse isn't ours to claim
	}
	op := q[0] // FIFO: oldest pending op confirmed first
	if len(q) == 1 {
		delete(sc.awaitConfirm, model)
	} else {
		sc.awaitConfirm[model] = q[1:]
	}
	sc.mu.Unlock()
	if cached > 0 {
		sc.record(kvEvent{
			Model:  model,
			Op:     "confirm",
			Detail: fmt.Sprintf("%s - %d of %d reused", strings.TrimPrefix(op, "restore-"), cached, cached+prompt),
			Tokens: int64(cached),
		})
		return
	}
	sc.record(kvEvent{Model: model, Op: "confirm-miss", Detail: op + " - 0 reused"})
}

// KVCacheStats is the /api/kvcache snapshot powering the Observe → KV Cache tab.
type KVCacheStats struct {
	Enabled   bool        `json:"enabled"`
	Dir       string      `json:"dir"`
	MaxBytes  int64       `json:"maxBytes"`
	MaxFiles  int         `json:"maxFiles"`
	DiskBytes int64       `json:"diskBytes"`
	Counters  kvCounters  `json:"counters"`
	Files     []kvFileRow `json:"files"`
	// PreambleFiles are the system+tools seed caches (distinct from per-conversation
	// Files): one per agent/environment, reused to seed cold/warm loads.
	PreambleFiles []kvFileRow `json:"preambleFiles"`
	Events        []kvEvent   `json:"events"` // newest first
}

// kvFileRow is one persisted session on disk.
type kvFileRow struct {
	Model    string    `json:"model"`
	Key      string    `json:"key"`
	Bytes    int64     `json:"bytes"`
	ModAt    time.Time `json:"modAt"`
	Preamble string    `json:"preamble,omitempty"` // short snippet for the UI
}

// stats builds the monitoring snapshot: lifetime counters, the recent-event ring
// (newest first), and the current on-disk session files with a preamble snippet.
func (sc *slotCache) stats() KVCacheStats {
	out := KVCacheStats{Enabled: sc != nil && sc.enabled}
	if !out.Enabled {
		return out
	}
	out.Dir = sc.dir
	out.MaxBytes = sc.maxBytes
	out.MaxFiles = sc.maxFiles

	sc.statsMu.Lock()
	out.Counters = sc.counters
	out.Events = make([]kvEvent, len(sc.events))
	for i, ev := range sc.events { // reverse: newest first for the UI
		out.Events[len(sc.events)-1-i] = ev
	}
	sc.statsMu.Unlock()

	entries, _ := os.ReadDir(sc.dir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".bin") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out.DiskBytes += info.Size()
		model, key := splitFileName(name)
		row := kvFileRow{Model: model, Bytes: info.Size(), ModAt: info.ModTime()}
		if snip, err := os.ReadFile(filepath.Join(sc.dir, strings.TrimSuffix(name, ".bin")+".meta")); err == nil {
			row.Preamble = preambleSnippet(string(snip))
		}
		if isPreambleKey(key) {
			row.Key = short(strings.TrimPrefix(key, preambleKeyPrefix))
			out.PreambleFiles = append(out.PreambleFiles, row)
			continue
		}
		row.Key = short(key)
		out.Files = append(out.Files, row)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].ModAt.After(out.Files[j].ModAt) })
	sort.Slice(out.PreambleFiles, func(i, j int) bool { return out.PreambleFiles[i].ModAt.After(out.PreambleFiles[j].ModAt) })
	return out
}

// splitFileName reverses fileName: "model__key.bin" -> (model, key). The model
// half is the sanitized id; the key is the trailing hash after the last "__".
func splitFileName(name string) (model, key string) {
	base := strings.TrimSuffix(name, ".bin")
	if i := strings.LastIndex(base, "__"); i >= 0 {
		return base[:i], base[i+2:]
	}
	return base, ""
}

// preambleSnippet returns a short, single-line excerpt of a stored preamble for
// display (strips the tools sentinel and collapses whitespace).
func preambleSnippet(s string) string {
	if i := strings.Index(s, "\x00"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return ' '
		}
		return r
	}, s))
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// sessionConvHeader is an optional client-supplied stable conversation ID. When
// present it keys the slot file directly, which is more robust than the content
// anchor: it survives a compacted/rewritten opening (same ID => same file, so the
// next save just OVERWRITES the now-dead pre-compaction KV) and never collides
// two distinct chats that happen to share an opening. Clients that can mint a
// per-conversation ID (e.g. the playground) should send it.
const sessionConvHeader = "X-Conversation-Id"

// sessionAnchor derives a stable per-conversation key and the shared preamble
// from a chat-style request, restoring the body for downstream handlers.
//
// Key = the X-Conversation-Id header when the client sends one (preferred:
// survives compaction, no opening collisions); otherwise sha256(first system
// message + first user message) — invariant across a conversation's turns
// (history grows, the opening doesn't), so a chat maps to one file, but fragile
// if the opening is rewritten (compaction). ok=false when the body has no user
// message.
func sessionAnchor(r *http.Request) (key string, preamble string, ok bool) {
	if r.Body == nil {
		return "", "", false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return "", "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body)) // restore for the next handler

	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return "", "", false
	}
	var sys, firstUser string
	var userTurns int
	sysIdx := -1
	for i, m := range msgs.Array() {
		switch m.Get("role").String() {
		case "system", "developer":
			if sys == "" {
				sys = m.Get("content").Raw
				sysIdx = i
			}
		case "user":
			if userTurns == 0 {
				firstUser = m.Get("content").Raw
			}
			userTurns++
		}
	}
	if userTurns == 0 {
		return "", "", false
	}
	// Anthropic /v1/messages carries the system prompt in a top-level "system" field
	// (string or content-block array), not a system-role message — fall back to it so
	// non-OpenAI harnesses get a preamble cache too.
	sysFromTop := false
	if sys == "" {
		sys = gjson.GetBytes(body, "system").Raw
		sysFromTop = true
	}
	// Strip sub-day timestamps from the system prompt. Agents that stamp the
	// wall-clock time into their preamble (pi's per-session memory snapshot:
	// "session_start at 2026-06-29 12:35:44") otherwise change the preamble hash
	// every run, forcing a fresh preamble-KV mint on each /clear. Rewriting the
	// forwarded body too keeps the upstream prefill byte-identical to the date-only
	// KV we cache, so it actually prefix-matches on restore.
	// ponytail: system prompt only — user messages may carry timestamps that matter
	// (pasted logs). Add a config gate only if the date-only forward ever bites.
	if sys != "" {
		if norm := normalizeTimestamps(sys); norm != sys {
			path := "system"
			if !sysFromTop && sysIdx >= 0 {
				path = "messages." + strconv.Itoa(sysIdx) + ".content"
			}
			if nb, err := sjson.SetRawBytes(body, path, []byte(norm)); err == nil {
				body = nb
				// Forward the normalized (shorter) body — fix Content-Length too, or the
				// reverse proxy advertises the stale length and the upstream stalls (502).
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.Header.Del("Transfer-Encoding")
				r.Header.Set("Content-Length", strconv.Itoa(len(body)))
				r.ContentLength = int64(len(body))
				sys = norm
			}
		}
	}
	// preamble = the stable leading prefix shared across turns and across distinct
	// chats from the same agent: system/developer content + the tool definitions.
	// Used only for Tier-1 seed prefix matching, never as the file key.
	preamble = sys + "\x00tools\x00" + gjson.GetBytes(body, "tools").Raw
	// Explicit conversation ID wins: hash it (so arbitrary client strings become a
	// safe fixed-width filename) and use it as the anchor. Same ID across a
	// compaction => same file => the stale KV is overwritten on the next save.
	if id := strings.TrimSpace(r.Header.Get(sessionConvHeader)); id != "" {
		sum := sha256.Sum256([]byte("id\x00" + id))
		return hex.EncodeToString(sum[:16]), preamble, true
	}
	sum := sha256.Sum256([]byte(sys + "\x00" + firstUser))
	return hex.EncodeToString(sum[:16]), preamble, true
}
