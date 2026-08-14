package server

import (
	"context"
	_ "embed"
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

// titlegenAsset is the vendored title model, embedded so reasoning titles are
// core functionality in every install — no download step, no setup, no
// per-machine registry entry. 79 MiB of binary buys a feature that would
// otherwise silently not exist on a fresh box. See assets/README.md for
// provenance and license.
//
//go:embed assets/titlegen-flan-t5-small-q8_0.gguf
var titlegenAsset []byte

const titlegenAssetName = "titlegen-flan-t5-small-q8_0.gguf"

// titlegenExtractMu serializes the one-time extraction so two turns starting
// together can't both write the temp file.
var titlegenExtractMu sync.Mutex

// titlegenModelPath resolves the title gguf: the QM_TITLEGEN_MODEL env var wins
// (bring your own title model), else the embedded one, extracted once into
// <dir(generatePath)>/titlegen. That folder is deliberately OUTSIDE the models
// root so autogen's discovery walk never picks the title model up and publishes
// it as a servable chat model — and it cannot be served anyway (llama-server has
// no encoder-decoder path).
func titlegenModelPath(generatePath string) string {
	titlegenExtractMu.Lock()
	defer titlegenExtractMu.Unlock()
	if p := strings.TrimSpace(os.Getenv("QM_TITLEGEN_MODEL")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	if generatePath == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(generatePath), "titlegen")
	path := filepath.Join(dir, titlegenAssetName)
	// Size check, not just existence: a truncated extraction (disk full, killed
	// mid-write) would otherwise poison every later run.
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(titlegenAsset)) {
		return path
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	// Write-then-rename so a concurrent reader never sees a partial file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, titlegenAsset, 0o644); err != nil {
		os.Remove(tmp)
		return ""
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return ""
	}
	return path
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
	return cleanTitlegenOutput(string(out), prompt), nil
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

// titleReasoning fills in at.reasoningTitle / at.thinkTitles once the turn's text
// is final, then fans a snapshot delta so an attached tab swaps its local
// heuristic title for the model's. Called between endInline and flush, so the
// titles land in the same chats.json write as the answer.
//
// Best-effort by contract: no title model, no CLI, a timeout or garbage output
// all end with the UI keeping its instant local heuristic (thinkSummary).
func (tm *turnManager) titleReasoning(ctx context.Context, at *activeTurn) {
	if ctx.Err() != nil {
		return // user stopped the turn: don't spend CPU on chrome they cancelled
	}
	if tm.pg == nil {
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

	at.mu.Lock()
	reasoning, content := at.reasoning, at.content
	at.mu.Unlock()

	// A model either emits reasoning in the dedicated field or inline in the
	// content, never both, so exactly one of these produces work.
	var reasoningTitle string
	if strings.TrimSpace(reasoning) != "" {
		if t, terr := tg.title(ctx, reasoning); terr != nil {
			if tm.log != nil {
				tm.log.Warnf("titlegen: %v", terr)
			}
		} else {
			reasoningTitle = t
		}
	}
	var thinkTitles []string
	for _, span := range splitThinkSpans(content, titlegenMaxSpans) {
		t, terr := tg.title(ctx, span)
		if terr != nil {
			if tm.log != nil {
				tm.log.Warnf("titlegen: %v", terr)
			}
			break
		}
		thinkTitles = append(thinkTitles, t)
	}
	if reasoningTitle == "" && len(thinkTitles) == 0 {
		return
	}

	at.mu.Lock()
	at.reasoningTitle = reasoningTitle
	at.thinkTitles = thinkTitles
	at.fan(turnDelta{Kind: "titles", Replace: true, Data: mustJSON(map[string]any{
		"reasoningTitle": reasoningTitle,
		"thinkTitles":    thinkTitles,
	})})
	at.mu.Unlock()
}

// thinkSpanRe tokenizes reasoning spans out of an assistant message, matching the
// UI's own splitter (ChatMessage.svelte) so the Nth title lines up with the Nth
// rendered box. An unclosed span (the turn ended mid-think) still counts.
var thinkSpanRe = regexp.MustCompile(`(?is)<(think|thinking|reasoning)>(.*?)(</(?:think|thinking|reasoning)>|$)`)

// splitThinkSpans returns the inner text of each reasoning span in content, in
// order, capped at max spans (titles past that are never displayed before the
// user has expanded the box anyway).
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
