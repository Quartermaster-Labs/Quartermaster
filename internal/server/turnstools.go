package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Server-side ports of the playground's tool + reasoning helpers, so the turn
// runner (turns.go) can drive the model→tool→model loop headlessly (survives a
// closed tab). Kept behaviourally identical to the client originals:
//   - wiki: ui-svelte/src/lib/wiki.ts (imports the same wiki_articles.json)
//   - web: ui-svelte/src/lib/webSearch.ts
//   - reasoning: ui-svelte/src/lib/reasoning.ts

// --- wiki corpus (THE single source; the Svelte UI imports this very file.
// It lives here, not in ui-svelte/, because //go:embed cannot reference a
// path outside its own package directory) --------------------------------

//go:embed wiki_articles.json
var wikiJSON []byte

type wikiArticle struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Keywords []string `json:"keywords"`
	Body     string   `json:"body"`
}

var wikiArticles = func() []wikiArticle {
	var a []wikiArticle
	_ = json.Unmarshal(wikiJSON, &a)
	return a
}()

const wikiMaxResults = 3

// searchWiki scores articles by term overlap (title > keywords > body) and
// returns the best few. Port of wiki.ts searchWiki.
func searchWiki(query string) []wikiArticle {
	terms := regexp.MustCompile(`[a-z0-9]+`).FindAllString(strings.ToLower(query), -1)
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		a wikiArticle
		s int
	}
	var out []scored
	for _, a := range wikiArticles {
		title := strings.ToLower(a.Title)
		keys := strings.ToLower(strings.Join(a.Keywords, " "))
		body := strings.ToLower(a.Body)
		score := 0
		for _, t := range terms {
			if strings.Contains(title, t) {
				score += 3
			}
			if strings.Contains(keys, t) {
				score += 2
			}
			if strings.Contains(body, t) {
				score += 1
			}
		}
		if score > 0 {
			out = append(out, scored{a, score})
		}
	}
	// stable sort by score desc (small N, insertion sort keeps ties in corpus order)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].s > out[j-1].s; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > wikiMaxResults {
		out = out[:wikiMaxResults]
	}
	res := make([]wikiArticle, len(out))
	for i, s := range out {
		res[i] = s.a
	}
	return res
}

// formatWikiResults renders the plain-text tool message. Port of wiki.ts.
func formatWikiResults(query string, results []wikiArticle, numbers []int) string {
	if len(results) == 0 {
		var topics []string
		for _, a := range wikiArticles {
			topics = append(topics, "- "+a.Title)
		}
		return fmt.Sprintf("No wiki article matched %q. Available topics:\n%s", query, strings.Join(topics, "\n"))
	}
	var parts []string
	for i, a := range results {
		parts = append(parts, fmt.Sprintf("## [%d] %s\n%s", numbers[i], a.Title, a.Body))
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// --- web search (SearXNG) --------------------------------------------------
//
// The executor (searchChain → tools.Search) and the result types moved to
// internal/tools so the /v1/tools API shares them. This file keeps only the
// turn-layer concern: parsing the model's `count` argument, which collapses
// out-of-range values to tools.DefaultResults rather than failing the call.

// parseSearchCount reads the optional `count` off a web_search call. Out-of-range
// and missing values collapse to the default rather than failing the call: the
// number is a preference, and refusing a search over it would cost a round trip
// to gain nothing.
func parseSearchCount(raw string) int {
	var a struct {
		Count      any `json:"count"`
		NumResults any `json:"num_results"`
		Limit      any `json:"limit"`
	}
	if json.Unmarshal([]byte(raw), &a) != nil {
		return webDefaultResults
	}
	for _, v := range []any{a.Count, a.NumResults, a.Limit} {
		n := 0
		switch t := v.(type) {
		case float64:
			n = int(t)
		case string:
			fmt.Sscanf(strings.TrimSpace(t), "%d", &n)
		default:
			continue
		}
		if n < 1 {
			return webDefaultResults
		}
		if n > webMaxResults {
			return webMaxResults
		}
		return n
	}
	return webDefaultResults
}

// --- reasoning markup (port of reasoning.ts) -------------------------------

var (
	harmonyCtrlRe = regexp.MustCompile(`(?i)<\|(?:start|end|return|constrain)\|?>(?:assistant|user|system)?`)
	harmonyMsgRe  = regexp.MustCompile(`<\|message\|?>`)
	channelRe     = regexp.MustCompile(`(?i)<\|channel\|?>\s*([a-zA-Z]+)\s*(?:<\|message\|?>)?`)

	thinkClosedRe = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)>.*?</(?:think|thinking|reasoning)>`)
	thinkOpenRe   = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)>.*$`)
)

