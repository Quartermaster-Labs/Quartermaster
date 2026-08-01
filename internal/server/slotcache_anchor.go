package server

// Deriving the cache key from a request: which conversation this is
// (sessionAnchor), what its stable system+tools preamble is, and the
// timestamp normalization that keeps that preamble byte-identical turn to turn.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
