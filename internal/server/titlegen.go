package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// Title generation for collapsed reasoning boxes ("Thought for 2s · <gist>").
//
// This is FLAN-T5-small (80M, ~83 MB Q8_0), not a chat LLM, and it runs
// **exec-per-request on the CPU** — no scheduler entry, no VRAM accounting, no
// group eviction. That matters: routing a title through the loaded chat model
// would either swap a model in or contend with the user's own generation for the
// slot, to produce six words of chrome. This one answers in well under a second
// on CPU and costs nothing that was going to be used for inference.
//
// Model choice is deliberate. A summarization/headline fine-tune (t5-small or
// t5-base on article→title) is *extractive* in practice: it emits the input's
// own opening clause, which reads fine for prose but is worthless for a user
// request. FLAN's instruction tuning plus the few-shot prompt below (titlegenShots)
// gives an abstractive phrase instead. See assets/README.md.
//
// It must run under `llama-completion`, NOT `llama-server`: llama.cpp's server
// has no encoder-decoder path (it never calls llama_encode), so a T5 gguf either
// 400s or asserts. Hence the CLI shell-out rather than a normal backend entry.

// titlegenMu serializes title runs. Each run is its own short-lived process, and
// a turn asks for one title per reasoning span; letting a burst of them run
// concurrently would spawn N CPU-saturating processes while the user's real
// generation is still streaming.
var titlegenMu sync.Mutex

// titlegenTimeout caps one title run. A cold-cache CPU run of an 80M model is
// well under a second; a second and a half is a hang, and a title is never worth
// blocking a turn's persistence on.
const titlegenTimeout = 4 * time.Second

const (
	titlegenMaxInput  = 1200 // chars of reasoning fed to the model (T5 ctx is small)
	titlegenMaxOutput = 60   // chars of title kept
	titlegenMaxTokens = 24
)

// titlegen is a resolved (exe, model) pair. Resolve once per turn and reuse for
// every span so a multi-span turn doesn't re-read the sidecar per title.
type titlegen struct {
	exe   string
	model string
}

// titlegenExeNames are tried in order in the llama backend's own directory.
// llama-completion is the current name of the encoder-decoder-capable CLI;
// llama-cli is the older build that still accepts T5 on some versions.
var titlegenExeNames = []string{"llama-completion", "llama-cli"}

// resolveTitlegen locates the title model and a CLI that can run it. Returns a
// nil runner with a nil error when titling is simply not configured — the caller
// treats that as "no titles", not as a failure.
func resolveTitlegen(generatePath string) (*titlegen, error) {
	if strings.TrimSpace(generatePath) == "" {
		return nil, nil // not running from a generate control file: no sidecar to read
	}
	model := titlegenModelPath(generatePath)
	if model == "" {
		return nil, nil
	}
	entries, err := autogen.LoadSidecarBackendList(generatePath)
	if err != nil {
		return nil, fmt.Errorf("load backends: %w", err)
	}
	server := pickLlamaExe(entries)
	if server == "" {
		return nil, nil
	}
	exe := siblingExe(server, titlegenExeNames)
	if exe == "" {
		return nil, fmt.Errorf("no llama-completion binary beside %s (T5 title models cannot run under llama-server)", filepath.Base(server))
	}
	return &titlegen{exe: exe, model: model}, nil
}

// pickLlamaExe returns the ★default llama-kind backend path, else the first one.
// Mirrors deriveBackendExes' kind aliases so a hand-edited sidecar still resolves.
func pickLlamaExe(entries []autogen.BackendEntry) string {
	var first string
	for _, e := range entries {
		switch strings.ToLower(strings.TrimSpace(e.Kind)) {
		case "llama", "llama.cpp", "server":
		default:
			continue
		}
		p := strings.TrimSpace(e.Path)
		if p == "" {
			continue
		}
		if e.Default {
			return p
		}
		if first == "" {
			first = p
		}
	}
	return first
}

