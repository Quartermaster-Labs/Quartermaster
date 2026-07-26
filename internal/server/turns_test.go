package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
)

// A turn that thinks before it speaks keeps the field-based reasoning box; one
// that speaks first gets its later thinking inlined into content, in order.
func TestActiveTurn_ReasoningOrdering(t *testing.T) {
	t.Run("think first stays field-based", func(t *testing.T) {
		at := &activeTurn{}
		at.append("reasoning", "pondering")
		at.append("content", "answer")
		at.endInline()
		if at.reasoning != "pondering" {
			t.Fatalf("reasoning = %q, want %q", at.reasoning, "pondering")
		}
		if at.content != "answer" {
			t.Fatalf("content = %q, want %q", at.content, "answer")
		}
	})

	t.Run("answer first inlines later thinking", func(t *testing.T) {
		at := &activeTurn{}
		at.append("content", "hi")
		at.append("reasoning", "wait")
		at.append("content", "actually no")
		at.append("reasoning", "hmm")
		at.endInline()
		if at.reasoning != "" {
			t.Fatalf("reasoning = %q, want empty (all inlined)", at.reasoning)
		}
		want := "hi\n\n<think>wait</think>\n\nactually no\n\n<think>hmm</think>\n\n"
		if at.content != want {
			t.Fatalf("content = %q, want %q", at.content, want)
		}
		if at.thinkBytes != len("wait")+len("hmm") {
			t.Fatalf("thinkBytes = %d, want %d", at.thinkBytes, len("wait")+len("hmm"))
		}
	})

	// A search fired mid-inline-think must land inside the <think> span so the
	// UI nests it there instead of spilling it into the answer.
	t.Run("search offset lands inside the inline think", func(t *testing.T) {
		at := &activeTurn{}
		at.append("content", "hi")
		at.append("reasoning", "let me look")
		contentLen, _, during := at.lens()
		at.append("reasoning", " more")
		at.endInline()
		if during {
			t.Fatal("duringReasoning = true, want false (answer already started)")
		}
		open := len("hi\n\n<think>")
		if contentLen <= open || contentLen >= len(at.content)-len("</think>\n\n") {
			t.Fatalf("search offset %d not inside the think span of %q", contentLen, at.content)
		}
	})
}

// A tool-calling round often leaks a stray newline before any real answer token;
// treating that as "the answer started" used to split one thought into a 1-char
// reasoning box plus a detached inline box.
func TestActiveTurn_WhitespaceContentIsNotAnAnswer(t *testing.T) {
	at := &activeTurn{}
	at.append("reasoning", "first")
	at.append("content", "\n ")
	at.append("reasoning", " second")

	if at.reasoning != "first second" {
		t.Errorf("reasoning=%q, want the whole thought in the field box", at.reasoning)
	}
	if strings.Contains(at.content, "<think>") {
		t.Errorf("inline span opened after whitespace-only content: %q", at.content)
	}
	// thinkBytes counts every reasoning byte, inlined ones included — the soft
	// budget would undercount if it read len(at.reasoning).
	if want := len("first") + len(" second"); at.thinking() != want {
		t.Errorf("thinking()=%d want %d", at.thinking(), want)
	}
}

// endInline closes a span still open when the stream ends, so a finished answer
// never renders a forever-"Thinking" box — and is idempotent.
func TestActiveTurn_EndInlineIsIdempotent(t *testing.T) {
	at := &activeTurn{}
	at.append("content", "answer")
	at.append("reasoning", "trailing thought")
	at.endInline()
	at.endInline()

	if n := strings.Count(at.content, "</think>"); n != 1 {
		t.Errorf("</think> count=%d, want 1", n)
	}
	if len(at.thinkMs) != 1 {
		t.Errorf("thinkMs=%v, want exactly one closed span", at.thinkMs)
	}
}

// --- subscribe / fan-out --------------------------------------------------

