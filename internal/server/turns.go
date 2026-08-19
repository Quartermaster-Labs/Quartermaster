package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
)

// Server-owned turn runner. See turns_design.md for the full design.
//
// PHASE 1 (this file): the backbone — a turn runs as a server goroutine that
// streams ONE completion straight into chats.json (the single source of truth)
// and to any attached SSE viewer, so a closed/refreshed tab no longer loses (or
// stops) the answer. Tool loop / reasoning-budget finalize / compaction
// (phases 2-4) hang off the same runner + delta protocol and are added later.
//
// Ownership: the SERVER writes the in-flight assistant message into chats.json;
// a merge-guard (guardedChatsPut) stops a concurrent client PUT from reverting
// it. Live streaming is an in-memory snapshot+tail — there is NO sidecar file,
// so nothing is duplicated. A finished turn lives only in chats.json.

const turnFlushInterval = 2 * time.Second

// turnDelta is one SSE frame. Replace=true carries a full snapshot (sent once on
// subscribe so a reopened tab syncs), Replace=false is an incremental append.
type turnDelta struct {
	Kind    string          `json:"kind"` // content | reasoning | search | thinkMs | titles | done | error
	Text    string          `json:"text,omitempty"`
	Replace bool            `json:"replace,omitempty"`
	Msg     string          `json:"msg,omitempty"`
	GenMs   int64           `json:"genMs,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"` // search payload for kind=search
}

// persisted turn artifacts — schema mirrors the client's ChatMessage fields so
// the UI renders a server-run turn identically to a client-run one.
type turnSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type turnSearch struct {
	Query           string       `json:"query"`
	Results         string       `json:"results"`
	Kind            string       `json:"kind"` // web | wiki | quartermaster | youtube
	At              int          `json:"at"`
	ReasoningAt     int          `json:"reasoningAt"`
	DuringReasoning bool         `json:"duringReasoning"`
	Sources         []turnSource `json:"sources"`
}

type turnCitation struct {
	N      int    `json:"n"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	WikiID string `json:"wikiId,omitempty"`
}

// activeTurn is the in-memory handle for the one turn a user has generating.
type activeTurn struct {
	mu          sync.Mutex
	user        string // playground user this turn belongs to (for per-user prefs writes)
	chatID      string
	content     string
	reasoning   string
	reasoningMs int64
	genMs       int64
	searches    []turnSearch
	citations   []turnCitation
	authKey     string // configured API key injected into the loopback self-call (empty = keys off)
	thinkBytes  int    // all reasoning emitted this turn (field-based + inlined), for the soft budget
	inlineThink bool   // an inline <think> span is open in content (see append)
	inlineStart time.Time
	inlineAt    int     // content offset of the open span's inner text (see closeInline)
	thinkMs     []int64 // wall time of each closed inline span, in span order
	// Reasoning-box titles from the CPU title model (titlegen.go). reasoningTitle
	// covers the field-based trace; thinkTitles is one per inline <think> span, in
	// span order. Empty = UI keeps its local heuristic. Filled INCREMENTALLY: a
	// box is titled the moment it closes (its text is final then — only the open
	// span is still moving), with a gap-filling pass at end of turn. A tool loop
	// can run for minutes, so waiting for the whole turn left every finished box
	// on the local heuristic for the entire run.
	reasoningTitle string
	thinkTitles    []string
	// titleJobs feeds the per-turn title worker (titlegen.go, titleWorker).
	// Non-blocking sends: a full queue means the CPU model is behind, and the
	// end-of-turn pass fills whatever was dropped.
	titleJobs   chan titleJob
	titleDone   chan struct{}
	titleQueued bool             // reasoning-field trace is final (the answer started) and was queued
	titleSent   map[int]struct{} // inline span ordinals already handed to the worker
	done        bool
	subs        map[chan turnDelta]struct{}
	cancel      context.CancelFunc
	// pending is the config change awaiting the user's accept/deny (qm tools).
	// Non-nil only while a quartermaster_configure call is blocked on approval.
	pending *pendingApproval
	// busy is what the turn is doing RIGHT NOW ("Searching for …", "Reading
	// example.com"), empty when it is just generating. A tool call only produces
	// a search card once it has FINISHED, so without this the UI shows nothing
	// for the seconds a search actually takes — the user watches a source counter
	// tick up with no indication anything is running.
	busy string
}

// turnManager owns all server-side turn generation for the playground. One
// active turn per user (mirrors the client's single-in-flight genId).
type turnManager struct {
	pg     *Playground
	log    *logmon.Monitor
	client *http.Client

	mu     sync.Mutex
	active map[string]*activeTurn // key = user

	tgMu sync.Mutex
	tg   *titlegen // resolved title model+CLI, memoized (titlegen.go)
}

func newTurnManager(pg *Playground, log *logmon.Monitor) *turnManager {
	return &turnManager{
		pg:     pg,
		log:    log,
		client: &http.Client{}, // no timeout: a turn may stream for minutes
		active: map[string]*activeTurn{},
	}
}

// turnStart is the POST body: the assembled request the client would otherwise
// have streamed itself. `messages` is the API-shaped array (system + history +
// user); `chatId` names the chats.json session to write the answer into. The
// session (with the user message) must already be persisted by the client —
// server-owns means the server appends the ANSWER, not the whole conversation.
type turnStart struct {
	ChatID      string          `json:"chatId"`
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	Reasoning   *bool           `json:"reasoning,omitempty"`
	// ReasoningEffort is a level off the model's own ladder (advertised as
	// capabilities.reasoning_effort). Passed through as the standard top-level
	// OpenAI field and translated into the chat-template kwarg by the request
	// filter on the way out — the same path an external client takes, so there
	// is exactly one place that knows how a template spells its levels.
	// Empty = the model has no ladder, or thinking is off entirely.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`

	// Tool loop (phase 2). The client still assembles messages+tools; these are
	// the numeric knobs + web-search config the server needs to dispatch tools
	// and enforce per-turn caps (mirrors the client's turn-loop state).
	ReasoningBudget int    `json:"reasoningBudget,omitempty"` // soft cumulative-thinking cap (tokens); 0 = off. Enforced at round boundaries, see runLoop.
	WebSearch       bool   `json:"webSearch,omitempty"`
	SearxngURL      string `json:"searxngUrl,omitempty"` // legacy single-provider field; superseded by SearchProviders
	MaxSearches     int    `json:"maxSearches,omitempty"`
	ThrottleMs      int    `json:"throttleMs,omitempty"`
	Dedupe          bool   `json:"dedupe,omitempty"`
	MaxWiki         int    `json:"maxWiki,omitempty"`

	// SearchProviders is the ordered failover chain (search.go). It rides on the
	// turn payload rather than being read from stored prefs for the same reason
	// SearxngURL always has: the turn is a snapshot of what the client had
	// configured when the user pressed send. turnStart is in-memory only and is
	// never written into chats.json, so the API keys in it are not persisted.
	SearchProviders []searchProviderCfg `json:"searchProviders,omitempty"`
}

// providerChain is the chain to search with, falling back to the legacy
// single-URL field so an older client (or a stored payload) still works.
func (s turnStart) providerChain() []searchProviderCfg {
	if len(s.SearchProviders) > 0 {
		return s.SearchProviders
	}
	return legacySearchChain(s.SearxngURL)
}

// --- delta fan-out --------------------------------------------------------

