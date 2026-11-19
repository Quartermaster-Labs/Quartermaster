package process

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// exitTailMax bounds what one launch keeps of its backend's output. A normal
// llama-server load prints well past this in metadata, but a startup failure
// is the last thing written, so the end of the stream is the part that matters.
const exitTailMax = 16 << 10

// exitReasonMax caps the line handed back to a client in an error body.
const exitReasonMax = 300

// exitTail keeps the last exitTailMax bytes a backend wrote during ONE launch.
//
// The process's own log Monitor is not usable for this: the same Monitor lives
// across every restart of the model, so its history mixes this launch's output
// with earlier runs, and an old run's error would be reported as today's.
type exitTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *exitTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - exitTailMax; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// reason picks the line most likely to say why the backend quit: the FIRST line
// in the tail that reads like an error, else the last non-empty line.
//
// First, not last, because failures cascade. An out-of-memory load prints
// "cudaMalloc failed: out of memory", then "error loading model", then
// "failed to load model", then "exiting due to model loading error"; the last
// line is the least specific one. An argument error is a single line, so
// either rule finds it.
func (t *exitTail) reason() string {
	t.mu.Lock()
	text := string(t.buf)
	wrapped := len(t.buf) >= exitTailMax
	t.mu.Unlock()

	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	// The buffer may have been cut mid-line at the front.
	if wrapped && len(lines) > 1 {
		lines = lines[1:]
	}
	pick := lines[len(lines)-1]
	for _, l := range lines {
		if looksLikeError(l) {
			pick = l
			break
		}
	}
	return clipRunes(pick, exitReasonMax)
}

func looksLikeError(line string) bool {
	l := strings.ToLower(line)
	for _, w := range []string{"error", "failed", "fatal", "exception", "out of memory"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
