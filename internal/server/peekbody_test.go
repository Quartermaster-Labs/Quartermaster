package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A body past the peek limit must still reach the handler whole.
func TestPeekBody_OversizedBodyIsNotTruncated(t *testing.T) {
	full := bytes.Repeat([]byte("a"), 100)
	r := httptest.NewRequest("POST", "/v1/music/transcriptions", bytes.NewReader(full))

	peek, complete, err := peekBody(r, 10)
	if err != nil || complete {
		t.Fatalf("complete=%v err=%v, want an incomplete peek", complete, err)
	}
	if len(peek) != 11 {
		t.Errorf("peeked %d bytes, want limit+1", len(peek))
	}
	got, _ := io.ReadAll(r.Body)
	if !bytes.Equal(got, full) {
		t.Errorf("handler saw %d bytes, want all %d", len(got), len(full))
	}
}

func TestPeekBody_SmallBodyIsComplete(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"a":1}`)))
	peek, complete, err := peekBody(r, 10)
	if err != nil || !complete || string(peek) != `{"a":1}` {
		t.Fatalf("peek=%q complete=%v err=%v", peek, complete, err)
	}
	if got, _ := io.ReadAll(r.Body); string(got) != `{"a":1}` {
		t.Errorf("restored body = %q", got)
	}
}

// The prompt canonicalizer is where the truncation actually happened.
func TestPromptCanon_PassesLargeBodyThroughWhole(t *testing.T) {
	big := append([]byte(`{"model":"m","audio":"`), bytes.Repeat([]byte("A"), maxPeekBytes+1024)...)
	big = append(big, []byte(`"}`)...)
	var seen int
	h := newPromptCanon().middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = len(b)
	}))
	r := httptest.NewRequest("POST", "/v1/music/transcriptions", bytes.NewReader(big))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if seen != len(big) {
		t.Errorf("handler saw %d bytes, want %d", seen, len(big))
	}
}
