package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Oversize-message digest: a playground message too long for the model's
// context is read in parts, and the model answers from notes taken on each part.
//
// The existing compaction (chatCompact.ts) folds OLD turns and always keeps the
// newest messages verbatim, so one pasted wall of text, which IS the newest
// message, is exactly what it cannot shrink. This runs before the first round of
// a turn whose prompt would pass the client's compaction threshold (DigestAt,
// the same 0.8 of the window), and rewrites only that message.
//
// Shape, and why:
//   - Task first. One cheap call on the message's beginning and end (where a
//     request almost always sits) names what the user wants, so every part is
//     read FOR that request. A blind summary keeps the gist and drops the one
//     line the question was about. Each part's prompt also asks the model to
//     copy out any request it finds, which catches one buried in the middle.
//   - Map, then reduce: every part is read independently into notes, and only if
//     the notes together overflow are they merged (at most digestMaxReduce
//     times). A rolling "summary so far + next part" would re-summarize part 1
//     once per part; here nothing is summarized more than a couple of times.
//   - The message's end stays verbatim. It is the likeliest place for the
//     question, and paraphrasing the question is the worst loss possible.
//   - Persisted on the user message (`digest`, `digestInfo`), so later turns
//     send the digest instead of re-reading, and send it byte-for-byte, which
//     keeps the KV prefix reusable.
//
// Token counts are estimated from bytes, calibrated by the probe call's measured
// prompt size (or, after an overflow, by llama-server's own n_prompt_tokens).
// Chunks are sized well under the window so a miscalibrated ratio still fits,
// and a part that overflows anyway is split in half and retried.

const (
	// Starting bytes-per-token guess before any measurement: between code (~3)
	// and English prose (~4.2), so the first estimate errs toward "too big".
	digestBytesPerToken = 3.5
	// A part may claim this share of the window. The rest is the instructions,
	// the note being written, and slack for a ratio that is off.
	digestChunkShare = 0.45
	// The verbatim tail: at most this many tokens, and at most a fifth of the
	// room the message has.
	digestTailMaxTokens = 4000
	// How much of the beginning and of the end the task probe sees.
	digestProbeTokens = 2000
	// Per-part note size floor. Below it the notes say nothing, so many parts
	// are reduced in a second pass instead of starved.
	digestMinNoteTokens = 256
	// A note's ceiling is its part's size over this. The window's free room is
	// what sizes a note; this only stops a few parts in a big window from
	// asking for notes that are nearly a copy, whose decode (~45 t/s on a 27B)
	// costs more than the detail is worth. It replaced a flat 3000-token cap
	// that, on a 131k window, used 12k of ~96k tokens of note room and cut
	// every note of a 4-part book short: the answer named the missing chapters.
	digestMinCompression = 6
	// The digest's own framing (the explanatory header, tags).
	digestFrameTokens = 400
	// Hard stops: a message needing more parts than this is refused with a
	// number, not read for an hour; merging more than this many times is
	// compounding loss, not reading.
	digestMaxParts  = 64
	digestMaxReduce = 3
	digestMaxSplits = 8
)

var (
	taskTagRe       = regexp.MustCompile(`(?is)<task>(.*?)</task>`)
	nPromptTokensRe = regexp.MustCompile(`"n_prompt_tokens"\s*:\s*(\d+)`)
)

// isContextOverflow reports llama-server's "prompt longer than the window"
// rejection. It arrives before any prefill, so detecting it costs nothing.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "exceed_context_size") || strings.Contains(s, "exceeds the available context")
}