// A (re)attaching viewer must see the accumulated state and then the live tail
// with no gap and no duplicate — subscribe snapshots and joins under one lock.
func TestActiveTurn_SubscribeSnapshotThenTail(t *testing.T) {
	at := &activeTurn{}
	at.append("reasoning", "thinking")
	at.append("content", "part one")

	ch, snap, ok := at.subscribe()
	if !ok {
		t.Fatal("subscribe on a live turn returned ok=false")
	}
	kinds := map[string]turnDelta{}
	for _, d := range snap {
		kinds[d.Kind] = d
	}
	if d := kinds["reasoning"]; d.Text != "thinking" || !d.Replace {
		t.Errorf("reasoning snapshot=%+v, want the full text with Replace", d)
	}
	if d := kinds["content"]; d.Text != "part one" || !d.Replace {
		t.Errorf("content snapshot=%+v, want the full text with Replace", d)
	}

	at.append("content", " part two")
	select {
	case d := <-ch:
		if d.Kind != "content" || d.Text != " part two" || d.Replace {
			t.Errorf("tail delta=%+v, want the incremental content chunk", d)
		}
	default:
		t.Fatal("subscriber got no live delta after subscribe")
	}
}

// Unsubscribing stops delivery; finish closes every remaining channel and makes
// later subscribes fail so a viewer falls back to reading chats.json.
func TestActiveTurn_UnsubscribeAndFinish(t *testing.T) {
	at := &activeTurn{}
	gone, _, _ := at.subscribe()
	live, _, _ := at.subscribe()

	at.unsubscribe(gone)
	at.append("content", "hi")
	select {
	case d, open := <-gone:
		t.Fatalf("unsubscribed channel still received %+v (open=%v)", d, open)
	default:
	}
	if d := <-live; d.Text != "hi" {
		t.Errorf("live subscriber delta=%+v", d)
	}

	at.finish("done", "")
	if d, open := <-live; !open || d.Kind != "done" {
		t.Errorf("want a done delta, got %+v open=%v", d, open)
	}
	if _, open := <-live; open {
		t.Error("finish must close subscriber channels")
	}
	if _, _, ok := at.subscribe(); ok {
		t.Error("subscribe on a finished turn should return ok=false")
	}
}

// A subscriber that stops reading must be dropped, not block the generator: the
// fan-out is a non-blocking send onto a bounded buffer.
func TestActiveTurn_SlowSubscriberDoesNotBlock(t *testing.T) {
	at := &activeTurn{}
	if _, _, ok := at.subscribe(); !ok { // never read from
		t.Fatal("subscribe failed")
	}
	for i := 0; i < 1000; i++ { // far past the 256-deep buffer
		at.append("content", "x")
	}
	if len(at.content) != 1000 {
		t.Errorf("content len=%d, want 1000 — generator stalled on a slow consumer", len(at.content))
	}
}

// Concurrent producers and (re)attaching viewers must not race — every mutation
// and every subscribe/unsubscribe goes through at.mu. Meaningful under -race.
func TestActiveTurn_ConcurrentSubscribeAppend(t *testing.T) {
	at := &activeTurn{}
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			at.append("content", "a")
			at.appendSearch(turnSearch{Query: "q", Kind: "web"}, []turnCitation{{N: 1}})
		}
	}()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ch, _, ok := at.subscribe()
				if !ok {
					return
				}
				select { // consume whatever landed, then detach
				case <-ch:
				default:
				}
				at.unsubscribe(ch)
				at.lens()
				at.thinking()
			}
		}()
	}
	wg.Wait()

	at.finish("done", "")
	if !at.isDone() {
		t.Error("turn should be done after finish")
	}
}

// --- test wiring ----------------------------------------------------------

// newTurnTestManager wires a turnManager over a throwaway playground data dir.
func newTurnTestManager(t *testing.T) *turnManager {
	t.Helper()
	p := &Playground{DataDir: t.TempDir()}
	tm := newTurnManager(p, logmon.NewWriter(io.Discard))
	p.turns = tm
	return tm
}

func seedChats(t *testing.T, p *Playground, user string, arr []map[string]any) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeChatsLocked(user, arr)
}