// siblingExe looks for one of names next to ref, preserving ref's extension.
func siblingExe(ref string, names []string) string {
	dir := filepath.Dir(ref)
	ext := filepath.Ext(ref)
	for _, n := range names {
		p := filepath.Join(dir, n+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ansiRe strips the CLI's colour codes; llama-completion writes them even when
// stdout is a pipe.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// title runs one title generation. Returns "" (no error) when the model produced
// nothing usable — callers fall back to the UI's local heuristic.
func (tg *titlegen) title(ctx context.Context, text string) (string, error) {
	prompt := titlegenPrompt(text)
	if prompt == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, titlegenTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, tg.exe,
		"-m", tg.model,
		"-p", prompt,
		"-n", fmt.Sprint(titlegenMaxTokens),
		"-ngl", "0", // CPU only: this must never take VRAM from a loaded model
		"--temp", "0",
		// Greedy decoding on a tiny model loops on token-dense input: a span
		// comparing Q4_K_M with Q5_K_M titled itself "Q4_K_M: Q4_K_M: Q5_K_M: Q5_".
		"--repeat-penalty", "1.3",
		"--no-warmup",
		"--simple-io",
	)
	hideConsole(cmd) // no CLI window popup (Windows)

	// stdout ONLY: the CLI writes its load/sampler/perf log to stderr, timestamped
	// (`0.00.184.451 I sampler seed: …`), and merging the two streams would leave
	// the first line of "output" a log line rather than the title.
	titlegenMu.Lock()
	out, err := cmd.Output()
	titlegenMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(tg.exe), err)
	}
	return sanitizeTitle(cleanTitlegenOutput(string(out), prompt), text), nil
}

// titlegenRefusalRe matches the assistant-voice boilerplate an 80M instruction
// tuned model falls back to when the few-shot shape doesn't catch: "I'm sorry, I
// can't", "As an AI language model", "I don't know". It is answering the text as
// if it were a question instead of naming it, and the result is a reasoning box
// titled "I'm sorry" over a trace about quant sizes.
var titlegenRefusalRe = regexp.MustCompile(`(?i)^(i(['’]m| am)? ?(so+rry|apolog)|sorry\b|i (can(['’]?t| ?not)|don['’]?t|do not|won['’]?t|am unable)|as an ai|unfortunately\b|there (is|are) no\b)`)

// titlegenApologyRe finds a genuine apology in the SOURCE. A trace that really is
// about apologizing may legitimately title itself that way, so the refusal filter
// only fires when the source has nothing of the kind.
var titlegenApologyRe = regexp.MustCompile(`(?i)(sorry|apolog|refus)`)

// sanitizeTitle drops a title that answers the text rather than naming it.
// Returning "" is the documented "no title" signal: the UI keeps its own local
// heuristic, which is always better than a wrong apology.
func sanitizeTitle(title, source string) string {
	if title == "" || !titlegenRefusalRe.MatchString(title) {
		return title
	}
	if titlegenApologyRe.MatchString(source) {
		return title // the source really is about an apology/refusal
	}
	return ""
}

// titlegenShots is the few-shot prompt, shared by both callers. The shots are
// what make this abstractive at all: a summarization-tuned T5 fed
// "summarize: <text>" emits the text's own lead clause verbatim — fine for prose,
// useless for a request ("How do I stop my llama-server from spilling into shared
// VRAM on windows when I load" is not a title). FLAN is instruction-tuned, so the
// same input under demonstrated examples produces an abstractive phrase. Both
// shots deliberately drop incidental detail so the model generalizes "name the
// thing" rather than "shorten the sentence".
//
// One prompt, not one per caller. Reasoning-specific shots (traces instead of
// questions) and activity phrasing ("Comparing prices against the invoice", cue
// "Doing:", gerund examples) were both tried and both measured WORSE on real
// spans at this model size — tail-copying and degeneration ("The docs page is a
// docs page.", "I should be able to get them to a dry brine."). An 80M model
// follows the demonstrated shape it was already good at; steering it toward a
// different one costs more than it buys. The activity verb belongs in the UI,
// which already knows whether the turn searched or read pages.
const titlegenShots = `Give a short descriptive title for the text, 4 to 6 words.

Text: I keep getting a 403 from the GitHub API in my CI job but it works locally.
Title: Debugging GitHub API 403 in CI

Text: Can you explain how sourdough starter fermentation works and why mine smells like acetone?
Title: Sourdough starter fermentation and smell

Text: `

// titlegenPrompt builds the task prompt. The head of a reasoning trace is what
// the title is about; the tail is usually the conclusion restated, and the
// model's context is small, so cap at the front rather than sampling.
func titlegenPrompt(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if t == "" {
		return ""
	}
	if len(t) > titlegenMaxInput {
		t = t[:titlegenMaxInput]
		if i := strings.LastIndexByte(t, ' '); i > titlegenMaxInput/2 {
			t = t[:i]
		}
	}
	return titlegenShots + t + "\nTitle:"
}