// overflowPromptTokens is the exact prompt size llama-server measured when it
// refused, or 0 when the error does not carry it.
func overflowPromptTokens(err error) int {
	if err == nil {
		return 0
	}
	if m := nPromptTokensRe.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// digestInfo is what the UI is told about a digested message.
type digestInfo struct {
	Parts  int    `json:"parts"`
	Tokens int    `json:"tokens"` // estimated size of the original message
	Task   string `json:"task,omitempty"`
}

// digestOversize rewrites the last user message of start.Messages into a digest
// when the prompt would pass DigestAt of the window. force skips the "does it
// fit" check (the model already refused the prompt); measured is the refusal's
// exact prompt size, 0 if unknown. Returns whether it rewrote anything.
func (tm *turnManager) digestOversize(ctx context.Context, user string, at *activeTurn, start *turnStart, force bool, measured int) (bool, error) {
	if start.DigestAt <= 0 || start.ctxTokens <= 0 {
		return false, nil
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(start.Messages, &msgs); err != nil {
		return false, nil
	}
	ui := lastUserIndex(msgs)
	if ui < 0 {
		return false, nil
	}
	var um map[string]any
	if json.Unmarshal(msgs[ui], &um) != nil {
		return false, nil
	}
	text, media := splitContent(um["content"])
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	otherBytes, mediaTok := len(start.Tools), mediaTokens(media)
	for i, m := range msgs {
		if i == ui {
			continue
		}
		b, t := messageWeight(m)
		otherBytes += b
		mediaTok += t
	}

	ratio := digestBytesPerToken
	if measured > mediaTok {
		ratio = clampRatio(float64(len(text)+otherBytes) / float64(measured-mediaTok))
	}
	tok := func(b int) int { return int(math.Ceil(float64(b) / ratio)) }
	budget := int(float64(start.ctxTokens) * start.DigestAt)
	if !force && tok(len(text)+otherBytes)+mediaTok <= budget {
		return false, nil
	}

	// The task probe: beginning + end of the message.
	at.setBusy("Reading a long message")
	probeBytes := int(digestProbeTokens * ratio)
	head := text[:cutAt(text, probeBytes)]
	tailFrom := tailCut(text, probeBytes)
	if tailFrom < len(head) {
		tailFrom = len(head)
	}
	probe := "<beginning>\n" + head + "\n</beginning>\n\n[... about " + fmtK(tok(tailFrom-len(head))) +
		" tokens omitted ...]\n\n<end>\n" + text[tailFrom:] + "\n</end>"
	out, promptTok, err := tm.digestComplete(ctx, at, *start, digestTaskSys, probe, 512)
	if err != nil {
		return false, fmt.Errorf("reading the long message: %w", err)
	}
	if measured <= 0 && promptTok > 0 {
		ratio = clampRatio(float64(len(digestTaskSys)+len(probe)) / float64(promptTok))
		if !force && tok(len(text)+otherBytes)+mediaTok <= budget {
			at.setBusy("")
			return false, nil // the first guess was pessimistic: it fits
		}
	}
	task := firstTask(out)

	available := budget - tok(otherBytes) - mediaTok - digestFrameTokens
	if available < max(1024, budget/8) {
		return false, errors.New("the conversation before this message already fills most of the context window; compact it (type /compact) or start a new chat, then send the message again")
	}

	tailTok := min(digestTailMaxTokens, available/5)
	tailStart := tailCut(text, int(float64(tailTok)*ratio))
	tail, body := text[tailStart:], text[:tailStart]
	notesBudget := available - tok(len(tail)) - tok(len(task))

	sys := digestPartSys(task)
	// One call holds a part (digestChunkShare of the window) and its note, which
	// noteMaxTokens keeps under a quarter of the part: both fit with room.
	chunkTok := int(float64(start.ctxTokens)*digestChunkShare) - tok(len(sys))
	if chunkTok < 1024 {
		return false, errors.New("this model's context window is too small to read the message in parts")
	}
	parts := splitChunks(body, int(float64(chunkTok)*ratio))
	if len(parts) > digestMaxParts {
		return false, fmt.Errorf("the message is about %s tokens: reading it would take %d parts, over the limit of %d. Use a model with a larger context, or send less text", fmtK(tok(len(text))), len(parts), digestMaxParts)
	}
	noteCap := chunkTok / digestMinCompression
	perNote := noteTarget(notesBudget, len(parts), noteCap)

	notes := make([]string, 0, len(parts))
	var found []string
	splits := 0
	for i := 0; i < len(parts); i++ {
		at.setBusy(fmt.Sprintf("Reading part %d of %d", i+1, len(parts)))
		out, _, err := tm.digestComplete(ctx, at, *start, sys, fmt.Sprintf("<part n=\"%d\" of=\"%d\">\n%s\n</part>\n\n%s", i+1, len(parts), parts[i], noteLength(perNote)), noteMaxTokens(perNote))
		if err != nil {
			if isContextOverflow(err) && splits < digestMaxSplits && len(parts[i]) > 4096 {
				// The ratio was off for this stretch (code, CJK): halve it and retry.
				cut := cutAt(parts[i], len(parts[i])/2)
				a, b := parts[i][:cut], parts[i][cut:]
				parts = append(parts[:i], append([]string{a, b}, parts[i+1:]...)...)
				splits++
				i--
				continue
			}
			return false, fmt.Errorf("reading part %d of %d: %w", i+1, len(parts), err)
		}
		ts, note := pullTasks(out)
		found = append(found, ts...)
		notes = append(notes, note)
	}
	if task == "" {
		task = strings.Join(dedupe(found), "\n")
	}

	// Reduce: merge neighbouring notes until they fit.
	labelled := false
	for round := 0; tok(totalLen(notes)) > notesBudget; round++ {
		if round == digestMaxReduce {
			return false, fmt.Errorf("the notes on this message (about %s tokens) still do not fit the context window after %d merges; use a model with a larger context, or send less text", fmtK(tok(totalLen(notes))), digestMaxReduce)
		}
		groups := groupNotes(notes, int(float64(chunkTok)*ratio))
		target := noteTarget(notesBudget, len(groups), noteCap)
		rsys := digestMergeSys(task)
		merged := make([]string, 0, len(groups))
		for gi, g := range groups {
			at.setBusy(fmt.Sprintf("Merging notes %d of %d", gi+1, len(groups)))
			out, _, err := tm.digestComplete(ctx, at, *start, rsys, g+"\n\n"+noteLength(target), noteMaxTokens(target))
			if err != nil {
				return false, fmt.Errorf("merging notes: %w", err)
			}
			_, note := pullTasks(out)
			merged = append(merged, note)
		}
		if len(merged) >= len(notes) {
			return false, errors.New("the notes on this message do not fit the context window")
		}
		notes = merged
		labelled = true
	}

	digest := buildDigest(tok(len(text)), start.ctxTokens, len(parts), task, notes, labelled, tail)
	um["content"] = joinContent(digest, media)
	msgs[ui] = mustJSON(um)
	start.Messages = mustJSON(msgs)
	info := digestInfo{Parts: len(parts), Tokens: tok(len(text)), Task: task}
	tm.persistDigest(user, at.chatID, digest, info)
	at.setBusy("")
	tm.log.Infof("turn: digested a %s-token message on %s into %d parts (%s tokens of notes)", fmtK(info.Tokens), start.Model, len(parts), fmtK(tok(len(digest))))
	return true, nil
}

// digestComplete is one non-streamed, thinking-off call to the turn's model,
// under the chat's digest conversation id so the parts share a KV prefix
// (identical system message) without displacing the chat's own slot entry.
// Returns the answer and the measured prompt size.
func (tm *turnManager) digestComplete(ctx context.Context, at *activeTurn, start turnStart, sys, user string, maxTokens int) (string, int, error) {
	body := map[string]any{
		"model": start.Model,
		"messages": []map[string]string{
			{"role": "system", "content": sys},
			{"role": "user", "content": user},
		},
		"stream":               false,
		"max_tokens":           maxTokens,
		"temperature":          0.2,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	req, err := tm.selfCompletionRequest(ctx, body, start.ChatID+"#digest", at.authKey)
	if err != nil {
		return "", 0, err
	}
	// Hold the model between parts: without tools selfCompletionRequest asks
	// for no hold, and a swap between part 3 and part 4 reloads the model.
	req.Header.Set("X-QM-Hold-Ms", strconv.Itoa(playgroundHoldMs))
	resp, err := tm.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := readLimited(resp.Body, 1<<12)
		return "", 0, fmt.Errorf("upstream %s: %s", resp.Status, snippet)
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", 0, err
	}
	if len(r.Choices) == 0 {
		return "", r.Usage.PromptTokens, errors.New("empty completion")
	}
	if r.Choices[0].FinishReason == "length" {
		// The note ran into max_tokens and lost its end. max_tokens is half
		// again the target noteLength asks for, so the model ignored it badly.
		tm.log.Warnf("turn: a digest note on %s hit its %d-token cap and was cut short", start.Model, maxTokens)
	}
	return strings.TrimSpace(answerOnly(r.Choices[0].Message.Content)), r.Usage.PromptTokens, nil
}

// persistDigest stores the digest on the session's user message, so the client
// sends it instead of the original on every later turn.
func (tm *turnManager) persistDigest(user, chatID, digest string, info digestInfo) {
	tm.pg.mu.Lock()
	defer tm.pg.mu.Unlock()
	arr := tm.pg.readChatsLocked(user)
	sess := findSession(arr, chatID)
	if sess == nil {
		return
	}
	msgs, _ := sess["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "user" {
			m["digest"] = digest
			m["digestInfo"] = info
			tm.pg.writeChatsLocked(user, arr)
			return
		}
	}
}

// --- prompts ---------------------------------------------------------------

const digestTaskSys = "You are a preprocessing step for an assistant. You are shown the BEGINNING and the END of a very long message a user sent; the middle is omitted. " +
	"Identify what the user wants the assistant to do with the message: a question, an instruction or a request, including any constraints on the form of the answer. " +
	"Reply with exactly one line: <task>the request, quoted or closely restated</task>. " +
	"If the visible parts contain no request (the message is only material), reply <task>NONE</task>."

func digestPartSys(task string) string {
	lead := "You are reading one part of a long message a user sent to an assistant. The message is too long to read at once, so it is read in parts and notes are taken on each part. The assistant will answer from the notes alone and will never see this part again.\n\n"
	if task != "" {
		return lead + "The user's request: " + task + "\n\n" +
			"Write notes on this part that contain everything the assistant needs to answer that request: facts, names, numbers, dates, definitions, claims, errors and code. " +
			"Quote exact wording where it matters (code, error messages, figures, anything that may be cited). Leave out what does not bear on the request. " +
			"If nothing in this part is relevant, write: Nothing relevant.\n" +
			"If this part itself contains a request or instruction addressed to the assistant, copy it exactly on its own line as <task>...</task>.\n" +
			"Write only the notes."
	}
	return lead + "The user's request is not known. " +
		"Write dense notes that preserve this part's content in order: every fact, name, number, date, claim, error and piece of code. " +
		"Quote exact wording where it matters (code, error messages, figures). Drop only repetition and filler.\n" +
		"If this part contains a request, question or instruction addressed to the assistant, copy it exactly on its own line as <task>...</task>.\n" +
		"Write only the notes."
}

// noteLength is the length target for one note. It goes AFTER the text the note
// is about, where the model reads it last, and not in the system prompt, which
// stays byte-identical across parts. With no stated target the model writes
// until max_tokens and the note loses its end. The target is ~0.6 words per
// token of cap, so a note that meets it finishes under the cap.
func noteLength(maxTokens int) string {
	return fmt.Sprintf("Keep the notes under about %d words.", max(100, maxTokens*6/10))
}

// noteTarget is the length each of n notes is asked for. Two thirds of an even
// share of the room, so that notes which all run to noteMaxTokens still fit it
// together and an overrun never forces a merge pass, which costs detail.
func noteTarget(room, n, ceiling int) int {
	return clampInt(room*2/3/max(1, n), digestMinNoteTokens, ceiling)
}

// noteMaxTokens is the hard stop for a note aimed at target tokens. Models
// overrun a stated length (the 27B overran its target on every part of a
// book), and a note cut at max_tokens loses its END, which is the end of the
// part it covers: whole chapters went missing that way. With headroom the note
// finishes instead.
func noteMaxTokens(target int) int { return target + target/2 }

func digestMergeSys(task string) string {
	s := "You are merging notes taken on consecutive parts of a long message a user sent to an assistant. The assistant will answer from the merged notes alone. "
	if task != "" {
		s += "The user's request: " + task + "\n\nKeep everything that bears on that request, "
	} else {
		s += "Keep every fact, "
	}
	return s + "with exact quotes, names and numbers intact; remove repetition. Write only the merged notes."
}

func buildDigest(tokens, ctxTokens, parts int, task string, notes []string, merged bool, tail string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Quartermaster: the user's message was about %s tokens, longer than this model's %s-token context allows. It was read in %d parts and the notes below were taken on it. You cannot see the original text: answer from the notes and the verbatim ending. If the notes do not cover something the answer needs, say so rather than guess.]\n\n",
		fmtK(tokens), fmtK(ctxTokens), parts)
	if task != "" {
		b.WriteString("Request found in the message: " + task + "\n\n")
	} else {
		b.WriteString("No explicit request was found in the message; the notes record its content.\n\n")
	}
	for i, n := range notes {
		if merged {
			fmt.Fprintf(&b, "<notes section=\"%d\" of=\"%d\">\n%s\n</notes>\n\n", i+1, len(notes), n)
		} else {
			fmt.Fprintf(&b, "<notes part=\"%d\" of=\"%d\">\n%s\n</notes>\n\n", i+1, len(notes), n)
		}
	}
	b.WriteString("The message ended with this text, verbatim:\n<end_of_message>\n" + tail + "\n</end_of_message>")
	return b.String()
}