// append accumulates a delta and pushes it to live subscribers. Held under
// at.mu so accumulation order == delivery order (a subscriber snapshots the
// accumulated state then joins the live feed under the same lock — no gap/dup).
func (at *activeTurn) append(kind, text string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	switch kind {
	case "content":
		at.closeInline()
		// The reasoning FIELD is final the moment the answer starts: everything the
		// model thinks after this point is spliced in as an inline span instead. So
		// title it now rather than at end of turn — on a plain one-round turn that
		// box is collapsed and on screen for the whole answer.
		if !at.titleQueued && strings.TrimSpace(at.reasoning) != "" {
			at.titleQueued = true
			at.enqueueTitle(titleJob{idx: -1, text: at.reasoning})
		}
		at.content += text
		// A model that writes its own <think> tags into content closes a box with a
		// token, not with an event — so the close IS this delta. Cheap tail check
		// (endedThinkSpan) gates the scan, which therefore runs once per closed box.
		if endedThinkSpan(at.content, text) {
			at.queueTitles()
		}
		at.fan(turnDelta{Kind: "content", Text: text})
	case "reasoning":
		at.thinkBytes += len(text)
		// reasoning_content is a single field, so the UI can only render it as ONE
		// box above the answer. That is only true for a turn that thinks BEFORE it
		// speaks. Models that answer first and think after — and every tool round
		// after the first — would have their later thinking (and any tool call made
		// inside it) yanked to the top, out of order. So once content exists, splice
		// later reasoning INTO the content as an inline <think> span: the UI already
		// tokenizes those in place, and a search's content offset lands inside them.
		// Whitespace-only content does NOT count as having spoken: a tool-calling
		// round often leaks a stray newline/space around the call before any real
		// answer token, and treating that as "the answer started" flipped the rest
		// of the pre-answer thinking into an inline <think> span — splitting one
		// thought into a 1-char reasoning box plus a detached inline box.
		if strings.TrimSpace(at.content) == "" {
			at.reasoning += text
			at.fan(turnDelta{Kind: "reasoning", Text: text})
			return
		}
		if !at.inlineThink {
			at.inlineThink = true
			at.inlineStart = time.Now()
			at.emitContent("\n\n<think>")
			at.inlineAt = len(at.content)
		}
		at.emitContent(text)
	}
}

// emitContent appends server-injected content and fans it; caller holds at.mu.
func (at *activeTurn) emitContent(text string) {
	at.content += text
	at.fan(turnDelta{Kind: "content", Text: text})
}

// closeInline closes an open inline <think> span. Caller holds at.mu.
func (at *activeTurn) closeInline() {
	if !at.inlineThink {
		return
	}
	at.inlineThink = false
	span := at.content[min(at.inlineAt, len(at.content)):]
	at.emitContent("</think>\n\n")
	// Per-span duration, so each inline box can say "Thought for 4.2s" like the
	// leading reasoning_content one does. Spans are ordered, so the UI zips this
	// onto the think segments it tokenizes out of content by index.
	ms := time.Since(at.inlineStart).Milliseconds()
	idx := len(at.thinkMs)
	at.thinkMs = append(at.thinkMs, ms)
	at.fan(turnDelta{Kind: "thinkMs", GenMs: ms})
	// This box is closed, so its text will not change again: title it now instead
	// of waiting for the turn (which may still have several tool rounds to run).
	if idx < titlegenMaxSpans && strings.TrimSpace(span) != "" {
		at.markTitleSent(idx)
		at.enqueueTitle(titleJob{idx: idx, text: span})
	}
}

// endInline closes a trailing inline <think> span at end of turn, so a finished
// answer never renders a forever-"Thinking" (unclosed) box.
func (at *activeTurn) endInline() {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.closeInline()
}

// appendSearch records a completed tool search and refreshes the citation
// registry, then fans a search delta (search entry + full citation snapshot).
func (at *activeTurn) appendSearch(s turnSearch, cites []turnCitation) {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.searches = append(at.searches, s)
	at.citations = append([]turnCitation(nil), cites...)
	payload, _ := json.Marshal(map[string]any{"search": s, "citations": at.citations})
	at.fan(turnDelta{Kind: "search", Data: payload})
}

// setBusy publishes (or clears) the live activity label.
func (at *activeTurn) setBusy(label string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.busy == label {
		return
	}
	at.busy = label
	at.fan(turnDelta{Kind: "busy", Text: label})
}

// busyLabel is what the user reads while a tool runs. The instant local tools
// (clock / calculator / units) deliberately have none: they return in under a
// millisecond, and a label that flashes on and off reads as a glitch.
func busyLabel(name, query string) string {
	short := query
	if len([]rune(short)) > 60 {
		short = string([]rune(short)[:60]) + "…"
	}
	switch name {
	case "web_search":
		if short == "" {
			return "Searching the web"
		}
		return fmt.Sprintf("Searching for %q", short)
	case "wiki_search":
		return "Searching the help articles"
	case "fetch_page":
		if u, err := url.Parse(query); err == nil && u.Host != "" {
			return "Reading " + strings.TrimPrefix(u.Host, "www.")
		}
		return "Reading the page"
	case "fetch_feed":
		return "Reading the feed"
	case "youtube_search":
		return "Searching YouTube"
	case "youtube_transcript":
		return "Reading the transcript"
	case "youtube_comments":
		return "Reading the comments"
	case "convert_currency":
		return "Checking the exchange rate"
	case "get_weather":
		return "Checking the weather"
	case "quartermaster_inspect":
		return "Checking quartermaster"
	case "quartermaster_configure":
		return "Applying the config change"
	}
	return ""
}

// lens returns the current content/reasoning lengths + reasoning state, for
// positioning a search inside the assistant bubble (at/reasoningAt/during).
//
// Lengths are UTF-16 code units, NOT bytes: the only consumer is the UI, which
// slices the same text with JavaScript string indices. A byte offset is equal
// only while the text is pure ASCII — one emoji earlier in the answer pushes it
// three units too far right and the tool cards land inside a word.
func (at *activeTurn) lens() (contentLen, reasoningLen int, duringReasoning bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	return utf16Len(at.content), utf16Len(at.reasoning), answerOnly(at.content) == ""
}

// utf16Len counts s the way JavaScript's String.length does: one unit per rune
// in the BMP, two for anything above it (emoji, most symbols a model writes).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// answerText returns the turn's answer so far, with any inlined thinking
// stripped — what the user actually reads, which is what the fabricated-link
// check must run against.
func (at *activeTurn) answerText() string {
	at.mu.Lock()
	defer at.mu.Unlock()
	return answerOnly(at.content)
}

// thinking returns every reasoning byte emitted this turn, including what was
// inlined into content — len(at.reasoning) alone would undercount the budget.
func (at *activeTurn) thinking() int {
	at.mu.Lock()
	defer at.mu.Unlock()
	return at.thinkBytes
}

// fan pushes to subscribers; caller holds at.mu.
func (at *activeTurn) fan(d turnDelta) {
	for ch := range at.subs {
		select {
		case ch <- d:
		default: // slow consumer: drop; a reconnect re-snapshots from scratch
		}
	}
}