func readChats(t *testing.T, p *Playground, user string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(p.chatsPath(user))
	if err != nil {
		t.Fatalf("read chats: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatalf("decode chats: %v", err)
	}
	return arr
}

// chatSession builds a chats.json session with the given messages.
func chatSession(id string, msgs ...map[string]any) map[string]any {
	list := make([]any, 0, len(msgs))
	for _, m := range msgs {
		list = append(list, m)
	}
	return map[string]any{"id": id, "messages": list}
}

func chatMsg(role, content string) map[string]any {
	return map[string]any{"role": role, "content": content}
}

// registerTurn puts at into the manager's active map as if run() had started it.
func registerTurn(tm *turnManager, user string, at *activeTurn) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.active[user] = at
}

func sessionMessages(t *testing.T, arr []map[string]any, id string) []any {
	t.Helper()
	sess := findSession(arr, id)
	if sess == nil {
		t.Fatalf("session %q missing from %+v", id, arr)
	}
	msgs, _ := sess["messages"].([]any)
	return msgs
}

// --- flush (persistence) --------------------------------------------------

// flush writes into the session's trailing assistant bubble (the client seeds an
// empty one on send) and leaves every other field and session untouched.
func TestTurnManager_FlushReusesTrailingAssistant(t *testing.T) {
	tm := newTurnTestManager(t)
	live := chatSession("c1", chatMsg("user", "hi"), chatMsg("assistant", ""))
	live["title"] = "keep me" // unknown-to-the-server field must survive
	seedChats(t, tm.pg, "radu", []map[string]any{
		chatSession("other", chatMsg("user", "untouched")),
		live,
	})

	at := &activeTurn{chatID: "c1", content: "the answer", reasoning: "why", genMs: 1234}
	at.searches = []turnSearch{{Query: "q", Kind: "web"}}
	at.citations = []turnCitation{{N: 1, Title: "t", URL: "u"}}
	tm.flush("radu", at, false, "")

	arr := readChats(t, tm.pg, "radu")
	if len(arr) != 2 {
		t.Fatalf("session count=%d, want 2", len(arr))
	}
	sess := findSession(arr, "c1")
	if sess["title"] != "keep me" {
		t.Errorf("unknown session field dropped: %+v", sess)
	}
	msgs := sessionMessages(t, arr, "c1")
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2 (no new bubble appended)", len(msgs))
	}
	am, _ := msgs[1].(map[string]any)
	if am["content"] != "the answer" || am["reasoning_content"] != "why" {
		t.Errorf("assistant bubble=%+v", am)
	}
	if am["genTimeMs"] != float64(1234) {
		t.Errorf("genTimeMs=%v want 1234", am["genTimeMs"])
	}
	if am["searches"] == nil || am["citations"] == nil {
		t.Errorf("searches/citations not persisted: %+v", am)
	}
	if sess["updatedAt"] == nil {
		t.Error("updatedAt not bumped")
	}
	if findSession(arr, "other") == nil {
		t.Error("unrelated session dropped")
	}
}

// No trailing assistant (a client that didn't seed one) => append a fresh bubble
// rather than overwriting the user's message.
func TestTurnManager_FlushAppendsAssistantWhenMissing(t *testing.T) {
	tm := newTurnTestManager(t)
	seedChats(t, tm.pg, "radu", []map[string]any{chatSession("c1", chatMsg("user", "hi"))})

	tm.flush("radu", &activeTurn{chatID: "c1", content: "answer"}, false, "")

	msgs := sessionMessages(t, readChats(t, tm.pg, "radu"), "c1")
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want the user msg plus a new assistant bubble", len(msgs))
	}
	if m, _ := msgs[0].(map[string]any); m["content"] != "hi" {
		t.Errorf("user message clobbered: %+v", msgs[0])
	}
	if m, _ := msgs[1].(map[string]any); m["role"] != "assistant" || m["content"] != "answer" {
		t.Errorf("appended bubble=%+v", msgs[1])
	}
}

