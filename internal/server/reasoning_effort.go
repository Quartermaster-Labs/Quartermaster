package server

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Reasoning effort is a CHAT-TEMPLATE feature, not a sampler knob: Qwen 3.8
// injects a sentence at the top of the system block per level, so the only way
// to drive it is a `chat_template_kwargs.reasoning_effort` entry the jinja
// renderer reads. External clients (pi, anything on an OpenAI SDK) instead send
// the standard top-level `reasoning_effort` field, which llama-server 9886
// ignores outright — newer builds forward it, but verbatim, so an OpenAI-ladder
// value the template doesn't know ("minimal") hits its raise-on-unknown guard
// and comes back as a 500.
//
// This translates the standard field into the kwarg, snapped to a level the
// model's own template declares (config `capabilities.reasoningEffort`, emitted
// by autogen from the baked template's guard). A model that advertises no ladder
// is left completely untouched.
//
// Cost worth knowing: the level lands at the TOP of the system block, so
// changing it mid-conversation invalidates the whole KV prefix and reprocesses
// the entire history. It is a per-session setting, not a per-request dial.

// effortRank orders the level vocabularies both sides use — the OpenAI ladder
// (minimal/low/medium/high) and the Qwen one (low/medium/xhigh) — on one scale,
// so a value from either can be snapped to the nearest level a given template
// actually accepts. Values outside this table are unrankable and pass only on an
// exact match.
var effortRank = map[string]int{
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// normalizeReasoningEffort maps a requested effort onto the levels a model's
// chat template accepts.
//
//   - "none" (OpenAI's "don't think") => disableThinking, no level.
//   - an exact match (case-insensitive) => that level.
//   - a rankable value => the accepted level with the closest rank, ties going
//     to the richer one. So "minimal" lands on low, "high" on xhigh — which is
//     also the alias Qwen 3.8's own template applies to "high".
//   - anything else => ok=false, and the caller leaves the request alone.
func normalizeReasoningEffort(requested string, levels []string) (level string, disableThinking bool, ok bool) {
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "" || len(levels) == 0 {
		return "", false, false
	}
	if req == "none" || req == "off" {
		return "", true, true
	}
	for _, l := range levels {
		if strings.EqualFold(l, req) {
			return l, false, true
		}
	}
	wantRank, rankable := effortRank[req]
	if !rankable {
		return "", false, false
	}
	best, bestDist := "", 0
	for _, l := range levels {
		r, ok := effortRank[strings.ToLower(l)]
		if !ok {
			continue
		}
		dist := r - wantRank
		if dist < 0 {
			dist = -dist
		}
		// Ties go to the richer level, independent of declaration order: asking
		// for more thinking and silently getting less is the worse miss, and it
		// is what the template's own high->xhigh alias does.
		if best == "" || dist < bestDist || (dist == bestDist && r > effortRank[strings.ToLower(best)]) {
			best, bestDist = l, dist
		}
	}
	if best == "" {
		return "", false, false
	}
	return best, false, true
}

// applyReasoningEffort rewrites a JSON request body so a client's standard
// `reasoning_effort` field reaches the chat template. It is a no-op unless the
// model advertises a ladder and the body carries a string `reasoning_effort`.
//
// An explicit `chat_template_kwargs.reasoning_effort` in the request always
// wins — a caller that already speaks llama.cpp's dialect is not second-guessed.
// The top-level field is dropped once translated, so exactly one mechanism
// carries the value no matter how new the llama-server build is.
func applyReasoningEffort(body []byte, levels []string) ([]byte, error) {
	if len(levels) == 0 {
		return body, nil
	}
	req := gjson.GetBytes(body, "reasoning_effort")
	if req.Type != gjson.String {
		return body, nil
	}
	if gjson.GetBytes(body, "chat_template_kwargs.reasoning_effort").Exists() {
		return sjson.DeleteBytes(body, "reasoning_effort")
	}

	level, disableThinking, ok := normalizeReasoningEffort(req.String(), levels)
	if !ok {
		return body, nil
	}

	var err error
	if disableThinking {
		// enable_thinking is llama.cpp's own kwarg, understood by every
		// thinking-capable template; the effort kwarg is meaningless with
		// thinking off (Qwen gates the whole block on it).
		if body, err = sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", false); err != nil {
			return nil, err
		}
	} else if body, err = sjson.SetBytes(body, "chat_template_kwargs.reasoning_effort", level); err != nil {
		return nil, err
	}
	return sjson.DeleteBytes(body, "reasoning_effort")
}
