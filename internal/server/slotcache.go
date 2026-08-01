package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
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

	// Locking is three-way, because the three things being protected have very
	// different hold times:
	//
	//   stateMu  — the bookkeeping maps below (and the occInfo values they point
	//              at). Held for map reads/writes only, never across I/O.
	//   slotMu   — per model id, from lockModel. Serializes the LONG work for one
	//              model: /slots save+restore, the synthetic preamble prefill. Each
	//              model is a separate llama-server with its own slot 0, so two
	//              models have nothing to serialize; a multi-GB save for model A no
	//              longer blocks a request for model B (it used to — one global lock
	//              covered both the maps and the HTTP calls).
	//   diskMu   — the shared cache directory, for the scan-and-delete passes
	//              (enforceCaps / prunePreambleFiles / dropStalePreambles). Those
	//              read the whole dir and unlink across models, so they must not run
	//              concurrently even though their callers hold different slotMus.
	//
	// Lock order when more than one is needed: slotMu -> stateMu, slotMu -> diskMu.
	// Nothing takes stateMu and diskMu together, and none of them nest into
	// statsMu's users (record() takes only statsMu, so it stays callable anywhere).
	stateMu  sync.Mutex
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

	// slotMu holds one mutex per model id (see lockModel). Guarded by stateMu.
	slotMu map[string]*sync.Mutex
	// diskMu serializes the directory-wide prune passes.
	diskMu sync.Mutex

	// stats: counters + a bounded ring of recent events, surfaced at /api/kvcache
	// for the Observe → KV Cache tab. Guarded by its own lock so record() can be
	// called from inside any of the locks above without reentrancy.
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
		slotMu:          map[string]*sync.Mutex{},
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
		// Resolve the model first so we can bail before reading/rewriting the body
		// for models the cache won't touch.
		data, _ := shared.ReadContext(r.Context())
		model := data.ModelID
		if model == "" || sc.participates == nil || !sc.participates(model) {
			next.ServeHTTP(w, r) // model opted out (no --slot-save-path) — stay out of its way
			return
		}
		if sc.recurrentSkip(model) {
			// Hybrid/recurrent arch: whole-slot save/restore reuses 0 tokens (see
			// recurrentSkip). Skip all disk work — promptCanon has already stabilized
			// the prefix for the native warm reuse that IS the win on these models.
			next.ServeHTTP(w, r)
			return
		}
		key, preamble, ok := sessionAnchor(r)
		if !ok {
			next.ServeHTTP(w, r)
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

// recurrentSkip reports whether a model is a hybrid/recurrent arch (GatedDeltaNet/
// SSM, FullAttnInterval>0) for which the slot cache should do NOTHING. These models
// can only restore their recurrent state at its exact saved length, so llama-server
// reprocesses the whole prompt on restore — measured on Qwen3.6-35B-A3B: a warm,
// same-process, exact whole-slot restore of a 31,993-token prompt reused 0 tokens
// (confirm-miss, prefill 86.1s→85.5s). So save/restore is pure overhead here — a
// multi-GB write under the global lock for zero benefit. We skip ALL disk work
// (save + restore + partial-prefix seeding), not just seeding. The native warm
// prefix reuse (stabilized by promptCanon) is the only win on these archs, and it
// doesn't need us. Re-enable (drop this guard) if upstream llama.cpp #21831 lands.
func (sc *slotCache) recurrentSkip(model string) bool {
	return sc.recurrent != nil && sc.recurrent(model)
}

// lockModel acquires the per-model slot lock and returns its unlocker. One
// llama-server per model, one slot 0 per llama-server — so the only thing that
// must be serialized is two operations on the SAME model. The map itself is
// tiny (one entry per model ever seen) and never pruned.
func (sc *slotCache) lockModel(model string) func() {
	sc.stateMu.Lock()
	if sc.slotMu == nil { // zero-value slotCache (tests build literals)
		sc.slotMu = map[string]*sync.Mutex{}
	}
	mu := sc.slotMu[model]
	if mu == nil {
		mu = &sync.Mutex{}
		sc.slotMu[model] = mu
	}
	sc.stateMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// occupantOf returns the model's current occupant plus a snapshot of its fields,
// so the caller can read them without holding stateMu across I/O.
func (sc *slotCache) occupantOf(model string) (occ *occInfo, key, preamble string, dirty bool) {
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
	if occ = sc.occupant[model]; occ == nil {
		return nil, "", "", false
	}
	return occ, occ.key, occ.preamble, occ.dirty
}

func (sc *slotCache) setOccupant(model string, occ *occInfo) {
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
	sc.occupant[model] = occ
}

// onSwitch handles the moment a (possibly) different conversation arrives for a
// warm model: save the outgoing one if worth it, restore the incoming one if we
// have it on disk, then mark the incoming as resident.
func (sc *slotCache) onSwitch(ctx context.Context, model, base, key, preamble string) {
	// Per-model, not global: the save/restore below can take seconds on a
	// multi-GB slot, and only requests for THIS model must wait behind it.
	defer sc.lockModel(model)()

	occ, occKey, occPreamble, occDirty := sc.occupantOf(model)
	if occ != nil && occKey == key {
		return // same conversation continuing — nothing to swap
	}

	if occ != nil && occDirty {
		// Read the live slot occupancy; only persist a conversation big enough to
		// be expensive to reprefill. No turn-count gate: a single-turn chat with a
		// long answer (or big context) is still expensive — and if we skip it here,
		// the outgoing conversation is dropped from occupant tracking unsaved, so a
		// later unload's saveOnEvict can't recover it either (it only sees whatever
		// anchor took the slot next). The toks>=minTokens check is the worth-it gate.
		if toks := sc.slotTokens(ctx, base); toks >= sc.minTokens {
			if err := sc.save(ctx, base, model, occKey, occPreamble); err != nil {
				sc.log.Warnf("slotcache: save %s/%s: %v", model, occKey, err)
				sc.record(kvEvent{Model: model, Op: "error", Key: short(occKey), Detail: "save"})
			} else {
				sc.stateMu.Lock()
				occ.dirty = false
				sc.stateMu.Unlock()
				sc.enforceCaps(model, occKey)
				sc.record(kvEvent{Model: model, Op: "save", Key: short(occKey), Tokens: toks})
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
	} else if occ != nil && occPreamble == preamble && preamble != "" {
		// Slot already holds this exact preamble live (a different conversation from
		// the same agent). Restoring the disk copy would clobber valid live state with
		// a worse one — and on hybrid/linear-attn models (Qwen3.6) a disk-restored
		// preamble doesn't re-extend for a new continuation (confirm-miss, 0 reused).
		// Leave the warm slot; the request reuses the shared prefix natively.
		sc.record(kvEvent{Model: model, Op: "preamble-warm"})
	} else if sc.ensurePreambleSeed(ctx, base, model, preamble) {
		sc.pushAwait(model, "preamble")
	}
	sc.setOccupant(model, &occInfo{key: key, preamble: preamble})
}

// markPendingRestore records that, when `model` next reaches Ready, its slot
// should be restored before serving. Two tiers: an EXACT saved file for this
// conversation, or — Tier 1 — a SEED from the most similar prior session's
// prefix (system + tools) so a brand-new chat, a post-compaction lineage, or a
// fresh agent run still reuses the shared static preamble instead of cold
// reprefilling it. Called from the middleware when a request hits a cold model.
func (sc *slotCache) markPendingRestore(model, key, preamble string) {
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
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
	defer sc.lockModel(model)()

	sc.stateMu.Lock()
	key := sc.pending[model]
	preamble := sc.pendingPreamble[model]
	delete(sc.pending, model)
	delete(sc.pendingSeed, model)
	delete(sc.pendingPreamble, model)
	sc.stateMu.Unlock()
	if key == "" && preamble == "" {
		return
	}
	if sc.participates == nil || !sc.participates(model) {
		return
	}
	if sc.recurrentSkip(model) {
		return // hybrid: whole-slot restore yields 0 reuse — skip (see recurrentSkip)
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
		sc.setOccupant(model, &occInfo{key: key}) // resident, not yet dirty (nothing ran)
		sc.pushAwait(model, "restore-hit")
		return
	}

	// No exact file: seed this agent's shared system+tools preamble, minting the
	// preamble cache on first sight. The real conversation's key is set by
	// markResident once the triggering request runs, so don't claim occupancy here.
	if sc.ensurePreambleSeed(ctx, base, model, preamble) {
		sc.pushAwait(model, "preamble")
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
		sc.pushAwait(model, "restore-seed")
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
	if sc.recurrentSkip(model) {
		return // hybrid: restore yields 0 reuse, so saving is pure overhead (see recurrentSkip)
	}
	defer sc.lockModel(model)()

	occ, occKey, occPreamble, occDirty := sc.occupantOf(model)
	if occ == nil || !occDirty {
		return // nothing ran since the last save (or nothing resident)
	}
	base, running := sc.running()[model]
	if !running {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if toks := sc.slotTokens(ctx, base); toks >= sc.minTokens {
		if err := sc.save(ctx, base, model, occKey, occPreamble); err != nil {
			sc.log.Warnf("slotcache: evict-save %s/%s: %v", model, occKey, err)
			sc.record(kvEvent{Model: model, Op: "error", Key: short(occKey), Detail: "evict-save"})
		} else {
			sc.enforceCaps(model, occKey)
			sc.record(kvEvent{Model: model, Op: "save", Key: short(occKey), Detail: "evict", Tokens: toks})
		}
	}
	// The process is about to die; its slot will be gone. Drop the occupant so a
	// later load + request restores from disk rather than treating it as resident.
	sc.stateMu.Lock()
	delete(sc.occupant, model)
	sc.stateMu.Unlock()
}

// markResident records that `key` now holds the model's slot and has run.
func (sc *slotCache) markResident(model, key, preamble string) {
	if model == "" {
		return
	}
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
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
