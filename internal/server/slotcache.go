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
	// slots reports how many server slots a model launches with (--parallel N,
	// >=1), read from its configured cmd so a COLD model can still be assigned a
	// slot. nil => every model is single-slot.
	slots func(model string) int
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
	//   slotMu   — per SLOT (model id + slot index, see sk), from lockSlot.
	//              Serializes the LONG work for one slot: its /slots save+restore and
	//              the synthetic preamble prefill. Two models share nothing, and on a
	//              multi-slot model a multi-GB save for slot 0 doesn't block a request
	//              landing on slot 1 — llama-server processes a save/restore task for
	//              an idle slot while the others keep generating.
	//   diskMu   — the shared cache directory, for the scan-and-delete passes
	//              (enforceCaps / prunePreambleFiles / dropStalePreambles). Those
	//              read the whole dir and unlink across models, so they must not run
	//              concurrently even though their callers hold different slotMus.
	//
	// Lock order when more than one is needed: slotMu -> stateMu, slotMu -> diskMu.
	// Nothing takes stateMu and diskMu together, and none of them nest into
	// statsMu's users (record() takes only statsMu, so it stays callable anywhere).
	stateMu  sync.Mutex
	occupant map[string]*occInfo // slot key (see sk) -> who currently holds that slot
	// lastUse[slotKey] is when a conversation was last pinned to that slot; it
	// orders the LRU that picks which slot a new conversation evicts.
	lastUse map[string]time.Time
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
func newSlotCache(cfg config.SlotCacheConfig, running func() map[string]string, participates func(string) bool, slots func(string) int, recurrent func(string) bool, log *logmon.Monitor) *slotCache {
	sc := &slotCache{
		enabled:         cfg.Enable,
		dir:             cfg.Path,
		running:         running,
		participates:    participates,
		slots:           slots,
		recurrent:       recurrent,
		log:             log,
		client:          &http.Client{Timeout: 60 * time.Second}, // a large save/restore can be slow
		occupant:        map[string]*occInfo{},
		lastUse:         map[string]time.Time{},
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
		sc.log.Warnf("slotcache: cannot create %s: %v - disabling", sc.dir, err)
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
		var idx int
		if base, running := sc.running()[model]; running {
			idx = sc.onSwitch(r.Context(), model, base, key, preamble)
		} else {
			// Cold: model not loaded. The forwarded request will trigger a router
			// load; arrange for its KV to be restored (exact match) or seeded from a
			// similar session's prefix (Tier 1) once it readies.
			idx = sc.markPendingRestore(model, key, preamble)
		}
		// Pin the request to the slot we prepared, so llama-server serves it there
		// instead of running its own prefix-match slot picker over state we just
		// rewrote. Single-slot models keep an untouched body.
		if sc.slotCount(model) > 1 {
			pinSlot(r, idx)
		}
		next.ServeHTTP(w, r)
		// The request generated (or at least ran), so that slot's KV changed.
		sc.markResident(model, idx, key, preamble)
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

// lockSlot acquires the lock for ONE slot (model + index) and returns its
// unlocker. Only two operations on the same slot of the same llama-server must
// be serialized: different models are different processes, and different slots
// of one process are independent save/restore tasks. The map is tiny (one entry
// per slot ever seen) and never pruned.
func (sc *slotCache) lockSlot(model string, idx int) func() {
	sc.stateMu.Lock()
	if sc.slotMu == nil { // zero-value slotCache (tests build literals)
		sc.slotMu = map[string]*sync.Mutex{}
	}
	key := sk(model, idx)
	mu := sc.slotMu[key]
	if mu == nil {
		mu = &sync.Mutex{}
		sc.slotMu[key] = mu
	}
	sc.stateMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// occupantOf returns the model's current occupant plus a snapshot of its fields,
// so the caller can read them without holding stateMu across I/O.
func (sc *slotCache) occupantOf(model string, idx int) (occ *occInfo, key, preamble string, dirty bool) {
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
	if occ = sc.occupant[sk(model, idx)]; occ == nil {
		return nil, "", "", false
	}
	return occ, occ.key, occ.preamble, occ.dirty
}

func (sc *slotCache) setOccupant(model string, idx int, occ *occInfo) {
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
	sc.occupant[sk(model, idx)] = occ
	if sc.lastUse == nil {
		sc.lastUse = map[string]time.Time{}
	}
	sc.lastUse[sk(model, idx)] = time.Now()
}

// onSwitch handles the moment a (possibly) different conversation arrives for a
// warm model: pick the slot that conversation belongs on, save whoever is being
// evicted from it if they are worth it, restore the incoming one if we have it on
// disk, then mark the incoming as resident. Returns the slot index the caller
// must pin the request to.
func (sc *slotCache) onSwitch(ctx context.Context, model, base, key, preamble string) int {
	n := sc.slotCount(model)
	// One /slots read covers every slot: it says which ones are mid-generation
	// (never evict those) and how many tokens the outgoing conversation is worth
	// saving. Done BEFORE the slot lock so a save in flight on another slot of the
	// same model doesn't serialize the read.
	states := sc.slotStates(ctx, base)
	idx, prev, same := sc.acquire(model, key, preamble, n, busySet(states))
	if same {
		return idx // same conversation continuing on its own slot — nothing to swap
	}

	// Per-slot, not per-model: the save/restore below can take seconds on a
	// multi-GB slot, and only requests landing on THIS slot must wait behind it.
	defer sc.lockSlot(model, idx)()

	if prev.occupied && prev.dirty {
		// Only persist a conversation big enough to be expensive to reprefill. No
		// turn-count gate: a single-turn chat with a long answer (or big context) is
		// still expensive — and if we skip it here, the outgoing conversation is
		// dropped from occupant tracking unsaved, so a later unload's saveOnEvict
		// can't recover it either (it only sees whatever anchor took the slot next).
		// The toks>=minTokens check is the worth-it gate.
		if toks := tokensAt(states, idx); toks >= sc.minTokens {
			if err := sc.save(ctx, base, model, idx, prev.key, prev.preamble); err != nil {
				sc.log.Warnf("slotcache: save %s/%s: %v", model, prev.key, err)
				sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(prev.key), Detail: "save"})
			} else {
				sc.enforceCaps(model, prev.key)
				sc.record(kvEvent{Model: model, Slot: idx, Op: "save", Key: short(prev.key), Tokens: toks})
			}
		}
	}

	// Restore the incoming conversation's KV so the forwarded request reuses it
	// instead of reprefilling from scratch. No exact file: seed this agent's
	// system+tools preamble cache (minting it on first sight) so a brand-new
	// conversation on a warm model still reuses the shared prefix — the same Tier-1
	// seed the cold path does, not just cold loads.
	if sc.fileExists(model, key) {
		if err := sc.restore(ctx, base, model, idx, key); err != nil {
			sc.log.Warnf("slotcache: restore %s/%s: %v", model, key, err)
			sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(key), Detail: "restore"})
		} else {
			sc.record(kvEvent{Model: model, Slot: idx, Op: "restore-hit", Key: short(key)})
			sc.pushAwait(model, "restore-hit") // expect a forwarded request to confirm reuse
		}
	} else if prev.occupied && prev.preamble == preamble && preamble != "" {
		// This slot already holds this exact preamble live (a different conversation
		// from the same agent). Restoring the disk copy would clobber valid live state
		// with a worse one — and on hybrid/linear-attn models (Qwen3.6) a disk-restored
		// preamble doesn't re-extend for a new continuation (confirm-miss, 0 reused).
		// Leave the warm slot; the request reuses the shared prefix natively.
		sc.record(kvEvent{Model: model, Slot: idx, Op: "preamble-warm"})
	} else if sc.ensurePreambleSeed(ctx, base, model, idx, preamble) {
		sc.pushAwait(model, "preamble")
	}
	return idx
}