// cleanTitlegenOutput extracts the title from the CLI's stdout: strip ANSI, drop
// the end-of-stream marker and any echoed prompt, keep the first non-empty line,
// then trim to one short clause.
func cleanTitlegenOutput(out, prompt string) string {
	s := ansiRe.ReplaceAllString(out, "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "[end of text]", "")
	s = strings.ReplaceAll(s, prompt, "")
	var line string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		// The CLI interleaves its own load/timing chatter with the completion.
		if l == "" || strings.HasPrefix(l, "llama_") || strings.HasPrefix(l, "ggml_") ||
			strings.HasPrefix(l, "build:") || strings.HasPrefix(l, "main:") ||
			strings.HasPrefix(l, "load:") || strings.HasPrefix(l, "print_info:") ||
			strings.HasPrefix(l, "init:") || strings.HasPrefix(l, "sampler") {
			continue
		}
		line = l
		break
	}
	return trimTitle(line)
}

// trimTitle normalizes a raw model title: single line, no surrounding quotes or
// trailing punctuation, capped at titlegenMaxOutput on a word boundary.
func trimTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'“”‘’ ")
	s = strings.TrimRight(s, ".,;:- -")
	if s == "" {
		return ""
	}
	if len(s) > titlegenMaxOutput {
		cut := s[:titlegenMaxOutput]
		if i := strings.LastIndexByte(cut, ' '); i > titlegenMaxOutput/2 {
			cut = cut[:i]
		}
		s = strings.TrimRight(cut, ".,;:- -") + "…"
	}
	if len([]rune(s)) < 4 {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// chatTitleRequest is the opening exchange of a conversation. The client sends
// the first user message (and optionally the assistant's reply) rather than the
// whole transcript: the title names what the chat is *about*, which is set by how
// it opened, and the model's context is a few hundred tokens anyway.
type chatTitleRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type chatTitleResponse struct {
	Title string `json:"title"` // "" means "use your own fallback"
}

// handleChatTitle names a conversation with the vendored CPU model. It answers
// 200 with an empty title rather than an error whenever titling is unavailable
// (no generate file, no CLI, bad output) — the client then falls back to the chat
// model, so a missing title model degrades to the old behaviour instead of
// surfacing an error the user can do nothing about.
func (s *Server) handleChatTitle(w http.ResponseWriter, r *http.Request) {
	p := s.playground
	if p == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return
	}
	if user := p.userFromRequest(r); user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	var in chatTitleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var b strings.Builder
	for _, m := range in.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Reasoning is the model talking to itself; it names the deliberation, not
		// the conversation.
		c := strings.TrimSpace(thinkSpanRe.ReplaceAllString(m.Content, ""))
		if c == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(c)
	}

	tg, err := s.resolveChatTitlegen()
	if err != nil || tg == nil {
		if err != nil {
			s.proxylog.Warnf("titlegen: %v", err)
		}
		writeJSON(w, chatTitleResponse{})
		return
	}
	title, err := tg.title(r.Context(), b.String())
	if err != nil {
		s.proxylog.Warnf("titlegen: %v", err)
		writeJSON(w, chatTitleResponse{})
		return
	}
	writeJSON(w, chatTitleResponse{Title: title})
}

// resolveChatTitlegen reaches the title model through the autogen admin, which is
// the Server-side equivalent of Playground.GeneratePath (the turn runner outlives
// any single Server, so it carries its own copy).
func (s *Server) resolveChatTitlegen() (*titlegen, error) {
	if s.autogen == nil {
		return nil, nil
	}
	return resolveTitlegen(s.autogen.GeneratePath)
}

// titlegenMaxSpans caps how many reasoning boxes in one turn get a model title.
// A tool-looping turn can open a dozen spans; each is another process spawn, and
// the later ones are near-identical "check the next source" steps.
const titlegenMaxSpans = 6

// titleJob is one finished reasoning box handed to the turn's title worker.
// idx -1 is the reasoning FIELD trace (at.reasoningTitle); idx >= 0 is the inline
// <think> span with that ordinal (at.thinkTitles[idx]).
type titleJob struct {
	idx  int
	text string
}

// titleQueueDepth bounds the per-turn backlog. The title model is one CPU process
// at a time and a box takes a second or two; if a tool loop closes boxes faster
// than that, the excess is dropped here and picked up by the end-of-turn pass
// rather than queued behind a growing backlog nobody is looking at yet.
const titleQueueDepth = 4

// closeTags are the reasoning close tags thinkSpanRe accepts. Longest first is
// irrelevant here; the longest LENGTH is what sizes the overlap window below.
var closeTags = []string{"</think>", "</thinking>", "</reasoning>"}