// --- text helpers (pure) -----------------------------------------------------

// lastUserIndex is the index of the last user message, -1 if none.
func lastUserIndex(msgs []json.RawMessage) int {
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

// splitContent separates a message's text from its media parts. A string
// content is all text; an array joins its text parts and keeps the rest.
func splitContent(c any) (string, []any) {
	switch v := c.(type) {
	case string:
		return v, nil
	case []any:
		var texts []string
		var media []any
		for _, p := range v {
			pm, ok := p.(map[string]any)
			if ok && pm["type"] == "text" {
				if s, _ := pm["text"].(string); s != "" {
					texts = append(texts, s)
				}
				continue
			}
			media = append(media, p)
		}
		return strings.Join(texts, "\n\n"), media
	}
	return "", nil
}

// joinContent is splitContent's inverse for a rewritten text: a plain string
// when there was no media, else one text part followed by the media parts
// (the same shape the client rebuilds from the stored digest).
func joinContent(text string, media []any) any {
	if len(media) == 0 {
		return text
	}
	return append([]any{map[string]any{"type": "text", "text": text}}, media...)
}

// mediaTokens is a flat per-part allowance: an inlined image is megabytes of
// base64 that the model sees as a few hundred to a few thousand tokens, so its
// bytes must never be counted as text.
func mediaTokens(media []any) int { return 1024 * len(media) }

// messageWeight is a message's text bytes and media token allowance.
func messageWeight(raw json.RawMessage) (int, int) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return len(raw), 0
	}
	text, media := splitContent(m["content"])
	n := len(text)
	if tc, ok := m["tool_calls"]; ok {
		b, _ := json.Marshal(tc)
		n += len(b)
	}
	if rc, _ := m["reasoning_content"].(string); rc != "" {
		n += len(rc)
	}
	return n + 16, mediaTokens(media) // +16: the template's per-message framing
}

