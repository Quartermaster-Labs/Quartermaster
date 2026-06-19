package server

import (
	"testing"
	"time"

	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/shared"
)

// TestLiveTokenCounter_CountsContentChunks verifies that content-bearing SSE
// chunks each count as one token, that lines split across feeds are reassembled,
// and that a streamed usage object overrides the running estimate.
func TestLiveTokenCounter_CountsContentChunks(t *testing.T) {
	var last shared.LiveTokensEvent
	off := event.On(func(e shared.LiveTokensEvent) { last = e })
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
	if last.Model != "m" || last.ElapsedMs <= 0 {
		t.Fatalf("unexpected event: %+v", last)
	}
}