// endedThinkSpan reports whether the content delta just completed a reasoning
// close tag, i.e. whether a box the MODEL wrote closed on this token. Scans only
// the delta plus enough preceding bytes to catch a tag split across two deltas —
// never the whole content, so it is free to run on every token.
func endedThinkSpan(content, delta string) bool {
	n := len(delta) + len("</reasoning>")
	if n > len(content) {
		n = len(content)
	}
	tail := strings.ToLower(content[len(content)-n:])
	for _, t := range closeTags {
		if strings.Contains(tail, t) {
			return true
		}
	}
	return false
}

// markTitleSent records that a span ordinal has been handed to the worker, so a
// later sweep doesn't re-title it. Caller holds at.mu.
func (at *activeTurn) markTitleSent(idx int) {
	if at.titleSent == nil {
		at.titleSent = map[int]struct{}{}
	}
	at.titleSent[idx] = struct{}{}
}

// queueTitles titles every reasoning box in content that has CLOSED and isn't
// titled yet. Never a partial box — a title of a half-written thought is wrong by
// the time the thought finishes, and each pass is a CPU process, so re-titling a
// growing box would spend one per pass to be wrong.
//
// It exists for reasoning the MODEL wrote as literal <think> tags in its content
// (harmony, --reasoning-format none): those never pass through closeInline, so
// there is no span-close event for the server to react to. Driven by
// endedThinkSpan on the content delta, so it runs on the close, not on a timer.
//
// Caller holds at.mu.
func (at *activeTurn) queueTitles() {
	if !strings.Contains(at.content, "<think") { // cheap guard: most content has no spans at all
		return
	}
	for i, span := range splitClosedThinkSpans(at.content, titlegenMaxSpans) {
		if _, ok := at.titleSent[i]; ok {
			continue
		}
		if strings.TrimSpace(span) == "" {
			continue
		}
		at.markTitleSent(i)
		at.enqueueTitle(titleJob{idx: i, text: span})
	}
}

// enqueueTitle hands a closed box to the title worker. Caller holds at.mu.
// Non-blocking by design: this runs on the streaming path, and a title is chrome.
func (at *activeTurn) enqueueTitle(j titleJob) {
	if at.titleJobs == nil {
		return
	}
	select {
	case at.titleJobs <- j:
	default:
	}
}

// startTitler spins up the per-turn title worker. Titles are generated as boxes
// CLOSE, not at end of turn: a tool-looping turn runs for minutes, and every box
// it already finished would otherwise sit on the UI's local heuristic for the
// whole run. Only the still-open box is unstable, and that one is never queued.
//
// The worker is deliberately serial (one CPU process at a time, further
// serialized globally by titlegenMu) so mid-turn titling can't turn into a fan of
// llama-completion processes competing with the model that is generating.
func (tm *turnManager) startTitler(ctx context.Context, at *activeTurn) {
	if tm.pg == nil {
		return
	}
	jobs := make(chan titleJob, titleQueueDepth)
	done := make(chan struct{})
	at.mu.Lock()
	at.titleJobs, at.titleDone = jobs, done
	at.mu.Unlock()

	go func() {
		defer close(done)
		var tg *titlegen
		resolved := false
		for j := range jobs {
			if ctx.Err() != nil {
				continue // turn cancelled: drain so senders never block, do no work
			}
			if !resolved {
				resolved = true
				g, err := resolveTitlegen(tm.pg.GeneratePath)
				if err != nil && tm.log != nil {
					tm.log.Warnf("titlegen: %v", err)
				}
				tg = g
			}
			if tg == nil {
				continue
			}
			t, err := tg.title(ctx, j.text)
			if err != nil {
				if tm.log != nil {
					tm.log.Warnf("titlegen: %v", err)
				}
				continue
			}
			if t != "" {
				at.setTitle(j.idx, t)
			}
		}
	}()
}

// stopTitler closes the queue and waits for the in-flight title, so the worker
// can never write into at after the turn has been flushed and finished.
func (tm *turnManager) stopTitler(at *activeTurn) {
	at.mu.Lock()
	jobs, done := at.titleJobs, at.titleDone
	at.titleJobs, at.titleDone = nil, nil
	at.mu.Unlock()
	if jobs == nil {
		return
	}
	close(jobs)
	<-done
}

