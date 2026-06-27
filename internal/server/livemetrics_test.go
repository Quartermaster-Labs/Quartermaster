package server

import (
	"testing"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/event"
	"github.com/radu0120/llama-quartermaster/internal/shared"
)

// TestLiveTokenCounter_CountsContentChunks verifies that content-bearing SSE
// chunks each count as one token, that lines split across feeds are reassembled,
// and that a streamed usage object overrides the running estimate.
func TestLiveTokenCounter_CountsContentChunks(t *testing.T) {
	// Event delivery is async (each subscriber runs its own goroutine), so capture
	// into a buffered channel and wait for it rather than racing the handler.
	events := make(chan shared.LiveTokensEvent, 16)
	off := event.On(func(e shared.LiveTokensEvent) {
		select {
		case events <- e:
		default:
		}
	})
	defer off()

	c := newLiveTokenCounter("m", time.Now().Add(-time.Second))

	// Two content chunks; one empty (role-only) chunk that must not count.
	c.feed([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n"))
	c.feed([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n"))
	// A chunk delivered in two writes — must be joined before parsing.
	c.feed([]byte("data: {\"choices\":[{\"delta\":{\"content\""))
	c.feed([]byte(":\"llo\"}}]}\n"))

	if c.tokens != 2 {
		t.Fatalf("tokens = %d, want 2", c.tokens)
	}

	// A streamed usage object is authoritative and overrides the estimate.
	c.feed([]byte("data: {\"usage\":{\"completion_tokens\":42}}\n"))
	if c.tokens != 42 {
		t.Fatalf("tokens after usage = %d, want 42", c.tokens)
	}

	// At least one throttled event fired for this model with a real elapsed time.
	select {
	case last := <-events:
		if last.Model != "m" || last.ElapsedMs <= 0 {
			t.Fatalf("unexpected event: %+v", last)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no LiveTokensEvent delivered")
	}
}

// TestLiveTokenCounter_CountsToolCallChunks verifies that streamed tool-call
// argument deltas (which carry no `content`) and legacy completion `text` chunks
// each count as one token — without them the live rate decays to zero while the
// model is writing a tool call.
func TestLiveTokenCounter_CountsToolCallChunks(t *testing.T) {
	c := newLiveTokenCounter("m", time.Now().Add(-time.Second))

	// Tool-call argument deltas: no `content`, args stream under tool_calls.
	c.feed([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n"))
	c.feed([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a.txt\\\"}\"}}]}}]}\n"))
	// Legacy completions API streams generated text at choices.0.text.
	c.feed([]byte("data: {\"choices\":[{\"text\":\"hi\"}]}\n"))
	// Empty text must not count.
	c.feed([]byte("data: {\"choices\":[{\"text\":\"\"}]}\n"))

	if c.tokens != 3 {
		t.Fatalf("tokens = %d, want 3", c.tokens)
	}
}