// markPendingRestore records that, when `model` next reaches Ready, the slot
// this conversation was assigned should be restored before serving. Two tiers:
// an EXACT saved file for this conversation, or — Tier 1 — a SEED from the most
// similar prior session's prefix (system + tools) so a brand-new chat, a
// post-compaction lineage, or a fresh agent run still reuses the shared static
// preamble instead of cold reprefilling it. Called from the middleware when a
// request hits a cold model; returns the slot index to pin the request to.
//
// Cold means every slot is empty, so acquire() hands out 0, 1, 2… in arrival
// order and several conversations racing the same cold load each get their own.
func (sc *slotCache) markPendingRestore(model, key, preamble string) int {
	idx, _, same := sc.acquire(model, key, preamble, sc.slotCount(model), nil)
	if same {
		return idx // already pinned here by an earlier request in this cold window
	}
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
	slot := sk(model, idx)
	if sc.fileExists(model, key) {
		sc.pending[slot] = key
		sc.pendingSeed[slot] = false
		delete(sc.pendingPreamble, slot)
		return idx
	}
	// No exact file: defer the seed decision to restoreOnLoad, where the process is
	// up so we can mint/restore this agent's preamble cache (Tier 1). Stash the
	// preamble for it to use.
	delete(sc.pending, slot)
	sc.pendingPreamble[slot] = preamble
	return idx
}

