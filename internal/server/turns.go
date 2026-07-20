package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	Kind    string          `json:"kind"` // content | reasoning | search | thinkMs | done | error
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
	Kind            string       `json:"kind"` // web | wiki
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
	thinkMs     []int64 // wall time of each closed inline span, in span order
	done        bool
	subs        map[chan turnDelta]struct{}
	cancel      context.CancelFunc
	// pending is the config change awaiting the user's accept/deny (qm tools).
	// Non-nil only while a quartermaster_configure call is blocked on approval.
	pending *pendingApproval
}

// turnManager owns all server-side turn generation for the playground. One
// active turn per user (mirrors the client's single-in-flight genId).
type turnManager struct {
	pg     *Playground
	log    *logmon.Monitor
	client *http.Client

	mu     sync.Mutex
	active map[string]*activeTurn // key = user
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

	// Tool loop (phase 2). The client still assembles messages+tools; these are
	// the numeric knobs + web-search config the server needs to dispatch tools
	// and enforce per-turn caps (mirrors the client's turn-loop state).
	ReasoningBudget int    `json:"reasoningBudget,omitempty"` // soft cumulative-thinking cap (tokens); 0 = off. Enforced at round boundaries, see runLoop.
	WebSearch       bool   `json:"webSearch,omitempty"`
	SearxngURL      string `json:"searxngUrl,omitempty"`
	MaxSearches     int    `json:"maxSearches,omitempty"`
	ThrottleMs      int    `json:"throttleMs,omitempty"`
	Dedupe          bool   `json:"dedupe,omitempty"`
	MaxWiki         int    `json:"maxWiki,omitempty"`
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
		at.content += text
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
	at.emitContent("</think>\n\n")
	// Per-span duration, so each inline box can say "Thought for 4.2s" like the
	// leading reasoning_content one does. Spans are ordered, so the UI zips this
	// onto the think segments it tokenizes out of content by index.
	ms := time.Since(at.inlineStart).Milliseconds()
	at.thinkMs = append(at.thinkMs, ms)
	at.fan(turnDelta{Kind: "thinkMs", GenMs: ms})
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

// lens returns the current content/reasoning byte lengths + reasoning state,
// for positioning a search inside the assistant bubble (at/reasoningAt/during).
func (at *activeTurn) lens() (contentLen, reasoningLen int, duringReasoning bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	return len(at.content), len(at.reasoning), answerOnly(at.content) == ""
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
	if len(at.thinkMs) > 0 {
		snap = append(snap, turnDelta{Kind: "thinkMs", Replace: true, Data: mustJSON(at.thinkMs)})
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
	if err := json.NewDecoder(r.Body).Decode(&start); err != nil || start.ChatID == "" || start.Model == "" {
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
	user := playgroundUser(r)
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
	kind, msg := "done", ""
	if err := tm.runLoop(ctx, at, start); err != nil && ctx.Err() == nil {
		kind, msg = "error", err.Error()
	}
	at.endInline()
	at.mu.Lock()
	at.genMs = time.Since(began).Milliseconds()
	at.mu.Unlock()

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
	maxTokens := 0
	if start.MaxTokens != nil {
		maxTokens = *start.MaxTokens
	}
	maxWiki := start.MaxWiki
	if maxWiki <= 0 {
		maxWiki = 4 // client hardcodes this and doesn't send it
	}
	wikiCount, searchCount := 0, 0
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
	// overthinks is bounded by max_tokens, not force-closed.
	baseThink := start.Reasoning == nil || *start.Reasoning

	for {
		think := baseThink
		if think && start.ReasoningBudget > 0 && at.thinking()/4 >= start.ReasoningBudget {
			think = false // ~4 bytes/token: cumulative thinking hit the budget
		}
		msgs := append(append([]json.RawMessage{}, base...), apiTail...)
		roundContent, calls, _, err := tm.streamRound(ctx, at, start, msgs, maxTokens, think)
		if err != nil {
			return err
		}

		// No tool calls (or tools off) → the turn is complete.
		if !useTools || len(calls) == 0 {
			return nil
		}

		// Record this round's calls so the model sees them next round.
		apiTail = append(apiTail, mustJSON(map[string]any{
			"role": "assistant", "content": roundContent, "tool_calls": rawToolCalls(calls),
		}))

		contentLen, reasoningLen, during := at.lens()
		for _, tc := range calls {
			citesBefore := citeOffset
			query := parseToolQuery(tc.Args)
			var resultText string
			var sources []turnSource
			kind := "web"

			if tc.Name == "quartermaster_inspect" || tc.Name == "quartermaster_configure" {
				kind = "quartermaster"
				query, resultText = tm.dispatchQM(ctx, at, tc)
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
			} else {
				if cached, ok := searchCache[query]; ok && start.Dedupe {
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
					results, serr := searxngSearch(ctx, start.SearxngURL, query)
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
							searchCache[query] = cachedSearch{text: resultText, sources: sources}
						}
					}
				}
			}

			// Cite reminder co-located with the numbered results (high compliance).
			citeHint := ""
			if citeOffset > citesBefore {
				citeHint = fmt.Sprintf("\n\nWhen you use any of the above in your answer, cite it inline with its bracketed number (e.g. [%d]) right after the statement.", citesBefore+1)
			}
			apiTail = append(apiTail, mustJSON(map[string]any{
				"role": "tool", "tool_call_id": tc.ID, "content": resultText + citeHint,
			}))
			at.appendSearch(turnSearch{
				Query: query, Results: resultText, Kind: kind,
				At: contentLen, ReasoningAt: reasoningLen, DuringReasoning: during, Sources: sources,
			}, citations)
		}
		tm.flush("", at, false, "") // checkpoint after the tool round
	}
}

// streamRound POSTs one streamed round and folds the deltas into the turn (live
// view + periodic flush), returning the round's raw content, assembled tool
// calls, and finish reason.
func (tm *turnManager) streamRound(ctx context.Context, at *activeTurn, start turnStart, msgs []json.RawMessage, maxTokens int, think bool) (string, []toolCall, string, error) {
	body := buildBody(start, msgs, maxTokens, think)

	var roundContent string
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
		if time.Since(lastFlush) >= turnFlushInterval {
			tm.flush("", at, false, "")
			lastFlush = time.Now()
		}
	}

	finish, err := tm.streamSSE(ctx, body, start.ChatID, at.authKey, onContent, onReasoning, onTool, onProgress)
	if err != nil {
		return "", nil, "", err
	}
	calls := make([]toolCall, 0, len(order))
	for _, idx := range order {
		calls = append(calls, *accs[idx])
	}
	return roundContent, calls, finish, nil
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
func buildBody(start turnStart, msgs []json.RawMessage, maxTokens int, think bool) map[string]any {
	b := map[string]any{"model": start.Model, "messages": msgs, "stream": true}
	if start.Temperature != nil {
		b["temperature"] = *start.Temperature
	}
	if maxTokens > 0 {
		b["max_tokens"] = maxTokens
	}
	if len(start.Tools) > 0 {
		b["tools"] = start.Tools
	}
	b["chat_template_kwargs"] = map[string]any{"enable_thinking": think}
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

func orURL(title, url string) string {
	if title != "" {
		return title
	}
	return url
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

func readLimited(r interface{ Read([]byte) (int, error) }, n int64) (string, error) {
	b := make([]byte, n)
	total := 0
	for int64(total) < n {
		m, err := r.Read(b[total:])
		total += m
		if err != nil {
			break
		}
	}
	return string(b[:total]), nil
}
