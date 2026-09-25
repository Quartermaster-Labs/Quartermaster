package server

import (
	"encoding/json"
	"io"
	"net/http"
)

// Chat compaction as an APPEND to the live conversation.
//
// Compaction used to be a fresh client-side completion: the older messages, with
// no tool plumbing, no system prompt and no conversation id, followed by a
// "summarize" instruction. To the slot cache that is a different conversation,
// so on a single-slot model it evicted the chat it was summarizing (a
// multi-gigabyte snapshot write on a recurrent arch) and then prefilled the
// whole slice cold.
//
// This endpoint sends the model what it already holds instead. The client posts
// the same turn body it would send for the next message (its system prompt,
// history and tools), with the instruction as the last user message, and the
// history goes through the same server-side assembly a turn round gets
// (inlineMedia, replayToolCalls) under the same X-Conversation-Id. The prompt is
// then the resident KV plus one user message, and nothing is evicted. Whatever
// part of the history the next ordinary turn would re-prefill, this does too:
// the two send the same bytes up to the appended instruction.
//
// Rules that keep that true:
//   - tools ride along unchanged and tool_choice is left unset. llama.cpp
//     renders a tool_choice:"none" request without the tool list, and the tools
//     live inside the system block, so "none" would rewrite the head of the
//     prompt and void the whole cache. A reply that calls a tool anyway is
//     returned as-is; the client treats a summary with no content as a failure.
//   - thinking off. enable_thinking only changes the generation suffix after
//     the final user message, so it costs no reuse, and a summary that thinks
//     can spend its whole budget in reasoning and return nothing.
//   - nothing is persisted. The client owns summary + compactedCount.

// POST /api/chats/compact: the body is a turnStart whose messages end with the
// summary instruction. Synchronous: answers with the upstream completion.
func (s *Server) handleTurnCompact(w http.ResponseWriter, r *http.Request) {
	tm, user := s.turnAuth(w, r)
	if tm == nil {
		return
	}
	var start turnStart
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBlobBytes)).Decode(&start); err != nil || start.ChatID == "" || start.Model == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// A running turn on this user's account owns the model; a summary taken now
	// would describe a conversation that is still changing.
	tm.mu.Lock()
	cur, busy := tm.active[user]
	busy = busy && !cur.isDone()
	tm.mu.Unlock()
	if busy {
		http.Error(w, "a turn is already running", http.StatusConflict)
		return
	}

	msgs, err := tm.compactMessages(user, start)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	maxTokens := 0
	if start.MaxTokens != nil {
		maxTokens = *start.MaxTokens
	}
	body := buildBody(start, msgs, maxTokens, false, "")
	body["stream"] = false

	req, err := tm.selfCompletionRequest(r.Context(), body, start.ChatID, s.pickSelfKey(start.Model))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := tm.client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	// Passed through untouched: the client already knows how to read a
	// completion and how to say which kind of empty it got.
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// compactMessages assembles the history exactly as runLoop does for a turn round,
// so the forwarded prompt is a continuation of what the slot holds.
func (tm *turnManager) compactMessages(user string, start turnStart) ([]json.RawMessage, error) {
	var base []json.RawMessage
	if err := json.Unmarshal(tm.pg.inlineMedia(user, start.Messages), &base); err != nil {
		return nil, err
	}
	if len(start.Tools) > 0 {
		base = replayToolCalls(base, tm.replayLookup(start.ChatID))
	}
	return base, nil
}