// restoreOnLoad restores a model's slot KV right after its process becomes Ready
// and before the triggering request is served, completing the cross-swap round
// trip: saveOnEvict wrote the files when the model was evicted; this reads them
// back so the returning conversations reuse their KV instead of reprefilling.
// The router calls this from the process post-start hook.
//
// Every slot with something pending is restored, not just one: a multi-agent
// harness can put two conversations in flight against the same cold model, and
// markPendingRestore assigned each its own slot.
func (sc *slotCache) restoreOnLoad(model string) {
	if sc == nil || !sc.enabled || model == "" {
		return
	}
	if sc.participates == nil || !sc.participates(model) {
		return
	}
	if sc.recurrentSkip(model) {
		return // hybrid: whole-slot restore yields 0 reuse — skip (see recurrentSkip)
	}
	for idx := 0; idx < sc.slotCount(model); idx++ {
		sc.restoreSlotOnLoad(model, idx)
	}
}

// restoreSlotOnLoad performs the post-load restore for ONE slot: the exact saved
// conversation pinned to it, else its agent's preamble cache, else a similar
// session's prefix.
func (sc *slotCache) restoreSlotOnLoad(model string, idx int) {
	defer sc.lockSlot(model, idx)()

	slot := sk(model, idx)
	sc.stateMu.Lock()
	key := sc.pending[slot]
	preamble := sc.pendingPreamble[slot]
	delete(sc.pending, slot)
	delete(sc.pendingSeed, slot)
	delete(sc.pendingPreamble, slot)
	sc.stateMu.Unlock()
	if key == "" && preamble == "" {
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
		if err := sc.restore(ctx, base, model, idx, key); err != nil {
			sc.log.Warnf("slotcache: load-restore %s/%s: %v", model, key, err)
			sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(key), Detail: "load-restore"})
			return
		}
		sc.record(kvEvent{Model: model, Slot: idx, Op: "restore-hit", Key: short(key)})
		sc.setOccupant(model, idx, &occInfo{key: key}) // resident, not yet dirty (nothing ran)
		sc.pushAwait(model, "restore-hit")
		return
	}

	// No exact file: seed this agent's shared system+tools preamble, minting the
	// preamble cache on first sight. The conversation's own key is already claimed
	// on this slot by markPendingRestore; markResident marks it dirty once the
	// triggering request runs.
	if sc.ensurePreambleSeed(ctx, base, model, idx, preamble) {
		sc.pushAwait(model, "preamble")
		return
	}
	// Fallback: a similar prior session's prefix (handles preambles too short or
	// system-less to mint a clean preamble cache from).
	if seedKey, _, ok := sc.bestSeed(model, preamble); ok {
		if err := sc.restore(ctx, base, model, idx, seedKey); err != nil {
			sc.log.Warnf("slotcache: load-seed %s/%s: %v", model, seedKey, err)
			sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(seedKey), Detail: "load-seed"})
			return
		}
		sc.record(kvEvent{Model: model, Slot: idx, Op: "restore-seed", Key: short(seedKey)})
		sc.pushAwait(model, "restore-seed")
		return
	}
	sc.record(kvEvent{Model: model, Slot: idx, Op: "miss"})
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
// here, right after a cold load, or on the slot a new conversation was just
// assigned, before the triggering request is served. The prefill is pinned to
// that slot (id_slot), so on a multi-slot server it never disturbs the other
// conversations mid-flight.
func (sc *slotCache) ensurePreambleSeed(ctx context.Context, base, model string, idx int, preamble string) bool {
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
		if err := sc.restore(ctx, base, model, idx, pkey); err != nil {
			sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(hash), Detail: "preamble-restore"})
			return false
		}
		sc.record(kvEvent{Model: model, Slot: idx, Op: "preamble-hit", Key: short(hash)})
		// Touch mtime so prunePreambleFiles is LRU-by-use, not LRU-by-mint: a preamble
		// minted once but restored often (pi's stable prompt) must not look "oldest" and
		// get evicted when another environment mints on the same model.
		now := time.Now()
		_ = os.Chtimes(filepath.Join(sc.dir, fileName(model, pkey)), now, now)
		return true
	}
	// Mint: a synthetic system+tools-only prefill leaves the preamble KV in the
	// slot, which we then save as this agent's preamble cache.
	if err := sc.synthPrefill(ctx, base, model, idx, preamble); err != nil {
		sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(hash), Detail: "preamble-mint"})
		return false
	}
	if err := sc.save(ctx, base, model, idx, pkey, preamble); err != nil {
		sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(hash), Detail: "preamble-save"})
		return false
	}
	sc.dropStalePreambles(model, pkey, preamble) // delete this agent's prior date-bumped generations
	sc.prunePreambleFiles(model)
	var bytes int64
	if fi, err := os.Stat(filepath.Join(sc.dir, fileName(model, pkey))); err == nil {
		bytes = fi.Size()
	}
	sc.record(kvEvent{Model: model, Slot: idx, Op: "preamble-mint", Key: short(hash), Bytes: bytes})
	return true
}

