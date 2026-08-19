package router

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
)

var loadingPaths = []string{
	"/v1/chat/completions",
}

func isLoadingPath(path string) bool {
	for _, p := range loadingPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// loadingWriter keeps a streaming client's connection alive while its model
// loads, and narrates the wait on the way.
//
// It narrates in SSE *comment* frames (`: text`), never in `data:` frames. The
// connection is held open either way, but a comment is discarded by every
// conforming SSE parser, so nothing this writer emits can reach a client as
// model output. It used to send synthetic `reasoning_content` deltas: those
// prepended fabricated reasoning to the assistant message and carried none of
// the chunk fields (id/object/model/created) a strict client expects. Whatever
// is added here stays a comment -- progress belongs beside the stream, not
// inside it.
type loadingWriter struct {
	hasWritten bool
	writer     http.ResponseWriter
	req        *http.Request
	ctx        context.Context
	logger     *logmon.Monitor
	modelName  string
	startTime  time.Time

	pendingMu     sync.Mutex
	pendingUpdate string

	// writeMu serializes writes to the underlying writer and guards released.
	// Once released is set, the streaming goroutine must not touch the writer
	// again — ServeHTTP has reclaimed it (to run the real handler or to return)
	// and writing/flushing a finalized response panics.
	writeMu  sync.Mutex
	released bool

	// closed by start when the goroutine finishes (after cleanup messages)
	done chan struct{}

	// test-only: closed when start enters its loop
	loopStarted chan struct{}
	// test-only: override the 1s tick interval
	tickDuration time.Duration
}

func newLoadingWriter(logger *logmon.Monitor, modelName string, w http.ResponseWriter, req *http.Request) *loadingWriter {
	s := &loadingWriter{
		writer:       w,
		req:          req,
		ctx:          req.Context(),
		logger:       logger,
		modelName:    modelName,
		startTime:    time.Now(),
		tickDuration: 750 * time.Millisecond,
	}

	s.Header().Set("Content-Type", "text/event-stream")
	s.Header().Set("Cache-Control", "no-cache")
	s.Header().Set("Connection", "keep-alive")
	s.WriteHeader(http.StatusOK)
	s.sendComment(fmt.Sprintf("quartermaster loading model: %s", modelName))
	return s
}

func (s *loadingWriter) setUpdate(msg string) {
	s.pendingMu.Lock()
	s.pendingUpdate = msg
	s.pendingMu.Unlock()
}

func (s *loadingWriter) start(ctx context.Context) {
	s.done = make(chan struct{})
	defer close(s.done)

	defer func() {
		// Skip cleanup writes if the client disconnected — the connection
		// is being torn down and flushing against it will panic.
		if s.ctx.Err() != nil {
			return
		}
		duration := time.Since(s.startTime)
		s.sendComment(fmt.Sprintf("model ready (%.2fs)", duration.Seconds()))
	}()

	remarks := make([]string, len(loadingRemarks))
	copy(remarks, loadingRemarks)
	rand.Shuffle(len(remarks), func(i, j int) {
		remarks[i], remarks[j] = remarks[j], remarks[i]
	})
	ri := 0

	nextRemarkIn := time.Duration(2+rand.Intn(4)) * time.Second
	lastRemarkTime := time.Time{}

	ticker := time.NewTicker(s.tickDuration)
	defer ticker.Stop()

	if s.loopStarted != nil {
		close(s.loopStarted)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pendingMu.Lock()
			update := s.pendingUpdate
			s.pendingUpdate = ""
			s.pendingMu.Unlock()

			if update != "" {
				s.sendComment(update)
				lastRemarkTime = time.Now()
				nextRemarkIn = time.Duration(5+rand.Intn(5)) * time.Second
			} else if time.Since(lastRemarkTime) >= nextRemarkIn {
				remark := remarks[ri%len(remarks)]
				ri++
				s.sendComment(remark)
				lastRemarkTime = time.Now()
				nextRemarkIn = time.Duration(5+rand.Intn(5)) * time.Second
			} else {
				// bare keepalive comment: holds the connection without saying
				// anything
				s.sendComment("")
			}
		}
	}
}

func (s *loadingWriter) waitForCompletion(timeout time.Duration) bool {
	if s.done == nil {
		return true
	}
	select {
	case <-s.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// sendComment emits one SSE comment frame. Comments are the protocol's own
// keepalive: they hold the connection open and every conforming parser drops
// them, so this narration can never be mistaken for model output. An empty
// text is a bare keepalive. Newlines would end the comment and turn the rest
// into an unintended field, so they are flattened.
func (s *loadingWriter) sendComment(text string) {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// Once ServeHTTP has reclaimed the writer (release), writing/flushing it
	// races the real handler or panics on a finalized response. Stop here.
	if s.released {
		return
	}

	if _, err := fmt.Fprintf(s.writer, ": %s\n\n", text); err != nil {
		s.logger.Debugf("<%s> Failed to write SSE comment (client likely disconnected): %v", s.modelName, err)
		return
	}
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// release fences the loadingWriter off from the underlying ResponseWriter.
// After it returns, the streaming goroutine will not write to or flush the
// writer again: any in-flight write completes under writeMu first, and later
// writes short-circuit on released. The caller can then safely hand the writer
// to the real handler or let ServeHTTP return without racing a finalized
// response (a use-after-return Flush panics on the recycled *bufio.Writer).
func (s *loadingWriter) release() {
	s.writeMu.Lock()
	s.released = true
	s.writeMu.Unlock()
}

func (s *loadingWriter) Header() http.Header {
	return s.writer.Header()
}

func (s *loadingWriter) Write(data []byte) (int, error) {
	return s.writer.Write(data)
}

func (s *loadingWriter) WriteHeader(statusCode int) {
	if s.hasWritten {
		return
	}
	s.hasWritten = true
	s.writer.WriteHeader(statusCode)
	s.Flush()
}

func (s *loadingWriter) Flush() {
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