// An errored turn keeps its partial answer and appends the error, so the user
// sees what was generated plus why it stopped.
func TestTurnManager_FlushErrorKeepsPartial(t *testing.T) {
	tm := newTurnTestManager(t)
	seedChats(t, tm.pg, "radu", []map[string]any{chatSession("c1", chatMsg("assistant", ""))})

	tm.flush("radu", &activeTurn{chatID: "c1", content: "partial"}, true, "upstream died")

	msgs := sessionMessages(t, readChats(t, tm.pg, "radu"), "c1")
	m, _ := msgs[0].(map[string]any)
	got, _ := m["content"].(string)
	if !strings.HasPrefix(got, "partial") || !strings.Contains(got, "upstream died") {
		t.Errorf("content=%q, want the partial answer plus the error", got)
	}
}

// The client must persist the session (with the user message) before starting —
// the server appends the ANSWER, not the conversation. An unknown chatId is a
// no-op, not a synthesized session.
func TestTurnManager_FlushIgnoresUnknownSession(t *testing.T) {
	tm := newTurnTestManager(t)
	seedChats(t, tm.pg, "radu", []map[string]any{chatSession("c1", chatMsg("user", "hi"))})

	tm.flush("radu", &activeTurn{chatID: "nope", content: "answer"}, false, "")

	if arr := readChats(t, tm.pg, "radu"); len(arr) != 1 || findSession(arr, "nope") != nil {
		t.Errorf("unknown chatId should not create a session: %+v", arr)
	}
}

// flush is also called mid-stream with user="" (from streamRound); the user is
// recovered from the active map.
func TestTurnManager_FlushRecoversUserFromActiveMap(t *testing.T) {
	tm := newTurnTestManager(t)
	seedChats(t, tm.pg, "radu", []map[string]any{chatSession("c1", chatMsg("assistant", ""))})
	at := &activeTurn{chatID: "c1", content: "streamed"}
	registerTurn(tm, "radu", at)

	tm.flush("", at, false, "")

	msgs := sessionMessages(t, readChats(t, tm.pg, "radu"), "c1")
	if m, _ := msgs[0].(map[string]any); m["content"] != "streamed" {
		t.Errorf("periodic flush did not resolve the user: %+v", msgs[0])
	}
}

// --- guardedChatsPut (stale-client merge) ---------------------------------

// A client PUT carries the WHOLE array from a tab that may not have seen the
// in-flight answer. The generating session must be forced back to the server's
// on-disk copy; every other session takes the client's version.
func TestTurnManager_GuardedChatsPutProtectsLiveSession(t *testing.T) {
	tm := newTurnTestManager(t)
	seedChats(t, tm.pg, "radu", []map[string]any{
		chatSession("c1", chatMsg("assistant", "server answer so far")),
	})
	registerTurn(tm, "radu", &activeTurn{chatID: "c1"})

	client := []map[string]any{
		chatSession("c1", chatMsg("assistant", "")), // stale: the tab never saw the answer
		chatSession("c2", chatMsg("user", "a new chat")),
	}

	tm.pg.mu.Lock()
	out := tm.guardedChatsPut("radu", client)
	tm.pg.mu.Unlock()

	if len(out) != 2 {
		t.Fatalf("sessions=%d, want 2", len(out))
	}
	msgs := sessionMessages(t, out, "c1")
	if m, _ := msgs[0].(map[string]any); m["content"] != "server answer so far" {
		t.Errorf("stale client PUT reverted the live answer: %+v", m)
	}
	if findSession(out, "c2") == nil {
		t.Error("unrelated client session dropped")
	}
}

// A tab that never had the generating session at all (opened elsewhere) must not
// delete it — the authoritative copy is appended back.
func TestTurnManager_GuardedChatsPutReaddsMissingSession(t *testing.T) {
	tm := newTurnTestManager(t)
	seedChats(t, tm.pg, "radu", []map[string]any{chatSession("c1", chatMsg("assistant", "live"))})
	registerTurn(tm, "radu", &activeTurn{chatID: "c1"})

	tm.pg.mu.Lock()
	out := tm.guardedChatsPut("radu", []map[string]any{chatSession("c2")})
	tm.pg.mu.Unlock()

	if findSession(out, "c1") == nil {
		t.Errorf("live session dropped by a client that lacked it: %+v", out)
	}
}