// subscribe registers a live channel and returns the current snapshot to send
// first, so a (re)attaching viewer syncs to the full state then tails.
func (at *activeTurn) subscribe() (chan turnDelta, []turnDelta, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.done {
		return nil, nil, false
	}
	var snap []turnDelta
	if at.reasoning != "" {
		snap = append(snap, turnDelta{Kind: "reasoning", Text: at.reasoning, Replace: true})
	}
	snap = append(snap, turnDelta{Kind: "content", Text: at.content, Replace: true})
	if at.busy != "" {
		// A tab that reattaches mid-search must see the search, not a silent gap.
		snap = append(snap, turnDelta{Kind: "busy", Text: at.busy})
	}
	if len(at.thinkMs) > 0 {
		snap = append(snap, turnDelta{Kind: "thinkMs", Replace: true, Data: mustJSON(at.thinkMs)})
	}
	if at.reasoningTitle != "" || len(at.thinkTitles) > 0 {
		snap = append(snap, turnDelta{Kind: "titles", Replace: true, Data: mustJSON(map[string]any{
			"reasoningTitle": at.reasoningTitle,
			"thinkTitles":    at.thinkTitles,
		})})
	}
	for _, s := range at.searches {
		payload, _ := json.Marshal(map[string]any{"search": s, "citations": at.citations})
		snap = append(snap, turnDelta{Kind: "search", Data: payload})
	}
	// A reopened tab re-sees a live approval prompt so it can still accept/deny.
	if at.pending != nil {
		payload, _ := json.Marshal(at.pending)
		snap = append(snap, turnDelta{Kind: "approval", Data: payload})
	}
	ch := make(chan turnDelta, 256)
	if at.subs == nil {
		at.subs = map[chan turnDelta]struct{}{}
	}
	at.subs[ch] = struct{}{}
	return ch, snap, true
}

func (at *activeTurn) unsubscribe(ch chan turnDelta) {
	at.mu.Lock()
	defer at.mu.Unlock()
	delete(at.subs, ch)
}

func (at *activeTurn) finish(kind, msg string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.done = true
	at.fan(turnDelta{Kind: kind, Msg: msg, GenMs: at.genMs})
	for ch := range at.subs {
		close(ch)
	}
	at.subs = nil
}

// --- HTTP handlers --------------------------------------------------------