// synthPrefill issues a system+tools-only chat request (max_tokens 1) so the
// upstream renders the preamble through its chat template and leaves the matching
// KV in the target slot, ready to be saved as a preamble cache.
func (sc *slotCache) synthPrefill(ctx context.Context, base, model string, idx int, preamble string) error {
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
	b.WriteString(`"max_tokens":1,"stream":false,"cache_prompt":true,"id_slot":`)
	b.WriteString(strconv.Itoa(idx))
	b.WriteString(`,"model":`)
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
// SAME warm model, but evicting model A to load model B kills A's process (slots
// gone) without any A request to trigger a save. The router calls this first.
//
// Every occupied slot is saved, not just one: the whole point of running N slots
// is that N conversations are resident, and a swap would otherwise throw away
// N-1 of them.
//
// Gated on cost only (per-slot tokens >= minTokens): an expensive-to-reprefill
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
	base, running := sc.running()[model]
	if !running {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	states := sc.slotStates(ctx, base)
	for idx := 0; idx < sc.slotCount(model); idx++ {
		sc.saveSlotOnEvict(ctx, base, model, idx, tokensAt(states, idx))
	}
}

// saveSlotOnEvict snapshots one slot's occupant if it has run and is expensive
// enough to be worth the disk write, then drops it from occupancy tracking (the
// process — and with it the slot — is about to die).
func (sc *slotCache) saveSlotOnEvict(ctx context.Context, base, model string, idx int, toks int64) {
	defer sc.lockSlot(model, idx)()

	occ, occKey, occPreamble, occDirty := sc.occupantOf(model, idx)
	if occ == nil || !occDirty {
		return // nothing ran since the last save (or nothing resident)
	}
	if toks >= sc.minTokens {
		if err := sc.save(ctx, base, model, idx, occKey, occPreamble); err != nil {
			sc.log.Warnf("slotcache: evict-save %s/%s: %v", model, occKey, err)
			sc.record(kvEvent{Model: model, Slot: idx, Op: "error", Key: short(occKey), Detail: "evict-save"})
		} else {
			sc.enforceCaps(model, occKey)
			sc.record(kvEvent{Model: model, Slot: idx, Op: "save", Key: short(occKey), Detail: "evict", Tokens: toks})
		}
	}
	sc.stateMu.Lock()
	delete(sc.occupant, sk(model, idx))
	sc.stateMu.Unlock()
}

// markResident records that `key` now holds one of the model's slots and has run.
func (sc *slotCache) markResident(model string, idx int, key, preamble string) {
	if model == "" {
		return
	}
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
	slot := sk(model, idx)
	occ := sc.occupant[slot]
	if occ == nil || occ.key != key {
		occ = &occInfo{key: key}
		sc.occupant[slot] = occ
	}
	occ.preamble = preamble
	occ.dirty = true
	if sc.lastUse == nil {
		sc.lastUse = map[string]time.Time{}
	}
	sc.lastUse[slot] = time.Now()
}

func (sc *slotCache) save(ctx context.Context, base, model string, idx int, key, preamble string) error {
	if err := sc.slotAction(ctx, base, idx, "save", fileName(model, key)); err != nil {
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

func (sc *slotCache) restore(ctx context.Context, base, model string, idx int, key string) error {
	return sc.slotAction(ctx, base, idx, "restore", fileName(model, key))
}

// slotAction calls llama-server's POST /slots/<idx>?action=save|restore. The
// filename is relative to the server's --slot-save-path (== sc.dir), and is
// keyed by conversation only — a snapshot is not tied to the slot it came from,
// so a conversation can come back on whichever slot it is assigned next time.
func (sc *slotCache) slotAction(ctx context.Context, base string, idx int, action, filename string) error {
	u := strings.TrimRight(base, "/") + "/slots/" + strconv.Itoa(idx) + "?action=" + action
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