// setTitle records one box's title and fans the full snapshot. thinkTitles is
// index-addressed (ordinal of the <think> span, same index the UI zips onto its
// tokenized boxes), so it is grown with blanks rather than appended to.
func (at *activeTurn) setTitle(idx int, title string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	if idx < 0 {
		at.reasoningTitle = title
	} else {
		for len(at.thinkTitles) <= idx {
			at.thinkTitles = append(at.thinkTitles, "")
		}
		at.thinkTitles[idx] = title
	}
	at.fanTitles()
}

// fanTitles pushes the current titles to attached viewers. Caller holds at.mu.
// Replace-style: the arrays are the whole state, and a box the model hasn't
// titled yet is a blank the UI falls back to its own heuristic for.
func (at *activeTurn) fanTitles() {
	at.fan(turnDelta{Kind: "titles", Replace: true, Data: mustJSON(map[string]any{
		"reasoningTitle": at.reasoningTitle,
		"thinkTitles":    at.thinkTitles,
	})})
}

// titleReasoning stops the streaming titler and fills whatever it did not get to
// — a box dropped by a full queue, a span that closed as the turn ended, or the
// reasoning field of a turn that never spoke. Called between endInline and flush,
// so the titles land in the same chats.json write as the answer.
//
// Best-effort by contract: no title model, no CLI, a timeout or garbage output
// all end with the UI keeping its instant local heuristic (thinkSummary).
func (tm *turnManager) titleReasoning(ctx context.Context, at *activeTurn) {
	tm.stopTitler(at)
	if ctx.Err() != nil {
		return // user stopped the turn: don't spend CPU on chrome they cancelled
	}
	if tm.pg == nil {
		return
	}

	at.mu.Lock()
	reasoning, content := at.reasoning, at.content
	haveReasoning := at.reasoningTitle != ""
	have := append([]string(nil), at.thinkTitles...)
	at.mu.Unlock()

	// A model either emits reasoning in the dedicated field or inline in the
	// content, never both, so exactly one of these produces work.
	needReasoning := !haveReasoning && strings.TrimSpace(reasoning) != ""
	spans := splitThinkSpans(content, titlegenMaxSpans)
	todo := make([]titleJob, 0, len(spans)+1)
	if needReasoning {
		todo = append(todo, titleJob{idx: -1, text: reasoning})
	}
	for i, span := range spans {
		if i < len(have) && have[i] != "" {
			continue
		}
		todo = append(todo, titleJob{idx: i, text: span})
	}
	if len(todo) == 0 {
		return
	}

	tg, err := resolveTitlegen(tm.pg.GeneratePath)
	if err != nil {
		if tm.log != nil {
			tm.log.Warnf("titlegen: %v", err)
		}
		return
	}
	if tg == nil {
		return
	}
	for _, j := range todo {
		t, terr := tg.title(ctx, j.text)
		if terr != nil {
			if tm.log != nil {
				tm.log.Warnf("titlegen: %v", terr)
			}
			break
		}
		if t != "" {
			at.setTitle(j.idx, t)
		}
	}
}

// thinkSpanRe tokenizes reasoning spans out of an assistant message, matching the
// UI's own splitter (ChatMessage.svelte) so the Nth title lines up with the Nth
// rendered box. An unclosed span (the turn ended mid-think) still counts.
var thinkSpanRe = regexp.MustCompile(`(?is)<(think|thinking|reasoning)>(.*?)(</(?:think|thinking|reasoning)>|$)`)

// splitThinkSpans returns the inner text of each reasoning span in content, in
// order, capped at max spans (titles past that are never displayed before the
// user has expanded the box anyway).
// splitClosedThinkSpans is splitThinkSpans minus a trailing UNCLOSED span — the
// box the model is still writing, whose text is not final and must not be titled.
// An unclosed span can only be the last one, so ordinals still line up with the
// UI's boxes (and with thinkMs).
func splitClosedThinkSpans(content string, max int) []string {
	ms := thinkSpanRe.FindAllStringSubmatch(content, -1)
	spans := make([]string, 0, len(ms))
	for _, m := range ms {
		if m[3] == "" {
			break // unclosed: still being written
		}
		body := strings.TrimSpace(m[2])
		if body == "" {
			continue
		}
		spans = append(spans, body)
		if max > 0 && len(spans) >= max {
			break
		}
	}
	return spans
}

func splitThinkSpans(content string, max int) []string {
	ms := thinkSpanRe.FindAllStringSubmatch(content, -1)
	spans := make([]string, 0, len(ms))
	for _, m := range ms {
		body := strings.TrimSpace(m[2])
		if body == "" {
			continue
		}
		spans = append(spans, body)
		if max > 0 && len(spans) >= max {
			break
		}
	}
	return spans
}