// harmonyToThink rewrites gpt-oss harmony channel markup into <think> blocks.
// Port of reasoning.ts harmonyToThink. No-op when no channel markup present.
func harmonyToThink(text string) string {
	locs := channelRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text
	}
	clean := func(s string) string {
		return harmonyMsgRe.ReplaceAllString(harmonyCtrlRe.ReplaceAllString(s, ""), "")
	}
	var b strings.Builder
	b.WriteString(clean(text[:locs[0][0]]))
	for i, m := range locs {
		start := m[1] // end of this channel marker
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		channel := strings.ToLower(text[m[2]:m[3]])
		body := clean(text[start:end])
		isFinal := channel == "final" || channel == "commentary"
		switch {
		case isFinal:
			b.WriteString(body)
		case i == len(locs)-1:
			b.WriteString("<think>" + body) // last segment, still streaming — leave open
		default:
			b.WriteString("<think>" + body + "</think>")
		}
	}
	return b.String()
}

// Recovery from a round that ended after thinking with no answer (EOS on an
// unterminated <think>). See runLoop.
const (
	// noAnswerMarker is written into the answer when even the retry produced
	// nothing, so the chat never shows a thought bubble with silence under it.
	noAnswerMarker = "_(the model stopped after thinking, without writing an answer)_"
	// nudgeThoughtMax caps how much reasoning is handed back. The tail is what
	// matters — that's where the model was heading when it stopped.
	nudgeThoughtMax = 6000
)

var thinkTagStripper = strings.NewReplacer("<think>", "", "</think>", "")

// noAnswerNudge builds the synthetic user turn that hands the model its own
// reasoning back and asks for the answer alone. Deliberately a *user* message
// with the reasoning inlined as text rather than an assistant turn carrying
// reasoning_content: chat templates routinely drop an assistant message with
// empty content, and only some keep prior-turn <think> at all, so the field
// route would silently lose the thought on most models. This survives any
// template. It is never persisted — it lives only in the round's message list.
func noAnswerNudge(thought string) string {
	// Strip the tags, keep the text: for a model that inlines its <think> into
	// content the thought IS the text between them (answerOnly would delete
	// exactly the part we want to hand back).
	thought = strings.TrimSpace(thinkTagStripper.Replace(thought))
	if thought == "" {
		thought = "(empty)"
	}
	if len(thought) > nudgeThoughtMax {
		// Keep the tail; cut on a rune boundary so the prompt stays valid UTF-8.
		thought = "…" + strings.ToValidUTF8(thought[len(thought)-nudgeThoughtMax:], "")
	}
	return "You worked through this reply but stopped before writing it. Your reasoning was:\n\n" +
		thought +
		"\n\nWrite the final answer now, directly and in full. Do not think further, do not mention this message, and do not restate the reasoning - just give the answer."
}

// --- fabricated videos ------------------------------------------------------
//
// Models invent YouTube videos. Asked "find me a video about X" they will write
// a plausible title, a plausible channel and a syntactically valid 11-character
// watch URL — all of it made up — instead of calling a tool. It is the single
// most confident failure mode this tool set has: the answer LOOKS like tool
// output, and the link even resolves to *something* (YouTube shows "video
// unavailable" for an unused id, not an error the user reads as fabrication).
//
// The system prompt alone does not fix it, because a model that never calls the
// tool never sees a result contradicting itself. So there are two mechanical
// guards, in runLoop:
//
//  1. pastedNewVideo + tool_choice:"required" — when the user pastes a video link
//     the model has not seen before, the first round MUST call a tool. This is
//     the one case where forcing is provably right: the model cannot know that
//     video's contents, so there is nothing to answer from and nothing to ask
//     about. Any looser trigger (the word "youtube" anywhere) takes away the
//     model's option to answer, or to ask which video is meant, on questions that
//     never needed a lookup.
//  2. unverifiedYtIDs — if a turn nonetheless ends with video links that came
//     from neither the conversation nor a tool result, the answer is marked. The
//     content already streamed to the browser and deltas are append-only, so the
//     marker is appended rather than the links rewritten; a silent fake is far
//     worse than a visible correction.
//
// Guard 2 is the one that carries the weight: it is a check on output, not a
// guess at intent, so it cannot derail a legitimate answer. A third guard used to
// live here — a regex that spotted "let me fetch those now." with no tool call
// and nudged the model to follow through — and was deleted. Detecting an
// abandoned promise means pattern-matching English prose, and every false
// positive cost a whole round of the model disputing the nudge instead of
// answering. An un-made call leaves a short answer; only guard 2 catches the
// failure that actually misleads.

// ytLinkRe matches a YouTube video link in prose. Deliberately narrower than
// parseYouTubeID: this runs over model output, where a bare 11-char word is
// usually just a word.
var ytLinkRe = regexp.MustCompile(`(?i)https?://(?:www\.|m\.|music\.)?(?:youtube\.com/(?:watch\?(?:[^\s"'<)]*&)?v=|shorts/|embed/|live/|v/)|youtu\.be/)([A-Za-z0-9_-]{11})`)

// ytLinkIDs returns every YouTube video id linked in a string, deduped.
func ytLinkIDs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range ytLinkRe.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// unverifiedYtIDs returns the video ids in an answer that were never seen in the
// conversation or in a tool result this turn — i.e. the ones the model made up.
func unverifiedYtIDs(answer string, seen map[string]bool) []string {
	var bad []string
	for _, id := range ytLinkIDs(answer) {
		if !seen[id] {
			bad = append(bad, id)
		}
	}
	return bad
}

