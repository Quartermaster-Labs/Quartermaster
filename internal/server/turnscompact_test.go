package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// The compaction request must reach upstream as a continuation of the chat's
// own prompt: same conversation id, the tools unchanged and no tool_choice (a
// "none" drops the tool list from the system block), thinking off, and the
// instruction as the final user message.
func TestServer_HandleTurnCompactForwardsAsContinuation(t *testing.T) {
	var got map[string]any
	var convID string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		convID = r.Header.Get("X-Conversation-Id")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"the summary"}}]}`)
	}))
	defer up.Close()

	tm := newTurnTestManager(t)
	tm.pg.SelfBase = up.URL
	s := &Server{playground: tm.pg}
	s.cfg.Store(&config.Config{})

	body := `{"chatId":"c1","model":"m","tools":[{"type":"function","function":{"name":"web_search"}}],
		"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},{"role":"user","content":"summarize"}]}`
	r := httptest.NewRequest(http.MethodPost, "/api/chats/compact", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: pgCookie, Value: tm.pg.cookieValue("radu")})
	w := httptest.NewRecorder()
	s.handleTurnCompact(w, r)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "the summary") {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if convID != "c1" {
		t.Errorf("X-Conversation-Id=%q, want the chat id", convID)
	}
	if got["stream"] != false {
		t.Errorf("stream=%v, want false", got["stream"])
	}
	if _, ok := got["tools"]; !ok {
		t.Error("tools dropped: the system block would no longer match the KV")
	}
	if _, ok := got["tool_choice"]; ok {
		t.Errorf("tool_choice=%v sent; it must stay unset", got["tool_choice"])
	}
	if kw, _ := got["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs=%v, want thinking off", got["chat_template_kwargs"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages=%d, want the 4 sent", len(msgs))
	}
	last, _ := msgs[3].(map[string]any)
	if last["role"] != "user" || last["content"] != "summarize" {
		t.Errorf("last message=%v, want the instruction as a user turn", last)
	}
}

// A running turn owns the model; compaction must not race it.
func TestServer_HandleTurnCompactRejectsWhileTurnRuns(t *testing.T) {
	tm := newTurnTestManager(t)
	s := &Server{playground: tm.pg}
	s.cfg.Store(&config.Config{})
	registerTurn(tm, "radu", &activeTurn{chatID: "c1"})

	r := httptest.NewRequest(http.MethodPost, "/api/chats/compact",
		strings.NewReader(`{"chatId":"c1","model":"m","messages":[]}`))
	r.AddCookie(&http.Cookie{Name: pgCookie, Value: tm.pg.cookieValue("radu")})
	w := httptest.NewRecorder()
	s.handleTurnCompact(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("status=%d want 409", w.Code)
	}
}
