package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// digestUpstream answers the digest's non-streamed calls: the task probe, then
// part notes. The first part's note is cut off (finish_reason length) once.
type digestUpstream struct {
	mu        sync.Mutex
	probe     string
	partCalls []string
	cutDone   bool
}

func (u *digestUpstream) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		user := body.Messages[len(body.Messages)-1].Content
		u.mu.Lock()
		answer, finish := "", "stop"
		switch {
		case strings.Contains(user, "<beginning>"):
			u.probe = user
			answer = "<task>Summarise the story's arcs.</task>"
		case strings.Contains(user, "<notes_so_far>"):
			u.partCalls = append(u.partCalls, user)
			answer = "NOTES-REST"
		default:
			u.partCalls = append(u.partCalls, user)
			answer = "NOTES-PART"
			if !u.cutDone {
				u.cutDone = true
				answer, finish = "NOTES-START", "length"
			}
		}
		u.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": answer}, "finish_reason": finish}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 2026-11-19: the request ("summarise my book") was made two turns before the
// message that was only the book. The probe now sees those turns; and a part
// note that runs out of room is continued instead of losing the part's end.
func TestDigest_ProbeSeesPriorTurnsAndCutNoteContinues(t *testing.T) {
	up := &digestUpstream{}
	srv := up.serve(t)
	tm := newTurnTestManager(t)
	tm.pg.SelfBase = srv.URL

	book := strings.Repeat("The camp lay quiet under the snow, and nobody spoke of the night before.\n\n", 800)
	msgs, _ := json.Marshal([]map[string]any{
		{"role": "user", "content": "Read my book and summarise the red thread so I can keep writing."},
		{"role": "assistant", "content": "Sure, send it over."},
		{"role": "user", "content": book},
	})
	start := &turnStart{ChatID: "c1", Model: "m", Messages: msgs, DigestAt: 0.8, ctxTokens: 8192}
	at := &activeTurn{user: "u", chatID: "c1"}
	ok, err := tm.digestOversize(context.Background(), "u", at, start, true, 0)
	if err != nil || !ok {
		t.Fatalf("digestOversize = %v, %v", ok, err)
	}

	if !strings.Contains(up.probe, "summarise the red thread") || !strings.Contains(up.probe, "<conversation_before>") {
		t.Errorf("probe did not see the earlier request:\n%.400s", up.probe)
	}
	if len(up.partCalls) < 3 {
		t.Fatalf("want at least 2 parts + 1 continuation, got %d calls", len(up.partCalls))
	}
	if !strings.Contains(up.partCalls[0], digestPacing) {
		t.Error("part call must carry the pacing line")
	}
	cont := up.partCalls[1]
	if !strings.Contains(cont, "<notes_so_far>\nNOTES-START\n</notes_so_far>") || !strings.Contains(cont, `<part n="1"`) {
		t.Errorf("second call must continue part 1 from its notes so far:\n%.300s", cont)
	}

	var out []map[string]any
	_ = json.Unmarshal(start.Messages, &out)
	digest, _ := out[len(out)-1]["content"].(string)
	if !strings.Contains(digest, "NOTES-START\nNOTES-REST") {
		t.Errorf("part 1's note must be the start plus its continuation:\n%.600s", digest)
	}
	if !strings.Contains(digest, "Summarise the story's arcs.") {
		t.Error("digest must carry the task the probe found")
	}
}
