package router

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
)

func TestLoadingWriter_SSEHeadersAndInitialMessage(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)

	if ct := lw.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: want text/event-stream, got %q", ct)
	}
	if cc := lw.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: want no-cache, got %q", cc)
	}
	if conn := lw.Header().Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection: want keep-alive, got %q", conn)
	}

	body := w.Body.String()
	if !strings.HasPrefix(body, ": ") {
		t.Errorf("expected SSE comment prefix, got: %s", body)
	}

	content := extractComments(body)
	if !strings.Contains(content, "quartermaster loading model: test-model") {
		t.Errorf("missing initial message in streamed comments: %q", content)
	}
}

func TestLoadingWriter_WriteHeaderOnce(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)
	lw.WriteHeader(http.StatusCreated)

	if w.Code != http.StatusOK {
		t.Errorf("first WriteHeader: want %d, got %d", http.StatusOK, w.Code)
	}
}

func TestLoadingWriter_WritePassthrough(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)
	lw.Write([]byte("hello"))
	lw.Flush()

	body := w.Body.String()
	if !strings.Contains(body, "hello") {
		t.Errorf("Write passthrough failed, body: %s", body)
	}
}

func TestLoadingWriter_StartStopsOnCancel(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)
	lw.tickDuration = 10 * time.Millisecond
	lw.loopStarted = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())

	go lw.start(ctx)
	<-lw.loopStarted
	cancel()

	if !lw.waitForCompletion(time.Second) {
		t.Fatal("waitForCompletion timed out")
	}

	body := w.Body.String()
	if !strings.Contains(body, "model ready") {
		t.Errorf("expected ready message, body: %s", body)
	}
}

func TestLoadingWriter_StartShowsSetUpdate(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)
	lw.tickDuration = 10 * time.Millisecond
	lw.loopStarted = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	go lw.start(ctx)
	<-lw.loopStarted

	lw.setUpdate("custom status message")
	time.Sleep(50 * time.Millisecond)
	cancel()

	if !lw.waitForCompletion(time.Second) {
		t.Fatal("waitForCompletion timed out")
	}

	body := w.Body.String()
	content := extractComments(body)
	if !strings.Contains(content, "custom status message") {
		t.Errorf("expected setUpdate message in output, got: %q", content)
	}
}

func TestLoadingWriter_SendCommentFormat(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)

	before := w.Body.Len()
	lw.sendComment("hello world")
	frame := w.Body.String()[before:]

	if frame != ": hello world\n\n" {
		t.Errorf("comment frame: want %q, got %q", ": hello world\n\n", frame)
	}
}

// A comment ends at its first newline, so an embedded one would turn the rest
// of the text into an unintended SSE field.
func TestLoadingWriter_SendCommentFlattensNewlines(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)

	before := w.Body.Len()
	lw.sendComment("line one\nline two\r\nline three")
	frame := w.Body.String()[before:]

	if frame != ": line one line two  line three\n\n" {
		t.Errorf("newlines not flattened, got %q", frame)
	}
}

// The load placeholder must never reach the client as model output: everything
// it emits is a comment, so a conforming SSE parser drops all of it.
func TestLoadingWriter_NeverEmitsDataFrames(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)
	lw.tickDuration = 5 * time.Millisecond
	lw.loopStarted = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	go lw.start(ctx)
	<-lw.loopStarted
	lw.setUpdate("Queue position: #2")
	time.Sleep(60 * time.Millisecond)
	cancel()

	if !lw.waitForCompletion(time.Second) {
		t.Fatal("waitForCompletion timed out")
	}

	scanner := bufio.NewScanner(strings.NewReader(w.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ":") {
			t.Fatalf("non-comment line in loading stream: %q", line)
		}
	}
}

func TestLoadingWriter_FlushesPeriodicallyDuringStatusUpdates(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)
	lw.tickDuration = 10 * time.Millisecond
	lw.loopStarted = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lw.start(ctx)
		close(done)
	}()

	<-lw.loopStarted
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if got := countSSEMessages(body); got < 2 {
		t.Errorf("expected multiple SSE messages from periodic updates, got %d", got)
	}
}

func TestLoadingWriter_ReqStored(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	lw := newLoadingWriter(logger, "test-model", w, req)
	if lw.req != req {
		t.Fatal("req not stored")
	}
}

func TestIsLoadingPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/chat/completions/extra", true},
		{"/v1/completions", false},
		{"/v1/embeddings", false},
		{"/health", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isLoadingPath(tt.path); got != tt.want {
				t.Errorf("isLoadingPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func countSSEMessages(s string) int {
	scanner := bufio.NewScanner(strings.NewReader(s))
	count := 0
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), ":") {
			count++
		}
	}
	return count
}

func extractComments(body string) string {
	var result strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, ":") {
			continue
		}
		result.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, ":"), " "))
		result.WriteString("\n")
	}
	return result.String()
}