// unverifiedVideoMarker is appended to an answer carrying invented links. It
// addresses the user, not the model: by this point the fabricated text has
// already been streamed and persisted, and the only remaining honest move is to
// say it is not trustworthy.
const unverifiedVideoMarker = "\n\n---\n\n⚠️ **Unverified:** the video link(s) above were not looked up with the YouTube tools, so the videos may not exist. Ask again and they will be searched for properly."

// pastedNewVideo reports whether the final user message links a video that
// appears nowhere earlier in the conversation. This is the trigger for forcing a
// tool call, and it is deliberately the narrowest possible one: a link the model
// has never seen is a question it cannot answer from memory and cannot sensibly
// ask a clarifying question about, so "you must call a tool" is always right.
//
// Naming YouTube is NOT enough. "why does youtube compress so hard?" wants an
// answer, not a lookup, and "what did he say about X?" after a transcript was
// already read wants the transcript, not a second fetch — forcing a call in
// either case takes away the only correct move the model had.
func pastedNewVideo(msgs []json.RawMessage) bool {
	i := lastUserIdx(msgs)
	if i < 0 {
		return false
	}
	seen := map[string]bool{}
	for _, m := range msgs[:i] {
		for _, id := range ytLinkIDs(string(m)) {
			seen[id] = true
		}
	}
	return len(unverifiedYtIDs(msgText(msgs[i]), seen)) > 0
}

// lastUserIdx is the index of the final user message, or -1.
func lastUserIdx(msgs []json.RawMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		var m struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(msgs[i], &m) == nil && m.Role == "user" {
			return i
		}
	}
	return -1
}

// msgText pulls the text out of one message. Content is either a string or an
// array of parts (vision attachments), and only the text parts matter here.
func msgText(raw json.RawMessage) string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text + " ")
			}
		}
		return b.String()
	}
	return ""
}

// lastUserText is the text of the final user message.
func lastUserText(msgs []json.RawMessage) string {
	i := lastUserIdx(msgs)
	if i < 0 {
		return ""
	}
	return msgText(msgs[i])
}

// Phantom citations. The citation directive lives in the system prompt whenever
// a citing tool is AVAILABLE (wiki/youtube always are) — it can't be added per
// turn without breaking the byte-stable prefix the KV cache depends on — so a
// model in a conversation that searched nothing is still being told how to cite,
// and some will emit "[1]" with no source behind it. The renderer already leaves
// an unresolvable marker as literal text, which is exactly the confusing artifact
// the user sees. So drop it from the answer instead, before it is persisted.
var citeMarkerRe = regexp.MustCompile(`[ \t]*\[(\d{1,3})\]`)

// stripPhantomCites removes bracketed citation markers whose number is not in
// this turn's citation registry. Real citations are untouched. Fenced and inline
// code are skipped verbatim — "arr[0]" and friends are not citations.
func stripPhantomCites(s string, cites []turnCitation) string {
	if !strings.Contains(s, "[") {
		return s
	}
	valid := make(map[string]bool, len(cites))
	for _, c := range cites {
		valid[strconv.Itoa(c.N)] = true
	}
	var b strings.Builder
	b.Grow(len(s))
	// Split on code spans/fences: odd segments are code and pass through as-is.
	for i, seg := range splitCode(s) {
		if i%2 == 1 {
			b.WriteString(seg)
			continue
		}
		b.WriteString(citeMarkerRe.ReplaceAllStringFunc(seg, func(m string) string {
			if valid[citeMarkerRe.FindStringSubmatch(m)[1]] {
				return m
			}
			return ""
		}))
	}
	return b.String()
}

// splitCode cuts text on ``` fences and ` inline spans, alternating
// prose/code/prose/code… (even indexes are prose). An unclosed opener runs to
// the end of the string and is treated as code — safer than rewriting inside
// what is probably a code block still being streamed.
func splitCode(s string) []string {
	out := []string{}
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			return append(out, s)
		}
		delim := "`"
		if strings.HasPrefix(s[i:], "```") {
			delim = "```"
		}
		out = append(out, s[:i])
		rest := s[i+len(delim):]
		j := strings.Index(rest, delim)
		if j < 0 {
			return append(out, s[i:]) // unclosed — rest of the string is code
		}
		out = append(out, s[i:i+len(delim)+j+len(delim)])
		s = rest[j+len(delim):]
	}
}

// answerOnly strips reasoning markup of every flavour, leaving the answer text.
// Port of the inline answerOnly() in ChatInterface.svelte.
func answerOnly(s string) string {
	s = harmonyToThink(s)
	s = thinkClosedRe.ReplaceAllString(s, "")
	s = thinkOpenRe.ReplaceAllString(s, "")
	return strings.TrimLeft(s, " \t\r\n")
}