// Nothing generating (or the turn already finished) => the client's array wins
// untouched, so a normal save isn't second-guessed.
func TestTurnManager_GuardedChatsPutPassthrough(t *testing.T) {
	tm := newTurnTestManager(t)
	seedChats(t, tm.pg, "radu", []map[string]any{chatSession("c1", chatMsg("assistant", "old"))})
	client := []map[string]any{chatSession("c1", chatMsg("assistant", "client wins"))}

	tm.pg.mu.Lock()
	out := tm.guardedChatsPut("radu", client) // no active turn
	tm.pg.mu.Unlock()
	if m, _ := sessionMessages(t, out, "c1")[0].(map[string]any); m["content"] != "client wins" {
		t.Errorf("no active turn: client PUT should pass through, got %+v", m)
	}

	done := &activeTurn{chatID: "c1"}
	done.finish("done", "")
	registerTurn(tm, "radu", done)
	tm.pg.mu.Lock()
	out = tm.guardedChatsPut("radu", client)
	tm.pg.mu.Unlock()
	if m, _ := sessionMessages(t, out, "c1")[0].(map[string]any); m["content"] != "client wins" {
		t.Errorf("finished turn should stop guarding, got %+v", m)
	}
}

// --- stop / state / auth --------------------------------------------------

// DELETE cancels the run goroutine's context — but only for the chat the client
// named, so a stale Stop from a previous chat can't kill the current answer.
func TestServer_HandleTurnStopCancelsMatchingChat(t *testing.T) {
	tm := newTurnTestManager(t)
	s := &Server{playground: tm.pg}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registerTurn(tm, "radu", &activeTurn{chatID: "c1", cancel: cancel})

	stop := func(chatID string) int {
		r := httptest.NewRequest(http.MethodDelete, "/api/chats/turn?chatId="+chatID, nil)
		r.AddCookie(&http.Cookie{Name: pgCookie, Value: tm.pg.cookieValue("radu")})
		w := httptest.NewRecorder()
		s.handleTurnStop(w, r)
		return w.Code
	}

	if code := stop("other"); code != http.StatusNoContent {
		t.Fatalf("stop(other) status=%d", code)
	}
	if ctx.Err() != nil {
		t.Fatal("a stop for a different chatId cancelled the running turn")
	}

	if code := stop("c1"); code != http.StatusNoContent {
		t.Fatalf("stop(c1) status=%d", code)
	}
	if ctx.Err() == nil {
		t.Error("stop for the running chatId did not cancel it")
	}
}

// Every turn endpoint is per-user: an absent or unsigned cookie is rejected
// before any turn state is touched.
func TestServer_TurnAuthRequiresSignedCookie(t *testing.T) {
	tm := newTurnTestManager(t)
	s := &Server{playground: tm.pg}

	w := httptest.NewRecorder()
	s.handleTurnState(w, httptest.NewRequest(http.MethodGet, "/api/chats/turn/state", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: status=%d want 401", w.Code)
	}

	// Forged (unsigned, pre-HMAC format) cookie must not authenticate either.
	r := httptest.NewRequest(http.MethodGet, "/api/chats/turn/state", nil)
	r.AddCookie(&http.Cookie{Name: pgCookie, Value: "radu"})
	w = httptest.NewRecorder()
	s.handleTurnState(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("forged cookie: status=%d want 401", w.Code)
	}
}

// state reports the running chat to its owner only.
func TestServer_HandleTurnState(t *testing.T) {
	tm := newTurnTestManager(t)
	s := &Server{playground: tm.pg}
	registerTurn(tm, "radu", &activeTurn{chatID: "c1"})

	state := func(user string) map[string]any {
		r := httptest.NewRequest(http.MethodGet, "/api/chats/turn/state", nil)
		r.AddCookie(&http.Cookie{Name: pgCookie, Value: tm.pg.cookieValue(user)})
		w := httptest.NewRecorder()
		s.handleTurnState(w, r)
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode state: %v (%q)", err, w.Body.String())
		}
		return resp
	}

	if got := state("radu"); got["running"] != true || got["chatId"] != "c1" {
		t.Errorf("owner state=%+v, want running c1", got)
	}
	if got := state("someoneelse"); len(got) != 0 {
		t.Errorf("another user sees the turn: %+v", got)
	}
}