// POST /api/chats/turn — start a server-side turn. 409 if one is already running
// for this user. Returns immediately; the answer streams via the SSE endpoint
// and persists into chats.json regardless of whether any client stays attached.
func (s *Server) handleTurnStart(w http.ResponseWriter, r *http.Request) {
	tm, user := s.turnAuth(w, r)
	if tm == nil {
		return
	}
	var start turnStart
	// Same cap as a chats PUT: the payload carries the message history, inline
	// attachments included, and is decoded whole into memory.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBlobBytes)).Decode(&start); err != nil || start.ChatID == "" || start.Model == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	tm.mu.Lock()
	if cur, ok := tm.active[user]; ok && !cur.isDone() {
		tm.mu.Unlock()
		http.Error(w, "a turn is already running", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	at := &activeTurn{user: user, chatID: start.ChatID, cancel: cancel, authKey: s.pickSelfKey(start.Model)}
	tm.active[user] = at
	tm.mu.Unlock()

	go tm.run(ctx, user, at, start)
	writeJSON(w, map[string]string{"chatId": start.ChatID})
}

// GET /api/chats/turn/stream?chatId= — SSE snapshot + live tail of the running
// turn. 204 if nothing is generating for that chat (client falls back to the
// chats.json copy, which is the truth for a finished turn).
func (s *Server) handleTurnStream(w http.ResponseWriter, r *http.Request) {
	tm, user := s.turnAuth(w, r)
	if tm == nil {
		return
	}
	chatID := r.URL.Query().Get("chatId")

	tm.mu.Lock()
	at := tm.active[user]
	tm.mu.Unlock()
	if at == nil || at.chatID != chatID {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ch, snap, ok := at.subscribe()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		at.unsubscribe(ch)
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	writeDelta := func(d turnDelta) bool {
		b, _ := json.Marshal(d)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	defer at.unsubscribe(ch)
	for _, d := range snap {
		if !writeDelta(d) {
			return
		}
	}
	for {
		select {
		case d, open := <-ch:
			if !open {
				return
			}
			if !writeDelta(d) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// GET /api/chats/turn/state — which chat (if any) is generating for this user,
// so a reopened tab knows to resubscribe. In-memory only.
func (s *Server) handleTurnState(w http.ResponseWriter, r *http.Request) {
	tm, user := s.turnAuth(w, r)
	if tm == nil {
		return
	}
	tm.mu.Lock()
	at := tm.active[user]
	tm.mu.Unlock()
	resp := map[string]any{}
	if at != nil && !at.isDone() {
		resp["chatId"] = at.chatID
		resp["running"] = true
	}
	writeJSON(w, resp)
}

// DELETE /api/chats/turn?chatId= — Stop. Cancels the goroutine; the partial
// answer stays in chats.json (the run defer flushes it).
func (s *Server) handleTurnStop(w http.ResponseWriter, r *http.Request) {
	tm, user := s.turnAuth(w, r)
	if tm == nil {
		return
	}
	chatID := r.URL.Query().Get("chatId")
	tm.mu.Lock()
	at := tm.active[user]
	tm.mu.Unlock()
	if at != nil && at.chatID == chatID {
		at.cancel()
		// Wait for the goroutine to actually unwind before answering. cancel()
		// only signals; the runner still has to close the upstream body and run
		// its flush defer. A user who hits Stop and immediately re-sends would
		// otherwise race that window and get 409 "a turn is already running".
		for i := 0; i < 100 && !at.isDone(); i++ {
			select {
			case <-r.Context().Done():
				w.WriteHeader(http.StatusNoContent)
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// pickSelfKey returns a configured API key the server can use to authenticate
// its own loopback /v1/chat/completions call for a turn (keys gate that route).
// Prefers an unscoped key, then one scoped to include the model, else the first;
// returns "" when no keys are configured (auth middleware is a pass-through).
func (s *Server) pickSelfKey(model string) string {
	cfg := s.config()
	if len(cfg.RequiredAPIKeys) == 0 {
		return ""
	}
	scopes := buildKeyScopes(cfg)
	first := ""
	for _, k := range cfg.RequiredAPIKeys {
		if first == "" {
			first = k
		}
		sc := scopes[k]
		if len(sc) == 0 {
			return k // unscoped = full access
		}
		if sc[model] {
			return k
		}
	}
	return first
}

func (s *Server) turnAuth(w http.ResponseWriter, r *http.Request) (*turnManager, string) {
	p := s.playground
	if p == nil || p.turns == nil {
		http.Error(w, "playground not enabled", http.StatusNotImplemented)
		return nil, ""
	}
	user := p.userFromRequest(r)
	if user == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return nil, ""
	}
	return p.turns, user
}

func (at *activeTurn) isDone() bool {
	at.mu.Lock()
	defer at.mu.Unlock()
	return at.done
}

// --- the runner (PHASE 2: model→tool→model loop + budget finalize) ---------
//
// Ports ChatInterface.svelte's regenerateFromIndex turn loop. The client still
// assembles messages + tool defs and POSTs them; the server drives the rounds,
// dispatches web/wiki tools, numbers citations, and runs the reasoning-budget
// finalize. Compaction + title-gen deliberately STAY client-side (they run
// AFTER the answer, so the viewer does them on completion/reconnect — a closed
// tab just catches up next turn; nothing mid-answer is lost).

// toolCall is one assembled tool call from a streamed round.
type toolCall struct {
	ID   string
	Name string
	Args string
}

type cachedSearch struct {
	text    string
	sources []turnSource
}

// run executes the turn to completion, independent of any attached client,
// flushing the growing answer into chats.json.
func (tm *turnManager) run(ctx context.Context, user string, at *activeTurn, start turnStart) {
	began := time.Now()
	// History replayed to the model must carry real image/audio bytes: the
	// client's copy holds /api/media refs once the session round-trips through
	// extractMedia, and the upstream can't resolve those.
	start.Messages = tm.pg.inlineMedia(user, start.Messages)
	tm.startTitler(ctx, at) // titles each reasoning box as it closes (titlegen.go)
	kind, msg := "done", ""
	if err := tm.runLoop(ctx, at, start); err != nil && ctx.Err() == nil {
		kind, msg = "error", err.Error()
	}
	at.endInline()
	at.mu.Lock()
	at.genMs = time.Since(began).Milliseconds()
	// Drop citation markers with no source behind them before the answer is
	// persisted. Done once at end of turn (not per delta) so a marker mid-stream
	// isn't judged before the round that would have registered its source ran;
	// attached viewers get a content snapshot so the live bubble matches disk.
	if cleaned := stripPhantomCites(at.content, at.citations); cleaned != at.content {
		at.content = cleaned
		at.fan(turnDelta{Kind: "content", Text: cleaned, Replace: true})
	}
	at.mu.Unlock()

	// Reasoning-box titles, before the final flush so they persist with the answer.
	tm.titleReasoning(ctx, at)

	tm.flush(user, at, kind == "error", msg) // final write of the completed answer
	at.finish(kind, msg)
	tm.mu.Lock()
	if tm.active[user] == at {
		delete(tm.active, user)
	}
	tm.mu.Unlock()
}

// runLoop is the model→tool→model loop. Returns when the model stops calling
// tools, the budget finalize writes a forced answer, or an error/cancel occurs.
func (tm *turnManager) runLoop(ctx context.Context, at *activeTurn, start turnStart) error {
	var base []json.RawMessage
	if err := json.Unmarshal(start.Messages, &base); err != nil {
		return fmt.Errorf("bad messages: %w", err)
	}
	var apiTail []json.RawMessage

	useTools := len(start.Tools) > 0
	// Put previous turns' tool calls and results back into the history the model
	// reads (see turnsreplay.go). Only when tools are on: without them the model
	// cannot answer a tool_calls message and the template may reject it.
	if useTools {
		base = replayToolCalls(base)
	}
	maxTokens := 0
	if start.MaxTokens != nil {
		maxTokens = *start.MaxTokens
	}
	maxWiki := start.MaxWiki
	if maxWiki <= 0 {
		// Client hardcodes this and doesn't send it. The wiki is embedded — no
		// network, no rate limit — so this is a runaway-loop stop, not a budget:
		// a model working through a multi-part question legitimately reads a
		// handful of articles, and cutting it off mid-answer is the worse failure.
		maxWiki = 15
	}
	// Transcripts are the most expensive tool result by far (up to ytMaxTokens
	// each), so cap how many one turn may pull into context.
	wikiCount, searchCount, ytCount, fetchCount, fxCount := 0, 0, 0, 0, 0
	// Local tools (no network, no rate limit); the caps here are runaway-loop
	// stops, not cost controls.
	dtCount, calcCount, unitCount, wxCount, feedCount := 0, 0, 0, 0, 0
	ytTokens := 0 // transcript tokens spent this turn (the real transcript limiter)
	// Discovery tools are cheap per call but compound: a model left to browse
	// freely will list a channel, search twice more, then read comments on three
	// videos before writing a word. Capped separately from transcripts.
	ytBrowseCount, ytCommentCount := 0, 0
	var lastSearchAt time.Time
	searchCache := map[string]cachedSearch{}
	var citations []turnCitation
	citeOffset := 0

	// Soft reasoning budget: enforced HERE at round boundaries, not by llama.cpp's
	// native reasoning_budget. The native cap hard-closes </think> mid-generation;
	// on a tool-using model mid-search that derails it into dumping its in-thinking
	// search stream as the answer (fabricated result blocks, no real tool call, no
	// reply). Instead we let every round finish its thought (and any in-flight tool
	// call) naturally, and once cumulative thinking passes the budget we simply turn
	// thinking OFF for subsequent rounds so the model must answer. A lone round that
	// overthinks is bounded by max_tokens, not force-closed. Models with their own
	// reasoning-effort ladder opt out of the budget entirely (see the loop below).
	baseThink := start.Reasoning == nil || *start.Reasoning
	noAnswerRetried := false

	// Anti-fabrication state (see turnstools.go, "fabricated videos"). ytSeen is
	// every video id already present in the conversation — anything the user
	// pasted, and anything a previous turn's tool results put there — so links in
	// the answer can be told apart from links the model dreamt up.
	ytOffered := useTools && bytes.Contains(start.Tools, []byte(`"youtube_search"`))
	forceTools := ytOffered && pastedNewVideo(base)
	ytSeen := map[string]bool{}
	for _, id := range ytLinkIDs(string(start.Messages)) {
		ytSeen[id] = true
	}
	ytToolUsed := false
	doneCalls := map[string]string{} // name+args → result, for the repeat-call guard
	round := 0

	for {
		think := baseThink
		// A model driven by its own effort ladder is exempt: the level IS the
		// budget, and it lives in the chat template's system block. Cutting
		// thinking off between rounds would rewrite that block mid-conversation
		// (invalidating the KV prefix) to enforce a second, cruder limit on top
		// of the one the user picked.
		if think && start.ReasoningEffort == "" && start.ReasoningBudget > 0 && at.thinking()/4 >= start.ReasoningBudget {
			think = false // ~4 bytes/token: cumulative thinking hit the budget
		}
		msgs := append(append([]json.RawMessage{}, base...), apiTail...)
		// A video the model has never seen must be looked up, not recalled: force a
		// tool call on the opening round. Only that round — once the model has real
		// results the choice is its own again, and forcing later rounds would loop
		// forever.
		toolChoice := ""
		if forceTools && round == 0 {
			toolChoice = "required"
		}
		round++
		roundContent, roundReasoning, calls, _, err := tm.streamRound(ctx, at, start, msgs, maxTokens, think, toolChoice)
		if err != nil {
			return err
		}

		// No tool calls (or tools off) → the turn is complete.
		if !useTools || len(calls) == 0 {
			// ...unless the model thought and then stopped WITHOUT writing an
			// answer: it hit EOS on an unterminated <think>, so every token it
			// generated landed in reasoning_content and the bubble would render
			// as a thought with no reply. Retry once with thinking OFF, handing
			// the model its own reasoning back so it finishes from there instead
			// of starting over. The nudge is appended at the tail, so the prompt
			// prefix stays byte-identical and the KV is reused.
			if _, _, noAnswer := at.lens(); noAnswer {
				thought := roundReasoning
				if thought == "" {
					thought = roundContent // model inlined its <think> into content
				}
				if think && !noAnswerRetried && strings.TrimSpace(thought) != "" {
					noAnswerRetried = true
					baseThink = false
					at.mu.Lock()
					at.closeInline() // don't append the retry inside the open think box
					at.mu.Unlock()
					apiTail = append(apiTail, mustJSON(map[string]any{
						"role": "user", "content": noAnswerNudge(thought),
					}))
					continue
				}
				// Retry declined or also came back empty — never leave a silent
				// bubble the user can only stare at.
				at.mu.Lock()
				at.closeInline()
				at.emitContent(noAnswerMarker)
				at.mu.Unlock()
			}

			// Last line of defence: links to videos that came from neither the
			// conversation nor any tool result this turn are invented. Cannot be
			// unsaid — already streamed — so it gets labelled.
			if ytOffered && !ytToolUsed {
				if bad := unverifiedYtIDs(at.answerText(), ytSeen); len(bad) > 0 {
					at.mu.Lock()
					at.closeInline()
					at.emitContent(unverifiedVideoMarker)
					at.mu.Unlock()
				}
			}
			return nil
		}

		// Record this round's calls so the model sees them next round.
		apiTail = append(apiTail, mustJSON(map[string]any{
			"role": "assistant", "content": roundContent, "tool_calls": rawToolCalls(calls),
		}))

		contentLen, reasoningLen, during := at.lens()
		for _, tc := range calls {
			// Models re-issue a call they already made — the same channel listing
			// twice in one round, or again next round after ignoring the result.
			// Re-running it costs a yt-dlp process and paints a second identical
			// card in the trail. Answer from the first run instead. The tool
			// message is still emitted (the API needs one per tool_call_id), but
			// nothing re-executes and no duplicate card is appended.
			dupKey := tc.Name + "\x00" + strings.TrimSpace(tc.Args)
			if prev, ok := doneCalls[dupKey]; ok {
				apiTail = append(apiTail, mustJSON(map[string]any{
					"role": "tool", "tool_call_id": tc.ID,
					"content": prev + "\n\n(You already made this exact call this turn - this is the same result, not a new one. Use it, or call with different arguments.)",
				}))
				continue
			}

			citesBefore := citeOffset
			query := parseToolQuery(tc.Args)
			// Live status for the duration of the call; cleared when it returns.
			at.setBusy(busyLabel(tc.Name, query))
			var resultText string
			var sources []turnSource
			kind := "web"

			if tc.Name == "quartermaster_inspect" || tc.Name == "quartermaster_configure" {
				kind = "quartermaster"
				query, resultText = tm.dispatchQM(ctx, at, tc)
			} else if tc.Name == "memory_save" || tc.Name == "memory_delete" {
				kind = "memory"
				query, resultText = tm.dispatchMemory(at, tc)
			} else if tc.Name == "wiki_search" {
				kind = "wiki"
				if wikiCount >= maxWiki {
					resultText = fmt.Sprintf("Wiki lookup limit reached (%d per turn). Answer with what you have.", maxWiki)
				} else {
					wikiCount++
					arts := searchWiki(query)
					numbers := make([]int, len(arts))
					for i, a := range arts {
						if n, ok := citeByWiki(citations, a.ID); ok {
							numbers[i] = n
							continue
						}
						citeOffset++
						numbers[i] = citeOffset
						citations = append(citations, turnCitation{N: citeOffset, Title: a.Title, WikiID: a.ID})
					}
					resultText = formatWikiResults(query, arts, numbers)
				}
			} else if tc.Name == "fetch_page" {
				kind = "page"
				link := parseFetchArgs(tc.Args)
				query = link
				switch {
				case link == "":
					resultText = "fetch_page needs a `url` argument (a full http(s) link, e.g. from a previous search result)."
				case fetchCount >= maxFetches:
					resultText = fmt.Sprintf("Page-read limit reached (%d per turn). Answer with the pages already read.", maxFetches)
				default:
					fetchCount++
					doc, ferr := fetchPage(ctx, link)
					if ferr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						// Loud failure: a shop that bot-blocks or renders in JS must
						// read as "could not check", never as an absent price the
						// model fills in from memory.
						resultText = "Could not read " + link + ": " + ferr.Error() +
							"\nDo not guess this page's contents - say it could not be read, or try a different source."
					} else {
						title := orURL(doc.Title, doc.URL)
						query = title
						n, ok := citeByURL(citations, doc.URL)
						if !ok {
							citeOffset++
							n = citeOffset
							citations = append(citations, turnCitation{N: n, Title: title, URL: doc.URL})
						}
						resultText = formatPage(doc, n)
						sources = append(sources, turnSource{Title: title, URL: doc.URL})
					}
				}
			} else if tc.Name == "convert_currency" {
				kind = "currency"
				amount, from, to := parseConvertArgs(tc.Args)
				query = fmt.Sprintf("%s %s → %s", trimNum(amount), from, to)
				switch {
				case from == "" || to == "":
					query = "currency"
					resultText = "convert_currency needs `from` and `to` as three-letter currency codes (e.g. {\"amount\":1299,\"from\":\"RON\",\"to\":\"EUR\"})."
				case fxCount >= maxConverts:
					resultText = fmt.Sprintf("Currency-conversion limit reached (%d per turn). Convert the remaining figures from the rates already fetched.", maxConverts)
				default:
					fxCount++
					q, ferr := fetchFxRate(ctx, from, to)
					if ferr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						// A guessed rate is the failure this tool exists to stop,
						// so the error says so rather than leaving room for one.
						resultText = fmt.Sprintf("Could not get a %s→%s rate: %v\nDo not convert from memory - quote the price in %s as the page states it and say you could not convert it.", from, to, ferr, from)
					} else {
						resultText = formatFxRate(amount, q)
					}
				}
			} else if tc.Name == "get_datetime" {
				kind = "time"
				tz, until := parseDatetimeArgs(tc.Args)
				query = tz
				if query == "" {
					query = "now"
				}
				if until != "" {
					query += " → " + until
				}
				if dtCount >= maxDatetime {
					resultText = fmt.Sprintf("Date lookup limit reached (%d per turn).", maxDatetime)
				} else {
					dtCount++
					txt, derr := formatDatetime(tz, until)
					if derr != nil {
						resultText = derr.Error()
					} else {
						resultText = txt
					}
				}
			} else if tc.Name == "calculate" {
				kind = "calc"
				expr := parseCalcArgs(tc.Args)
				query = expr
				switch {
				case expr == "":
					query = "calculate"
					resultText = "calculate needs an `expression`, e.g. {\"expression\":\"(1299 * 3) / 36\"}."
				case calcCount >= maxCalcs:
					resultText = fmt.Sprintf("Calculation limit reached (%d per turn).", maxCalcs)
				default:
					calcCount++
					v, cerr := evalExpr(expr)
					if cerr != nil {
						// Named, specific failure: a model told only "error" tends
						// to answer with its own arithmetic, which is the thing
						// this tool exists to replace.
						resultText = fmt.Sprintf("Could not evaluate %q: %v. This tool takes plain arithmetic only - numbers, + - * / ^, parentheses, %% for percent, and the functions sqrt/abs/round/floor/ceil/min/max/sum/avg/pow/ln/log.", expr, cerr)
					} else {
						resultText = formatCalc(expr, v)
					}
				}
			} else if tc.Name == "convert_units" {
				kind = "units"
				amount, from, to := parseUnitArgs(tc.Args)
				query = fmt.Sprintf("%s %s → %s", fmtCalcNum(amount), from, to)
				switch {
				case from == "" || to == "":
					query = "convert units"
					resultText = "convert_units needs `from` and `to`, e.g. {\"amount\":15.6,\"from\":\"in\",\"to\":\"cm\"}."
				case unitCount >= maxUnitConverts:
					resultText = fmt.Sprintf("Unit-conversion limit reached (%d per turn).", maxUnitConverts)
				default:
					unitCount++
					v, cf, ct, uerr := convertUnit(amount, from, to)
					if uerr != nil {
						resultText = uerr.Error()
					} else {
						resultText = formatUnitConvert(amount, cf, v, ct)
					}
				}
			} else if tc.Name == "get_weather" {
				kind = "weather"
				place, days, imperial := parseWeatherArgs(tc.Args)
				query = place
				switch {
				case place == "":
					query = "weather"
					resultText = "get_weather needs a `location`, e.g. {\"location\":\"Cluj-Napoca\",\"days\":3}."
				case wxCount >= maxWeather:
					resultText = fmt.Sprintf("Weather limit reached (%d per turn).", maxWeather)
				default:
					wxCount++
					txt, werr := fetchWeather(ctx, place, days, imperial)
					if werr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						resultText = fmt.Sprintf("Could not get the weather for %q: %v\nDo not describe the weather from memory - say the forecast could not be read.", place, werr)
					} else {
						resultText = txt
					}
				}
			} else if tc.Name == "fetch_feed" {
				kind = "feed"
				furl, limit := parseFeedArgs(tc.Args)
				query = furl
				switch {
				case furl == "":
					query = "feed"
					resultText = "fetch_feed needs a feed `url` (RSS or Atom), e.g. {\"url\":\"https://example.com/feed.xml\"}."
				case feedCount >= maxFeeds:
					resultText = fmt.Sprintf("Feed limit reached (%d per turn).", maxFeeds)
				default:
					feedCount++
					fd, ferr := fetchFeed(ctx, furl, limit)
					if ferr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						resultText = fmt.Sprintf("Could not read the feed at %s: %v", furl, ferr)
					} else {
						if fd.Title != "" {
							query = fd.Title
						}
						resultText = formatFeed(fd)
						sources = append(sources, turnSource{Title: fd.Title, URL: fd.URL})
					}
				}
			} else if tc.Name == "youtube_transcript" {
				kind = "youtube"
				link, lang := parseYouTubeArgs(tc.Args)
				query = link
				vid := parseYouTubeID(link)
				switch {
				case vid == "":
					resultText = fmt.Sprintf("%q is not a YouTube link. Pass the full video URL (or its 11-character video id).", link)
				case ytCount >= maxYouTube:
					resultText = fmt.Sprintf("Transcript limit reached (%d per turn). Answer with what you have.", maxYouTube)
				case ytTurnTokens-ytTokens < ytMinTranscript:
					resultText = fmt.Sprintf("Transcript budget for this turn is spent (~%d tokens of transcript already read, across %d video(s)). Answer from those; ask the user which remaining video matters most if you need another.", ytTokens, ytCount)
				default:
					ytCount++
					tr, terr := fetchYouTubeTranscript(ctx, vid, lang)
					if terr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						// Loud, specific failure: an empty transcript the model
						// narrates over is worse than no tool at all.
						resultText = "Transcript unavailable: " + terr.Error()
					} else {
						vurl := "https://www.youtube.com/watch?v=" + vid
						title := orURL(tr.Title, vurl)
						query = title
						n, ok := citeByURL(citations, vurl)
						if !ok {
							citeOffset++
							n = citeOffset
							citations = append(citations, turnCitation{N: n, Title: title, URL: vurl})
						}
						// Each transcript gets whatever is left of the turn's budget,
						// capped at the per-video ceiling: five shorts all fit, one
						// three-hour stream still gets truncated (loudly) rather
						// than eating the whole window.
						resultText = formatYouTubeTranscript(tr, n, ytTurnTokens-ytTokens)
						ytTokens += len(resultText) / 4 // ~4 bytes/token, same estimate as the reasoning budget
						sources = append(sources, turnSource{Title: title, URL: vurl})
					}
				}
			} else if tc.Name == "youtube_search" {
				// Distinct kind from the transcript tool: the trail labels these
				// "Searched YouTube", not "Read transcript" — a listing is metadata,
				// and a card claiming the video was read is a lie about the evidence.
				kind = "youtube-search"
				sa := parseYtSearchArgs(tc.Args)
				switch {
				case sa.Query == "" && sa.Channel == "":
					resultText = "youtube_search needs either a `query` (free-text search) or a `channel` (a @handle or channel URL to list videos from)."
				case ytBrowseCount >= maxYtBrowse:
					resultText = fmt.Sprintf("YouTube search limit reached (%d per turn). Work with the videos already found.", maxYtBrowse)
				default:
					ytBrowseCount++
					var vids []ytVideo
					var verr error
					// A channel argument wins: "what did X post about Y" is answered
					// by listing X, not by a global search that may never surface it.
					if sa.Channel != "" {
						query = sa.Channel
						vids, verr = ytChannelVideos(ctx, sa.Channel, sa.Tab, sa.Limit)
					} else {
						query = sa.Query
						vids, verr = ytSearch(ctx, sa.Query, sa.Limit)
					}
					if verr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						resultText = "YouTube search failed: " + verr.Error() +
							"\nDo not invent video titles or links - say the search did not work."
					} else {
						numbers := make([]int, len(vids))
						for i, v := range vids {
							vurl := "https://www.youtube.com/watch?v=" + v.ID
							if n, ok := citeByURL(citations, vurl); ok {
								numbers[i] = n
								continue
							}
							citeOffset++
							numbers[i] = citeOffset
							citations = append(citations, turnCitation{N: citeOffset, Title: orURL(v.Title, vurl), URL: vurl})
							sources = append(sources, turnSource{Title: orURL(v.Title, vurl), URL: vurl})
						}
						what := fmt.Sprintf("%q", query)
						if sa.Channel != "" {
							what = fmt.Sprintf("channel %s", query)
						}
						// A channel/playlist tab comes back in upload order; a
						// search comes back in relevance order. The model is told
						// which, because the listing carries no dates of its own.
						resultText = formatYouTubeVideos(what, vids, numbers, sa.Channel != "")
					}
				}
			} else if tc.Name == "youtube_comments" {
				kind = "youtube-comments"
				link, limit := parseYtCommentArgs(tc.Args)
				query = link
				vid := parseYouTubeID(link)
				switch {
				case vid == "":
					resultText = fmt.Sprintf("%q is not a YouTube link. Pass the full video URL (or its 11-character video id).", link)
				case ytCommentCount >= maxYtComments:
					resultText = fmt.Sprintf("Comment-read limit reached (%d per turn). Answer with what you have.", maxYtComments)
				default:
					ytCommentCount++
					cs, meta, cerr := fetchYouTubeComments(ctx, vid, limit)
					if cerr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						resultText = "Comments unavailable: " + cerr.Error() +
							"\nSay so plainly - never write what commenters 'probably' said."
					} else {
						vurl := "https://www.youtube.com/watch?v=" + vid
						title := orURL(meta.Title, vurl)
						query = title
						n, ok := citeByURL(citations, vurl)
						if !ok {
							citeOffset++
							n = citeOffset
							citations = append(citations, turnCitation{N: n, Title: title, URL: vurl})
						}
						resultText = formatYouTubeComments(cs, meta, n)
						sources = append(sources, turnSource{Title: title, URL: vurl})
					}
				}
			} else {
				cacheKey := fmt.Sprintf("%s|%d", query, parseSearchCount(tc.Args))
				if cached, ok := searchCache[cacheKey]; ok && start.Dedupe {
					resultText, sources = cached.text, cached.sources
				} else if start.MaxSearches > 0 && searchCount >= start.MaxSearches {
					resultText = fmt.Sprintf("Search limit reached (%d per turn). Answer with the information already gathered.", start.MaxSearches)
				} else {
					if start.ThrottleMs > 0 && !lastSearchAt.IsZero() {
						if wait := time.Duration(start.ThrottleMs)*time.Millisecond - time.Since(lastSearchAt); wait > 0 {
							if err := sleepCtx(ctx, wait); err != nil {
								return err
							}
						}
					}
					results, _, serr := searchChain(ctx, start.providerChain(), query, parseSearchCount(tc.Args))
					if serr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						resultText = "Search failed: " + serr.Error()
					} else {
						lastSearchAt = time.Now()
						searchCount++
						numbers := make([]int, len(results))
						for i, r := range results {
							if r.URL == "" {
								citeOffset++
								numbers[i] = citeOffset
								continue
							}
							if n, ok := citeByURL(citations, r.URL); ok {
								numbers[i] = n
								continue
							}
							citeOffset++
							numbers[i] = citeOffset
							citations = append(citations, turnCitation{N: citeOffset, Title: orURL(r.Title, r.URL), URL: r.URL})
						}
						resultText = formatSearchResults(query, results, numbers)
						for _, r := range results {
							if r.URL != "" {
								sources = append(sources, turnSource{Title: orURL(r.Title, r.URL), URL: r.URL})
							}
						}
						if start.Dedupe {
							searchCache[cacheKey] = cachedSearch{text: resultText, sources: sources}
						}
					}
				}
			}

			at.setBusy("")

			// Cite reminder co-located with the numbered results (high compliance).
			citeHint := ""
			if citeOffset > citesBefore {
				citeHint = fmt.Sprintf("\n\nWhen you use any of the above in your answer, cite it inline with its bracketed number (e.g. [%d]) right after the statement - once, where you first use it, not on every following sentence. Anything you did not take from these results stays uncited.", citesBefore+1)
			}
			apiTail = append(apiTail, mustJSON(map[string]any{
				"role": "tool", "tool_call_id": tc.ID, "content": resultText + citeHint,
			}))
			doneCalls[dupKey] = resultText
			// Any video id a tool actually returned is a real one the model may
			// link (a web search hit counts too, not just the YouTube tools).
			if strings.HasPrefix(kind, "youtube") {
				ytToolUsed = true
			}
			for _, id := range ytLinkIDs(resultText) {
				ytSeen[id] = true
			}
			at.appendSearch(turnSearch{
				Query: query, Results: resultText, Kind: kind,
				At: contentLen, ReasoningAt: reasoningLen, DuringReasoning: during, Sources: sources,
			}, citations)
		}
		tm.flush("", at, false, "") // checkpoint after the tool round
	}
}

// streamRound POSTs one streamed round and folds the deltas into the turn (live
// view + periodic flush), returning the round's raw content, its reasoning_content,
// the assembled tool calls, and the finish reason.
func (tm *turnManager) streamRound(ctx context.Context, at *activeTurn, start turnStart, msgs []json.RawMessage, maxTokens int, think bool, toolChoice string) (string, string, []toolCall, string, error) {
	body := buildBody(start, msgs, maxTokens, think, toolChoice)

	var roundContent, roundReasoning string
	var reasoningStart time.Time
	lastFlush := time.Now()
	accs := map[int]*toolCall{}
	var order []int

	onContent := func(c string) {
		roundContent += c
		at.append("content", c)
		if answerOnly(roundContent) != "" && !reasoningStart.IsZero() {
			at.mu.Lock()
			if at.reasoningMs == 0 {
				at.reasoningMs = time.Since(reasoningStart).Milliseconds()
			}
			at.mu.Unlock()
		}
	}
	onReasoning := func(rc string) {
		if reasoningStart.IsZero() {
			reasoningStart = time.Now()
		}
		roundReasoning += rc
		at.append("reasoning", rc)
	}
	onTool := func(idx int, id, name, args string) {
		acc := accs[idx]
		if acc == nil {
			acc = &toolCall{}
			accs[idx] = acc
			order = append(order, idx)
		}
		if id != "" {
			acc.ID = id
		}
		if name != "" {
			acc.Name = name
		}
		acc.Args += args
	}
	onProgress := func() {
		now := time.Now()
		if now.Sub(lastFlush) >= turnFlushInterval {
			tm.flush("", at, false, "")
			lastFlush = now
		}
	}

	finish, err := tm.streamSSE(ctx, body, start.ChatID, at.authKey, onContent, onReasoning, onTool, onProgress)
	if err != nil {
		return "", "", nil, "", err
	}
	calls := make([]toolCall, 0, len(order))
	for _, idx := range order {
		calls = append(calls, *accs[idx])
	}
	return roundContent, roundReasoning, calls, finish, nil
}

// streamSSE POSTs a streamed chat completion to quartermaster's own inference
// loopback and dispatches each delta to the callbacks. onReasoning/onTool/
// onProgress may be nil. Returns the finish_reason.
func (tm *turnManager) streamSSE(ctx context.Context, body map[string]any, chatID, authKey string, onContent func(string), onReasoning func(string), onTool func(int, string, string, string), onProgress func()) (string, error) {
	buf, _ := json.Marshal(body)
	url := strings.TrimRight(tm.pg.SelfBase, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Conversation-Id", chatID) // key the slot KV cache by conversation
	if authKey != "" {
		req.Header.Set("Authorization", "Bearer "+authKey) // authenticate the loopback (API keys gate /v1)
	}
	resp, err := tm.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := readLimited(resp.Body, 1<<12)
		return "", fmt.Errorf("upstream %s: %s", resp.Status, snippet)
	}

	finish := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content    string `json:"content"`
					Reasoning  string `json:"reasoning_content"`
					Reasoning2 string `json:"reasoning"`
					ToolCalls  []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
		d := ch.Delta
		if rc := d.Reasoning + d.Reasoning2; rc != "" && onReasoning != nil {
			onReasoning(rc)
		}
		if d.Content != "" && onContent != nil {
			onContent(d.Content)
		}
		if onTool != nil {
			for _, tc := range d.ToolCalls {
				onTool(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
			}
		}
		if onProgress != nil {
			onProgress()
		}
	}
	return finish, sc.Err()
}

// --- small helpers --------------------------------------------------------

// buildBody assembles one round's request. `think` is the resolved enable_thinking
// for THIS round; the soft reasoning budget is applied by the caller flipping it to
// false at a round boundary (see runLoop). No native reasoning_budget is sent — that
// hard-closes </think> mid-generation and derails tool-using models.
// toolChoice, when non-empty, is passed through as OpenAI's tool_choice for this
// round only. runLoop uses "required" on the first round of a YouTube question to
// stop the model answering from imagination without ever looking (see
// turnstools.go, "fabricated videos").
func buildBody(start turnStart, msgs []json.RawMessage, maxTokens int, think bool, toolChoice string) map[string]any {
	b := map[string]any{"model": start.Model, "messages": msgs, "stream": true}
	if start.Temperature != nil {
		b["temperature"] = *start.Temperature
	}
	if maxTokens > 0 {
		b["max_tokens"] = maxTokens
	}
	if len(start.Tools) > 0 {
		b["tools"] = start.Tools
		if toolChoice != "" {
			b["tool_choice"] = toolChoice
		}
	}
	b["chat_template_kwargs"] = map[string]any{"enable_thinking": think}
	// Top-level, not a kwarg: the request filter (reasoning_effort.go) snaps it
	// onto the levels this model actually declares and moves it into
	// chat_template_kwargs. Pointless once thinking is off — the template never
	// reaches the branch that reads it.
	if think && start.ReasoningEffort != "" {
		b["reasoning_effort"] = start.ReasoningEffort
	}
	return b
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func rawToolCalls(calls []toolCall) []map[string]any {
	out := make([]map[string]any, len(calls))
	for i, c := range calls {
		out[i] = map[string]any{
			"id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": c.Args},
		}
	}
	return out
}

func parseToolQuery(args string) string {
	var a struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	return a.Query
}

func parseFetchArgs(args string) string {
	var a struct {
		URL  string `json:"url"`
		Link string `json:"link"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	if a.URL == "" {
		a.URL = a.Link
	}
	return strings.TrimSpace(a.URL)
}

func parseYouTubeArgs(args string) (urlOrID, lang string) {
	var a struct {
		URL   string `json:"url"`
		Video string `json:"video"`
		Lang  string `json:"lang"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	if a.URL == "" {
		a.URL = a.Video
	}
	return strings.TrimSpace(a.URL), strings.TrimSpace(a.Lang)
}

// ytSearchArgs is one youtube_search call: a free-text query, or a channel to
// list (with an optional tab), plus a result count.
type ytSearchArgs struct {
	Query   string
	Channel string
	Tab     string
	Limit   int
}

func parseYtSearchArgs(args string) ytSearchArgs {
	var a struct {
		Query   string `json:"query"`
		Search  string `json:"search"`
		Q       string `json:"q"`
		Channel string `json:"channel"`
		Handle  string `json:"handle"`
		Tab     string `json:"tab"`
		// Models name a count half a dozen ways; accept the obvious ones rather
		// than silently ignoring the one they picked.
		Limit   *int `json:"limit"`
		MaxRes  *int `json:"max_results"`
		Count   *int `json:"count"`
		NumRes  *int `json:"n"`
		Results *int `json:"results"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	out := ytSearchArgs{
		Query:   strings.TrimSpace(firstNonEmpty(a.Query, a.Search, a.Q)),
		Channel: strings.TrimSpace(firstNonEmpty(a.Channel, a.Handle)),
		Tab:     strings.TrimSpace(a.Tab),
	}
	for _, p := range []*int{a.Limit, a.MaxRes, a.Count, a.NumRes, a.Results} {
		if p != nil && *p > 0 {
			out.Limit = *p
			break
		}
	}
	return out
}

func parseYtCommentArgs(args string) (urlOrID string, limit int) {
	var a struct {
		URL    string `json:"url"`
		Video  string `json:"video"`
		ID     string `json:"id"`
		Limit  *int   `json:"limit"`
		MaxCom *int   `json:"max_comments"`
		Count  *int   `json:"count"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	for _, p := range []*int{a.Limit, a.MaxCom, a.Count} {
		if p != nil && *p > 0 {
			limit = *p
			break
		}
	}
	return strings.TrimSpace(firstNonEmpty(a.URL, a.Video, a.ID)), limit
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func citeByURL(cs []turnCitation, url string) (int, bool) {
	for _, c := range cs {
		if c.URL == url {
			return c.N, true
		}
	}
	return 0, false
}

func citeByWiki(cs []turnCitation, id string) (int, bool) {
	for _, c := range cs {
		if c.WikiID == id {
			return c.N, true
		}
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- chats.json persistence (server-owns the in-flight assistant message) --

// flush writes the turn's current answer into the chat session inside
// chats.json, creating/reusing the trailing assistant message. Sessions and
// their other fields pass through untouched (opaque map round-trip), so the
// client's schema (searches, citations, instructions, …) is preserved.
func (tm *turnManager) flush(user string, at *activeTurn, isErr bool, errMsg string) {
	// user is threaded from run(); streamRound's periodic call passes "" and we
	// recover it from the active map (the turn owns exactly one user).
	if user == "" {
		user = tm.userOf(at)
		if user == "" {
			return
		}
	}
	at.mu.Lock()
	content, reasoning, genMs, reasoningMs, done := at.content, at.reasoning, at.genMs, at.reasoningMs, at.done
	thinkMs := append([]int64(nil), at.thinkMs...)
	reasoningTitle := at.reasoningTitle
	thinkTitles := append([]string(nil), at.thinkTitles...)
	searches := append([]turnSearch(nil), at.searches...)
	citations := append([]turnCitation(nil), at.citations...)
	at.mu.Unlock()
	if isErr && errMsg != "" {
		content += "\n\n**Error:** " + errMsg
	}

	tm.pg.mu.Lock()
	defer tm.pg.mu.Unlock()
	arr := tm.pg.readChatsLocked(user)
	sess := findSession(arr, at.chatID)
	if sess == nil {
		return // client must persist the session (with the user msg) before starting
	}
	am := trailingAssistant(sess)
	am["content"] = content
	if reasoning != "" {
		am["reasoning_content"] = reasoning
	}
	if reasoningMs > 0 {
		am["reasoningTimeMs"] = reasoningMs
	}
	if len(thinkMs) > 0 {
		am["thinkMs"] = thinkMs
	}
	if reasoningTitle != "" {
		am["reasoningTitle"] = reasoningTitle
	}
	if len(thinkTitles) > 0 {
		am["thinkTitles"] = thinkTitles
	}
	if len(searches) > 0 {
		am["searches"] = searches
	}
	if len(citations) > 0 {
		am["citations"] = citations
	}
	if done || genMs > 0 {
		am["genTimeMs"] = genMs
	}
	sess["updatedAt"] = time.Now().UnixMilli()
	tm.pg.writeChatsLocked(user, arr)
}

func (tm *turnManager) userOf(at *activeTurn) string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for u, a := range tm.active {
		if a == at {
			return u
		}
	}
	return ""
}

// guardedChatsPut replaces the client's copy of the actively-generating session
// with the server's authoritative on-disk copy, so a stale/whole-array client
// PUT can't revert the in-flight answer. Other sessions take the client's
// version as usual. Falls through to a plain write when nothing is generating.
func (tm *turnManager) guardedChatsPut(user string, clientArr []map[string]any) []map[string]any {
	tm.mu.Lock()
	at := tm.active[user]
	tm.mu.Unlock()
	if at == nil || at.isDone() {
		return clientArr
	}
	disk := tm.pg.readChatsLocked(user) // caller holds pg.mu
	authoritative := findSession(disk, at.chatID)
	if authoritative == nil {
		return clientArr
	}
	for i, s := range clientArr {
		if sessionID(s) == at.chatID {
			clientArr[i] = authoritative
			return clientArr
		}
	}
	return append(clientArr, authoritative)
}

// --- chat-file helpers (opaque map round-trip preserves unknown fields) ----

func (p *Playground) readChatsLocked(user string) []map[string]any {
	b, err := os.ReadFile(p.chatsPath(user))
	if err != nil {
		return []map[string]any{}
	}
	var arr []map[string]any
	if json.Unmarshal(b, &arr) != nil {
		return []map[string]any{}
	}
	return arr
}

func (p *Playground) writeChatsLocked(user string, arr []map[string]any) {
	path := p.chatsPath(user)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	b, _ := json.Marshal(arr)
	b = p.extractMedia(user, b)
	os.WriteFile(path, b, 0o644)
}

func sessionID(s map[string]any) string {
	id, _ := s["id"].(string)
	return id
}

func findSession(arr []map[string]any, id string) map[string]any {
	for _, s := range arr {
		if sessionID(s) == id {
			return s
		}
	}
	return nil
}

// trailingAssistant returns the session's last message if it's an assistant
// bubble (the client seeds an empty one on send), else appends a fresh one.
func trailingAssistant(sess map[string]any) map[string]any {
	msgs, _ := sess["messages"].([]any)
	if n := len(msgs); n > 0 {
		if m, ok := msgs[n-1].(map[string]any); ok {
			if role, _ := m["role"].(string); role == "assistant" {
				return m
			}
		}
	}
	am := map[string]any{"role": "assistant", "content": ""}
	sess["messages"] = append(msgs, am)
	return am
}

