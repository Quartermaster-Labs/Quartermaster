package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Tool-call replay.
//
// The client sends conversation history as plain role+content messages: the
// tool calls a previous turn made, and the results it got back, exist only as
// the UI's `searches` metadata on the assistant message and were dropped before
// the model ever saw them. So a model reading its own history saw nothing but
// prose — turns where it announced a lookup and then narrated an answer, with no
// evidence a tool was ever called or ever worked.
//
// That reads as a demonstration of how to behave in this conversation, and
// models follow it: in the "Dodging Sixth Grade Pranks" thread the model spent
// three consecutive turns saying "let me grab the remaining three now" and
// emitting no call, because nothing in its context showed a call being made.
// Worse, the answers it *had* built from real transcripts were unsupported by
// anything in the window, one step from being re-derived as invention.
//
// replayToolCalls rebuilds those turns: assistant-with-tool_calls, the real
// stored results as tool messages, then the assistant's actual answer. The model
// gets back both the evidence and the example.

// replayResultMax caps ONE replayed result. Truncation is per-result and fixed —
// deliberately not a whole-history budget, which would re-trim older entries
// every time a new turn spent some of it, changing the prompt prefix and voiding
// the KV cache for the entire conversation on every message. A fixed cap makes
// each replayed message depend only on itself, so the prefix stays byte-stable
// as the thread grows. Overall growth is bounded by chat compaction, which
// already drops the front of long threads.
const replayResultMax = 6000

// replayNote marks a result the model is re-reading rather than receiving fresh,
// so it does not report a months-old page as the current state of the world.
const replayNote = "\n\n(Result from an earlier turn in this conversation, replayed for reference.)"

// replayToolCalls expands a client-sent history so prior turns' tool calls and
// results are present as real tool messages. Messages without searches pass
// through byte-identical.
func replayToolCalls(msgs []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(msgs))
	for i, raw := range msgs {
		var m struct {
			Role     string       `json:"role"`
			Content  string       `json:"content"`
			Searches []turnSearch `json:"searches"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Role != "assistant" || len(m.Searches) == 0 {
			out = append(out, raw)
			continue
		}
		var calls []any
		var results []json.RawMessage
		for j, s := range m.Searches {
			name, args, ok := replayCall(s)
			if !ok {
				continue
			}
			id := fmt.Sprintf("hist_%d_%d", i, j)
			calls = append(calls, map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			})
			results = append(results, mustJSON(map[string]any{
				"role": "tool", "tool_call_id": id,
				"content": truncateResult(s.Results) + replayNote,
			}))
		}
		if len(calls) == 0 {
			out = append(out, raw)
			continue
		}
		// One batched call block per turn, then the results, then the answer. A
		// real turn may have interleaved several rounds of calls and prose; that
		// ordering cannot be reconstructed from `searches` (it records byte
		// offsets, not round boundaries) and does not matter here — what the
		// model needs is that the calls happened and what they returned.
		out = append(out, mustJSON(map[string]any{
			"role": "assistant", "content": "", "tool_calls": calls,
		}))
		out = append(out, results...)
		out = append(out, mustJSON(map[string]any{"role": "assistant", "content": m.Content}))
	}
	return out
}

// replayCall maps a recorded search back to the tool that made it. Arguments are
// reconstructed from what was stored, not from the original call — enough to
// show the shape and the target, which is what the model reads it for.
func replayCall(s turnSearch) (name, args string, ok bool) {
	// A URL-taking tool must replay with the real URL: turnSearch.Query holds the
	// resolved page/video TITLE once the fetch succeeded, and feeding that back as
	// a `url` argument would teach the model to call fetch_page with a title.
	link := s.Query
	if len(s.Sources) > 0 && s.Sources[0].URL != "" {
		link = s.Sources[0].URL
	}
	switch s.Kind {
	case "web":
		name = "web_search"
	case "wiki":
		name = "wiki_search"
	case "youtube-search":
		name = "youtube_search"
	case "page":
		return "fetch_page", mustArg("url", link), true
	case "youtube":
		return "youtube_transcript", mustArg("url", link), true
	case "youtube-comments":
		return "youtube_comments", mustArg("url", link), true
	case "feed":
		return "fetch_feed", mustArg("url", link), true
	case "weather":
		return "get_weather", mustArg("location", s.Query), true
	case "calc":
		// The recorded query IS the expression, so this one round-trips exactly.
		return "calculate", mustArg("expression", s.Query), true
	default:
		// "time" and "units" record a rendered label ("15.6 in → cm"), not their
		// arguments, so replaying them would put a call in the history that
		// could not have been made. They are also instant to re-issue.
		//
		// "quartermaster" and anything a later version adds: a config action is
		// not reference data, and guessing which of the two QM tools ran (the
		// kind does not say) would put a call in the history that never happened.
		return "", "", false
	}
	return name, mustArg("query", s.Query), true
}

func mustArg(key, val string) string {
	b, _ := json.Marshal(map[string]string{key: val})
	return string(b)
}

// truncateResult cuts a replayed result at a line boundary and says so, so the
// model treats it as partial instead of as the whole page.
func truncateResult(s string) string {
	if len(s) <= replayResultMax {
		return s
	}
	cut := s[:replayResultMax]
	if i := strings.LastIndexByte(cut, '\n'); i > replayResultMax/2 {
		cut = cut[:i]
	}
	return cut + "\n\n[…truncated: this result was longer when first fetched. Fetch it again if you need the rest.]"
}
