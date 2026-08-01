package server

// Slot-cache observability: the counter set, the bounded event ring, the
// pending-confirmation queue that pairs a restore with the reuse llama-server
// actually reports, and the /api/kvcache snapshot they feed. Guarded by its own
// statsMu so record() is callable from inside any other lock.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// record appends an event to the bounded ring and bumps the matching counter.
// Safe to call while holding any other slotCache lock (uses a separate one).
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

// pushAwait queues an op for the next request on model to confirm.
func (sc *slotCache) pushAwait(model, op string) {
	sc.stateMu.Lock()
	defer sc.stateMu.Unlock()
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
	sc.stateMu.Lock()
	q := sc.awaitConfirm[model]
	if len(q) == 0 {
		sc.stateMu.Unlock()
		return // not a post-restore request; warm-slot reuse isn't ours to claim
	}
	op := q[0] // FIFO: oldest pending op confirmed first
	if len(q) == 1 {
		delete(sc.awaitConfirm, model)
	} else {
		sc.awaitConfirm[model] = q[1:]
	}
	sc.stateMu.Unlock()
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