func clampRatio(r float64) float64 {
	if math.IsNaN(r) || r < 1 {
		return 1
	}
	return math.Min(r, 6)
}

func clampInt(v, lo, hi int) int { return max(lo, min(v, hi)) }

// cutAt picks where to end a piece of s no longer than limit bytes: the last
// paragraph break in the final 40% of the window, else the last line break,
// sentence end or space there, else limit itself (backed off to a rune
// boundary). Never splits a word when a space is anywhere near.
func cutAt(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	if limit < 1 {
		limit = 1
	}
	floor := limit * 6 / 10
	win := s[floor:limit]
	for _, sep := range []string{"\n\n", "\n", ". ", " "} {
		if i := strings.LastIndex(win, sep); i >= 0 {
			return floor + i + len(sep)
		}
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return limit
}

// tailCut is where a verbatim tail of at most size bytes starts: the first
// paragraph break in the earliest 40% of the tail window, else a line break,
// sentence end or space, so the tail opens on a whole word.
func tailCut(s string, size int) int {
	if size >= len(s) {
		return 0
	}
	start := len(s) - size
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	ceil := start + size*4/10
	if ceil > len(s) {
		ceil = len(s)
	}
	win := s[start:ceil]
	for _, sep := range []string{"\n\n", "\n", ". ", " "} {
		if i := strings.Index(win, sep); i >= 0 {
			return start + i + len(sep)
		}
	}
	return start
}

// splitChunks cuts s into consecutive pieces of at most size bytes on natural
// boundaries (see cutAt). Concatenated, the pieces are s exactly.
func splitChunks(s string, size int) []string {
	if size < 1 {
		size = 1
	}
	var out []string
	for len(s) > 0 {
		cut := cutAt(s, size)
		if cut <= 0 { // size under one rune: take the whole rune
			_, cut = utf8.DecodeRuneInString(s)
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

// pullTasks splits a part's output into the requests it copied out and the
// notes that remain.
func pullTasks(out string) ([]string, string) {
	var tasks []string
	for _, m := range taskTagRe.FindAllStringSubmatch(out, -1) {
		if t := strings.TrimSpace(m[1]); t != "" && !strings.EqualFold(t, "none") {
			tasks = append(tasks, t)
		}
	}
	return tasks, strings.TrimSpace(taskTagRe.ReplaceAllString(out, ""))
}

// firstTask is the probe's answer: the first real <task>, "" for NONE or none.
func firstTask(out string) string {
	if ts, _ := pullTasks(out); len(ts) > 0 {
		return ts[0]
	}
	return ""
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		k := strings.ToLower(strings.Join(strings.Fields(s), " "))
		if !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}

func totalLen(ss []string) int {
	n := 0
	for _, s := range ss {
		n += len(s)
	}
	return n
}

// groupNotes packs consecutive notes into groups of at most size bytes (a
// single note larger than size is a group of its own), each ready to merge.
func groupNotes(notes []string, size int) []string {
	var out []string
	var cur strings.Builder
	for _, n := range notes {
		if cur.Len() > 0 && cur.Len()+len(n)+2 > size {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(n)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// fmtK renders a token count as "182k" / "950".
func fmtK(n int) string {
	if n >= 1000 {
		return strconv.Itoa((n+500)/1000) + "k"
	}
	return strconv.Itoa(n)
}
